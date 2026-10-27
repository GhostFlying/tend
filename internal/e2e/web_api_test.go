package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

// startWeb serves the owner API of a server that runs the hosted main
// agent, as the web client uses it. Shutting it down ends its event
// streams first, as tend-server does.
func (s *server) startWeb() *httptest.Server {
	phone := netx.Static{Identity: &netx.Identity{Node: "phone", User: ownerLogin}}
	a := api.New(s.app, s.store, phone, secret,
		api.Config{Owners: []string{ownerLogin}, HostedMainAgent: true}, quiet())
	hs := httptest.NewServer(a.Handler())
	s.onStop(func() {
		a.EndStreams()
		hs.Close()
	})
	return hs
}

// webClient calls the owner API the way the web client does.
type webClient struct {
	t            *testing.T
	srv          *server
	url          string
	token        string
	deploymentID string
}

func (s *server) webLogin(url string) *webClient {
	s.t.Helper()
	c := &webClient{t: s.t, srv: s, url: url}
	var v protocol.SessionView
	if st := c.do("POST", "/api/v1/session", protocol.SessionRequest{ClientID: "web-client-1", Scope: protocol.ScopeOwner}, &v); st != http.StatusOK || v.Token == "" {
		s.t.Fatalf("login = %d %+v", st, v)
	}
	c.token = v.Token
	c.deploymentID = v.DeploymentID
	return c
}

// do sends a request and decodes the JSON answer into out, returning the
// status.
func (c *webClient) do(method, path string, body, out any) int {
	c.t.Helper()
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.url+path, r)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("X-Tend-Deployment-ID", c.deploymentID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			c.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// op posts one write under the given request ID and returns its
// operation, which must not have failed.
func (c *webClient) op(requestID, operation string, target protocol.Target, payload any) protocol.Operation {
	c.t.Helper()
	b, _ := json.Marshal(payload)
	var res protocol.APIResponse
	env := protocol.Envelope{RequestID: requestID, Operation: operation, Target: target, Payload: b}
	if st := c.do("POST", "/api/v1/ops", env, &res); st != http.StatusOK || res.Operation == nil || res.Operation.Status == protocol.OpFailed {
		c.t.Fatalf("%s = %d %+v", operation, st, res)
	}
	return *res.Operation
}

// send submits a message under a new request ID and returns its ID.
func (c *webClient) send(p protocol.SubmitMessagePayload) string {
	c.t.Helper()
	return messageID(c.t, c.op(c.srv.ids.New(protocol.PrefixRequest), "conversation.submit_message", protocol.Target{Type: "message"}, p))
}

func messageID(t *testing.T, op protocol.Operation) string {
	t.Helper()
	var res struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &res); err != nil || res.MessageID == "" {
		t.Fatalf("submit_message result = %s, %v", op.Result, err)
	}
	return res.MessageID
}

func (c *webClient) conversation(query string) protocol.ConversationPage {
	c.t.Helper()
	var page protocol.ConversationPage
	if st := c.do("GET", "/api/v1/conversation"+query, nil, &page); st != http.StatusOK {
		c.t.Fatalf("read the conversation = %d", st)
	}
	return page
}

// decisions reads the open decisions from the snapshot.
func (c *webClient) decisions() []protocol.DecisionView {
	c.t.Helper()
	var snap protocol.Snapshot
	if st := c.do("GET", "/api/v1/snapshot", nil, &snap); st != http.StatusOK {
		c.t.Fatalf("read the snapshot = %d", st)
	}
	return snap.Decisions
}

// eventStream is the client's end of the event stream.
type eventStream struct {
	t      *testing.T
	start  protocol.StreamStart
	events chan protocol.LedgerEvent
	// last is the cursor of the last ledger event read.
	last int64
}

// stream opens the event stream with the given query and reads its start.
func (c *webClient) stream(query string) *eventStream {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", c.url+"/api/v1/events"+query, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Tend-Deployment-ID", c.deploymentID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		c.t.Fatalf("open the event stream = %d", resp.StatusCode)
	}
	starts := make(chan protocol.StreamStart, 1)
	s := &eventStream{t: c.t, events: make(chan protocol.LedgerEvent, 100)}
	go func() {
		defer resp.Body.Close()
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data := []byte(strings.TrimPrefix(line, "data: "))
				if name == "start" {
					var st protocol.StreamStart
					_ = json.Unmarshal(data, &st)
					starts <- st
					continue
				}
				var ev protocol.LedgerEvent
				if json.Unmarshal(data, &ev) == nil {
					s.events <- ev
				}
			}
		}
	}()
	select {
	case s.start = <-starts:
	case <-time.After(10 * time.Second):
		c.t.Fatal("the event stream did not start")
	}
	s.last = s.start.EventSeq
	return s
}

// until reads events until one satisfies ok and returns those read.
func (s *eventStream) until(what string, ok func(protocol.LedgerEvent) bool) []protocol.LedgerEvent {
	s.t.Helper()
	var read []protocol.LedgerEvent
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, open := <-s.events:
			if !open {
				s.t.Fatalf("the stream ended before %s", what)
			}
			if ev.EventSeq != s.last+1 {
				s.t.Errorf("event %d follows %d", ev.EventSeq, s.last)
			}
			s.last = ev.EventSeq
			read = append(read, ev)
			if ok(ev) {
				return read
			}
		case <-timeout:
			s.t.Fatalf("no event: %s", what)
		}
	}
}

// itemIs matches a conversation event about the message in the given
// state.
func itemIs(message string, state protocol.MessageState) func(protocol.LedgerEvent) bool {
	return func(ev protocol.LedgerEvent) bool {
		return ev.Item != nil && ev.Item.MessageID == message && ev.Item.State == state
	}
}

// TestEndToEnd_WebConversation walks the conversation API the web client
// uses: send a message, follow the reply and its tool use on the event
// stream, read the conversation, and stop a reply that runs long.
func TestEndToEnd_WebConversation(t *testing.T) {
	srv := newServer(t, time.Minute)
	gate := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(gate) })
	agent := testkit.NewHarness()
	agent.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if strings.Contains(in.Text, "take your time") {
			<-gate
			return "" // stopped by the interrupt
		}
		use := harness.ToolUse{Name: "tasks_list", Input: "{}", Status: protocol.ToolRunning}
		th.UseTool(turn, "item_tool_"+turn, use)
		res := th.CallTool(context.Background(), turn, "call_"+turn, "tasks_list", map[string]any{})
		use.Output, use.Status = res.Text, protocol.ToolCompleted
		th.UseTool(turn, "item_tool_"+turn, use)
		th.Say(turn, "item_say_"+turn, "Nothing is running.")
		return "completed"
	}
	srv.startMainThread(agent)
	hs := srv.startWeb()
	web := srv.webLogin(hs.URL)

	events := web.stream("")
	asked := web.send(protocol.SubmitMessagePayload{Body: "what is running?"})
	read := events.until("the reply is done", itemIs(asked, protocol.MessageReplied))
	var reply, tool *protocol.ConversationItem
	for _, ev := range read {
		switch {
		case ev.Item == nil || ev.Item.ReplyTo != asked:
		case ev.Kind == "conversation.tool_use":
			tool = ev.Item
		case ev.Kind == "conversation.message":
			reply = ev.Item
		}
	}
	if reply == nil || reply.Author != "main_agent" || reply.Body != "Nothing is running." {
		t.Errorf("the reply on the stream = %+v", reply)
	}
	if tool == nil || tool.Tool == nil || tool.Tool.Name != "tasks_list" || tool.Tool.Status != protocol.ToolCompleted || tool.Tool.Output == "" {
		t.Errorf("the tool use on the stream = %+v", tool)
	}

	// The conversation reads as the stream showed it, in order.
	page := web.conversation("")
	var got []string
	for _, it := range page.Items {
		got = append(got, it.Author+"/"+it.Kind)
	}
	if want := "owner/message main_agent/tool_use main_agent/message"; strings.Join(got, " ") != want {
		t.Fatalf("conversation = %v, want %s", got, want)
	}
	if page.Items[0].State != protocol.MessageReplied || page.Items[1].Tool.Status != protocol.ToolCompleted {
		t.Errorf("conversation = %+v", page.Items)
	}
	if later := web.conversation("?after=" + strconv.FormatInt(page.Items[0].Seq, 10)); len(later.Items) != 2 || later.More {
		t.Errorf("items after the message = %+v", later)
	}

	// A reply that runs long is stopped.
	long := web.send(protocol.SubmitMessagePayload{Body: "plan the release, take your time", LastSeenSeq: page.Items[2].Seq})
	events.until("the long reply starts", itemIs(long, protocol.MessageProcessing))
	web.op(srv.ids.New(protocol.PrefixRequest), "conversation.interrupt_reply", protocol.Target{Type: "message", ID: long},
		protocol.InterruptReplyPayload{MessageID: long})
	events.until("the long reply is stopped", itemIs(long, protocol.MessageInterrupted))
	once.Do(func() { close(gate) })

	// The conversation goes on after it.
	next := web.send(protocol.SubmitMessagePayload{Body: "and now?"})
	events.until("the next reply is done", itemIs(next, protocol.MessageReplied))
}

// chosen matches the header the main agent's input carries for a message
// choosing a decision option (D71).
var chosen = regexp.MustCompile(`choosing option "([^"]*)" of decision (\S+) version (\d+)`)

// TestEndToEnd_WebAnswersDecisions answers both kinds of decision from
// the web client: an option tend carries out goes to decisions.respond,
// and an option of the main agent's own question is a message the agent
// records with decisions_resolve.
func TestEndToEnd_WebAnswersDecisions(t *testing.T) {
	srv := newServer(t, time.Minute)
	agent := testkit.NewHarness()
	sourceResults := make(chan harness.ToolResult, 4)
	agent.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		ctx := context.Background()
		message, err := srv.app.Message(ctx, in.UserMessageID)
		if err != nil {
			t.Errorf("read fake model input: %v", err)
			return "failed"
		}
		if message.Author == "system" {
			// Ledger events and stale-background coordination notices can use
			// different prompt headers. Neither is a new owner release request.
			if strings.HasPrefix(th.Config.Cwd, "/background/") {
				sourceResults <- th.CallTool(ctx, turn, "sources_"+turn, "background_sources", map[string]any{})
			}
			th.Say(turn, "item_"+turn, "The decision state changed; no new owner instruction.")
			return "completed"
		}
		if m := chosen.FindStringSubmatch(in.Text); m != nil {
			version, _ := strconv.Atoi(m[3])
			res := th.CallTool(ctx, turn, "call_"+turn, "decisions_resolve", map[string]any{"decision_id": m[2], "version": version, "option": m[1]})
			th.Say(turn, "item_"+turn, "Going with "+m[1]+": "+res.Text)
			return "completed"
		}
		if message.Author != "owner" || message.Body != "ship it" {
			t.Errorf("unexpected release request from %s: %s", message.Author, message.Body)
			return "failed"
		}
		th.CallTool(ctx, turn, "call_"+turn, "decisions_open", map[string]any{"question": "Which release branch?", "options": []string{"main", "release/1.x"}})
		th.Say(turn, "item_"+turn, "Which release branch should I use?")
		return "completed"
	}
	backgroundTurn := agent.Turn
	srv.startMainThread(agent, func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		status := backgroundTurn(th, turn, in)
		item := strings.TrimPrefix(th.Config.Cwd, "/background/work-item:")
		res := th.CallTool(context.Background(), turn, "read-context", "work_item_context_read", map[string]any{"work_item_id": item})
		var v struct {
			Result protocol.WorkItemContext `json:"result"`
		}
		if !res.Success || json.Unmarshal([]byte(res.Text), &v) != nil {
			return "failed"
		}
		res = th.CallTool(context.Background(), turn, "submit-background", "background_submit", protocol.BackgroundAssessmentPayload{ContextRevision: v.Result.Revision, Disposition: "waiting", Note: "Decision state read", EvidenceIDs: []string{}})
		if !res.Success {
			return "failed"
		}
		return status
	})
	hs := srv.startWeb()
	web := srv.webLogin(hs.URL)
	events := web.stream("")

	// A decision tend opened, answered with an option it carries out.
	n := newNode(t, srv, passing)
	n.ws.noChanges = true
	n.start()
	task := srv.createTask()
	eventually(t, "the task waits for a decision", func() bool { return srv.waitsForDecision(task) })
	d := web.decisions()
	if len(d) != 1 {
		t.Fatalf("open decisions = %+v, want the one tend opened", d)
	}
	cancel := ""
	for option, effect := range d[0].Effects {
		if effect == protocol.EffectCancel {
			cancel = option
		}
	}
	op := web.op(srv.ids.New(protocol.PrefixRequest), "decisions.respond", protocol.Target{Type: "decision", ID: d[0].DecisionID},
		protocol.AnswerDecisionPayload{Version: d[0].Version, Option: cancel})
	var answer protocol.AnswerResult
	if err := json.Unmarshal(op.Result, &answer); err != nil || answer.Effect == nil || !answer.Effect.Applied || answer.Effect.Effect != protocol.EffectCancel {
		t.Errorf("decisions.respond = %s, %v", op.Result, err)
	}
	events.until("the task is cancelled", func(ev protocol.LedgerEvent) bool { return ev.ObjectID == task && ev.Kind == "task.cancelled" })

	// The main agent's own question, answered by choosing an option in a
	// message.
	asked := web.send(protocol.SubmitMessagePayload{Body: "ship it"})
	events.until("the agent asks", itemIs(asked, protocol.MessageReplied))
	open := web.decisions()
	if len(open) != 1 || open[0].Effects != nil {
		for _, input := range businessStarts(agent) {
			t.Logf("business input: %s", input.Text)
		}
		t.Fatalf("open decisions = %+v, want the agent's question", open)
	}
	q := open[0]
	choice := &protocol.DecisionChoice{DecisionID: q.DecisionID, Version: q.Version, Option: "release/1.x"}
	answered := web.send(protocol.SubmitMessagePayload{Body: "release/1.x", Decision: choice})
	events.until("the choice is recorded", func(ev protocol.LedgerEvent) bool {
		return ev.ObjectID == q.DecisionID && ev.Kind == "decision.answered"
	})
	events.until("the agent replies", itemIs(answered, protocol.MessageReplied))
	if open := web.decisions(); len(open) != 0 {
		t.Errorf("open decisions = %+v, want none", open)
	}
	page := web.conversation("")
	var sent *protocol.ConversationItem
	for i := range page.Items {
		if page.Items[i].MessageID == answered {
			sent = &page.Items[i]
		}
	}
	if sent == nil || sent.Decision == nil || *sent.Decision != *choice {
		t.Errorf("the answering message = %+v, want it to carry the choice", sent)
	}
	if n := srv.count(`SELECT COUNT(*) FROM decisions WHERE decision_id = ? AND json_extract(answer_json, '$.option') = 'release/1.x'`, q.DecisionID); n != 1 {
		t.Errorf("decisions answered with the chosen option = %d, want 1", n)
	}
	// Unclaimed events may coalesce; both original sources must be read without
	// creating another owner question or running business operations twice.
	eventually(t, "owner replies and background decision sources settle", func() bool {
		return srv.count(`SELECT COUNT(*) FROM logical_turns WHERE status<>'completed'`) == 0 && srv.count(`SELECT COUNT(*) FROM background_sources s WHERE kind='decision.answered' AND NOT EXISTS(SELECT 1 FROM background_reads r WHERE r.batch_id=s.batch_id AND r.source_seq=s.seq)`) == 0
	})
	if srv.count("SELECT COUNT(*) FROM decisions") != 2 {
		t.Fatal("decision coordination duplicated a question")
	}
	for len(sourceResults) > 0 {
		if read := <-sourceResults; !read.Success {
			t.Fatal(read.Text)
		}
	}
	sawAppliedAnswer := false
	for _, input := range businessStarts(agent) {
		if strings.Contains(input.Text, `"applied":true`) && strings.Contains(input.Text, `"effect":"cancel"`) {
			sawAppliedAnswer = true
		}
	}
	if !sawAppliedAnswer {
		t.Fatal("main answer coordination omitted the canonical applied cancellation result")
	}
}

// TestFault_SendReceiptLost: a client that got no answer to a message it
// sent sends it again under the same request ID, and the conversation
// still has it once.
func TestFault_SendReceiptLost(t *testing.T) {
	srv := newServer(t, time.Minute)
	agent := testkit.NewHarness()
	agent.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		th.Say(turn, "item_"+turn, "Noted.")
		return "completed"
	}
	srv.startMainThread(agent)
	hs := srv.startWeb()
	web := srv.webLogin(hs.URL)

	retried := srv.ids.New(protocol.PrefixRequest)
	p := protocol.SubmitMessagePayload{Body: "remember the deadline"}
	first := web.op(retried, "conversation.submit_message", protocol.Target{Type: "message"}, p)
	again := web.op(retried, "conversation.submit_message", protocol.Target{Type: "message"}, p)
	if again.Status != first.Status || messageID(t, first) != messageID(t, again) {
		t.Errorf("the resend = %+v, want the first operation %+v", again, first)
	}
	eventually(t, "the reply is done", func() bool {
		return srv.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed' AND request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1)`) == 1
	})
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM conversation_messages WHERE author = 'owner'`:                                                       1,
		`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent'`:                                                  1,
		`SELECT COUNT(*) FROM main_turns WHERE request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1)`: 1,
	} {
		if got := srv.count(query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
	// The same request ID with another message is refused, not taken as
	// a new one.
	b, _ := json.Marshal(protocol.SubmitMessagePayload{Body: "something else"})
	var res protocol.APIResponse
	web.do("POST", "/api/v1/ops", protocol.Envelope{RequestID: retried, Operation: "conversation.submit_message", Target: protocol.Target{Type: "message"}, Payload: b}, &res)
	if res.Error == nil || res.Error.Code != protocol.CodeConflictPayload {
		t.Errorf("another message under the same request ID = %+v, want %s", res, protocol.CodeConflictPayload)
	}
}

// TestFault_StopReceiptLost: a client that got no answer to a stop sends
// it again under the same request ID while the first is still being
// carried out; it gets the first answer back and the turn is stopped
// once.
func TestFault_StopReceiptLost(t *testing.T) {
	srv := newServer(t, time.Minute)
	gate := make(chan struct{})
	defer close(gate)
	agent := testkit.NewHarness()
	agent.Turn = func(*testkit.FakeThread, string, harness.TurnInput) string {
		<-gate
		return ""
	}
	held, carried := make(chan struct{}, 1), make(chan struct{})
	agent.InterruptErr = func(ctx context.Context, _ string) error {
		select {
		case held <- struct{}{}:
		default:
		}
		select {
		case <-carried:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	srv.startMainThread(agent)
	web := srv.webLogin(srv.startWeb().URL)
	events := web.stream("")

	long := web.send(protocol.SubmitMessagePayload{Body: "think it over"})
	events.until("the reply starts", itemIs(long, protocol.MessageProcessing))
	retried := srv.ids.New(protocol.PrefixRequest)
	stop := func() protocol.Operation {
		return web.op(retried, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: long}, protocol.InterruptReplyPayload{MessageID: long})
	}
	first := stop()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the stop was not carried out")
	}
	// The turn is still running when the stop is sent again.
	again := stop()
	close(carried)
	if again.Status != first.Status || string(again.Result) != string(first.Result) {
		t.Errorf("the resend = %+v, want the first answer %+v", again, first)
	}
	events.until("the reply is stopped", itemIs(long, protocol.MessageInterrupted))
	if n := srv.count(`SELECT COUNT(*) FROM outbox WHERE destination = ?`, app.MainThreadControlDestination); n != 1 {
		t.Errorf("stops queued = %d, want 1", n)
	}
}

// TestFault_EventStreamAcrossRestart: when the server restarts, the
// client resumes the stream after the last event it received, with the
// token it has, and misses nothing written before it reconnected.
func TestFault_EventStreamAcrossRestart(t *testing.T) {
	agent := testkit.NewHarness()
	agent.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		th.Say(turn, "item_"+turn, "Noted.")
		return "completed"
	}
	srv := newServer(t, time.Minute)
	srv.startMainThread(agent)
	web := srv.webLogin(srv.startWeb().URL)
	events := web.stream("")
	first := web.send(protocol.SubmitMessagePayload{Body: "first"})
	events.until("the first reply is done", itemIs(first, protocol.MessageReplied))

	// The server restarts: its streams end, it stops and its ledger is
	// opened again.
	srv = srv.restart()
	timeout := time.After(10 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-events.events:
		case <-timeout:
			t.Fatal("the stream did not end at shutdown")
		}
	}
	srv.startMainThread(agent)
	web.srv, web.url = srv, srv.startWeb().URL
	// Work goes on before the client is back.
	srv.say("written from another client")
	eventually(t, "the reply is done", func() bool {
		return srv.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed' AND request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1)`) == 2
	})

	resumed := web.stream("?after=" + strconv.FormatInt(events.last, 10))
	if resumed.start.EventSeq != events.last {
		t.Errorf("resumed at %d, want %d", resumed.start.EventSeq, events.last)
	}
	read := resumed.until("the missed reply", func(ev protocol.LedgerEvent) bool {
		return ev.Item != nil && ev.Item.Author == "main_agent" && ev.Item.ReplyTo != first
	})
	if read[0].EventSeq != events.last+1 {
		t.Errorf("the resumed stream starts at %d, want %d", read[0].EventSeq, events.last+1)
	}
}
