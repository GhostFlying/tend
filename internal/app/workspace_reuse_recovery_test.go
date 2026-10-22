package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestReusedWorkspaceHostLossOffersWaitOrCancelWithoutRetargeting(t *testing.T) {
	for _, kind := range []string{"git", "directory"} {
		t.Run(kind, func(t *testing.T) {
			h := newDeliverableHarness(t)
			sourceSpec := temporaryReportSpec()
			if kind == "git" {
				sourceSpec.Workspace = domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}
			}
			source := h.createReportTask("source", sourceSpec)
			original, ok, _ := h.claimDeliverableRun()
			if !ok {
				t.Fatal("source not assigned")
			}
			if kind == "git" {
				if op := h.reportWorkspace(original); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			} else {
				h.reportDirectory(original, t.TempDir())
			}
			h.completeReport(original)
			if !h.releaseResource(source.TaskID, original.Workspace.ID) {
				t.Fatal("source not released")
			}
			followSpec := temporaryReportSpec()
			followSpec.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: source.TaskID}
			follow := h.createReportTask("follow-up", followSpec)
			pinned := h.resourceForTask(follow.TaskID)
			h.withHosts("linux-2")
			h.checkWorkspaces()
			if h.task(follow.TaskID).BlockReason != protocol.BlockWorkspaceUnavailable {
				t.Fatal("follow-up did not wait for its host")
			}
			questions := h.openDecisions(follow.TaskID)
			if len(questions) != 1 {
				t.Fatalf("owner questions: %+v", questions)
			}
			var options []string
			if err := json.Unmarshal([]byte(questions[0].OptionsJSON), &options); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(options, domain.StartOverOption) || !slices.Contains(options, domain.WaitForHostOption) || !slices.Contains(options, "cancel the task") || !strings.Contains(questions[0].Question, "workspace.mode=new") {
				t.Fatalf("invalid recovery choices: %+v", questions[0])
			}
			op := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: questions[0].DecisionID}, protocol.AnswerDecisionPayload{Version: questions[0].Version, Option: domain.StartOverOption}, nil), AnswerDecision{})
			if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
				t.Fatalf("owner selected unsupported recovery: %+v", op)
			}
			if got := h.resourceForTask(follow.TaskID); got.WorkspaceID != pinned.WorkspaceID || got.HostID != pinned.HostID || got.Kind != pinned.Kind {
				t.Fatalf("rejected recovery retargeted resource: %+v", got)
			}
			h.exec(runner("linux-2"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-2"}, protocol.HelloPayload{WorkspaceProtocol: 1}, nil), Hello{})
			if _, ok, _ := h.claim("linux-2"); ok {
				t.Fatal("follow-up allocated a replacement on another host")
			}
			// Host recovery must recognize the wait/cancel decision even though it
			// intentionally has no start-over effect.
			h.withHosts("linux-1", "linux-2")
			h.checkWorkspaces()
			if h.task(follow.TaskID).BlockReason != protocol.BlockNone || len(h.openDecisions(follow.TaskID)) != 0 {
				t.Fatal("host return failed to resolve reuse workspace decision")
			}
			resumed, ok, _ := h.claimDeliverableRun()
			if !ok || resumed.TaskID != follow.TaskID || resumed.Workspace.ID != pinned.WorkspaceID || !resumed.Workspace.Reused {
				t.Fatalf("host recovery changed fixed binding: %+v %v", resumed, ok)
			}
		})
	}
}

func TestLegacyStartOverDecisionCannotAbandonReusedResource(t *testing.T) {
	h := newDeliverableHarness(t)
	source := h.createReportTask("source", temporaryReportSpec())
	as, _, _ := h.claimDeliverableRun()
	h.reportDirectory(as, t.TempDir())
	h.completeReport(as)
	h.releaseResource(source.TaskID, as.Workspace.ID)
	spec := temporaryReportSpec()
	spec.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: source.TaskID}
	follow := h.createReportTask("follow-up", spec)
	h.withHosts("linux-2")
	h.checkWorkspaces()
	question := h.openDecisions(follow.TaskID)[0]
	// A previously persisted question may still expose the older effect.
	err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE decisions SET options_json=?,effects_json=? WHERE decision_id=?`, `["wait for the host","start over on another host","cancel the task"]`, `{"start over on another host":"start_over","cancel the task":"cancel"}`, question.DecisionID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	answer := h.answer(question, domain.StartOverOption)
	if answer.Effect == nil || answer.Effect.Applied || !strings.Contains(answer.Effect.Reason, "reused workspace is pinned") {
		t.Fatalf("legacy effect unexpectedly applied: %+v", answer)
	}
	if got := h.resourceForTask(follow.TaskID); got.WorkspaceID != as.Workspace.ID || got.HostID != "linux-1" || got.Kind != "directory" {
		t.Fatalf("legacy recovery replaced resource: %+v", got)
	}
	valid := h.openDecisions(follow.TaskID)
	if len(valid) != 1 || strings.Contains(valid[0].OptionsJSON, domain.StartOverOption) {
		t.Fatalf("legacy invalid choice left no valid owner decision: %+v", valid)
	}
	h.withHosts("linux-1", "linux-2")
	h.checkWorkspaces()
	if len(h.openDecisions(follow.TaskID)) != 0 || h.task(follow.TaskID).BlockReason != protocol.BlockNone {
		t.Fatal("replacement recovery decision failed to resolve on host return")
	}

}

func TestWorkspaceDecisionRecognitionDoesNotAnswerPlannerQuestions(t *testing.T) {
	question := store.DecisionRow{OptionsJSON: `["wait for the host","cancel the task"]`, EffectsJSON: `{}`}
	if isWorkspaceDecision(question) {
		t.Fatal("planner's no-effect question mistaken for automatic workspace decision")
	}
}
