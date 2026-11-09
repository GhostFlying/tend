package store

import "testing"

func TestRunnerLateReportDispositionMigrationPreservesOriginals(t *testing.T) {
	s := observationMigrationStore(t)
	if err := s.migrate(t.Context(), 105); err != nil {
		t.Fatal(err)
	}
	const originalPayload = "{\n  \"nonce\": \"original\", \"snapshot\": {\"state\": \"merged\"}\n}"
	for _, entry := range []struct{ nonce, payload string }{{"retired-unknown", ""}, {"retired-reported", originalPayload}} {
		mustExec(t, s, `INSERT INTO retired_runner_delivery_queries(nonce,issue_id,delivery_id,generation,host_id,instance_id,query_until,retired_at,event_id,late_report_json) VALUES(?,'original-issue','original-delivery',7,'original-host','original-instance',90,100,'original-event',?)`, entry.nonce, entry.payload)
	}
	if err := s.migrate(t.Context(), 106); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		for _, entry := range []struct{ nonce, payload string }{{"retired-unknown", ""}, {"retired-reported", originalPayload}} {
			var accept bool
			var issue, delivery, host, instance, event, payload string
			var generation, until, retired int64
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT accept_late_report,issue_id,delivery_id,generation,host_id,instance_id,query_until,retired_at,event_id,late_report_json FROM retired_runner_delivery_queries WHERE nonce=?`, entry.nonce).Scan(&accept, &issue, &delivery, &generation, &host, &instance, &until, &retired, &event, &payload); err != nil {
				return err
			}
			if accept || issue != "original-issue" || delivery != "original-delivery" || generation != 7 || host != "original-host" || instance != "original-instance" || until != 90 || retired != 100 || event != "original-event" || payload != entry.payload {
				t.Fatal("migration changed an original retirement or invented an accepted report", entry.nonce, accept, payload)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A new history-only policy is durable before its original report arrives;
	// it must not populate the payload as if receipt were already established.
	mustExec(t, s, `INSERT INTO retired_runner_delivery_queries(nonce,issue_id,delivery_id,generation,host_id,instance_id,query_until,retired_at,accept_late_report) VALUES('new-history-only','original-issue','original-delivery',8,'original-host','original-instance',100,110,1)`)
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var accept bool
		var payload string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT accept_late_report,late_report_json FROM retired_runner_delivery_queries WHERE nonce='new-history-only'`).Scan(&accept, &payload); err != nil {
			return err
		}
		if !accept || payload != "" {
			t.Fatal("response policy became a fabricated arrival fact", accept, payload)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
