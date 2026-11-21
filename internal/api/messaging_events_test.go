package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestMessagingEventsProjectInputsAndExplicitRead(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	e.say("A communication input")
	page, err := e.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	id := page.Items[0].MessageID
	raw, _ := json.Marshal(protocol.MessagingReadPayload{ContentRevision: page.Items[0].ContentRevision})
	op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "messaging.read", Target: protocol.Target{Type: "message", ID: id}, Payload: raw}, app.MessagingCommand{Action: "read"})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.url+"/api/v1/messaging/events?after=0", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scan := bufio.NewScanner(response.Body)
	var events []protocol.MessagingEvent
	for scan.Scan() {
		data, ok := strings.CutPrefix(scan.Text(), "data: ")
		if !ok {
			continue
		}
		var event protocol.MessagingEvent
		if err = json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatal(err)
		}
		if event.Kind == "" {
			continue
		}
		events = append(events, event)
		if len(events) == 2 {
			break
		}
	}
	if len(events) != 2 || events[0].Kind != "messaging.input" || events[1].Kind != "messaging.read" || events[0].ObjectID != id || events[1].EventSeq <= events[0].EventSeq {
		t.Fatal(events, scan.Err())
	}
}

func TestMessagingEventsKeepFilteredRefreshPending(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	var seq int64
	if err := e.store.Write(t.Context(), func(tx *store.Tx) (err error) {
		seq, err = tx.InsertEvent(t.Context(), store.EventRow{
			Kind: "token_usage.changed", ObjectType: "token_usage", ObjectID: "all",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/messaging/events?after=0", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := &cancelOnMessagingBatchFlush{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	e.server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.flushes != 2 || w.Body.String() != "event: start\ndata: {\"event_seq\":0}\n\n" {
		t.Fatalf("expected start and an empty batch, got status=%d flushes=%d body=%q", w.Code, w.flushes, w.Body.String())
	}
	e.server.EndStreams()
	if through := e.server.refreshAck.through; through >= seq {
		t.Errorf("offered acknowledgement through %d for refresh %d filtered out of the stream", through, seq)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT pending FROM refresh_notifications WHERE event_seq=?`, seq).Scan(&pending); err != nil {
			return err
		}
		if pending != 1 {
			t.Errorf("filtered refresh %d: pending=%d, want 1", seq, pending)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type cancelOnMessagingBatchFlush struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	flushes int
}

func (w *cancelOnMessagingBatchFlush) Flush() {
	w.ResponseRecorder.Flush()
	w.flushes++
	if w.flushes == 2 {
		w.cancel()
	}
}

func TestMessagingEventsIncludeWithdrawal(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	e.say("A withdrawn communication input")
	page, err := e.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	id := page.Items[0].MessageID
	var control protocol.MessageControl
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		control, err = store.MessageControl(t.Context(), tx.Q(), id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := e.envelope("conversation.withdraw_message", protocol.Target{Type: "message", ID: id}, &control.Revision, protocol.WithdrawMessagePayload{})
	op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, &env, app.WithdrawMessage{})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	m, err := e.app.MessagingMessage(t.Context(), id)
	if err != nil || m.Processing != "interrupted" {
		t.Fatal(m, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.url+"/api/v1/messaging/events?after=0", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scan := bufio.NewScanner(response.Body)
	var events []protocol.MessagingEvent
	for scan.Scan() {
		if data, ok := strings.CutPrefix(scan.Text(), "data: "); ok {
			var event protocol.MessagingEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatal(err)
			}
			if event.Kind != "" {
				events = append(events, event)
			}
		}
	}
	if len(events) != 2 {
		t.Fatalf("message is interrupted, but events contain only %+v", events)
	}
}

func TestMessagingMessagingAssociationEvent(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	e.say("An input to be associated")
	page, err := e.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	id := page.Items[0].MessageID
	var after int64
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		after, err = store.LatestEventSeq(t.Context(), tx.Q())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	revision := int64(0)
	status, res := e.op(token, e.envelope("conversation.set_issue_associations", protocol.Target{Type: "message", ID: id}, &revision, protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", ContentRevision: 1, IssueIDs: []string{issue.IssueID}}))
	if status != http.StatusOK || res.Operation == nil || res.Operation.Error != nil {
		t.Fatal(status, res)
	}
	m, err := e.app.MessagingMessage(t.Context(), id)
	if err != nil || len(m.IssueIDs) != 1 {
		t.Fatal(m, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.url+"/api/v1/messaging/events?after="+strconv.FormatInt(after, 10), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scan := bufio.NewScanner(response.Body)
	var events []protocol.MessagingEvent
	for scan.Scan() {
		if data, ok := strings.CutPrefix(scan.Text(), "data: "); ok {
			var ev protocol.MessagingEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Kind != "" {
				events = append(events, ev)
			}
		}
	}
	if len(events) == 0 {
		t.Fatalf("input IssueIDs changed and processing is now %s, but messaging SSE emitted no event", m.Processing)
	}
	t.Log(events)
}

func TestMessagingEventsProjectSharedTurnRefresh(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	e.say("Original input")
	e.say("Clarification answer")
	page, err := e.app.MessagingMessages(ctx, 0, "", "", "")
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	first, second := page.Items[0].MessageID, page.Items[1].MessageID
	const turn = "shared-turn"
	var after int64
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		after, err = store.LatestEventSeq(ctx, tx.Q())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO logical_turns(logical_turn_id,message_id,status,updated_at)
			VALUES(?,?,'processing',1)`, turn, second); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_messages(logical_turn_id,message_id) VALUES (?,?),(?,?) ON CONFLICT DO NOTHING`, turn, first, turn, second); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE logical_turns SET status='unknown',updated_at=2 WHERE logical_turn_id=?`, turn)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Read(ctx, func(tx *store.ReadTx) error {
		_, projected, err := readMessagingEvents(ctx, tx.Q(), after)
		if err != nil {
			return err
		}
		if len(projected) != 1 {
			t.Fatalf("want one scoped stream event, got %v", projected)
		}
		for seq, event := range projected {
			if event.EventSeq != seq || seq <= after || event.Kind != "messaging.turn_processing" ||
				event.ObjectType != "logical_turn" || event.ObjectID != turn || len(event.MessageIDs) != 2 ||
				!slices.Contains(event.MessageIDs, first) || !slices.Contains(event.MessageIDs, second) {
				t.Fatalf("unexpected shared turn event: %+v", event)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
