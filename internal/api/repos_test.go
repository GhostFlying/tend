package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRepoRegistryAuthentication(t *testing.T) {
	e := newEnv(t)
	for _, tok := range []string{"", "invalid", e.login(protocol.ScopeAgent)} {
		if st := e.do("GET", "/api/v1/repos", tok, nil, nil); st != http.StatusUnauthorized && st != http.StatusForbidden {
			t.Fatalf("non-owner read = %d", st)
		}
	}
	tok := e.login(protocol.ScopeOwner)
	e.peer.set(netx.Identity{Node: "guest-device", User: "guest@example.com"})
	if st := e.do("GET", "/api/v1/repos", tok, nil, nil); st != http.StatusForbidden {
		t.Fatalf("non-owner device = %d", st)
	}
}

func TestRepoRegistryCompleteSafeReadAndToolParity(t *testing.T) {
	e := newEnv(t)
	host := func(id string, repos ...string) app.HostConfig {
		return app.HostConfig{HostID: id, OS: "linux", Arch: "amd64", Slots: 1, Repos: repos, TokenHash: strings.Repeat("ab", 32), Nodes: []string{"private-node"}}
	}
	config := app.ConfigPayload{
		Repos: []app.RepoConfig{
			{Repo: "alpha", RemoteURL: "https://reader:fake-secret@example.com/alpha.git", BaseCandidates: []string{"candidate-only"}},
			{Repo: "beta", RemoteURL: "https://example.com/beta.git"},
			{Repo: "legacy", RemoteURL: "https://example.com/legacy.git", Retired: true},
			{Repo: "unserved", RemoteURL: "https://example.com/unserved.git"},
			{Repo: "removed", RemoteURL: "https://example.com/removed.git"},
		},
		Hosts:    []app.HostConfig{host("host-a", "alpha", "legacy"), host("host-b", "alpha", "beta"), host("host-retired", "beta")},
		Projects: []app.ProjectConfig{{Name: "only-alpha", Repos: []string{"alpha"}}},
	}
	e.exec("config.sync", protocol.Target{Type: "config"}, nil, config, app.SyncConfig{})
	// Removing configuration retires rather than deletes its ledger entry.
	config.Hosts = config.Hosts[:2]
	config.Repos = config.Repos[:4]
	e.exec("config.sync", protocol.Target{Type: "config"}, nil, config, app.SyncConfig{})
	// No runner has connected or checked out anything, and no task exists.
	session := e.sessionIn("registry-parity", "https://example.com/alpha")
	tool := session.call("repos_list", map[string]any{}, "")
	var toolView protocol.RepoRegistry
	if !tool.OK || json.Unmarshal(tool.Result, &toolView) != nil {
		t.Fatalf("repos_list = %+v", tool)
	}
	tok := e.login(protocol.ScopeOwner)
	counts := map[string]int{}
	for _, table := range []string{"repos", "hosts", "tasks", "events", "request_records", "outbox"} {
		counts[table] = e.count("SELECT count(*) FROM " + table)
	}
	var raw json.RawMessage
	if st := e.do("GET", "/api/v1/repos?project=only-alpha", tok, nil, &raw); st != http.StatusOK {
		t.Fatalf("registry = %d", st)
	}
	var got protocol.RepoRegistry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := protocol.RepoRegistry{Repos: []protocol.RegisteredRepoView{
		{Repo: "alpha", Hosts: []string{"host-a", "host-b"}},
		{Repo: "beta", Hosts: []string{"host-b"}},
		{Repo: "legacy", Retired: true, Hosts: []string{"host-a"}},
		{Repo: "removed", Retired: true, Hosts: []string{}},
		{Repo: "unserved", Hosts: []string{}},
	}}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got, toolView) {
		t.Fatalf("registry=%+v, tool=%+v, want=%+v", got, toolView, want)
	}
	// Check exact allowlisted fields, not only a few known secret strings.
	var fields struct {
		Repos []map[string]json.RawMessage `json:"repos"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, repo := range fields.Repos {
		for key := range repo {
			if key != "repo" && key != "retired" && key != "hosts" {
				t.Errorf("unsafe field %q", key)
			}
		}
	}
	for _, secret := range []string{"fake-secret", "reader", "example.com", "candidate-only", "private-node", "token_hash", "remote_url", "path"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("registry exposed %q", secret)
		}
	}
	for table, before := range counts {
		if after := e.count("SELECT count(*) FROM " + table); after != before {
			t.Errorf("read changed %s: %d -> %d", table, before, after)
		}
	}
	// The endpoint is read-only and has no mutation route.
	if st := e.do("POST", "/api/v1/repos", tok, nil, nil); st != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", st)
	}
}

func TestRepoRegistryEmptyAndUpdated(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	var raw json.RawMessage
	if st := e.do("GET", "/api/v1/repos", tok, nil, &raw); st != http.StatusOK || string(raw) != `{"repos":[]}` {
		t.Fatalf("empty = %d %s", st, raw)
	}
	e.configure()
	var got protocol.RepoRegistry
	if st := e.do("GET", "/api/v1/repos", tok, nil, &got); st != http.StatusOK || len(got.Repos) != 1 || len(got.Repos[0].Hosts) != 1 {
		t.Fatalf("updated = %d %+v", st, got)
	}
}
