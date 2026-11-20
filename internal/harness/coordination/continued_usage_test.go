package coordination

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type usageDispatchThread struct {
	receiptNativeThread
	starts, activations int
}

func (t *usageDispatchThread) UsageSource() nativeusage.Source {
	return nativeusage.Source{ThreadID: t.ID(), Path: "rollout"}
}

func (t *usageDispatchThread) StartTurn(context.Context, harness.TurnInput) (string, error) {
	t.starts++
	return "child", nil
}

func TestLocalContinuationReactivatesUsageSource(t *testing.T) {
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.AcceptAssignment(t.Context(), protocol.Assignment{RunID: "run", Protocol: protocol.CurrentRunnerProtocol}, ""); err != nil {
		t.Fatal(err)
	}
	local := journal.LocalTurns{Journal: j, RunID: "run"}
	th := &usageDispatchThread{}
	collection := protocol.NewUsageCollection(false, false)
	collection.State = protocol.UsageNeedsReconcile
	if err := j.RegisterUsageSource(t.Context(), journal.UsageSource{ID: "source", RunID: "run", RegistrationQueued: true, Collection: collection, Source: nativeusage.Source{ThreadID: th.ID(), Path: "rollout", WriterID: "writer"}}); err != nil {
		t.Fatal(err)
	}
	if err := local.Queue(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: "root", CallID: "next"}, "next step"); err != nil {
		t.Fatal(err)
	}
	d := Driver{LocalTurns: local, BeforeUsageDispatch: func(context.Context, nativeusage.Source) error {
		th.activations++
		return j.ActivateUsageSource(t.Context(), "source", "run/thread")
	}, Emit: func(context.Context, Event) error { return nil }}
	if err := d.forwardNative(t.Context(), th, harness.Event{Kind: harness.EventTurnCompleted, TurnID: "root", TurnStatus: "completed"}, true); err != nil {
		t.Fatal(err)
	}
	sources, err := j.UsageSources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := j.PendingUsageSources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if th.starts != 1 || th.activations != 1 || len(sources) != 1 || sources[0].Collection.State != protocol.UsageActive {
		t.Fatalf("continuation starts=%d, usage activations=%d, sources=%+v, eligible=%d", th.starts, th.activations, sources, len(pending))
	}
}
