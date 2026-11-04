package journal

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityVerificationCrashAndConcurrentRetryDoNotProbeAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	p := protocol.VerifyLocalCapabilityRequest{RequestID: "original", ExpectedRevision: 2, Pin: protocol.RunExecutionPin{Identity: protocol.AgentIdentity{LocalID: "agent"}}, Capability: protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}, MaxAttempts: 1}
	var wg sync.WaitGroup
	starts := make(chan bool, 8)
	for range 8 {
		wg.Go(func() {
			_, fresh, err := j.BeginCapabilityVerification(t.Context(), p, "instance", 1)
			if err != nil {
				t.Error(err)
			}
			starts <- fresh
		})
	}
	wg.Wait()
	close(starts)
	n := 0
	for fresh := range starts {
		if fresh {
			n++
		}
	}
	if n != 1 {
		t.Fatal("more than one probe admitted", n)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	s, fresh, err := j.BeginCapabilityVerification(t.Context(), p, "instance", 1)
	if err != nil || fresh || s.State != "unknown" || s.Sequence < 1 {
		t.Fatal(s, fresh, err)
	}
	p.TimeoutMillis++
	if _, _, err := j.BeginCapabilityVerification(t.Context(), p, "instance", 1); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("same id changed payload", err)
	}
	p.RequestID = "replacement"
	if _, _, err := j.BeginCapabilityVerification(t.Context(), p, "instance", 1); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("unknown request replaced", err)
	}
}
