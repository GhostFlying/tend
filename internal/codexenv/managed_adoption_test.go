package codexenv

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagedAdoptionUsesExecutionTargetInsteadOfAnotherCategory(t *testing.T) {
	s, p := fixture(t)
	create(t, s, p)
	original := &Binding{Directory: s.Root, ID: p.ID}
	coordinator, err := original.Pin(t.Context(), "coordinator:main")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := original.Pin(t.Context(), "run:original")
	if err != nil {
		t.Fatal(err)
	}
	pin := func(category, hash string) protocol.ExtensionExecutionPin {
		return protocol.ExtensionExecutionPin{ReleaseID: "ext-" + strings.Repeat(hash, 64), CategoryID: category, Agent: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "planner"}, EffectiveSHA256: strings.Repeat(hash, 64)}
	}
	first := pin("planning", "a")
	p.ExtensionPin = first
	s.Managed = true
	if _, err = s.Execute(t.Context(), Request{Action: "update", ID: p.ID, RequestID: "managed-first", ExpectedRevision: 1, Profile: &p}); err != nil {
		t.Fatal(err)
	}
	status, err := s.Read(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := &Binding{Directory: s.Root, ID: p.ID, Generation: status.Latest, Extensions: first}
	if err = target.QueueAdoption(t.Context(), "coordinator:main"); err != nil {
		t.Fatal(err)
	}
	p.ExtensionPin = pin("review", "b")
	if _, err = s.Execute(t.Context(), Request{Action: "update", ID: p.ID, RequestID: "managed-other-category", ExpectedRevision: 2, Profile: &p}); err != nil {
		t.Fatal(err)
	}
	if coordinator.Current() {
		t.Fatal("managed target did not become pending")
	}
	if !worker.Current() {
		t.Fatal("another category attempted to migrate a pinned worker")
	}
	if err = coordinator.Adopt(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := original.Pin(t.Context(), "coordinator:main")
	if err != nil || reopened.Extensions != first || reopened.Generation != target.Generation || !reopened.ResumeOnly {
		t.Fatal("adoption selected another execution's latest category", reopened, err)
	}
	if !reopened.Current() {
		t.Fatal("adopted execution still follows the shared profile latest")
	}
	s.Managed = false
	if _, err = s.Execute(t.Context(), Request{Action: "sync", ID: p.ID, RequestID: "independent-write", ExpectedRevision: 3}); err == nil {
		t.Fatal("old local manager modified server-managed content")
	}
	status, err = s.Read(p.ID)
	if err != nil || len(status.PendingRequestIDs) != 0 {
		t.Fatal("rejected independent write left a pending control", status.PendingRequestIDs, err)
	}
}
