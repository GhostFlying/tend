//go:build linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWorkerCloseMissingOwnershipRequiresOriginalBoundaryClosure(t *testing.T) {
	for _, hybrid := range []bool{false, true} {
		t.Run(map[bool]string{false: "unified", true: "systemd_v1"}[hybrid], func(t *testing.T) {
			f := newWorkerCloseFixture(t)
			if hybrid {
				f = newWorkerCloseHybridFixture(t)
			}
			if err := os.RemoveAll(f.q.StateDirectory); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			e := Env{Stdout: &out, Stderr: &stderr}
			args := []string{"prepare", "--state-dir", f.q.StateDirectory, "--request-id", f.q.RequestID, "--run", f.q.RunID, "--thread", f.q.ThreadID,
				"--owned-missing", "--directory", filepath.Join(filepath.Dir(f.q.StateDirectory), "workspace"), "--runner-pid", "100", "--runner-start-ticks", "1000", "--runner-boot-id", f.q.RunnerBoot, "--unit", f.q.Unit, "--acknowledge-unit-execution"}
			if err := runWorkerCloseOnHost(t.Context(), &e, args, f.host); err != nil {
				t.Fatalf("missing original ownership cannot prepare boundary: %v", err)
			}
			var prepared protocol.WorkerCloseReceipt
			if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
				t.Fatal(err)
			}
			if prepared.Phase != "prepared" || prepared.Intent.OwnedRecordSHA256 != "" || len(prepared.Intent.Processes) != 0 {
				t.Fatalf("fabricated ownership: %+v", prepared)
			}
			q := f.q
			q.Action = "confirm"
			if _, err := executeWorkerClose(t.Context(), q, f.host); err == nil {
				t.Fatal("live runner accepted as closed")
			}
			f.stopped()
			if hybrid {
				if _, err := executeWorkerClose(t.Context(), q, f.host); err == nil {
					t.Fatal("v1 direct empty members accepted without kernel removal")
				}
				if err := os.RemoveAll(f.path); err != nil {
					t.Fatal(err)
				}
			}
			confirmed, err := executeWorkerClose(t.Context(), q, f.host)
			if err != nil || confirmed.Phase != "confirmed" {
				t.Fatalf("actual boundary closure rejected: %+v %v", confirmed, err)
			}
			if _, err := os.Lstat(filepath.Join(f.q.StateDirectory, "owned-appserver-process.json")); !os.IsNotExist(err) {
				t.Fatalf("ownership fabricated: %v", err)
			}
			// The exact request reads the original fact, including after restart.
			f.process(100, "9999", "S")
			again, err := executeWorkerClose(context.Background(), q, f.host)
			if err != nil || again.ProofSHA256 != confirmed.ProofSHA256 {
				t.Fatalf("historical proof changed: %v", err)
			}
			out.Reset()
			args[8] = "different-thread"
			if err := runWorkerCloseOnHost(t.Context(), &e, args, f.host); err == nil {
				t.Fatal("same request accepted changed input")
			}
		})
	}
}

func TestWorkerCloseMissingOwnershipRejectsLateOwnedRecord(t *testing.T) {
	f := newWorkerCloseFixture(t)
	f.q.OwnedMissing, f.q.OwnedSHA256, f.q.Directory = true, "", filepath.Join(filepath.Dir(f.q.StateDirectory), "workspace")
	if _, err := executeWorkerClose(t.Context(), f.q, f.host); err == nil {
		t.Fatal("existing ownership ignored")
	}
	if err := os.Remove(filepath.Join(f.q.StateDirectory, "owned-appserver-process.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := executeWorkerClose(t.Context(), f.q, f.host); err != nil {
		t.Fatal(err)
	}
	f.stopped()
	f.write(filepath.Join(f.q.StateDirectory, "owned-appserver-process.json"), "{")
	f.q.Action = "confirm"
	if _, err := executeWorkerClose(t.Context(), f.q, f.host); err == nil {
		t.Fatal("late ownership was bypassed")
	}
	if _, err := os.Lstat(filepath.Join(f.q.StateDirectory, protocol.WorkerCloseMissingProofFilename)); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed closure published: %v", err)
	}
}
