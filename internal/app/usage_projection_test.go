package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type projectionReadQueries struct {
	store.Querier
	queries []string
}

func (q *projectionReadQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.queries = append(q.queries, query)
	return q.Querier.QueryContext(ctx, query, args...)
}
func (q *projectionReadQueries) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.queries = append(q.queries, query)
	return q.Querier.QueryRowContext(ctx, query, args...)
}

func projectionView(t *testing.T, h *harness, kind string, before int64, filter views.UsageFilter) (protocol.TokenUsageView, []string) {
	t.Helper()
	var v protocol.TokenUsageView
	var queries []string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		q := &projectionReadQueries{Querier: tx.Q()}
		var err error
		v, err = views.TokenUsageFiltered(t.Context(), q, kind, 0, "", "", before, h.clock.Now(), filter)
		queries = q.queries
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return v, queries
}

func TestUsageProjectionSummariesPricesAndBoundedDetails(t *testing.T) {
	h := newHarness(t)
	quote := json.RawMessage(`{"data":[{"id":"test/main","pricing":{"prompt":"0.0000001","completion":"0.0000002","input_cache_read":"0.00000001","input_cache_write":"0.0000003"}}]}`)
	if err := h.app.RecordUsagePriceCatalog(t.Context(), quote); err != nil {
		t.Fatal(err)
	}
	at := (h.clock.Now().UnixMilli()/views.UsageBucketWidth - 2) * views.UsageBucketWidth
	var samples []protocol.TokenUsageSample
	for i := 0; i < 120; i++ {
		s := nativeSample(h.clock.Now().UnixMilli())
		s.RecordID, s.RequestID, s.TurnID = fmt.Sprint(i), fmt.Sprint(i), fmt.Sprint(i)
		s.Model = "main" // Model and role can have the same name.
		s.OccurredAt = usageNumber(at + int64(i))
		s.CacheRead, s.CacheWrite = usageNumber(60), usageNumber(10)
		samples = append(samples, s)
	}
	for range 2 {
		if err := h.app.RecordNativeConversationUsage(t.Context(), "main", samples); err != nil {
			t.Fatal(err)
		}
	}
	v, queries := projectionView(t, h, "total", 0, views.UsageFilter{})
	if v.Totals.Requests != 120 || v.Totals.Total != 13200 || v.Totals.EstimatedRequests != 120 || v.Totals.EstimatedUSD != "0.001032" || len(v.Models) != 1 || len(v.Roles) != 1 || len(v.Sessions) != 1 || len(v.Records) != 50 || !v.More {
		t.Fatalf("incorrect projection: %+v", v)
	}
	if len(queries) > 10 {
		t.Fatalf("read queries scale with request count: %v", queries)
	}
	for _, q := range queries {
		if strings.Contains(q, "payload_json") || strings.Contains(q, "token_usage_bindings") || strings.Contains(q, "run_logs") || strings.Contains(q, "catalog_json") {
			t.Fatalf("read recomputes original evidence: %s", q)
		}
	}
	if h.count("token_usage_summaries") != 1 || h.count("token_usage_contributions") != 120 {
		t.Fatal("requests were not grouped at write time")
	}
	page, _ := projectionView(t, h, "total", v.Next, views.UsageFilter{})
	if len(page.Records) != 50 || page.Records[0].Seq >= v.Next || page.Totals != v.Totals {
		t.Fatal("cursor changed aggregate or repeated a record")
	}
	last, _ := projectionView(t, h, "total", page.Next, views.UsageFilter{})
	if len(last.Records) != 20 || last.More {
		t.Fatal("wrong terminal page", last)
	}
	summary, queries := projectionView(t, h, "total", 0, views.UsageFilter{Summary: true})
	if summary.Next != v.Next || summary.More != v.More || len(summary.Records) != 0 {
		t.Fatal("summary changed the existing cursor contract")
	}
	for _, q := range queries {
		if strings.Contains(q, "record_json FROM") {
			t.Fatal("summary fetched request details")
		}
	}
	// Catalog updates reprice once in the write transaction, including historical buckets.
	h.clock.Advance(time.Second)
	quote = json.RawMessage(strings.ReplaceAll(string(quote), "0.0000002", "0.0000004"))
	if err := h.app.RecordUsagePriceCatalog(t.Context(), quote); err != nil {
		t.Fatal(err)
	}
	v, _ = projectionView(t, h, "total", 0, views.UsageFilter{})
	if v.Totals.EstimatedUSD != "0.001272" || v.Records[0].Estimate.USD != "0.0000106" {
		t.Fatal("quote update left stale contributions", v.Totals)
	}
}

func TestUsageProjectionExactWindowEdgesAndAdvancingClock(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now().UnixMilli()
	instants := []*int64{nil, usageNumber(now - 24*60*60*1000 - 1), usageNumber(now - 24*60*60*1000), usageNumber(now - 1), usageNumber(now), usageNumber(now + 1), usageNumber(now + 2*views.UsageBucketWidth)}
	for i, at := range instants {
		s := nativeSample(now)
		s.RecordID, s.RequestID = fmt.Sprint(i), fmt.Sprint(i)
		s.OccurredAt = at
		if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{s}); err != nil {
			t.Fatal(err)
		}
	}
	v, _ := projectionView(t, h, "24h", 0, views.UsageFilter{})
	if v.Totals.Requests != 2 || v.Unattributed.Requests != 4 || len(v.Records) != 2 || len(v.Sessions) != 1 || *v.Sessions[0].FirstAt != *instants[2] || *v.Sessions[0].LastAt != *instants[3] {
		t.Fatal("incorrect inclusive start/exclusive end", v)
	}
	v, _ = projectionView(t, h, "total", 0, views.UsageFilter{})
	if v.Totals.Requests != 7 || v.Unattributed.Requests != 4 {
		t.Fatal("future native time did not remain unknown", v)
	}
	// No new usage write is necessary when a future native timestamp becomes past.
	h.clock.Advance(2 * time.Millisecond)
	v, _ = projectionView(t, h, "24h", 0, views.UsageFilter{})
	if v.Totals.Requests != 3 || v.Unattributed.Requests != 2 {
		t.Fatal("clock advance froze a contribution in its old state", v)
	}
	var historical protocol.TokenUsageView
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		historical, err = views.TokenUsage(t.Context(), tx.Q(), "month", -1, "", "", 0, h.clock.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if historical.Unattributed.Requests != 2 {
		t.Fatal("historical window lost future/unknown coverage", historical)
	}
}

func TestUsageProjectionAttributionUpdatesAndRollback(t *testing.T) {
	h, as := messageRun(t)
	source := LocalUsageSource{ID: "central", RunID: as.RunID, Source: json.RawMessage(`{"thread_id":"native-thread","session_id":"native-session","provider":"traex"}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := h.app.CommitLocalUsage(t.Context(), source, []protocol.TokenUsageSample{nativeSample(h.clock.Now().UnixMilli())}); err != nil {
		t.Fatal(err)
	}
	before, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET title='corrected title' WHERE task_id=?`, as.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if after.Records[0].Attribution.TaskTitle != "corrected title" || before.Totals != after.Totals {
		t.Fatal("historical attribution was frozen", after)
	}
	sentinel := errors.New("rollback fixture")
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE tasks SET title='rolled back' WHERE task_id=?`, as.TaskID); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	after, _ = projectionView(t, h, "total", 0, views.UsageFilter{})
	if after.Records[0].Attribution.TaskTitle != "corrected title" || h.count("token_usage_dirty") != 0 {
		t.Fatal("failed write leaked partial projection")
	}
}

func TestUsageProjectionIssueHierarchyAndActorCorrections(t *testing.T) {
	h := newHarness(t)
	parent, child := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
	s := protocol.TokenUsageSample{Source: "legacy", RequestID: "request", ThreadID: "thread", Revision: 1, Settlement: "final", Total: usageNumber(7)}
	if err := h.app.RecordTokenUsage(t.Context(), "issue:"+child.IssueID, s); err != nil {
		t.Fatal(err)
	}
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE issue_controls SET parent_issue_id=? WHERE issue_id=?`, parent.IssueID, child.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	v, _ := projectionView(t, h, "total", 0, views.UsageFilter{Issue: parent.IssueID})
	if v.Totals.Total != 7 || len(v.Records[0].Attribution.IssuePath) != 2 {
		t.Fatal("parent move did not update descendant contributions", v)
	}
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_items SET body_json=json_set(body_json,'$.title','corrected parent') WHERE work_item_id=?`, parent.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	v, _ = projectionView(t, h, "total", 0, views.UsageFilter{Issue: parent.IssueID})
	if v.Records[0].Attribution.IssuePath[0].Title != "corrected parent" {
		t.Fatal("ancestor title was frozen", v)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_items SET body_json=json_set(body_json,'$.intent','new intent') WHERE work_item_id=?`, parent.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if h.count("token_usage_dirty") != 0 {
		t.Fatal("unrelated Issue edit invalidated usage")
	}
	source := LocalUsageSource{ID: "actor", Source: json.RawMessage(`{"thread_id":"native-thread","session_id":"native-session","provider":"traex","actor_name":"old actor","actor_id":"actor"}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := h.app.CommitLocalUsage(t.Context(), source, []protocol.TokenUsageSample{nativeSample(h.clock.Now().UnixMilli())}); err != nil {
		t.Fatal(err)
	}
	v, _ = projectionView(t, h, "total", 0, views.UsageFilter{Agent: "old actor"})
	if v.Totals.Total != 110 {
		t.Fatal("actor fixture was not attributed", v)
	}
	source.Source = json.RawMessage(strings.ReplaceAll(string(source.Source), "old actor", "corrected actor"))
	if err := h.app.CommitLocalUsage(t.Context(), source, nil); err != nil {
		t.Fatal(err)
	}
	v, _ = projectionView(t, h, "total", 0, views.UsageFilter{Agent: "corrected actor"})
	if v.Totals.Total != 110 || v.Records[0].Attribution.Agent != "corrected actor" {
		t.Fatal("actor correction did not update filters", v)
	}
}

func TestUsageProjectionMigrationBackfillAndReopen(t *testing.T) {
	h := newHarness(t)
	s := nativeSample(h.clock.Now().UnixMilli())
	// Simulate pre-projection persisted facts. A read cannot silently show stale totals.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return recordTokenUsage(t.Context(), tx, s, "session:native-session", "native", h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
		return err
	}); err == nil {
		t.Fatal("dirty projection appeared current")
	}
	if err := h.app.RefreshUsageProjection(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if v.Totals.Total != 110 {
		t.Fatal(v)
	}
	// A separate ledger exercises actual close/open persistence without replaying facts.
	path := filepath.Join(t.TempDir(), "reopen.db")
	db, err := openAppFixture(t.Context(), path, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	a := New(db, h.ids, h.clock.Now)
	if err := a.RecordTokenUsage(t.Context(), "main", s); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(t.Context(), path, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
		if err == nil && view.Totals.Total != 110 {
			t.Fatal(view)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageProjectionLegacyRevisionReleasesPendingNativeOverlap(t *testing.T) {
	h := newHarness(t)
	legacy := protocol.TokenUsageSample{Source: "legacy", RequestID: "response", ThreadID: "native-thread", TurnID: "native-turn", Revision: 1, Settlement: "final", Total: usageNumber(100)}
	if err := h.app.RecordTokenUsage(t.Context(), "main", legacy); err != nil {
		t.Fatal(err)
	}
	native := nativeSample(h.clock.Now().UnixMilli())
	native.RequestID = legacy.RequestID
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{native}); err != nil {
		t.Fatal(err)
	}
	before, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if before.Totals.Requests != 1 || before.Totals.Total != 100 || before.Conflicts != 1 {
		t.Fatal("fixture should retain legacy on unmatched overlap", before)
	}
	// The old legacy sequence is no longer current, so its unresolved native peer
	// must be released even when the new legacy fact creates no alias itself.
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		legacy.Revision = 2
		legacy.TurnID = "corrected-turn"
		return recordTokenUsage(t.Context(), tx, legacy, "conversation:main", "main", h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if after.Totals.Requests != 2 || after.Totals.Total != 210 || after.Conflicts != 0 {
		t.Fatal("old legacy sequence kept native peer quarantined", after)
	}
}

func TestUsageProjectionDirtyAndExecutionPlansStayWithinIndexedScope(t *testing.T) {
	h := newHarness(t)
	var samples []protocol.TokenUsageSample
	for i := 0; i < 128; i++ {
		s := nativeSample(h.clock.Now().UnixMilli())
		s.RecordID, s.RequestID = fmt.Sprint(i), fmt.Sprint(i)
		samples = append(samples, s)
	}
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", samples); err != nil {
		t.Fatal(err)
	}
	// Registering a different native source must not invalidate the main
	// conversation's entire historical ledger.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO token_usage_local_sources(source_key,conversation_id,source_json,updated_at) VALUES('unrelated','main','{"thread_id":"unrelated","session_id":"unrelated"}',1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if h.count("token_usage_dirty") != 0 {
		t.Fatal("unrelated actor invalidated historical requests")
	}
	s := nativeSample(h.clock.Now().UnixMilli())
	s.RecordID, s.RequestID = "new", "new"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return recordTokenUsage(t.Context(), tx, s, "session:native-session", "native", h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		q := &projectionReadQueries{Querier: tx.Q()}
		inputs, err := views.UsageProjectionInputs(t.Context(), q)
		if err != nil {
			return err
		}
		if len(inputs) != 1 {
			t.Fatal("dirty read included unrelated history", len(inputs))
		}
		queries := []string{usageDirtyGroupsSQL, expandUsageOverlapSQL}
		for _, query := range q.queries {
			if strings.Contains(query, "FROM token_usage_dirty d") {
				queries = append(queries, query)
			}
		}
		// Exercise the SELECT embedded in the actual migration trigger, rather
		// than a hand-maintained query that could diverge from production.
		var trigger string
		if err = tx.Q().QueryRowContext(t.Context(), `SELECT sql FROM sqlite_schema WHERE name='usage_projection_local_hosted_executions_insert'`).Scan(&trigger); err != nil {
			return err
		}
		statement := regexp.MustCompile(`(?s)INSERT INTO token_usage_dirty .*?;`).FindString(trigger)
		if statement == "" {
			t.Fatal("missing hosted invalidation statement")
		}
		queries = append(queries, strings.ReplaceAll(statement, "NEW.execution_id", "'new-execution'"))
		for _, query := range queries {
			rows, err := tx.Q().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
					rows.Close()
					return err
				}
				if strings.HasPrefix(detail, "SCAN c") || strings.HasPrefix(detail, "SCAN p") {
					t.Fatalf("write scans historical current/contributions: %s\n%s", detail, query)
				}
			}
			if err = rows.Err(); err != nil {
				rows.Close()
				return err
			}
			if err = rows.Close(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageProjectionOverlapInvalidationFollowsOnlyConnectedPeers(t *testing.T) {
	h := newHarness(t)
	var native []protocol.TokenUsageSample
	for i := 0; i < 20; i++ {
		s := nativeSample(h.clock.Now().UnixMilli())
		s.RecordID, s.RequestID, s.ThreadID, s.TurnID = fmt.Sprintf("native-%d", i), fmt.Sprintf("response-%d", i), fmt.Sprintf("thread-%d", i), fmt.Sprintf("turn-%d", i)
		legacy := protocol.TokenUsageSample{Source: "legacy", RequestID: s.RequestID, ThreadID: s.ThreadID, TurnID: s.TurnID, Revision: 1, Settlement: "final", Total: usageNumber(110)}
		if err := h.app.RecordTokenUsage(t.Context(), "main", legacy); err != nil {
			t.Fatal(err)
		}
		if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{s}); err != nil {
			t.Fatal(err)
		}
		native = append(native, s)
	}
	// Two legacy sequences share a native peer, so invalidation must traverse
	// that dependency while leaving the other eighteen pairs untouched.
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO token_usage_legacy_candidates(legacy_seq,native_source,native_request_id,matched) SELECT fact_seq,?,?,0 FROM token_usage_current WHERE source='legacy' AND request_id=?`, native[1].Source, native[1].RecordID, native[0].RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE token_usage_current SET conflict=1 WHERE source=? AND request_id=?`, native[0].Source, native[0].RecordID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), expandUsageOverlapSQL)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := h.count("token_usage_dirty"); n != 4 {
		t.Fatalf("overlap invalidated unrelated history or missed peers: got %d, want 4", n)
	}
	if err := h.app.RefreshUsageProjection(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if v.Totals.Requests != 20 || v.Totals.Total != 2200 || v.Conflicts != 2 || v.Collection.LegacyRequests != 2 || h.count("token_usage_overlap_dirty") != 0 {
		t.Fatal("connected conflict lost legacy fallback or conservation", v)
	}
}

func TestUsageProjectionUnknownTimeHistoryHasBoundedWritePartitions(t *testing.T) {
	h := newHarness(t)
	const count = 5000
	s := protocol.TokenUsageSample{Source: "legacy", ThreadID: "thread", Revision: 1, Settlement: "final", Total: usageNumber(1)}
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		for i := 0; i < count; i++ {
			s.RequestID = fmt.Sprint(i)
			if err := recordTokenUsage(t.Context(), tx, s, "conversation:main", "main", h.clock.Now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check := func(want int64) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var largest int64
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT MAX(n) FROM (SELECT COUNT(*) n FROM token_usage_contributions GROUP BY dimension_key,bucket)`).Scan(&largest); err != nil {
				return err
			}
			if largest > usageSummaryPartitionSize {
				t.Fatalf("unknown-time history creates an unbounded write bucket: %d", largest)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		all, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
		period, _ := projectionView(t, h, "24h", 0, views.UsageFilter{})
		if all.Totals.Requests != want || all.Unattributed.Requests != want || period.Totals.Requests != 0 || period.Unattributed.Requests != want {
			t.Fatal("partitioning changed unknown-time accounting", all.Totals, all.Unattributed, period.Totals, period.Unattributed)
		}
	}
	check(count)
	s.RequestID = "appended"
	if err := h.app.RecordTokenUsage(t.Context(), "main", s); err != nil {
		t.Fatal(err)
	}
	check(count + 1)
}
