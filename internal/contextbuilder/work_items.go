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
	for _, next := range w.FollowOns {
		if next.Status == "open" {
			line += fmt.Sprintf("; %s: %s (source: %s)", next.Kind, next.Text, next.Source)
		}
	}
	for _, delivery := range w.Deliveries {
		line += fmt.Sprintf("; delivery %s %s %s#%d (read work_items_read for last verified state)", delivery.DeliveryID, delivery.Provider, delivery.Repository, delivery.Number)
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
