package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
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
