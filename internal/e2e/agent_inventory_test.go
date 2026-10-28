package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type inventoryNetwork struct {
	mu        sync.Mutex
	mode      string
	blocked   bool
	settled   bool
	envelopes []protocol.Envelope
}

func (n *inventoryNetwork) RoundTrip(q *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(q.Body)
	if err != nil {
		return nil, err
	}
	q.Body.Close()
	q.Body = io.NopCloser(bytes.NewReader(raw))
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	n.mu.Lock()
	if n.blocked && env.Operation != protocol.OpRunnerHello {
		n.mu.Unlock()
		return nil, errors.New("test network interrupted")
	}
	first := len(n.envelopes) == 0
	if env.Operation == protocol.OpRunnerInventoryRegister {
		n.envelopes = append(n.envelopes, env)
	} else {
		first = false
	}
	mode := n.mode
	if first && (mode == "drop_restart" || mode == "lose_restart") {
		n.blocked = true
	}
	n.mu.Unlock()
	if first {
		defer func() { n.mu.Lock(); n.settled = true; n.mu.Unlock() }()
	}
	if first && (mode == "drop_restart" || mode == "drop_once") {
		return nil, errors.New("test request did not arrive")
	}
	response, err := http.DefaultTransport.RoundTrip(q)
	if first && mode == "lose_restart" && err == nil {
		response.Body.Close()
		return nil, errors.New("test accepted reply lost")
	}
	return response, err
}
func (n *inventoryNetwork) firstSettled() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.settled }

func (n *inventoryNetwork) sent() []protocol.Envelope {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]protocol.Envelope(nil), n.envelopes...)
}

type inventoryProcess struct {
	t       *testing.T
	server  *server
	home    string
	options agentcontrol.Options
	config  agentconfig.Config
	control *agentcontrol.Control
	runner  *runner.Runner
	cancel  context.CancelFunc
	done    chan error
	network *inventoryNetwork
}

func newInventoryProcess(t *testing.T, s *server) *inventoryProcess {
	t.Helper()
	home := t.TempDir()
	ids := protocol.NewULIDGen(s.now, nil)
	f := &inventoryProcess{t: t, server: s, home: home}
	f.config = agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "worker", Revision: 1, Kind: agentconfig.AppServerWorker, ExecutableRef: "PRIVATE_NATIVE_REFERENCE", DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "implement", Revision: 1, DisplayName: "Implement", RuntimeID: "worker", Categories: []string{"implementer"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "requested-model", Effort: "high", Enabled: true}}}
	f.options = agentcontrol.Options{Directory: filepath.Join(home, "registry"), EnvironmentID: "linux-1", Config: agentcontrol.Config{SourceFile: filepath.Join(home, "PRIVATE_SOURCE.json"), Installations: []agentcontrol.Installation{{RuntimeID: "worker", ArtifactID: "artifact", ExpectedSHA256: "sha256:" + strings.Repeat("a", 64), AdapterID: "appserver", AdapterRevision: "1", LocalReference: "PRIVATE_NATIVE_REFERENCE"}}}, Registry: agentregistry.Options{Now: s.now, NewSnapshotID: func() string { return "snapshot-" + ids.New(protocol.PrefixRequest) }}}
	f.write()
	ownerCLI := newOwnerCLI(t, filepath.Join(t.TempDir(), "cli"))
	ownerCLI.run("", "login", "--server", s.startAPI())
	ownerCLI.run("", "category", "put", "implementer", "--title", "Implementer", "--description", "Implementation responsibility")
	var category protocol.AgentCategory
	if err := json.Unmarshal([]byte(ownerCLI.run("", "category", "show", "implementer")), &category); err != nil || category.Revision != 1 {
		t.Fatalf("category CLI=%+v %v", category, err)
	}
	t.Cleanup(f.stop)
	return f
}
func (f *inventoryProcess) write() {
	f.t.Helper()
	raw, err := json.Marshal(f.config)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.options.Config.SourceFile, raw, 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *inventoryProcess) start(mode string, initial bool) {
	f.t.Helper()
	c, err := agentcontrol.Open(f.t.Context(), f.options)
	if err != nil {
		f.t.Fatal(err)
	}
	f.control = c
	if initial {
		if _, err := c.Reload(f.t.Context(), protocol.ReloadLocalAgentsRequest{RequestID: f.server.ids.New(protocol.PrefixRequest), ExpectedRevision: 0}); err != nil {
			f.t.Fatal(err)
		}
	}
	j, err := journal.Open(f.t.Context(), filepath.Join(f.home, "journal.db"), f.server.now)
	if err != nil {
		f.t.Fatal(err)
	}
	f.network = &inventoryNetwork{mode: mode}
	f.runner = runner.New(runner.Config{ServerURL: f.server.url, HostID: "linux-1", HostToken: hostToken, Version: "test", HTTPClient: &http.Client{Transport: f.network}, Journal: j, Workers: &testkit.Workers{Script: passing}, Workspaces: &workspaces{dir: f.t.TempDir()}, Now: f.server.now, IDs: protocol.NewULIDGen(f.server.now, nil), Log: quiet(), RetryDelay: 10 * time.Millisecond, AgentInventory: c.Status, AgentChanges: c.Changes()})
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan error, 1)
	go func() { err := f.runner.Run(ctx); _ = j.Close(); _ = c.Close(); f.done <- err }()
}
func (f *inventoryProcess) stop() {
	if f.cancel == nil {
		return
	}
	f.cancel()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		f.t.Fatal("inventory runner did not stop")
	}
	f.cancel = nil
}
func (f *inventoryProcess) current() *protocol.AgentInventoryReceipt {
	f.t.Helper()
	var receipt *protocol.AgentInventoryReceipt
	err := f.server.store.Read(f.t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetAgentInventory(f.t.Context(), tx.Q(), "linux-1")
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err == nil {
			receipt = &row.Receipt
		}
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return receipt
}
func (f *inventoryProcess) pending() []journal.Request {
	f.t.Helper()
	j, err := journal.Open(f.t.Context(), filepath.Join(f.home, "journal.db"), f.server.now)
	if err != nil {
		f.t.Fatal(err)
	}
	defer j.Close()
	requests, err := j.Requests(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	var out []journal.Request
	for _, request := range requests {
		if request.Envelope.Operation == protocol.OpRunnerInventoryRegister {
			out = append(out, request)
		}
	}
	return out
}
func TestEndToEnd_AgentInventoryAcceptedReloadAndOwnerCategory(t *testing.T) {
	s := newServer(t, time.Minute)
	f := newInventoryProcess(t, s)
	f.start("", false)
	eventually(t, "hello without accepted inventory", func() bool { _, err := f.runner.AgentRegistrationStatus(t.Context()); return err == nil })
	if len(f.network.sent()) != 0 {
		t.Fatal("empty registry was registered")
	}
	first, err := f.control.Reload(t.Context(), protocol.ReloadLocalAgentsRequest{RequestID: f.server.ids.New(protocol.PrefixRequest), ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "first accepted registration", func() bool { return f.current() != nil })
	if f.current().LocalRevision != 1 || f.current().SnapshotID != first.SnapshotID {
		t.Fatal("wrong first snapshot", f.current())
	}
	f.config.Agents[0].Revision = 2
	f.config.Agents[0].DisplayName = "Updated"
	f.write()
	second, err := f.control.Reload(t.Context(), protocol.ReloadLocalAgentsRequest{RequestID: f.server.ids.New(protocol.PrefixRequest), ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "accepted reload registration", func() bool { return f.current().LocalRevision == 2 })
	if f.current().SnapshotID != second.SnapshotID || f.current().ServerRevision != 2 {
		t.Fatal("reload did not publish current snapshot", f.current())
	}
	status, err := f.runner.AgentRegistrationStatus(t.Context())
	if err != nil || status.State != "registered_staged" || status.Server.Projection.Agents[0].Availability.Available {
		t.Fatalf("registration claimed runtime availability %+v %v", status, err)
	}
	for _, env := range f.network.sent() {
		if bytes.Contains(env.Payload, []byte("PRIVATE_")) {
			t.Fatal("private configuration uploaded")
		}
	}
}
func TestEndToEnd_AgentInventorySameProcessUnknownRetainsEnvelope(t *testing.T) {
	s := newServer(t, time.Minute)
	f := newInventoryProcess(t, s)
	f.start("drop_once", true)
	eventually(t, "same-process retry registration", func() bool { return f.current() != nil })
	sent := f.network.sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d registrations", len(sent))
	}
	firstHash, _ := protocol.PayloadHash(&sent[0])
	nextHash, _ := protocol.PayloadHash(&sent[1])
	if sent[0].RequestID != sent[1].RequestID || firstHash != nextHash || sent[0].ExpectedControlRevision == nil || *sent[0].ExpectedControlRevision != 0 {
		t.Fatal("unknown request changed ID/CAS/content")
	}
}
func TestEndToEnd_AgentInventoryRestartResolvesOriginalRequest(t *testing.T) {
	for _, mode := range []string{"lose_restart", "drop_restart"} {
		t.Run(mode, func(t *testing.T) {
			s := newServer(t, time.Minute)
			f := newInventoryProcess(t, s)
			f.start(mode, true)
			eventually(t, "first inventory attempt", func() bool { return f.network.firstSettled() })
			f.stop()
			requests := f.pending()
			if len(requests) != 1 {
				t.Fatalf("unknown not retained: %+v", requests)
			}
			original := requests[0].Envelope
			originalHash, _ := protocol.PayloadHash(&original)
			var old protocol.RegisterAgentInventoryPayload
			json.Unmarshal(original.Payload, &old)
			if (f.current() != nil) != (mode == "lose_restart") {
				t.Fatal("unexpected original server effect")
			}
			f.start("", false)
			eventually(t, "restart inventory reconciled", func() bool {
				status, err := f.runner.AgentRegistrationStatus(t.Context())
				return err == nil && status.PendingRequestID == "" && status.Server.Current != nil
			})
			sent := f.network.sent()
			if mode == "lose_restart" {
				if len(sent) != 0 || f.current().RequestID != original.RequestID || f.current().ServerRevision != 1 {
					t.Fatal("accepted predecessor was sent again", sent, f.current())
				}
			} else {
				if len(sent) != 1 || sent[0].RequestID == original.RequestID {
					t.Fatal("fenced not-recorded predecessor not replaced explicitly", sent)
				}
				var next protocol.RegisterAgentInventoryPayload
				json.Unmarshal(sent[0].Payload, &next)
				if next.Runtime.InstanceID == old.Runtime.InstanceID || next.Runtime.Generation <= old.Runtime.Generation || f.current().ServerRevision != 1 {
					t.Fatal("new hello did not fence predecessor")
				}
			}

			if mode == "lose_restart" {
				err := s.store.Read(t.Context(), func(tx *store.ReadTx) error {
					record, err := store.GetRequest(t.Context(), tx.Q(), (protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}).DedupDomain(), original.RequestID)
					if err == nil && record.PayloadHash != originalHash {
						t.Fatal("accepted receipt did not retain original CAS/runtime hash")
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}

		})
	}
}

// rewriteLocalAgentSocket exercises the thin CLI against the real private handler
// using an httptest listener; the production listener remains the shared socket.
type rewriteLocalAgentSocket struct{ base *url.URL }

func (x rewriteLocalAgentSocket) RoundTrip(q *http.Request) (*http.Response, error) {
	clone := q.Clone(q.Context())
	clone.URL.Scheme = x.base.Scheme
	clone.URL.Host = x.base.Host
	return http.DefaultTransport.RoundTrip(clone)
}
func TestEndToEnd_AgentInventoryManualRegisterAfterCategoryCorrection(t *testing.T) {
	s := newServer(t, time.Minute)
	f := newInventoryProcess(t, s)
	f.config.Agents[0].Categories = []string{"later"}
	f.write()
	f.start("", true)
	eventually(t, "missing category registration rejected", func() bool {
		status, err := f.runner.AgentRegistrationStatus(t.Context())
		return err == nil && status.PendingRequestID == "" && len(f.network.sent()) > 0
	})
	if f.current() != nil {
		t.Fatal("unknown category accepted")
	}
	ownerCLI := newOwnerCLI(t, filepath.Join(t.TempDir(), "owner"))
	ownerCLI.run("", "login", "--server", s.startAPI())
	ownerCLI.run("", "category", "put", "later", "--title", "Later")
	local := httptest.NewServer(f.runner.AgentInventoryHandler(f.control.Handler()))
	defer local.Close()
	base, err := url.Parse(local.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	code := cli.RunLocalAgents(t.Context(), cli.Env{Stdout: &out, Stderr: &errout, HTTP: &http.Client{Transport: rewriteLocalAgentSocket{base: base}}}, "unused", "register", "", nil)
	if code != 0 {
		t.Fatalf("manual CLI register=%d %s %s", code, out.String(), errout.String())
	}
	var result protocol.LocalAgentConfigurationResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Registration == nil || result.Registration.State != "registered_staged" {
		t.Fatalf("manual result=%s %v", out.String(), err)
	}
	if f.current() == nil || f.current().Categories[0].CategoryID != "later" {
		t.Fatal("manual register did not use corrected category")
	}
}
