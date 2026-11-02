package remote

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
)

func TestCentralStopPrecedesDelayedSourceReceipt(t *testing.T) {
	stopped := false
	b := &Broker{cfg: BrokerConfig{Cwd: t.TempDir(), UsageSource: func(_ context.Context, _ string, _ nativeusage.Source, done bool) error { stopped = done; return nil }}, ctx: t.Context(), pendingUsage: map[string]nativeBinding{}, log: slog.New(slog.DiscardHandler)}
	if err := b.saveBinding("run", nativeBinding{Cwd: "workspace", Thread: "thread"}); err != nil {
		t.Fatal(err)
	}
	b.stopUsage("run")
	b.trackUsage("run", nativeusage.Source{Path: "history.jsonl", Provider: "traex", ThreadID: "thread"}, false)
	raw, err := os.ReadFile(b.bindingPath("run"))
	if err != nil {
		t.Fatal(err)
	}
	var binding nativeBinding
	if err := json.Unmarshal(raw, &binding); err != nil || !stopped || !binding.UsageStopped {
		t.Fatal("delayed source lost confirmed writer stop", binding, stopped, err)
	}
}

func TestInitialThreadReceiptPreservesEarlierProcessStop(t *testing.T) {
	stopped := false
	b := &Broker{cfg: BrokerConfig{Cwd: t.TempDir(), UsageSource: func(_ context.Context, _ string, _ nativeusage.Source, done bool) error { stopped = done; return nil }}, ctx: t.Context(), pendingUsage: map[string]nativeBinding{}, log: slog.New(slog.DiscardHandler)}
	if err := b.saveBinding("run", nativeBinding{Cwd: "workspace", Pending: true}); err != nil {
		t.Fatal(err)
	}
	b.stopUsage("run")
	if err := b.saveThreadBinding("run", nativeBinding{Cwd: "workspace", Thread: "thread"}); err != nil {
		t.Fatal(err)
	}
	b.trackUsage("run", nativeusage.Source{Path: "history.jsonl", ThreadID: "thread", Provider: "traex"}, false)
	raw, err := os.ReadFile(b.bindingPath("run"))
	if err != nil {
		t.Fatal(err)
	}
	var binding nativeBinding
	if err := json.Unmarshal(raw, &binding); err != nil || !binding.UsageStopped || !stopped {
		t.Fatal(binding, stopped, err)
	}
}

func TestLegacyCentralRecoveryUsesInheritedProviderHomeAndLedgerRun(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-thread.jsonl"), []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread\",\"session_id\":\"thread\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRAECLI_HOME", home)
	b := &Broker{cfg: BrokerConfig{Cwd: t.TempDir(), RegisteredUsageRuns: func(context.Context) ([]string, error) { return []string{"run"}, nil }}, ctx: t.Context(), pendingUsage: map[string]nativeBinding{}}
	if err := writePrivate(b.bindingPath("run"), map[string]string{"cwd": "workspace", "thread": "thread"}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(b.bindingPath("unregistered"), map[string]string{"cwd": "workspace", "thread": "thread"}); err != nil {
		t.Fatal(err)
	}
	b.restoreUsage()
	if len(b.pendingUsage) != 1 || b.pendingUsage["run"].Usage == nil {
		t.Fatal(b.pendingUsage)
	}
}
