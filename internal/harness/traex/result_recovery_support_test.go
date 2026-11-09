package traex

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestReconcileImplementationResultUnsupportedUsesOriginalPath(t *testing.T) {
	scenarios := []string{"custom session acquisition"}
	if !ownedProcessSupported() {
		scenarios = append(scenarios, "platform without process recovery")
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			opts := WorkerOptions{Session: Options{Bin: "must-not-launch"}}
			if scenario == "custom session acquisition" {
				opts.Acquire = func(context.Context, harness.WorkerSpec) (*Session, func(error), error) {
					t.Error("optional reconciliation acquired a native session")
					return nil, nil, errors.New("must not acquire")
				}
			}
			ws := NewWorkers(opts)
			spec := harness.WorkerSpec{RunID: "original-run", StateDir: t.TempDir(), Dir: t.TempDir(), Thread: "original-thread"}
			original := []byte(`{"report":{"body":"previously submitted result"}}`)
			path := filepath.Join(spec.StateDir, "result.json")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ws.ReconcileImplementationResult(t.Context(), spec)
			if !errors.Is(err, harness.ErrNoImplementationResult) || errors.Is(err, harness.ErrRetainedResumePending) || got.Result != nil {
				t.Fatalf("unsupported optional recovery intercepted the original resume path: %+v %v", got, err)
			}
			body, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(body, original) {
				t.Fatal("unsupported recovery changed the original result", err)
			}
		})
	}
}
