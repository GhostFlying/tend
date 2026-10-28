package agentcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
	"github.com/GhostFlying/tend/internal/testkit"
)

const privateMarker = "PRIVATE_SOURCE_INSTALLATION"

type fixture struct {
	t       *testing.T
	options agentcontrol.Options
	config  agentconfig.Config
	control *agentcontrol.Control
	ids     protocol.IDGen
	socket  string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix local control")
	}
	root := t.TempDir()
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 81)
	f := &fixture{t: t, ids: ids, socket: filepath.Join(root, "control.sock")}
	f.config = agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "worker", Revision: 1, Kind: agentconfig.AppServerWorker, ExecutableRef: privateMarker, DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "implement", Revision: 1, DisplayName: "Implement", RuntimeID: "worker", Categories: []string{"implementer"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "requested-model", Effort: "high", Enabled: true}}}
	f.options = agentcontrol.Options{Directory: filepath.Join(root, "registry"), EnvironmentID: "runner-a", Config: agentcontrol.Config{SourceFile: filepath.Join(root, privateMarker+".json"), Installations: []agentcontrol.Installation{{RuntimeID: "worker", ArtifactID: "artifact", ExpectedSHA256: "sha256:" + strings.Repeat("a", 64), AdapterID: "appserver", AdapterRevision: "1", LocalReference: privateMarker}}}, Registry: agentregistry.Options{Now: clock.Now, NewSnapshotID: func() string { return "snapshot-" + ids.New(protocol.PrefixRequest) }}}
	f.write()
	return f
}
func (f *fixture) write() {
	f.t.Helper()
	data, err := json.Marshal(f.config)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.options.Config.SourceFile, data, 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) start() func() {
	f.t.Helper()
	control, err := agentcontrol.Open(f.t.Context(), f.options)
	if err != nil {
		f.t.Fatal(err)
	}
	f.control = control
	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		control.Close()
		f.t.Fatal(err)
	}
	srv := &http.Server{Handler: control.Handler(), ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	return func() { _ = srv.Close(); <-done; _ = control.Close() }
}
func (f *fixture) command(operation, id string, revision *uint64) (protocol.LocalAgentConfigurationResult, int, string) {
	f.t.Helper()
	var out, errout bytes.Buffer
	code := cli.RunLocalAgents(f.t.Context(), cli.Env{Stdout: &out, Stderr: &errout, IDs: f.ids}, f.socket, operation, id, revision)
	var result protocol.LocalAgentConfigurationResult
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			f.t.Fatal(err, out.String())
		}
		if strings.Contains(out.String(), privateMarker) || strings.Contains(out.String(), filepath.Dir(f.socket)) {
			f.t.Fatal("private reference leaked in response", out.String())
		}
	}
	return result, code, errout.String()
}
func revision(n uint64) *uint64 { return &n }

func TestLocalAgentsLifecycle(t *testing.T) {
	f := setup(t)
	stop := f.start()
	defer stop()
	status, code, _ := f.command("status", "", nil)
	if code != 0 || status.Current == nil || status.Current.State != "empty" || status.Current.Revision != 0 || status.Current.Projection != nil {
		t.Fatal("first startup accepted source implicitly", status, code)
	}
	checked, code, _ := f.command("validate", "", nil)
	if code != 0 || checked.Candidate == nil || checked.Current.Revision != 0 {
		t.Fatal("validation failed or committed source", checked, code)
	}
	accepted, code, message := f.command("reload", "", nil)
	if code != 0 || accepted.Receipt == nil || accepted.RequestState != "accepted" || accepted.Current.State != "local_accepted" || accepted.Current.Revision != 1 || !strings.Contains(message, accepted.Receipt.RequestID) || !strings.Contains(message, "revision 0") {
		t.Fatal("actual reload did not accept/print retry identity", accepted, code, message)
	}
	original := *accepted.Receipt
	if accepted.Current.Projection.Agents[0].Availability.Available {
		t.Fatal("acceptance enabled a factory")
	}
	again, code, _ := f.command("reload", original.RequestID, revision(0))
	if code != 0 || *again.Receipt != original || again.Current.Revision != 1 {
		t.Fatal("same-request replay advanced state", again, code)
	}
	_, code, _ = f.command("reload", original.RequestID, nil)
	if code != 2 {
		t.Fatal("retry without original CAS was sent", code)
	}
	stale, code, _ := f.command("reload", "", revision(0))
	if code != 1 || stale.ErrorCode != "revision_conflict" || stale.Current.Revision != 1 {
		t.Fatal("stale CAS changed state", stale, code)
	}
	f.config.Runtimes[0].ExecutableRef = privateMarker + "-new-private-ref"
	f.write()
	conflict, code, _ := f.command("reload", original.RequestID, revision(0))
	if code != 1 || conflict.ErrorCode != "request_conflict" {
		t.Fatal("changed source reused request identity", conflict, code)
	}
	second, code, _ := f.command("reload", "", nil)
	if code != 0 || second.Current.Revision != 2 || second.Receipt.PublicSHA256 != original.PublicSHA256 || second.Receipt.SnapshotID == original.SnapshotID {
		t.Fatal("private change was lost behind public hash", second, code)
	}
	if _, err := os.Stat(filepath.Join(f.options.Directory, "snapshots", original.SnapshotID+".json")); err != nil {
		t.Fatal("reload discarded old snapshot", err)
	}
}

func TestLocalAgentsLastGoodOnRestart(t *testing.T) {
	f := setup(t)
	stop := f.start()
	accepted, code, _ := f.command("reload", "", nil)
	if code != 0 {
		t.Fatal(accepted, code)
	}
	if err := os.WriteFile(f.options.Config.SourceFile, []byte(`{"format_version":1,"instructions":"unsupported"}`), 0600); err != nil {
		t.Fatal(err)
	}
	rejected, code, _ := f.command("reload", "", nil)
	if code != 1 || rejected.Current.Revision != 1 || !strings.HasPrefix(rejected.ErrorCode, "invalid_definition_") {
		t.Fatal("invalid source replaced last good", rejected, code)
	}
	stop()
	stop = f.start()
	defer stop()
	status, code, _ := f.command("status", "", nil)
	if code != 0 || status.Current.Revision != 1 || status.Current.SnapshotID != accepted.Receipt.SnapshotID {
		t.Fatal("restart reread invalid candidate or lost accepted source", status, code)
	}
	reconciled, code, _ := f.command("reconcile", accepted.Receipt.RequestID, nil)
	if code != 0 || reconciled.RequestState != "accepted" || *reconciled.Receipt != *accepted.Receipt {
		t.Fatal("bad source prevented original receipt read", reconciled, code)
	}
}

func TestLocalAgentsUnknownReconciles(t *testing.T) {
	f := setup(t)
	var fault atomic.Bool
	f.options.Registry.Before = func(step string) error {
		if step == "state.dir_sync" && fault.Swap(false) {
			return errors.New("injected sync failure")
		}
		return nil
	}
	stop := f.start()
	defer stop()
	one, code, _ := f.command("reload", "", nil)
	if code != 0 {
		t.Fatal(one, code)
	}
	f.config.Agents[0].Model = "new-requested-model"
	f.config.Agents[0].Revision++
	f.write()
	fault.Store(true)
	unknown, code, message := f.command("reload", "", nil)
	if code != 1 || unknown.ErrorCode != "unknown" || unknown.Current.Revision != 1 || unknown.Current.SnapshotID != one.Receipt.SnapshotID || unknown.Current.PendingRequestID == "" {
		t.Fatal("unknown published current or lost request identity", unknown, code)
	}
	pendingID := unknown.Current.PendingRequestID
	if !strings.Contains(message, "reconcile") || !strings.Contains(message, pendingID) {
		t.Fatal("unknown lacked actionable original request", message)
	}
	blocked, code, _ := f.command("reload", "", nil)
	if code != 1 || blocked.Current.PendingRequestID != pendingID || blocked.Current.Revision != 1 {
		t.Fatal("new request bypassed unknown", blocked, code)
	}
	outcome, code, _ := f.command("reconcile", pendingID, nil)
	if code != 0 || outcome.RequestState != "accepted" || outcome.Receipt == nil || outcome.Receipt.RequestID != pendingID || outcome.Current.Revision != 2 || outcome.Current.PendingRequestID != "" {
		t.Fatal("original unknown did not reconcile", outcome, code)
	}
}

func TestLocalAgentsRejectsMissingAcceptedState(t *testing.T) {
	f := setup(t)
	control, err := agentcontrol.Open(t.Context(), f.options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Reload(t.Context(), protocol.ReloadLocalAgentsRequest{RequestID: f.ids.New(protocol.PrefixRequest)}); err != nil {
		t.Fatal(err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.options.Directory, "registry.json")); err != nil {
		t.Fatal(err)
	}
	if reopened, err := agentcontrol.Open(context.Background(), f.options); err == nil {
		reopened.Close()
		t.Fatal("existing missing authority was rebuilt as empty")
	}
}

func TestLocalAgentsUnknownAcrossRestart(t *testing.T) {
	f := setup(t)
	var fault atomic.Bool
	f.options.Registry.Before = func(step string) error {
		if step == "state.dir_sync" && fault.Swap(false) {
			return errors.New("injected sync failure")
		}
		return nil
	}
	stop := f.start()
	fault.Store(true)
	unknown, code, _ := f.command("reload", "", nil)
	if code != 1 || unknown.Current.Revision != 0 || unknown.Current.PendingRequestID == "" {
		t.Fatal(unknown, code)
	}
	originalID := unknown.Current.PendingRequestID
	stop()
	if err := os.WriteFile(f.options.Config.SourceFile, []byte(`{"invalid":"candidate"}`), 0600); err != nil {
		t.Fatal(err)
	}
	stop = f.start()
	defer stop()
	result, code, _ := f.command("reconcile", originalID, nil)
	if code != 0 || result.RequestState != "accepted" || result.Receipt == nil || result.Receipt.RequestID != originalID || result.Current.Revision != 1 {
		t.Fatal("restart lost original published receipt", result, code)
	}
}
