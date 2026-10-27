package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDeliveryNoticesWakeOnceWithoutSettlingPlan(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	first := pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", first, "closed", "")) {
		t.Fatal("initial terminal observation rejected")
	}
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 1 || h.count("outbox WHERE destination LIKE 'background_thread:%'") != 1 {
		t.Fatal("initial terminal observation did not wake the agent once")
	}
	// A repeated report cannot produce a second notification or settle intent.
	reportDelivery(t, h, "linux-1", "observer", first, "closed", "")
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 1 || h.work(item.WorkItemID).Status != "open" {
		t.Fatal("duplicate notice or automatic closure")
	}
	if op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(item.WorkItemID), protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(1)), RefreshDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := pollDelivery(t, h, "linux-1", "observer")[0]
	reportDelivery(t, h, "linux-1", "observer", next, "", "authentication_required")
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 2 {
		t.Fatal("first error did not notify")
	}
	if op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(item.WorkItemID), protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(1)), RefreshDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next = pollDelivery(t, h, "linux-1", "observer")[0]
	reportDelivery(t, h, "linux-1", "observer", next, "", "authentication_required")
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 2 {
		t.Fatal("unchanged error spammed conversation")
	}
	if op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(item.WorkItemID), protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(1)), RefreshDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next = pollDelivery(t, h, "linux-1", "observer")[0]
	reportDelivery(t, h, "linux-1", "observer", next, "closed", "")
	if h.count("message_notices WHERE notice_type='work_item_updated'") != 3 {
		t.Fatal("recovery did not notify")
	}
}
