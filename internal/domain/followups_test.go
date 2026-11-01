package domain

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFollowupValidationAndExpiryPrecedence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	base := protocol.FollowupSpec{Purpose: "check", Instruction: "read facts", SourceID: "source", Timezone: "America/New_York", Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "tasks_completed", TaskIDs: []string{"a", "b"}}}
	p, err := NormalizeFollowup(base, now, time.UTC)
	if err != nil || p.ExpiresAt != now+7*24*60*60*1000 {
		t.Fatal(p, err)
	}
	r := protocol.FollowupRule{FollowupSpec: p, Status: "active"}
	if why, _ := FollowupDue(r, "open", true, p.ExpiresAt); why != "condition" {
		t.Fatal("expiry overrode proven facts")
	}
	if why, _ := FollowupDue(r, "open", false, p.ExpiresAt); why != "expired" {
		t.Fatal("expired condition did not wake")
	}
	if why, _ := FollowupDue(r, "snoozed", true, p.ExpiresAt); why != "" {
		t.Fatal("snoozed condition woke")
	}
	base.Kind = "every"
	base.Condition = nil
	base.EverySeconds = 299
	base.ExpiresAt = now + 3600000
	if _, err := NormalizeFollowup(base, now, time.UTC); err == nil {
		t.Fatal("unbounded short interval")
	}
	base.EverySeconds = 300
	p, err = NormalizeFollowup(base, now, time.UTC)
	if err != nil || p.MaxTriggers != 20 {
		t.Fatal(p, err)
	}
	base.ExpiresAt = 0
	if _, err := NormalizeFollowup(base, now, time.UTC); err == nil {
		t.Fatal("unbounded every rule accepted")
	}
	base.Kind = "at"
	base.EverySeconds = 0
	base.At = now - 1
	p, err = NormalizeFollowup(base, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if why, _ := FollowupDue(protocol.FollowupRule{FollowupSpec: p, Status: "active"}, "snoozed", false, now); why != "at" {
		t.Fatal("explicit overdue check lost")
	}
}
