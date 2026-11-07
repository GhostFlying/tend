package mainthread

import (
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestIssueStartupMigratesToolsetWithIdleSummariesDisabled(t *testing.T) {
	f := newHandoffFixture(t)
	m := f.e.m
	m.cfg.MaintenanceEnabled = false
	m.named = &hostedconfig.Selection{Purpose: agentconfig.IssueManager}
	instance := m.bindingInstance()
	if _, err := f.e.app.BindMainThread(t.Context(), instance, f.source.ID(), "old-issue-tools", rolloutPath(f.source)); err != nil {
		t.Fatal(err)
	}
	snap, err := f.e.app.HandoffSnapshot(t.Context(), instance)
	if err != nil {
		t.Fatal(err)
	}
	snap.Summary = &store.SummaryCoverage{Complete: true, Method: "single", Batches: 1}
	if _, err := f.e.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: instance, Snapshot: snap, Body: "retained issue context", FormatVersion: summaryFormatVersion}); err != nil {
		t.Fatal(err)
	}
	target, err := m.openThread(t.Context(), f.session)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID() == f.source.ID() {
		t.Fatal("Issue resumed obsolete dynamic tools because idle summaries are disabled")
	}
	fake := target.(*handoffTestThread).Thread.(*testkit.FakeThread)
	names := map[string]bool{}
	for _, tool := range fake.Config.Tools {
		names[tool.Name] = true
	}
	if !names["issues_review_delivery"] || !names["issues_delivery_reviews_read"] {
		t.Fatal("current Issue tool catalog missing", names)
	}
	if err := m.idleSummary(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, forks, _ := f.session.counts()
	if forks != 0 || m.cfg.MaintenanceEnabled {
		t.Fatal("Issue tool migration enabled model summaries", forks)
	}
}
