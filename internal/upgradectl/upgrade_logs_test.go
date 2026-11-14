package upgradectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

func TestUpgradeStepLogsAssociateFailureAndRecoveryWithoutPollSpam(t *testing.T) {
	c, _ := testController(t)
	var out bytes.Buffer
	c.log = slog.New(slog.NewJSONHandler(&out, nil))
	row := store.UpgradeRow{StartRequestID: "original-request", Deadline: time.Now().Add(time.Minute).UnixMilli()}
	row.View.UpgradeID, row.View.Phase = "original-batch", "server_updater"
	row.View.OverallDeadlineMillis = row.Deadline
	started := c.cfg.Now()
	err := errors.New("original connection failed")
	c.logUpgradeStep(t.Context(), row, "observe_supervisor", started, context.DeadlineExceeded, err)
	c.logUpgradeStep(t.Context(), row, "observe_supervisor", started, context.DeadlineExceeded, err)
	c.logUpgradeStep(t.Context(), row, "observe_supervisor", started, nil, nil)
	c.logUpgradeStep(t.Context(), row, "observe_supervisor", started, nil, nil)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatal("polls duplicated the same failure or dropped recovery", out.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["request_id"] != row.StartRequestID || record["upgrade_id"] != row.View.UpgradeID || record["step"] != "observe_supervisor" || record["overall_deadline_ms"] != float64(row.View.OverallDeadlineMillis) {
			t.Fatal("lost original operation or deadline", record)
		}
		if i == 0 && (record["error"] != err.Error() || record["context_error"] != context.DeadlineExceeded.Error()) {
			t.Fatal("first failure lost its actual cause", record)
		}
	}
}
