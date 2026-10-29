package contextbuilder

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCanonicalNoStageSnapshotRetainsTaskCount(t *testing.T) {
	view := protocol.WorkItemView{WorkItem: protocol.WorkItem{WorkItemID: "issue", Revision: 1, IssuePlan: protocol.IssuePlan{Title: "Goal"}}, Column: "active", Tasks: []protocol.TaskSummary{{TaskID: "task"}}}
	line := workItemLine(view)
	if !strings.Contains(line, "1 execution task(s)") || strings.Contains(line, "this round may be complete") {
		t.Fatalf("no-stage snapshot=%s", line)
	}
}
