package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestEndToEnd_QuestionInputsOwnerSupplementReachesOriginalExecutor(t *testing.T) {
	clock := testkit.NewClock()
	srv := newServer(t, time.Minute, clock.Now)
	srv.startHistoricalMainThread(testkit.NewHarness())
	web := srv.webLogin(srv.startWeb().URL)
	answerSeen := make(chan protocol.WorkerQuestion, 1)
	grantReady := make(chan string, 1)
	downloaded := make(chan error, 1)
	raw := []byte("original supplemental evidence\x00\xff")
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		script := passing(spec)
		if spec.Role == protocol.RoleImplement {
			script.Act = func(ctx context.Context) error {
				q, err := spec.AskOwner(ctx, protocol.AskOwnerPayload{QuestionKey: "evidence", Question: "Please provide original evidence", Evidence: "The report cannot be verified from inline text"})
				if err != nil {
					return err
				}
				answerSeen <- q
				var id string
				select {
				case id = <-grantReady:
				case <-ctx.Done():
					return ctx.Err()
				}
				path, err := spec.DownloadFile(ctx, id)
				if err == nil {
					var got []byte
					got, err = os.ReadFile(path)
					if err == nil && !bytes.Equal(got, raw) {
						err = fmt.Errorf("original bytes differ")
					}
				}
				downloaded <- err
				return err
			}
		}
		return script
	})
	n.heartbeat = 20 * time.Millisecond
	legacyQuestionContract(n)
	n.start()
	task := srv.createTask()
	n.diagnoseFailure(task)
	var decision protocol.DecisionView
	eventually(t, "original worker question", func() bool {
		for _, d := range web.decisions() {
			if d.Worker != nil && d.TaskID == task {
				decision = d
				return true
			}
		}
		return false
	})
	lease := workerQuestionLease(t, srv, decision.Worker.RunID)
	web.op(srv.ids.New(protocol.PrefixRequest), "decisions.respond", protocol.Target{Type: "decision", ID: decision.DecisionID}, protocol.AnswerDecisionPayload{Version: decision.Version, Text: "Use the original attachment"})
	var answered protocol.WorkerQuestion
	select {
	case answered = <-answerSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("original executor did not receive answer")
	}
	uploaded, err := srv.app.UploadConversationAttachment(t.Context(), owner, srv.ids.New(protocol.PrefixRequest), "evidence.bin", "application/octet-stream", bytes.NewReader(raw))
	if err != nil || uploaded.Status != protocol.OpSucceeded {
		t.Fatal(uploaded, err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(uploaded.Result, &file); err != nil {
		t.Fatal(err)
	}
	message := web.send(protocol.SubmitMessagePayload{Body: "Original evidence for the answered question", ReplyTo: decision.Worker.QuestionMessageID, Attachments: []string{file.FileID}})
	zero, sourceRevision := int64(0), int64(1)
	p := protocol.QuestionInputPayload{Version: answered.Version, RunID: answered.RunID, LeaseID: lease.LeaseID, FileIDs: []string{file.FileID}, IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: message, ExpectedSourceRevision: &sourceRevision}, Reason: "Owner supplied this exact original"}
	body, _ := json.Marshal(p)
	env := protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "decisions.grant_inputs", Target: protocol.Target{Type: "decision", ID: decision.DecisionID}, ExpectedControlRevision: &zero, Payload: body}
	var response protocol.APIResponse
	if status := web.do("POST", "/api/v1/ops", env, &response); status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, response)
	}
	var grants protocol.QuestionInputPage
	if err := json.Unmarshal(response.Operation.Result, &grants); err != nil || len(grants.Grants) != 1 || grants.Grants[0].Files[0].SHA256 != file.SHA256 {
		t.Fatal(grants, err)
	}
	// A coordinator can relay these exact IDs; the original download callback
	// discovers current metadata without changing its frozen assignment or run.
	grantReady <- grants.Grants[0].Files[0].FileID
	select {
	case err := <-downloaded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original executor could not download supplemental input")
	}
	eventually(t, "original execution completes normally", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=? AND role='implement'`, task) != 1 {
		t.Fatal("attachment replaced original execution")
	}
}
