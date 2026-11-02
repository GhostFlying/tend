package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// runHostUsage exposes the host-scoped maintenance boundary without borrowing
// owner or run credentials. The private runner performs the authenticated hop.
func runHostUsage(ctx context.Context, e *Env, action string, args []string) error {
	fs := newFlags(e, "usage "+action)
	socket := fs.String("socket", "", "running runner's private control socket")
	data := fs.String("data", "", "native usage batch JSON")
	file := fs.String("file", "", "native usage batch file; - reads stdin")
	request := fs.String("request-id", "", "original request identity for an identical retry")
	dry := fs.Bool("dry-run", false, "export the original host Envelope without sending")
	asJSON := fs.Bool("json", false, "print the complete result as JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *socket == "" {
		return usagef("usage %s requires a host ID and --socket", action)
	}
	operation := protocol.OpRunnerUsageSources
	payload := json.RawMessage(`{}`)
	if action != "host-sources" {
		body, err := issueInput(e, *data, *file)
		if err != nil {
			return err
		}
		var input protocol.UsageUpload
		if len(body) == 0 || json.Unmarshal(body, &input) != nil || input.RunID == "" || input.RootThreadID == "" {
			return usagef("native batch requires run_id, root_thread_id and samples")
		}
		payload, _ = json.Marshal(input)
		if action == "host-preview" {
			operation = protocol.OpRunnerUsagePreview
		} else {
			operation = protocol.OpRunnerUsageUpload
		}
	} else if *data != "" || *file != "" {
		return usagef("host-sources does not accept a usage batch")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	} else if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("malformed request identity")
	}
	env := protocol.Envelope{RequestID: *request, Operation: operation, Target: protocol.Target{Type: "host", ID: pos[0]}, Payload: payload}
	if *dry {
		return json.NewEncoder(e.Stdout).Encode(env)
	}
	if action == "host-upload" {
		fmt.Fprintln(e.Stderr, "request_id:", env.RequestID)
	}
	var response protocol.APIResponse
	if err := localUpgradeRequest(ctx, *socket, "POST", "/runner/v1/rpc", env, &response); err != nil {
		return err
	}
	if response.Error != nil {
		return response.Error
	}
	if response.Operation == nil {
		return fmt.Errorf("host usage receipt is missing; retain request %s", env.RequestID)
	}
	if response.Operation.Error != nil {
		return response.Operation.Error
	}
	if response.Operation.RequestID != env.RequestID || response.Operation.Status != protocol.OpSucceeded {
		return fmt.Errorf("host usage outcome is unconfirmed; retain request %s", env.RequestID)
	}
	return printCapabilityValue(e, response.Operation.Result, *asJSON)
}
