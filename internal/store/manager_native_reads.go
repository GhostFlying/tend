package store

import (
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// NativeDeliveryRejectedCurrentTask is still a definite provider rejection.
// The undelivered ordinary read body is retired; only current SoT is readable.
const NativeDeliveryRejectedCurrentTask = "rejected_current_task"

// NativeTaskReadTarget accepts only the original valid tasks_read arguments.
// It never classifies arbitrary historical callbacks as safe to execute again.
func NativeTaskReadTarget(tool, input string) string {
	if tool != "tasks_read" {
		return ""
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &args) != nil || len(args) != 1 {
		return ""
	}
	var task string
	if json.Unmarshal(args["task_id"], &task) != nil || !protocol.ValidID(protocol.PrefixTask, task) {
		return ""
	}
	return task
}

func reconstructibleTaskRead(tool, input, text string) bool {
	task := NativeTaskReadTarget(tool, input)
	if task == "" {
		return false
	}
	var value struct {
		OK     bool `json:"ok"`
		Result struct {
			TaskID string `json:"task_id"`
		} `json:"result"`
	}
	return json.Unmarshal([]byte(text), &value) == nil && value.OK && value.Result.TaskID == task
}
