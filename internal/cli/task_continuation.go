package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runTaskContinue(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "task continue")
	data := fs.String("data", "", "complete ContinueTaskPayload JSON")
	file := fs.String("file", "", "payload JSON file; - reads stdin")
	request := fs.String("request-id", "", "original request identity for an identical replay")
	revision := fs.Int64("revision", -1, "expected Task control revision")
	dry := fs.Bool("dry-run", false, "print the frozen Envelope without sending")
	fs.Bool("json", false, "print the complete operation receipt (default)")
	fs.Usage = func() {
		fmt.Fprintln(e.Stderr, "usage: tend task continue <task_id> --revision <revision> --data '{\"reason\":\"Continue the same goal after rebase\"}' [--request-id <id>] [--dry-run] [--json]")
		fs.PrintDefaults()
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !protocol.ValidID(protocol.PrefixTask, pos[0]) || *revision < 1 {
		return usagef("task continue requires a valid Task ID and positive --revision")
	}
	raw, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	var p protocol.ContinueTaskPayload
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return usagef("invalid Task continuation: %v", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return usagef("trailing Task continuation data")
	}
	if strings.TrimSpace(p.Reason) == "" || p.ExpectedAssignmentEpoch < 0 {
		return usagef("task continuation requires reason and a nonnegative assignment epoch")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("malformed request ID")
	}
	env := protocol.Envelope{Operation: "tasks.continue", RequestID: *request, Target: protocol.Target{Type: "task", ID: pos[0]}, ExpectedControlRevision: revision, Payload: raw, Reason: p.Reason}
	if *dry {
		return (owner{e: e, json: true}).printJSON(env)
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	return sendDirectEnvelope(ctx, owner{e: e, c: c, json: true}, env)
}
