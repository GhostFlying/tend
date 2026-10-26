package contextbuilder

import (
	"context"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func workItemLine(w protocol.WorkItemView) string {
	var parts []string
	for _, stage := range w.Stages {
		if stage.Status != "retired" && stage.Status != "cancelled" {
			parts = append(parts, fmt.Sprintf("%s=%s", stage.Title, stage.Status))
		}
	}
	line := fmt.Sprintf("%s %q, revision %d, %s; stages: %s", w.WorkItemID, w.Title, w.Revision, w.Column, strings.Join(parts, "; "))
	if len(w.PendingReviews) > 0 {
		line += fmt.Sprintf("; %d completed execution(s) need plan reconciliation (read pending_reviews and acceptance_id)", len(w.PendingReviews))
	}
	for _, choice := range w.Choices {
		line += "; " + workItemChoiceLine(choice)
	}
	for _, next := range w.FollowOns {
		if next.Status == "open" {
			line += fmt.Sprintf("; %s: %s (source: %s)", next.Kind, next.Text, next.Source)
		}
	}
	for _, delivery := range w.Deliveries {
		line += fmt.Sprintf("; delivery %s %s %s#%d (read work_items_read for last verified state)", delivery.DeliveryID, delivery.Provider, delivery.Repository, delivery.Number)
	}
	if w.Followups != nil && w.Followups.Active+w.Followups.Paused > 0 {
		line += fmt.Sprintf("; followups: %d waiting, %d paused, next UTC-ms %d, %q (read work_items_followups_list; waiting is not running)", w.Followups.Active, w.Followups.Paused, w.Followups.NextAt, w.Followups.Purpose)
	}
	if w.Column == "unplanned" {
		line += "; this round may be complete, next steps remain undecided"
	}
	return line
}

func gatherWorkItems(ctx context.Context, q store.Querier, s *Snapshot, sc *scope) error {
	page, err := views.WorkItems(ctx, q, "", false, "")
	if err != nil {
		return err
	}
	for _, item := range page.Items {
		line := sc.label(item.Project, workItemLine(item))
		if sc.covers(item.Project) {
			s.WorkItems = append(s.WorkItems, line)
		} else if item.Column == "attention" {
			s.OtherAttention = append(s.OtherAttention, line)
		}
	}
	return nil
}

func workItemChoiceLine(choice protocol.WorkItemChoice) string {
	line := fmt.Sprintf("next-step choice %s v%d %s: %q", choice.ChoiceID, choice.Version, choice.Status, clipRunes(choice.Question, 160))
	if choice.Answer != "" {
		label := "owner answer"
		if choice.AnswerSourceKind == "addon_relay" {
			label = "owner answer relayed by addon agent"
		}
		line += fmt.Sprintf("; %s: %q (source: %s)", label, clipRunes(choice.Answer, 500), choice.AnswerSourceID)
	}
	if choice.Reason != "" {
		line += fmt.Sprintf("; reason: %s", clipRunes(choice.Reason, 200))
	}
	if choice.ReplacedBy != "" {
		line += "; replaced by " + choice.ReplacedBy
	}
	if choice.Supersedes != "" {
		line += "; replaces " + choice.Supersedes
	}
	return line + " (read work_item_choices_list; do not repeat a deferred or answered question)"
}
