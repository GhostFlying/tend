package journal

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUsageRegistrationAndCursorAreAtomicAndSurviveRunCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	source := UsageSource{ID: "source", RunID: "manager", Source: nativeusage.Source{Provider: "codex", ThreadID: "thread"}, Cursors: map[string]nativeusage.Cursor{"history": {Offset: 123}}}
	env := protocol.Envelope{RequestID: "registration", Operation: protocol.OpRunnerUsageRegister, Target: protocol.Target{Type: "host", ID: "host"}, Payload: json.RawMessage(`{"run_id":"manager","thread_id":"thread","provider":"codex"}`)}
	if _, err := j.db.ExecContext(t.Context(), `CREATE TRIGGER fail_usage_request BEFORE INSERT ON requests BEGIN SELECT RAISE(ABORT,'injected request failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := j.QueueUsageRegistration(t.Context(), source, env); err == nil {
		t.Fatal("request fault did not roll back source")
	}
	if sources, err := j.UsageSources(t.Context()); err != nil || len(sources) != 0 {
		t.Fatal(sources, err)
	}
	if _, err := j.db.ExecContext(t.Context(), `DROP TRIGGER fail_usage_request`); err != nil {
		t.Fatal(err)
	}
	if err := j.AcceptAssignment(t.Context(), protocol.Assignment{RunID: "manager", LeaseID: "lease", RunToken: "token", Role: protocol.RoleIssueManager}, "claim"); err != nil {
		t.Fatal(err)
	}
	if err := j.RegisterUsageSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := j.QueueUsageRegistration(t.Context(), source, env); err != nil {
			t.Fatal(err)
		}
	}
	source.Cursors["history"] = nativeusage.Cursor{Offset: 200}
	if err := j.QueueUsage(t.Context(), source, nil); err != nil {
		t.Fatal(err)
	}
	if err := j.Done(t.Context(), "manager"); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	j = open(t, path)
	defer j.Close()
	sources, err := j.UsageSources(t.Context())
	if err != nil || len(sources) != 1 || !sources[0].RegistrationQueued || sources[0].Cursors["history"].Offset != 200 {
		t.Fatal(sources, err)
	}
	pending, err := j.Requests(t.Context())
	if err != nil || len(pending) != 1 || pending[0].RunID != "" || pending[0].Envelope.RequestID != "registration" {
		t.Fatal(pending, err)
	}
	if events, err := j.PendingEvents(t.Context(), "manager", 10); err != nil || len(events) != 0 {
		t.Fatal("usage entered the business event sequence", events, err)
	}
}

func TestNativeUsageCursorAndUploadSurviveRestartTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	s := UsageSource{ID: "source", RunID: "run", Source: nativeusage.Source{Provider: "traex", ThreadID: "thread"}, Cursors: map[string]nativeusage.Cursor{"history": {Offset: 123, Prefix: "prefix"}}}
	if err := j.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	env := protocol.Envelope{RequestID: "request", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: "host"}}
	if err := j.QueueUsage(t.Context(), s, &env); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	sources, err := j.UsageSources(t.Context())
	if err != nil || len(sources) != 1 || sources[0].Cursors["history"].Offset != 123 {
		t.Fatal(sources, err)
	}
	requests, err := j.Requests(t.Context())
	if err != nil || len(requests) != 1 || requests[0].Envelope.RequestID != "request" {
		t.Fatal(requests, err)
	}
	if err = j.DeleteRequest(t.Context(), "request"); err != nil {
		t.Fatal(err)
	}
	sources, err = j.UsageSources(t.Context())
	if err != nil || sources[0].Cursors["history"].Offset != 123 {
		t.Fatal(sources, err)
	}
}

func TestQueuedUploadWakesCollectorAfterHistoryHasSettled(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.db"))
	defer j.Close()
	src := UsageSource{ID: "source", RunID: "run", WriterStopped: true, RegistrationQueued: true, Coverage: "caught_up", Source: nativeusage.Source{Provider: "traex", ThreadID: "thread"}}
	if err := j.RegisterUsageSource(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	select {
	case <-j.UsageWake():
	default:
	}
	env := protocol.Envelope{RequestID: "original-upload", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: "host"}}
	if err := j.QueueUsage(t.Context(), src, &env); err != nil {
		t.Fatal(err)
	}
	select {
	case <-j.UsageWake():
	default:
		t.Fatal("durable upload did not wake the dormant collector")
	}
	pending, err := j.Requests(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Envelope.RequestID != env.RequestID {
		t.Fatal(pending, err)
	}
}
