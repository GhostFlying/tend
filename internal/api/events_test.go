package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

// sse is one server-sent event or heartbeat read from a stream.
type sse struct {
	id, name, data string
	heartbeat      bool
}

type stream struct {
	t      *testing.T
	cancel context.CancelFunc
	events chan sse
	status int
}

// openStream connects to the event stream of the server at base.
func openStream(t *testing.T, base, token, query string) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/events"+query, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &stream{t: t, cancel: cancel, events: make(chan sse, 100), status: resp.StatusCode}
	go func() {
		defer resp.Body.Close()
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		var cur sse
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur != (sse{}) {
					s.events <- cur
				}
				cur = sse{}
			case strings.HasPrefix(line, ":"):
				cur.heartbeat = true
			case strings.HasPrefix(line, "id: "):
				cur.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	t.Cleanup(cancel)
	return s
}

// next returns the next event that is not a heartbeat.
func (s *stream) next() sse {
	s.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				s.t.Fatal("the stream ended")
			}
			if !ev.heartbeat {
				return ev
			}
		case <-timeout:
			s.t.Fatal("no event")
		}
	}
}

func (e *env) say(body string) {
	e.t.Helper()
	b, _ := json.Marshal(protocol.SubmitMessagePayload{Body: body})
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}
	if res, err := e.app.Execute(context.Background(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest),
		Operation: "conversation.submit_message", Target: protocol.Target{Type: "message"}, Payload: b}, app.SubmitMessage{}); err != nil || res.Status == protocol.OpFailed {
		e.t.Fatalf("submit: %+v %v", res, err)
	}
}

func TestEventStream(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.say("first")

	s := openStream(t, e.url, tok, "")
	start := s.next()
	var at protocol.StreamStart
	if start.name != "start" || json.Unmarshal([]byte(start.data), &at) != nil || at.EventSeq != 1 {
		t.Fatalf("start = %+v", start)
	}
	e.say("second")
	ev := s.next()
	var le protocol.LedgerEvent
	if ev.name != "ledger" || ev.id != "2" || json.Unmarshal([]byte(ev.data), &le) != nil || le.EventSeq != 2 || le.Kind != "conversation.message" {
		t.Fatalf("event = %+v", ev)
	}
	if le.Item == nil || le.Item.Body != "second" || le.Item.State != protocol.MessageQueued || le.ObjectID != le.Item.MessageID {
		t.Errorf("conversation event item = %+v", le.Item)
	}

	// A client resumes after the last event it received.
	r := openStream(t, e.url, tok, "?after=0")
	if ev := r.next(); ev.name != "start" || ev.data != `{"event_seq":0}` {
		t.Errorf("resumed start = %+v", ev)
	}
	for _, want := range []string{"1", "2"} {
		if ev := r.next(); ev.id != want {
			t.Errorf("replayed event %s, want %s", ev.id, want)
		}
	}

	// A cursor past the ledger is refused.
	if f := openStream(t, e.url, tok, "?after=9"); f.status != http.StatusBadRequest {
		t.Errorf("cursor past the ledger = %d, want 400", f.status)
	}
	if f := openStream(t, e.url, "", ""); f.status != http.StatusUnauthorized {
		t.Errorf("without a token = %d, want 401", f.status)
	}
}

func TestEventStreamHeartbeatAndHangUp(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.server.cfg.Heartbeat = 20 * time.Millisecond
	var handlers sync.WaitGroup
	h := e.server.Handler()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		h.ServeHTTP(w, r)
	}))
	defer hs.Close()

	s := openStream(t, hs.URL, tok, "")
	defer s.cancel() // before hs.Close, which waits for the handler
	if ev := s.next(); ev.name != "start" {
		t.Fatalf("first event = %+v", ev)
	}
	select {
	case ev := <-s.events:
		if !ev.heartbeat {
			t.Errorf("a quiet stream sent %+v, want a heartbeat", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat")
	}

	// The handler returns once the client hangs up.
	s.cancel()
	done := make(chan struct{})
	go func() {
		handlers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream's handler did not return after the client hung up")
	}
}

func TestEventStreamEndsAtShutdown(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)

	// HEAD answers with headers only instead of holding the connection.
	req, _ := http.NewRequest("HEAD", e.url+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD = %v %v", resp, err)
	}
	resp.Body.Close()

	s := openStream(t, e.url, tok, "")
	if ev := s.next(); ev.name != "start" {
		t.Fatalf("first event = %+v", ev)
	}
	e.server.EndStreams()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-s.events:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("the stream did not end at shutdown")
		}
	}
}
