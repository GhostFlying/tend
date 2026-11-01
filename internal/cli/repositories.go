package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runRepository(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 || (args[0] != "register" && args[0] != "show") {
		return usagef("repo expects register|show <name> [--remote <url>] [--base <branch,...>] [--retired] [--json]")
	}
	fs := newFlags(e, "repo "+args[0])
	reason := fs.String("reason", "Register the owner's repository in the persistent online catalog.", "reason retained in the operation record")
	dry := fs.Bool("dry-run", false, "print original Envelope")
	data := fs.String("data", "", "complete repository payload")
	file := fs.String("file", "", "payload JSON file; - reads stdin")
	remote := fs.String("remote", "", "repository remote URL")
	bases := fs.String("base", "", "comma-separated default base candidates")
	retired := fs.Bool("retired", false, "stop new mounts while retaining existing tasks")
	asJSON := fs.Bool("json", false, "print the server answer as JSON")
	requestID := fs.String("request-id", "", "stable request identity for reconciliation")
	revision := fs.Int64("revision", -1, "expected control revision; defaults to the current catalog entry")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("repo %s takes one repository name", args[0])
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	input, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if args[0] == "show" && (len(input) > 0 || *dry || *requestID != "" || *revision != -1) {
		return usagef("show takes no mutation input")
	}
	if args[0] == "register" && *requestID != "" && len(input) > 0 && *revision < 0 {
		return usagef("retry requires original --revision and payload")
	}
	if args[0] == "register" && *requestID != "" && len(input) == 0 {
		var recorded protocol.RequestView
		err := c.do(ctx, "GET", "/api/v1/requests/"+url.PathEscape(*requestID), nil, &recorded)
		if err == nil {
			if recorded.Operation != "repos.register" {
				return usagef("request belongs to %s", recorded.Operation)
			}
			if recorded.Status == protocol.OpSucceeded {
				var registration protocol.RepositoryRegistration
				if err := json.Unmarshal(recorded.Result, &registration); err != nil {
					return err
				}
				if registration.Repo != pos[0] {
					return usagef("request belongs to repository %s", registration.Repo)
				}
			}
			return repositoryOutcome(e, *asJSON, pos[0], protocol.APIResponse{Operation: &protocol.Operation{RequestID: recorded.RequestID, Status: recorded.Status, Result: recorded.Result, Error: recorded.Error}})
		}
		pe, ok := errors.AsType[*protocol.Error](err)
		if !ok || pe.Code != protocol.CodeNotFound {
			return err
		}
		return usagef("original request is absent; supply original complete --data/--file and --revision, or use request replay")
	}
	var current protocol.RepositoryRegistration
	if args[0] == "show" || len(input) == 0 || *revision < 0 {
		err = c.do(ctx, "GET", "/api/v1/repos/"+url.PathEscape(pos[0]), nil, &current)
	}
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
	if *remote == "" {
		*remote = current.RemoteURL
	}
	if *remote == "" && len(input) == 0 {
		return usagef("--remote is required for a new repository")
	}
	if *requestID == "" {
		*requestID = e.IDs.New(protocol.PrefixRequest)
	} else if *revision < 0 {
		return usagef("reconciliation requires the original --revision")
	}
	if *revision < 0 {
		*revision = current.ControlRevision
	}
	baseCandidates := current.BaseCandidates
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "base" {
			baseCandidates = nil
			if *bases != "" {
				baseCandidates = strings.Split(*bases, ",")
			}
		}
	})
	retiredProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "retired" {
			retiredProvided = true
		}
	})
	if !retiredProvided {
		*retired = current.Retired
	}
	raw, err := json.Marshal(protocol.RegisterRepositoryPayload{RemoteURL: *remote, BaseCandidates: baseCandidates, Retired: *retired})
	if err != nil {
		return err
	}
	if len(input) > 0 {
		raw = input
	}
	env := protocol.Envelope{RequestID: *requestID, Operation: "repos.register", Target: protocol.Target{Type: "repo", ID: pos[0]}, ExpectedControlRevision: revision, Payload: raw, Reason: *reason}
	if *dry {
		return (owner{e: e, json: true}).printJSON(env)
	}
	fmt.Fprintf(e.Stderr, "Repository request %s; revision %d\n", *requestID, *revision)
	var response protocol.APIResponse
	err = c.do(ctx, "POST", "/api/v1/ops", env, &response)
	if isTransport(err) {
		err = c.do(ctx, "POST", "/api/v1/ops", env, &response)
	}
	if err != nil {
		return err
	}
	return repositoryOutcome(e, *asJSON, pos[0], response)
}

func repositoryOutcome(e *Env, asJSON bool, name string, response protocol.APIResponse) error {
	if response.Operation == nil {
		return errors.New("repository registration returned no operation")
	}
	if response.Operation.Error != nil {
		return response.Operation.Error
	}
	if response.Operation.Status != protocol.OpSucceeded {
		return fmt.Errorf("repository registration outcome is %s; reconcile request %s", response.Operation.Status, response.Operation.RequestID)
	}
	if asJSON {
		return json.NewEncoder(e.Stdout).Encode(response)
	}
	_, err := fmt.Fprintf(e.Stdout, "Repository %s registered without restarting the server.\n", name)
	return err
}
