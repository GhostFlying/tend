package contextbuilder

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestWorkerQuestionContextPreservesProvenanceAndAnswer(t *testing.T) {
	d := store.DecisionRow{
		DecisionID: "dec_question", TaskID: "task_investigate", Version: 1, Status: store.DecisionOpen,
		Question: "Which compatibility guarantee applies?", OptionsJSON: `["existing","new"]`,
		Worker: &protocol.WorkerQuestionSource{RunID: "run_investigate", Role: protocol.RoleImplement, SpecVersion: 3,
			QuestionKey: "compatibility", QuestionMessageID: "msg_question", Evidence: "Old clients use the earlier field."},
	}
	for _, part := range []string{"run_investigate", "spec v3", "msg_question", "Old clients use the earlier field.", "holding its execution slot"} {
		if line := decisionLine(d); !strings.Contains(line, part) {
			t.Errorf("open question lost %q: %s", part, line)
		}
	}
	d.Status, d.AnswerJSON = store.DecisionAnswered, `{"option":"existing","text":"Keep the old clients working."}`
	line := answerLine(d)
	for _, part := range []string{"run_investigate", "msg_question", "Keep the old clients working.", "do not retry merely to deliver it"} {
		if !strings.Contains(line, part) {
			t.Errorf("answered question lost %q: %s", part, line)
		}
	}
	if strings.Contains(line, "holding its execution slot") {
		t.Fatal("answered question still claims the worker is waiting")
	}
}
