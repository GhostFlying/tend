package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runAgentCategory(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 || (args[0] != "put" && args[0] != "show") {
		return usagef("category expects put|show <id> [--title <text>] [--description <text>] [--state active|retired]")
	}
	fs := newFlags(e, "category "+args[0])
	title := fs.String("title", "", "classification title; required for put")
	description := fs.String("description", "", "classification description")
	state := fs.String("state", "active", "active or retired; classification does not grant execution permission")
	requestID := fs.String("request-id", "", "original request ID when retrying")
	revision := fs.Int64("revision", -1, "expected revision; defaults to current category")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("category %s requires one ID", args[0])
	}
	if args[0] == "put" && (strings.TrimSpace(*title) == "" || (*state != "active" && *state != "retired")) {
		return usagef("put requires --title and --state active|retired")
	}
	if args[0] == "put" && *requestID != "" && *revision < 0 {
		return usagef("retry requires the original --revision with --request-id")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	var current protocol.AgentCategory
	err = c.do(ctx, "GET", "/api/v1/categories/"+url.PathEscape(pos[0]), nil, &current)
	if args[0] == "show" {
		if err != nil {
			return err
		}
		return json.NewEncoder(e.Stdout).Encode(current)
	}
	if err != nil {
		pe, ok := errors.AsType[*protocol.Error](err)
		if !ok || pe.Code != protocol.CodeNotFound {
			return err
		}
	}
	if *revision < 0 {
		*revision = current.Revision
	}
	if *requestID == "" {
		*requestID = e.IDs.New(protocol.PrefixRequest)
	}
	payload, err := json.Marshal(protocol.PutAgentCategoryPayload{Title: *title, Description: *description, State: *state})
	if err != nil {
		return err
	}
	env := protocol.Envelope{RequestID: *requestID, Operation: "categories.put", Target: protocol.Target{Type: "category", ID: pos[0]}, ExpectedControlRevision: revision, Payload: payload}
	fmt.Fprintf(e.Stderr, "Category request %s; revision %d\n", *requestID, *revision)
	var response protocol.APIResponse
	err = c.do(ctx, "POST", "/api/v1/ops", env, &response)
	if isTransport(err) {
		err = c.do(ctx, "POST", "/api/v1/ops", env, &response)
	}
	if err != nil {
		return err
	}
	if response.Operation == nil {
		return fmt.Errorf("category operation returned no receipt; retain request %s and revision %d", *requestID, *revision)
	}
	if response.Operation.Error != nil {
		return response.Operation.Error
	}
	if response.Operation.Status != protocol.OpSucceeded {
		return fmt.Errorf("category request %s is %s; retain its original revision", *requestID, response.Operation.Status)
	}
	return json.NewEncoder(e.Stdout).Encode(response.Operation)
}
