package domain

import (
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func NormalizeFollowup(p protocol.FollowupSpec, now int64, zone *time.Location) (protocol.FollowupSpec, error) {
	bad := func() (protocol.FollowupSpec, error) {
		return p, protocol.Errorf(protocol.CodeInvalidRequest, "followup needs purpose, instruction, source, IANA timezone and a valid bounded trigger")
	}
	if p.At > 253402300799999 || p.ExpiresAt > 253402300799999 {
		return bad()
	}
	if strings.TrimSpace(p.Purpose) == "" || len(p.Purpose) > 500 || strings.TrimSpace(p.Instruction) == "" || len(p.Instruction) > 8000 || p.SourceID == "" || p.Timezone == "" || p.Timezone == "Local" {
		return bad()
	}
	if p.Kind != "cron" && p.Cron != "" {
		return bad()
	}
	switch p.Kind {
	case "at":
		if p.At <= 0 || p.EverySeconds != 0 || p.Condition != nil || p.ExpirySilent || (p.ExpiresAt != 0 && p.ExpiresAt < p.At) {
			return bad()
		}
		p.MaxTriggers = 1
	case "every":
		if p.EverySeconds < 300 || p.EverySeconds > 31536000 || p.ExpiresAt <= now || p.At != 0 || p.Condition != nil || p.ExpirySilent {
			return bad()
		}
		if p.MaxTriggers == 0 {
			p.MaxTriggers = 20
		}
		if p.MaxTriggers < 1 || p.MaxTriggers > 1000 {
			return bad()
		}
	case "cron":
		if p.ExpiresAt <= now || p.At != 0 || p.EverySeconds != 0 || p.Condition != nil || p.ExpirySilent {
			return bad()
		}
		c, err := ParseCron(p.Cron, zone)
		if err != nil {
			return bad()
		}
		next, err := c.Next(time.UnixMilli(now))
		if err != nil || next.UnixMilli() >= p.ExpiresAt {
			return bad()
		}
		if p.MaxTriggers == 0 {
			p.MaxTriggers = 20
		}
		if p.MaxTriggers < 1 || p.MaxTriggers > 1000 {
			return bad()
		}
	case "condition":
		if p.Condition == nil || p.At != 0 || p.EverySeconds != 0 {
			return bad()
		}
		if p.ExpiresAt == 0 {
			p.ExpiresAt = now + 7*24*60*60*1000
		}
		if p.ExpiresAt <= 0 {
			return bad()
		}
		p.MaxTriggers = 1
		c := p.Condition
		switch c.Kind {
		case "tasks_completed":
			if len(c.TaskIDs) == 0 || len(c.TaskIDs) > 100 || c.DeliveryID != "" || c.DeliveryState != "" || c.DecisionID != "" || c.ChoiceID != "" {
				return bad()
			}
			seen := map[string]bool{}
			for _, id := range c.TaskIDs {
				if id == "" || seen[id] {
					return bad()
				}
				seen[id] = true
			}
		case "delivery_state":
			if c.DeliveryID == "" || (c.DeliveryState != "merged" && c.DeliveryState != "closed") || len(c.TaskIDs) > 0 || c.DecisionID != "" || c.ChoiceID != "" {
				return bad()
			}
		case "decision_answered":
			if c.DecisionID == "" || len(c.TaskIDs) > 0 || c.DeliveryID != "" || c.DeliveryState != "" || c.ChoiceID != "" {
				return bad()
			}
		case "choice_answered":
			if c.ChoiceID == "" || len(c.TaskIDs) > 0 || c.DeliveryID != "" || c.DeliveryState != "" || c.DecisionID != "" {
				return bad()
			}
		default:
			return bad()
		}
	default:
		return bad()
	}
	return p, nil
}

// FollowupDue uses committed facts, never guesses that unknown means satisfied.
func FollowupDue(r protocol.FollowupRule, itemStatus string, satisfied bool, now int64) (reason string, silent bool) {
	if r.Status != "active" || itemStatus == "closed" || (itemStatus == "snoozed" && r.Kind != "at") {
		return "", false
	}
	if r.Kind == "condition" && satisfied {
		return "condition", false
	}
	if r.ExpiresAt > 0 && now >= r.ExpiresAt {
		if r.Kind == "every" || r.Kind == "cron" {
			return "expired", true
		}
		return "expired", r.ExpirySilent
	}
	if r.Kind == "at" && now >= r.At {
		return "at", false
	}
	if (r.Kind == "every" || r.Kind == "cron") && now >= r.NextAt {
		return "every", false
	}
	return "", false
}
