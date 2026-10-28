package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestSummaryBatchesPreserveAllUTF8Parts(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"text": strings.Repeat("用户约束\"\n", 3000)})
	batches := summaryBatches([]json.RawMessage{raw}, 4096)
	parts := []string{}
	count := 0
	for _, batch := range batches {
		b, _ := json.Marshal(batch)
		if len(b) > 4096 {
			t.Fatal("batch exceeds selected input share", len(b))
		}
		for _, record := range batch {
			var part struct {
				Part  int    `json:"part"`
				Parts int    `json:"parts"`
				Text  string `json:"json_text_part"`
			}
			if err := json.Unmarshal(record, &part); err != nil {
				t.Fatal(err)
			}
			if part.Part != len(parts)+1 || !utf8.ValidString(part.Text) {
				t.Fatal("lost ordering or UTF-8")
			}
			count = part.Parts
			parts = append(parts, part.Text)
		}
	}
	if count != len(parts) || strings.Join(parts, "") != string(raw) {
		t.Fatal("large object was truncated")
	}
}

func TestSummaryMergeFailureNeverSavesIntermediateCoverage(t *testing.T) {
	f := newHandoffFixture(t)
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "large-correction", Author: "system", Body: strings.Repeat("x", 300000)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, _ harness.TurnInput) string {
		calls++
		if calls == 1 {
			th.Say(id, "history", "History only")
			return "completed"
		}
		return "failed"
	}
	_, ok, err := f.e.m.prepareSummary(t.Context(), f.session, f.source)
	if err == nil || ok {
		t.Fatal("failed merge reported success")
	}
	if n := f.e.count(`SELECT COUNT(*) FROM conversation_summaries`); n != 0 {
		t.Fatal("intermediate summary was persisted as coverage", n)
	}
	_, forks, released := f.session.counts()
	if forks < 2 || released != forks {
		t.Fatal("summary branch leaked", forks, released)
	}
}

func TestLegacySummaryIsReferenceUntilCompleteV3(t *testing.T) {
	snap := store.HandoffSnapshot{Instance: "main", ThreadID: "source", Version: 3}
	s := store.HandoffSummary{Snapshot: snap, FormatVersion: 2}
	if summaryCovers(s, snap) {
		t.Fatal("v2 became current under the v3 contract")
	}
	s.FormatVersion = 3
	if summaryCovers(s, snap) {
		t.Fatal("missing coverage became complete")
	}
	s.Snapshot.Summary = &store.SummaryCoverage{Complete: true}
	if !summaryCovers(s, snap) {
		t.Fatal("complete matching v3 was stale")
	}
}

func TestNewerPreparedInputHasItsOwnDurableAttempt(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := t.Context()
	old, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.e.app.ClaimSummaryAttempt(ctx, old, (15 * time.Minute).Milliseconds()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "late-native-completion", Author: "system", Body: "Late native result reached the ledger"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input, err := f.e.app.HandoffSummaryInput(ctx, "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if old.Covers(input.Snapshot) {
		t.Fatal("fixture did not advance coverage")
	}
	if ok, err := f.e.m.reclaimSummaryInput(ctx, old, input.Snapshot); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := f.e.app.FinishSummaryAttempt(ctx, input.Snapshot, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.e.app.ClaimSummaryAttempt(ctx, input.Snapshot, 0); err != nil || ok {
		t.Fatal("new input failure bypassed backoff", ok, err)
	}
	if n := f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts WHERE status='failed'`); n != 2 {
		t.Fatal("attempt bookkeeping lost its exact keys", n)
	}
}

type lostSummaryStart struct {
	harness.Thread
	identity string
	calls    int
}

func (t *lostSummaryStart) StartTurn(_ context.Context, in harness.TurnInput) (string, error) {
	t.identity = in.UserMessageID
	t.calls++
	return "", context.DeadlineExceeded
}
func (t *lostSummaryStart) FindTurn(_ context.Context, identity, _ string) (harness.TurnReceipt, bool, error) {
	if identity != t.identity || identity == "" {
		return harness.TurnReceipt{}, false, nil
	}
	return harness.TurnReceipt{ID: "confirmed-private-turn", Status: "completed", Messages: []harness.Event{{Kind: harness.EventItemCompleted, ItemType: harness.ItemAgentMessage, Text: "Recovered summary; unknown business outcomes remain unknown."}}}, true, nil
}
func TestUnknownSummaryStartRecoversExactReceiptWithoutResending(t *testing.T) {
	f := newHandoffFixture(t)
	fork, forker, err := f.e.m.summaryFork(t.Context(), f.session, f.source)
	if err != nil {
		t.Fatal(err)
	}
	unknown := &lostSummaryStart{Thread: fork}
	snap, err := f.e.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := f.e.m.collectSummary(t.Context(), unknown, forker, snap, "Summarize this frozen history", harness.ModelSelection{})
	if err != nil || !strings.Contains(summary.Body, "Recovered summary") || unknown.calls != 1 {
		t.Fatal(summary, err, unknown.calls)
	}
	_, forks, released := f.session.counts()
	if forks != released {
		t.Fatal("private branch leaked", forks, released)
	}
}

type failedSummaryRelease struct{ *handoffTestSession }

func (s failedSummaryRelease) ReleaseThread(ctx context.Context, id string) error {
	if err := s.handoffTestSession.ReleaseThread(ctx, id); err != nil {
		return err
	}
	return errors.New("release acknowledgment was not confirmed")
}
func TestSummaryReleaseFailureDoesNotCertifyCoverage(t *testing.T) {
	f := newHandoffFixture(t)
	_, ok, err := f.e.m.prepareSummary(t.Context(), failedSummaryRelease{f.session}, f.source)
	if err == nil || ok {
		t.Fatal("unconfirmed cleanup certified a complete result", err)
	}
	if n := f.e.count(`SELECT COUNT(*) FROM conversation_summaries`); n != 0 {
		t.Fatal("cleanup failure persisted coverage", n)
	}
	if n := f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts WHERE status='failed'`); n != 1 {
		t.Fatal("cleanup failure escaped durable backoff", n)
	}
}
