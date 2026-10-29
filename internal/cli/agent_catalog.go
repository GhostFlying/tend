package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runAgentCategoryList(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "category list")
	state := fs.String("state", "active", "active, retired or all")
	after := fs.String("after", "", "last category ID from the preceding page")
	limit := fs.Int("limit", 50, "page size, at most 100")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("category list accepts only flags")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	q := url.Values{"state": {*state}, "after": {*after}, "limit": {strconv.Itoa(*limit)}}
	var page protocol.AgentCategoryPage
	if err := c.do(ctx, "GET", "/api/v1/categories?"+q.Encode(), nil, &page); err != nil {
		return err
	}
	return json.NewEncoder(e.Stdout).Encode(page)
}

func runAgentCatalog(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 && args[0] == "hosted" {
		return runHostedAgentCatalog(ctx, e, args[1:])
	}
	if len(args) == 0 || args[0] != "show" {
		return usagef("agents expects show --host <id>|hosted")
	}
	fs := newFlags(e, "agents show")
	host := fs.String("host", "", "runner host ID")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || *host == "" {
		return usagef("agents show requires --host <id>")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	var catalog protocol.RunnerAgentCatalog
	if err := c.do(ctx, "GET", "/api/v1/hosts/"+url.PathEscape(*host)+"/agents", nil, &catalog); err != nil {
		return err
	}
	return json.NewEncoder(e.Stdout).Encode(catalog)
}

func runHostedAgentCatalog(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "agents hosted")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("agents hosted accepts no arguments")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	var catalog protocol.HostedAgentInventory
	if err := c.do(ctx, "GET", "/api/v1/hosted-agents", nil, &catalog); err != nil {
		return err
	}
	return json.NewEncoder(e.Stdout).Encode(catalog)
}
