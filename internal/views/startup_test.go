package views

import (
	"database/sql"
	"testing"
)

func TestStartupProjectionClearsRecoveryButKeepsDiagnosis(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE run_logs(run_id TEXT,seq INTEGER,kind TEXT,body_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"worker.startup_failed", "worker.startup_retry_pending", "worker.startup_started"} {
		_, err := db.ExecContext(t.Context(), `INSERT INTO run_logs VALUES(?,?,?,?)`, "run", i, kind, `{"attempts":1,"diagnostic":{"code":"readiness_timeout","promptWriteState":"none","elapsedMs":60000,"terminalHandle":"private-terminal"}}`)
		if err != nil {
			t.Fatal(err)
		}
		startup, err := executionStartup(t.Context(), db, "run")
		if err != nil {
			t.Fatal(err)
		}
		if startup == nil || startup.Code != "readiness_timeout" || startup.Attempts != 1 || startup.ElapsedMS != 60000 {
			t.Fatalf("startup=%+v", startup)
		}
		recovery, err := executionRecovery(t.Context(), db, "run")
		if err != nil {
			t.Fatal(err)
		}
		if (recovery == nil) != (kind == "worker.startup_started") {
			t.Fatalf("kind=%s recovery=%+v", kind, recovery)
		}
	}
	other, err := executionStartup(t.Context(), db, "other-run")
	if err != nil || other != nil {
		t.Fatalf("other=%+v err=%v", other, err)
	}
}
