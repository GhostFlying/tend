package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUsageCollectionReadsRequireOwnerAndKeepRetirementSeparate(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	for _, path := range []string{"/api/v1/token-usage/collections", "/api/v1/token-usage/collections/source"} {
		if code := e.do("GET", path, "", nil, nil); code != http.StatusUnauthorized {
			t.Fatal(path, code)
		}
	}
	s := app.LocalUsageSource{ID: "source", WriterStopped: true, Source: []byte(`{"thread_id":"thread","provider":"codex"}`)}
	if err := e.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, _ := e.app.LocalUsageSources(t.Context(), "")
	s = all[0]
	s.Collection.RetireUnavailable(e.clock.Now().UnixMilli(), true)
	if err := e.app.CommitLocalUsage(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	var page protocol.UsageCollectionPage
	if code := e.do("GET", "/api/v1/token-usage/collections?state=retired&reason=unavailable&limit=1", token, nil, &page); code != http.StatusOK || len(page.Sources) != 1 || page.Sources[0].Coverage != "pending" {
		t.Fatal(code, page)
	}
	var detail protocol.UsageCollectionSource
	if code := e.do("GET", "/api/v1/token-usage/collections/source", token, nil, &detail); code != http.StatusOK || detail.Collection.RetirementReason != "unavailable" || detail.ControlRevision != s.Collection.Revision {
		t.Fatal(code, detail)
	}
	for _, query := range []string{"limit=0", "limit=101", "state=invalid", "reason=empty-and-complete"} {
		if code := e.do("GET", "/api/v1/token-usage/collections?"+query, token, nil, nil); code != http.StatusBadRequest {
			t.Fatal(query, code)
		}
	}
	if code := e.do("GET", "/api/v1/token-usage/collections/absent", token, nil, nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
	empty := app.LocalUsageSource{ID: "empty", Source: []byte(`{"thread_id":"unused","provider":"codex","no_dispatch_proven":true,"writer_id":"writer"}`)}
	if err := e.app.RegisterLocalUsage(t.Context(), empty); err != nil {
		t.Fatal(err)
	}
	if err := e.app.StopLocalUsage(t.Context(), "main", "unused"); err != nil {
		t.Fatal(err)
	}
	var usage protocol.TokenUsageView
	if code := e.do("GET", "/api/v1/token-usage?window=total", token, nil, &usage); code != http.StatusOK || usage.Collection.Pending != 1 || usage.Collection.RetiredIncomplete != 1 || usage.Collection.Empty != 1 {
		t.Fatal("retirement hid incomplete accounting", code, usage.Collection)
	}
}
