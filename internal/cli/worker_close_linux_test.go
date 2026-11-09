//go:build linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type workerCloseFixture struct {
	t    *testing.T
	host workerCloseHost
	q    workerCloseOptions
	path string
}

func newWorkerCloseFixture(t *testing.T) workerCloseFixture {
	t.Helper()
	dir := t.TempDir()
	f := workerCloseFixture{t: t, host: workerCloseHost{procRoot: filepath.Join(dir, "proc"), cgroupRoot: filepath.Join(dir, "cgroup"), uid: os.Geteuid(), now: func() time.Time { return time.Unix(100, 0) }}}
	f.q = workerCloseOptions{Action: "prepare", StateDirectory: filepath.Join(dir, "state"), RequestID: "original-request", RunID: "original-run", ThreadID: "original-thread", RunnerPID: 100, RunnerStart: "1000", RunnerBoot: "original-boot", Unit: "original.service", AcknowledgeUnit: true}
	f.path = filepath.Join(f.host.cgroupRoot, "system.slice", f.q.Unit)
	f.write(filepath.Join(f.host.procRoot, "sys/kernel/random/boot_id"), f.q.RunnerBoot)
	f.process(100, "1000", "S")
	f.write(filepath.Join(f.host.procRoot, "100/cgroup"), "0::/system.slice/original.service\n")
	f.write(filepath.Join(f.path, "cgroup.events"), "populated 1\nfrozen 0\n")
	f.write(filepath.Join(f.path, "cgroup.procs"), "100\n")
	owned := workerCloseOwnedRecord{Version: 1, RunID: f.q.RunID, ThreadID: f.q.ThreadID, Dir: filepath.Join(dir, "workspace"), Process: protocol.WorkerCloseProcess{PID: 200, StartTicks: "2000", BootID: f.q.RunnerBoot}, Detached: []protocol.WorkerCloseProcess{{PID: 300, StartTicks: "3000", BootID: f.q.RunnerBoot}}}
	body, err := json.Marshal(owned)
	if err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(f.q.StateDirectory, "owned-appserver-process.json"), string(body))
	f.q.OwnedSHA256 = protocol.WorkerCloseSHA256(body)
	return f
}

func (f workerCloseFixture) write(path, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f workerCloseFixture) process(pid int, start, state string) {
	fields := []string{state}
	for len(fields) < 20 {
		fields = append(fields, "0")
	}
	fields[19] = start
	f.write(filepath.Join(f.host.procRoot, strconv.Itoa(pid), "stat"), strconv.Itoa(pid)+" (a name) with ) parentheses) "+strings.Join(fields, " "))
}

func (f workerCloseFixture) stopped() {
	f.t.Helper()
	if err := os.RemoveAll(filepath.Join(f.host.procRoot, "100")); err != nil {
		f.t.Fatal(err)
	}
	f.write(filepath.Join(f.path, "cgroup.events"), "populated 0\nfrozen 0\n")
	f.write(filepath.Join(f.path, "cgroup.procs"), "")
}

func TestWorkerCloseTwoPhaseOriginalRequestAndHistoricalProof(t *testing.T) {
	f := newWorkerCloseFixture(t)
	ctx := context.Background()
	prepared, err := executeWorkerClose(ctx, f.q, f.host)
	if err != nil || prepared.Phase != "prepared" {
		t.Fatalf("prepare=%+v err=%v", prepared, err)
	}
	if prepared.Intent.Boundary.CgroupPath != "/system.slice/original.service" || len(prepared.Intent.Processes) != 2 || prepared.Intent.Runner.StartTicks != "1000" {
		t.Fatalf("lost exact original identities: %+v", prepared.Intent)
	}
	q := f.q
	q.Action = "confirm"
	if _, err := executeWorkerClose(ctx, q, f.host); err == nil {
		t.Fatal("live original runner must not produce a proof")
	}
	f.stopped()
	confirmed, err := executeWorkerClose(ctx, q, f.host)
	if err != nil || confirmed.Phase != "confirmed" || confirmed.Proof.Source != protocol.ExternalExecutionBoundary || len(confirmed.Proof.Observations) != 2 {
		t.Fatalf("confirm=%+v err=%v", confirmed, err)
	}
	original, err := os.ReadFile(filepath.Join(f.q.StateDirectory, "owned-appserver-process.json"))
	if err != nil || protocol.WorkerCloseSHA256(original) != f.q.OwnedSHA256 || bytes.Contains(original, []byte("close_observed")) {
		t.Fatalf("confirmation changed original ownership: %s err=%v", original, err)
	}
	// Once published, this remains an original historical fact even after the
	// successor occupies the same unit. The original request is read, not rerun.
	f.write(filepath.Join(f.path, "cgroup.events"), "populated 1\n")
	f.process(100, "9999", "S")
	for _, action := range []string{"show", "confirm", "prepare"} {
		q.Action = action
		got, err := executeWorkerClose(ctx, q, f.host)
		if err != nil || got.ProofSHA256 != confirmed.ProofSHA256 || got.IntentSHA256 != prepared.IntentSHA256 {
			t.Fatalf("%s replay changed original receipt: %+v err=%v", action, got, err)
		}
	}
	q.Action, q.RunID = "prepare", "other-run"
	if _, err := executeWorkerClose(ctx, q, f.host); err == nil {
		t.Fatal("same request must reject different Run")
	}
}

func TestWorkerCloseRejectsUnconfirmedExecutionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(workerCloseFixture)
	}{
		{"descendant_cgroup_still_populated", func(f workerCloseFixture) { f.write(filepath.Join(f.path, "cgroup.events"), "populated 1\n") }},
		{"cgroup_procs_still_present", func(f workerCloseFixture) { f.write(filepath.Join(f.path, "cgroup.procs"), "999\n") }},
		{"runner_pid_reused", func(f workerCloseFixture) { f.process(100, "9999", "S") }},
		{"known_detached_active", func(f workerCloseFixture) { f.process(300, "3000", "S") }},
		{"known_root_reused", func(f workerCloseFixture) { f.process(200, "9999", "S") }},
		{"kernel_boot_changed", func(f workerCloseFixture) {
			f.write(filepath.Join(f.host.procRoot, "sys/kernel/random/boot_id"), "other-boot")
		}},
		{"owned_record_changed", func(f workerCloseFixture) {
			f.write(filepath.Join(f.q.StateDirectory, "owned-appserver-process.json"), "{}")
		}},
		{"kernel_observation_missing", func(f workerCloseFixture) { _ = os.Remove(filepath.Join(f.path, "cgroup.events")) }},
		{"original_directory_replaced", func(f workerCloseFixture) {
			if err := os.Rename(f.path, f.path+".original"); err != nil {
				f.t.Fatal(err)
			}
			f.write(filepath.Join(f.path, "cgroup.events"), "populated 0\n")
			f.write(filepath.Join(f.path, "cgroup.procs"), "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkerCloseFixture(t)
			if _, err := executeWorkerClose(context.Background(), f.q, f.host); err != nil {
				t.Fatal(err)
			}
			f.stopped()
			tc.edit(f)
			f.q.Action = "confirm"
			if _, err := executeWorkerClose(context.Background(), f.q, f.host); err == nil {
				t.Fatal("unconfirmed original execution was accepted")
			}
			if _, err := os.Stat(filepath.Join(f.q.StateDirectory, protocol.WorkerCloseProofFilename(f.q.OwnedSHA256))); !os.IsNotExist(err) {
				t.Fatalf("failed observation published proof: %v", err)
			}
		})
	}
}

func TestWorkerCloseKernelRemovedAndBoundedRequestRecovery(t *testing.T) {
	f := newWorkerCloseFixture(t)
	if _, err := executeWorkerClose(context.Background(), f.q, f.host); err != nil {
		t.Fatal(err)
	}
	f.stopped()
	if err := os.RemoveAll(f.path); err != nil {
		t.Fatal(err)
	}
	f.q.Action = "confirm"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executeWorkerClose(ctx, f.q, f.host); err == nil {
		t.Fatal("cancelled confirmation succeeded")
	}
	f.q.Action = "show"
	shown, err := executeWorkerClose(context.Background(), f.q, f.host)
	if err != nil || shown.Phase != "prepared" {
		t.Fatalf("original request lost after interruption: %+v %v", shown, err)
	}
	f.q.Action = "confirm"
	confirmed, err := executeWorkerClose(context.Background(), f.q, f.host)
	if err != nil || confirmed.Proof.Observations[0].State != "kernel_removed" {
		t.Fatalf("removed original kernel boundary: %+v %v", confirmed, err)
	}
}

func TestWorkerCloseMainHelpAndExactDryRun(t *testing.T) {
	var out, stderr bytes.Buffer
	e := Env{Stdout: &out, Stderr: &stderr}
	if code := Main(context.Background(), &e, []string{"worker-close", "--help"}); code != 0 || !strings.Contains(out.String(), "external_execution_boundary") {
		t.Fatalf("group help: code=%d out=%s err=%s", code, &out, &stderr)
	}
	out.Reset()
	if code := Main(context.Background(), &e, []string{"worker-close", "prepare", "--help"}); code != 0 || !strings.Contains(out.String(), "acknowledge-unit-execution") {
		t.Fatalf("prepare help: code=%d out=%s err=%s", code, &out, &stderr)
	}
	out.Reset()
	if code := Main(context.Background(), &e, []string{"worker-close", "confirm", "--state-dir", "/abs/original-state", "--request-id", "fixed", "--dry-run"}); code != 0 || !strings.Contains(out.String(), "fixed") {
		t.Fatalf("dry run: code=%d out=%s err=%s", code, &out, &stderr)
	}
}

func TestWorkerCloseCommandRealLocalReceipts(t *testing.T) {
	f := newWorkerCloseFixture(t)
	var out, stderr bytes.Buffer
	e := Env{Stdout: &out, Stderr: &stderr}
	args := []string{"prepare", "--state-dir", f.q.StateDirectory, "--request-id", f.q.RequestID, "--run", f.q.RunID, "--thread", f.q.ThreadID, "--owned-sha256", f.q.OwnedSHA256, "--runner-pid", "100", "--runner-start-ticks", "1000", "--runner-boot-id", f.q.RunnerBoot, "--unit", f.q.Unit}
	if err := runWorkerCloseOnHost(context.Background(), &e, args, f.host); err == nil {
		t.Fatal("prepare without explicit unit acknowledgement succeeded")
	}
	args = append(args, "--acknowledge-unit-execution")
	if err := runWorkerCloseOnHost(context.Background(), &e, args, f.host); err != nil {
		t.Fatal(err)
	}
	var prepared protocol.WorkerCloseReceipt
	if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Phase != "prepared" || prepared.IntentSHA256 == "" {
		t.Fatalf("incomplete prepared output: %s", &out)
	}
	f.stopped()
	out.Reset()
	if err := runWorkerCloseOnHost(context.Background(), &e, []string{"confirm", "--state-dir", f.q.StateDirectory, "--request-id", f.q.RequestID}, f.host); err != nil {
		t.Fatal(err)
	}
	var confirmed protocol.WorkerCloseReceipt
	if err := json.Unmarshal(out.Bytes(), &confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed.Phase != "confirmed" || confirmed.ProofSHA256 == "" || confirmed.ProofPath == "" {
		t.Fatalf("incomplete confirmed output: %s", &out)
	}
}

func TestWorkerClosePrepareRejectsObservedForeignExecution(t *testing.T) {
	f := newWorkerCloseFixture(t)
	f.process(200, "2000", "S")
	f.write(filepath.Join(f.host.procRoot, "200/cgroup"), "0::/system.slice/another.service\n")
	if _, err := executeWorkerClose(context.Background(), f.q, f.host); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("foreign execution boundary accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.q.StateDirectory, protocol.WorkerCloseIntentFilename(f.q.RequestID))); !os.IsNotExist(err) {
		t.Fatalf("failed prepare wrote an intent: %v", err)
	}
}
