package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// RunLocalAgents is a thin client of the runner's existing private control socket.
// It never opens the local registry or interprets agent definitions.
func RunLocalAgents(ctx context.Context, env Env, socket, operation, requestID string, revision *uint64) int {
	if ((operation == "status" || operation == "validate" || operation == "register" || operation == "registration") && (requestID != "" || revision != nil)) || (operation == "reconcile" && revision != nil) {
		fmt.Fprintln(env.Stderr, "request ID/revision flags do not apply to this operation")
		return exitUsage
	}
	client := env.HTTP
	if client == nil {
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport, Timeout: 40 * time.Second}
	}
	do := func(method, path string, input any) (protocol.LocalAgentConfigurationResult, error) {
		var body io.Reader
		if input != nil {
			raw, err := json.Marshal(input)
			if err != nil {
				return protocol.LocalAgentConfigurationResult{}, err
			}
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, body)
		if err != nil {
			return protocol.LocalAgentConfigurationResult{}, err
		}
		response, err := client.Do(req)
		if err != nil {
			return protocol.LocalAgentConfigurationResult{}, fmt.Errorf("local runner unavailable; retain the original request ID and revision")
		}
		defer response.Body.Close()
		var result protocol.LocalAgentConfigurationResult
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
			return result, fmt.Errorf("local runner response unavailable")
		}
		if response.StatusCode != http.StatusOK || result.ErrorCode != "" {
			return result, fmt.Errorf("local agent configuration: %s", result.ErrorCode)
		}
		return result, nil
	}
	var result protocol.LocalAgentConfigurationResult
	var err error
	switch operation {
	case "status":
		result, err = do("GET", "/agents/status", nil)
	case "registration":
		result, err = do("GET", "/agents/registration", nil)
	case "register":
		result, err = do("POST", "/agents/register", nil)
	case "validate":
		result, err = do("POST", "/agents/validate", nil)
	case "reload":
		if requestID != "" && revision == nil {
			fmt.Fprintln(env.Stderr, "retry requires the original --revision with --request-id")
			return exitUsage
		}
		if requestID == "" {
			if env.IDs == nil {
				fmt.Fprintln(env.Stderr, "request ID generator is required")
				return exitError
			}
			requestID = env.IDs.New(protocol.PrefixRequest)
		}
		if revision == nil {
			status, readErr := do("GET", "/agents/status", nil)
			if readErr != nil || status.Current == nil {
				fmt.Fprintln(env.Stderr, "cannot read local revision; reload was not sent")
				return exitError
			}
			observed := status.Current.Revision
			revision = &observed
		}
		fmt.Fprintf(env.Stderr, "Agent configuration request %s; revision %d\n", requestID, *revision)
		result, err = do("POST", "/agents/reload", protocol.ReloadLocalAgentsRequest{RequestID: requestID, ExpectedRevision: *revision})
	case "reconcile":
		if requestID == "" {
			fmt.Fprintln(env.Stderr, "reconcile requires --request-id")
			return exitUsage
		}
		result, err = do("POST", "/agents/reconcile", protocol.ReconcileLocalAgentsRequest{RequestID: requestID})
	default:
		fmt.Fprintln(env.Stderr, "usage: tend-runner agents status|validate|reload|reconcile|register|registration --config <file>")
		return exitUsage
	}
	if result.Registration != nil || result.Current != nil || result.ErrorCode != "" || result.RequestState != "" {
		if encodeErr := json.NewEncoder(env.Stdout).Encode(result); encodeErr != nil {
			fmt.Fprintln(env.Stderr, "cannot write local configuration result")
			return exitError
		}
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		if result.Current != nil && result.Current.PendingRequestID != "" {
			fmt.Fprintf(env.Stderr, "Use agents reconcile with the same --config and --request-id %s before a new reload.\n", result.Current.PendingRequestID)
		}
		return exitError
	}
	if operation == "status" && result.Current != nil && result.Current.State == "empty" {
		fmt.Fprintln(env.Stderr, "No local configuration has been accepted; use agents validate or agents reload with the same --config.")
	}
	return exitOK
}
