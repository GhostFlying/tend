package app

import (
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestPhysicalMountDirectoryDoesNotInventLogicalSubdirectory(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "provider-root"
		if nested {
			name = "nested-layout"
		}
		t.Run(name, func(t *testing.T) {
			h := newDeliverableHarness(t)
			spec := testSpec
			spec.Deliverables = []string{domain.DeliveryReport}
			op, task := h.createTask(owner, createTaskPayload{Title: "investigation", Spec: spec})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			as, ok, _ := h.claimDeliverableRun()
			if !ok || !as.MountDirectories || !as.MountOriginIdentity {
				t.Fatal("assignment did not advertise optional mount facts")
			}
			root := t.TempDir()
			actual := root
			if nested {
				actual = filepath.Join(root, "tend")
			}
			branch := "tend/" + h.task(task.TaskID).WorkspaceName
			origin := protocol.RepositoryRemoteKey("https://github.com/acme/repo.git")
			report := protocol.WorkspaceReport{Path: root, Mounts: []protocol.ResolvedMount{{Path: "tend", ResolvedBaseSHA: baseSHA, ResolvedFrom: "candidate:main", Branch: branch, OriginIdentity: origin}}}
			send := func(as protocol.Assignment) {
				t.Helper()
				if op := h.runAs(as, protocol.OpRunsReportWorkspace, report, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			check := func(id, want string) {
				t.Helper()
				detail := relationDetail(t, h, id)
				if detail.WorkspaceState == nil || len(detail.WorkspaceState.Mounts) != 1 {
					t.Fatalf("workspace %+v", detail.WorkspaceState)
				}
				mount := detail.WorkspaceState.Mounts[0]
				if mount.Path != "tend" || mount.Directory != want || mount.OriginIdentity != origin || detail.WorkspaceState.Path != root {
					t.Fatalf("logical/physical locations conflated: %+v", detail.WorkspaceState)
				}
			}
			send(as)
			check(task.TaskID, "")
			report.Mounts[0].Directory = actual
			send(as)
			check(task.TaskID, actual)
			// A subsequent report cannot replace the origin captured at layout.
			report.Mounts[0].OriginIdentity = protocol.RepositoryRemoteKey("https://github.com/acme/changed.git")
			send(as)
			check(task.TaskID, actual)
			// An older runner may omit both optional facts.
			report.Mounts[0].OriginIdentity = ""
			report.Mounts[0].Directory = ""
			send(as)
			check(task.TaskID, actual)
			result := testArtifact(h.task(task.TaskID).WorkspaceName)
			result.Mounts[0].Changed = false
			result.Report = &protocol.ResultReport{Body: "investigated"}
			if op := h.runAs(as, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			accept, ok, _ := h.claimDeliverableRun()
			if !ok {
				t.Fatal("no acceptance")
			}
			if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			reuse := spec
			reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: task.TaskID}
			op, follow := h.createTask(owner, h.sameItemReusePayload("follow-up", reuse))
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if !h.releaseResource(task.TaskID, as.Workspace.ID) {
				t.Fatal("release failed")
			}
			next, ok, _ := h.claimDeliverableRun()
			if !ok || next.TaskID != follow.TaskID {
				t.Fatal("no reuse run")
			}
			send(next)
			check(follow.TaskID, actual)
			check(task.TaskID, actual)
		})
	}
}
