package traex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestThreadPermissionsUseNativeReadback(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, reported := range []bool{false, true} {
			f, s := newFake(t)
			method := methodThreadStart
			if resume {
				method = methodThreadResume
			}
			f.on(method, func(raw json.RawMessage) any {
				var params ThreadStartParams
				if err := json.Unmarshal(raw, &params); err != nil {
					t.Error(err)
				}
				if params.Sandbox != "workspace-write" || params.ApprovalPolicy != "on-request" || params.ApprovalsReviewer != "auto_review" {
					t.Errorf("permission request = %s", raw)
				}
				res := ThreadStartResponse{Thread: Thread{ID: "permission-test"}}
				if reported {
					// Deliberately differ from the request: never invent confirmation.
					res.Sandbox.Type = "readOnly"
					res.ApprovalPolicy = "never"
					res.ApprovalsReviewer = "guardian_subagent"
				}
				return res
			})
			cfg := harness.ThreadConfig{Sandbox: "workspace-write", ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review"}
			var th harness.Thread
			var err error
			if resume {
				th, err = s.ResumeThread(context.Background(), "permission-test", cfg)
			} else {
				th, err = s.StartThread(context.Background(), cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := harness.PermissionSettings{}
			if reported {
				want = harness.PermissionSettings{Sandbox: "read-only", ApprovalPolicy: "never", ApprovalsReviewer: "auto_review"}
			}
			if got := th.(harness.PermissionedThread).PermissionDefaults(); got != want {
				t.Errorf("resume=%v reported=%v: got %+v, want %+v", resume, reported, got, want)
			}
		}
	}
}

func TestUnexpectedNativeApprovalsAreNotAccepted(t *testing.T) {
	_, s := newFake(t)
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval"} {
		result, err := s.serverRequest(context.Background(), method, json.RawMessage(`{}`))
		if result != nil || !errors.Is(err, errUnknownMethod) {
			t.Errorf("%s = %v, %v; expected rejection", method, result, err)
		}
	}
}
