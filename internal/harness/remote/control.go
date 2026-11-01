package remote

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
)

func startupError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	err = errors.Join(err, controlCause(err))
	if cause := context.Cause(ctx); errors.Is(cause, harness.ErrPaused) || errors.Is(cause, harness.ErrHeld) {
		if !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
	}
	return err
}

func controlCause(err error) error {
	var reply *traex.RPCError
	if !errors.As(err, &reply) || reply.Code != -32600 {
		return nil
	}
	var data struct {
		Desired protocol.RunDesired `json:"desired"`
	}
	if json.Unmarshal(reply.Data, &data) != nil {
		return nil
	}
	switch data.Desired {
	case protocol.DesiredPaused:
		return harness.ErrPaused
	case protocol.DesiredHeld:
		return harness.ErrHeld
	default:
		return nil
	}
}

// constrainControl gives the run credential one native writer and one root
// conversation. It grants no access to the center user's other native history.
func constrainControl(data []byte, run *nativeRun, desired protocol.RunDesired) ([]byte, error) {
	var frame nativeFrame
	if json.Unmarshal(data, &frame) != nil {
		return nil, errTUIControl
	}
	if frame.Method == "" {
		if frame.ID == nil {
			return nil, errTUIControl
		}
		return data, nil
	}
	var p map[string]json.RawMessage
	if len(frame.Params) > 0 && string(frame.Params) != "null" && json.Unmarshal(frame.Params, &p) != nil {
		return nil, errTUIControl
	}
	if p == nil {
		p = map[string]json.RawMessage{}
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	switch frame.Method {
	case "initialize", "initialized", "model/list":
	case "thread/start":
		if run.thread != "" || run.startPending || desired != protocol.DesiredRun || frame.ID == nil {
			return nil, errTUIControl
		}
		pinExecution(p, run.cwd, true)
		run.startPending = true
		run.startID = string(frame.ID)
	case "thread/resume", "thread/inject_items", "thread/read", "thread/turns/list", "thread/items/list", "thread/context/usage", "thread/compact/start", "turn/start", "turn/steer", "turn/interrupt":
		var id string
		if json.Unmarshal(p["threadId"], &id) != nil || run.thread == "" || id != run.thread {
			return nil, errTUIControl
		}
		switch frame.Method {
		case "thread/resume", "thread/inject_items", "thread/compact/start", "turn/start", "turn/steer":
			if desired != protocol.DesiredRun {
				return nil, errTUIControl
			}
		case "turn/interrupt":
			if desired == protocol.DesiredHeld {
				return nil, errTUIControl
			}
		}
		if frame.Method == "thread/resume" {
			p["cwd"], _ = json.Marshal(run.cwd)
			delete(p, "config")
			delete(p, "history")
			delete(p, "path")
			delete(p, "runtimeWorkspaceRoots")
			delete(p, "environments")
		}
		if frame.Method == "turn/start" {
			pinExecution(p, run.cwd, false)
		}
	default:
		return nil, errTUIControl
	}
	out := map[string]any{"method": frame.Method}
	if frame.ID != nil {
		out["id"] = frame.ID
	}
	if frame.Method != "initialized" || len(p) > 0 {
		out["params"] = p
	}
	return json.Marshal(out)
}
func pinExecution(p map[string]json.RawMessage, cwd string, start bool) {
	p["cwd"], _ = json.Marshal(cwd)
	p["environments"], _ = json.Marshal([]map[string]string{{"environmentId": "remote", "cwd": cwd}})
	for _, key := range []string{"config", "runtimeWorkspaceRoots", "approvalPolicy", "approvalsReviewer", "sandboxPolicy", "permissions"} {
		delete(p, key)
	}
	if start {
		p["sandbox"], _ = json.Marshal("danger-full-access")
		p["approvalPolicy"], _ = json.Marshal("never")
	}
}
