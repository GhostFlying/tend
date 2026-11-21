package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUnsubscribedDeliveryObservationsRetainFactsWithoutWakingLegacyIssue(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	first := pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", first, "closed", "")) {
		t.Fatal("initial terminal observation rejected")
	}
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 0 || h.count("outbox WHERE destination='main_thread' AND operation='main_thread.handle_event'") != 0 {
		t.Fatal("initial provider closure woke the agent")
	}
	// A repeated report leaves the plan open and retains the original fact.
	reportDelivery(t, h, "linux-1", "observer", first, "closed", "")
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 0 || h.work(item.WorkItemID).Status != "open" {
		t.Fatal("duplicate notice or automatic closure")
	}
	if op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(item.WorkItemID), protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(1)), RefreshDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := pollDelivery(t, h, "linux-1", "observer")[0]
	reportDelivery(t, h, "linux-1", "observer", next, "", "authentication_required")
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 0 {
		t.Fatal("first provider error woke the agent")
	}
	if op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(item.WorkItemID), protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(1)), RefreshDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next = pollDelivery(t, h, "linux-1", "observer")[0]
	reportDelivery(t, h, "linux-1", "observer", next, "", "authentication_required")
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 0 {
		t.Fatal("unchanged error spammed conversation")
	}
	if op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(item.WorkItemID), protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(1)), RefreshDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next = pollDelivery(t, h, "linux-1", "observer")[0]
	reportDelivery(t, h, "linux-1", "observer", next, "closed", "")
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 0 {
		t.Fatal("provider recovery woke the agent")
	}
	if got := readObservations(t, h, item.WorkItemID)[0]; got.State != "closed" || got.ErrorKind != "" {
		t.Fatal("closed state or recovery was lost from the ledger", got)
	}
}
