package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runIssueHandoffPrompt(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "issue handoff-prompt")
	fs.Usage = func() { fmt.Fprint(e.Stderr, commandUsage([]string{"issue", "handoff-prompt"})); fs.PrintDefaults() }
	run := fs.String("run", "", "optional exact Run controlled by this Issue")
	asJSON := fs.Bool("json", false, "print context versions, cursors, queries and prompt")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("issue handoff-prompt requires one Issue ID")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	path := "/api/v1/issues/" + url.PathEscape(pos[0]) + "/handoff-prompt"
	if *run != "" {
		path += "?run=" + url.QueryEscape(*run)
	}
	var value protocol.IssueHandoffPrompt
	if err := c.do(ctx, "GET", path, nil, &value); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(e.Stdout).Encode(value)
	}
	_, err = fmt.Fprint(e.Stdout, value.Prompt)
	return err
}
