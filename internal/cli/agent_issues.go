package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runAgentIssues(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "agents issues")
	fs.Bool("json", true, "print complete frozen association facts")
	environment := fs.String("environment", "", "canonical execution environment ID")
	local := fs.String("agent", "", "canonical local Agent ID")
	scope := fs.String("scope", "all", "all, current (unsettled runs/original worker questions) or history")
	state := fs.String("state", "", "canonical Issue lifecycle filter")
	search := fs.String("search", "", "Issue title or ID search")
	after := fs.String("after", "", "Issue ID cursor from the preceding page")
	limit := fs.Int("limit", 25, "Issue page size, between 1 and 100; each item retains all matching runs")
	snapshot := fs.String("snapshot", "", "optional frozen inventory snapshot ID")
	revision := fs.Int64("agent-revision", 0, "optional frozen Agent revision")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || !protocol.AgentInventoryID(*environment) || !protocol.AgentInventoryID(*local) || *limit < 1 || *limit > 100 || *revision < 0 {
		return usagef("agents issues requires --environment <id> --agent <id> and a valid limit/revision")
	}
	if *scope != "all" && *scope != "current" && *scope != "history" {
		return usagef("scope must be all, current or history")
	}
	if *state != "" && *state != "active" && *state != "paused" && *state != "cancelling" && *state != "cancelled" && *state != "closed" {
		return usagef("invalid Issue state")
	}
	if (*after != "" && !protocol.ValidID(protocol.PrefixWorkItem, *after)) || (*snapshot != "" && !protocol.AgentInventoryID(*snapshot)) || len(*search) > 500 {
		return usagef("invalid cursor, snapshot or search")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	q := url.Values{"environment_id": {*environment}, "local_id": {*local}, "scope": {*scope}, "state": {*state}, "search": {*search}, "after": {*after}, "limit": {strconv.Itoa(*limit)}, "snapshot_id": {*snapshot}, "agent_revision": {strconv.FormatInt(*revision, 10)}}
	var page protocol.AgentIssuePage
	if err := c.do(ctx, "GET", "/api/v1/agent-issues?"+q.Encode(), nil, &page); err != nil {
		return err
	}
	return json.NewEncoder(e.Stdout).Encode(page)
}
