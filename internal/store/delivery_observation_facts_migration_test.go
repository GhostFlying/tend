package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func observationMigrationStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "schema104.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s := &Store{write: db, read: db, now: testkit.NewClock().Now}
	if err := s.migrate(t.Context(), 104); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeliveryObservationFactsMigrationPreservesLatestAndOriginals(t *testing.T) {
	for _, name := range []string{"newer canonical", "newer runner", "newer runner failure", "equal canonical", "empty target", "latest unknown", "first unknown"} {
		t.Run(name, func(t *testing.T) {
			s := observationMigrationStore(t)
			ref := protocol.IssueDelivery{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17}
			issue := canonicalIssueFixture("wi_00000000000000000000000001")
			issue.Plan.Deliveries = []protocol.IssueDelivery{ref}
			saveCanonicalFixture(t, s, issue, 0)
			legacy := protocol.DeliveryObservation{Observer: "runner", WorkItemID: issue.IssueID, DeliveryID: ref.DeliveryID, Generation: 7, AttemptedAt: 10, CheckedAt: 10, BaselineUpgradeID: "legacy-baseline", BaselineUpgradeRevision: 2, DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: "runner-head", BaseSHA: "base"}}
			canonical := protocol.DeliveryObservation{Observer: "server", AttemptedAt: 20, CheckedAt: 20, BaselineUpgradeID: "server-baseline", BaselineUpgradeRevision: 3, DeliverySnapshot: protocol.DeliverySnapshot{State: "merged", HeadSHA: "server-head", BaseSHA: "base", TargetSHA: "target", MergeSHA: "merge", TargetContainsMerge: true, CI: &protocol.DeliveryCI{HeadSHA: "server-head", State: "success"}, MergedBy: &protocol.DeliveryActor{Login: "original-merger"}, ClosedBy: &protocol.DeliveryActor{Login: "original-closer"}}}
			wantHead, wantError := canonical.HeadSHA, ""
			switch name {
			case "newer runner":
				legacy.AttemptedAt, legacy.CheckedAt = 30, 30
				wantHead = legacy.HeadSHA
			case "newer runner failure":
				legacy.AttemptedAt, legacy.ErrorKind = 30, "authentication_required"
				legacy.CI = &protocol.DeliveryCI{HeadSHA: "stale-head", State: "unknown", ErrorKind: "timeout", Truncated: true}
				legacy.MergedBy = &protocol.DeliveryActor{ID: "stale-merger-id", Login: "stale-merger"}
				legacy.ClosedBy = &protocol.DeliveryActor{ID: "stale-closer-id", Login: "stale-closer"}
				wantHead, wantError = canonical.HeadSHA, legacy.ErrorKind
			case "equal canonical":
				legacy.AttemptedAt, legacy.CheckedAt = 20, 20
			case "empty target":
				canonical = protocol.DeliveryObservation{}
				wantHead = legacy.HeadSHA
			case "latest unknown":
				legacy.AttemptedAt, legacy.CheckedAt, legacy.ErrorKind = 30, 0, "timeout"
				legacy.DeliverySnapshot = protocol.DeliverySnapshot{}
				wantHead, wantError = canonical.HeadSHA, "timeout"
			case "first unknown":
				canonical = protocol.DeliveryObservation{}
				legacy.AttemptedAt, legacy.CheckedAt, legacy.ErrorKind = 30, 0, "timeout"
				legacy.DeliverySnapshot = protocol.DeliverySnapshot{}
				wantHead, wantError = "", "timeout"
			}
			legacyRaw, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			canonicalRaw, err := json.Marshal(canonical)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, s, `INSERT INTO delivery_observations(work_item_id,delivery_id,generation,nonce,host_id,instance_id,query_until,next_check_at,refresh_requested,server_refresh_revision,observation_json) VALUES(?,?,7,'','original-host','original-instance',0,60,1,4,?)`, issue.IssueID, ref.DeliveryID, string(legacyRaw))
			mustExec(t, s, `UPDATE server_delivery_jobs SET observation_json=?,refresh_revision=4,observed_refresh_revision=3 WHERE provider=? AND repository=? AND number=?`, string(canonicalRaw), ref.Provider, ref.Repository, ref.Number)
			if err := s.migrate(t.Context(), 105); err != nil {
				t.Fatal(err)
			}
			row := readCurrentObservation(t, s, issue.IssueID, ref.DeliveryID)
			if row.Observation.HeadSHA != wantHead || row.Observation.ErrorKind != wantError || !row.Observation.RefreshPending || row.Observation.Generation != 7 || row.HostID != "original-host" || row.InstanceID != "original-instance" || row.ServerRefreshRevision != 4 || row.NextCheckAt != 60 || row.ObservedRevision != 0 {
				t.Fatalf("migration lost latest facts, unknown or query identity: %+v", row)
			}
			if name == "first unknown" && row.Observation.State != "unknown" {
				t.Fatal("latest failed unknown became an older success", row)
			}
			if name == "newer runner failure" || name == "latest unknown" {
				if row.Observation.AttemptedAt != 30 || row.Observation.CheckedAt != 20 || row.Observation.BaselineUpgradeID != "legacy-baseline" || row.Observation.BaselineUpgradeRevision != 2 || row.Observation.State != "merged" || !row.Observation.TargetContainsMerge || row.Observation.MergeSHA != "merge" || row.Observation.CI == nil || row.Observation.CI.HeadSHA != "server-head" || row.Observation.MergedBy == nil || row.Observation.MergedBy.Login != "original-merger" {
					t.Fatal("latest failed attempt rolled back known Snapshot or invented its query baseline", row)
				}
				if !reflect.DeepEqual(row.Observation.DeliverySnapshot, canonical.DeliverySnapshot) {
					t.Fatalf("highest-checked Snapshot was recursively mixed with old optional fields: got %+v, want %+v", row.Observation.DeliverySnapshot, canonical.DeliverySnapshot)
				}
			}
			if err := s.Read(t.Context(), func(tx *ReadTx) error {
				var retained, query, fact string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT observation_json FROM retained_delivery_observations WHERE work_item_id=? AND delivery_id=?`, issue.IssueID, ref.DeliveryID).Scan(&retained); err != nil {
					return err
				}
				if retained != string(legacyRaw) {
					t.Fatal("original observation bytes changed")
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT query_json FROM delivery_observations WHERE work_item_id=? AND delivery_id=?`, issue.IssueID, ref.DeliveryID).Scan(&query); err != nil {
					return err
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(query), &fields); err != nil {
					return err
				}
				if len(fields) != 3 || fields["head_sha"] != nil || fields["state"] != nil {
					t.Fatal("active query still contains a provider snapshot", query)
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT observation_json FROM server_delivery_jobs WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number).Scan(&fact); err != nil {
					return err
				}
				if err := json.Unmarshal([]byte(fact), &fields); err != nil {
					return err
				}
				if fields["work_item_id"] != nil || fields["delivery_id"] != nil || fields["generation"] != nil || fields["refresh_pending"] != nil {
					t.Fatal("target still belongs to an Issue", fact)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeliveryObservationFactsMigrationFreezesOriginalQueryBaseline(t *testing.T) {
	s := observationMigrationStore(t)
	ref := protocol.IssueDelivery{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17}
	issue := canonicalIssueFixture("wi_00000000000000000000000001")
	issue.Plan.Deliveries = []protocol.IssueDelivery{ref}
	saveCanonicalFixture(t, s, issue, 0)
	const raw = `{ "state":"open", "head_sha":"old-head", "checked_at":10, "attempted_at":10, "baseline_upgrade_id":"pending-query-baseline", "baseline_upgrade_revision":7 }`
	mustExec(t, s, `INSERT INTO delivery_observations(work_item_id,delivery_id,generation,nonce,host_id,instance_id,query_until,observation_json) VALUES(?,?,8,'original-query','server','server',90,?)`, issue.IssueID, ref.DeliveryID, raw)
	mustExec(t, s, `INSERT INTO server_delivery_queries(query_id,provider,repository,number,status,started_at) VALUES('original-query',?,?,?,'reading',20)`, ref.Provider, ref.Repository, ref.Number)
	mustExec(t, s, `INSERT INTO server_delivery_members(query_id,issue_id,delivery_id,generation) VALUES('original-query',?,?,8)`, issue.IssueID, ref.DeliveryID)
	mustExec(t, s, `UPDATE server_delivery_jobs SET nonce='original-query',query_until=90 WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number)
	if err := s.migrate(t.Context(), 105); err != nil {
		t.Fatal(err)
	}
	row := readCurrentObservation(t, s, issue.IssueID, ref.DeliveryID)
	if row.Nonce != "original-query" || row.QueryUntil != 90 || row.Observation.Generation != 8 || row.QueryBaselineUpgradeID != "pending-query-baseline" || row.QueryBaselineUpgradeRevision != 7 || !row.Observation.RefreshPending {
		t.Fatal("in-flight original query was changed", row)
	}
	if row.Observation.BaselineUpgradeID != "" || row.Observation.BaselineUpgradeRevision != 0 {
		t.Fatal("pending query's later baseline was attributed to older facts", row)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var baseline string
		var revision int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT baseline_upgrade_id,baseline_upgrade_revision FROM server_delivery_queries WHERE query_id='original-query'`).Scan(&baseline, &revision); err != nil {
			return err
		}
		if baseline != row.QueryBaselineUpgradeID || revision != row.QueryBaselineUpgradeRevision {
			t.Fatal("original query history lost its bound baseline", baseline, revision)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
