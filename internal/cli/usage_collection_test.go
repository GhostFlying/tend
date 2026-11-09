package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUsageCollectionFiltersAndOriginalControlEnvelope(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/token-usage/collections" || r.URL.Query().Get("reason") != "unavailable" || r.URL.Query().Get("state") != "retired" || r.URL.Query().Get("after") != "cursor" || r.URL.Query().Get("host") != "host" {
			t.Error(r.URL.String())
		}
		_, _ = w.Write([]byte(`{"sources":[],"next":"","more":false}`))
	}))
	defer server.Close()
	e := newTestEnv(t, server.URL)
	if code := e.main("usage", "collection", "list", "--reason", "unavailable", "--state", "retired", "--host", "host", "--after", "cursor", "--limit", "50", "--json"); code != exitOK {
		t.Fatal(code, e.errOut)
	}
	id := e.IDs.New(protocol.PrefixRequest)
	var original string
	for range 2 {
		e.out.Reset()
		if code := e.main("usage", "collection", "retire", "source", "--revision", "9", "--reason", "unavailable", "--request-id", id, "--dry-run"); code != exitOK {
			t.Fatal(code, e.errOut)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(e.out.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		var payload protocol.UsageCollectionControl
		_ = json.Unmarshal(env.Payload, &payload)
		if env.RequestID != id || env.Operation != protocol.OpRetireUsageCollection || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 9 || payload.Reason != "unavailable" {
			t.Fatal(env)
		}
		if original != "" && original != e.out.String() {
			t.Fatal("original control envelope changed")
		}
		original = e.out.String()
	}
	if requests != 1 {
		t.Fatal("dry run sent a mutation", requests)
	}
	for _, args := range [][]string{
		{"retire", "source", "--reason", "empty", "--revision", "9"},
		{"retire", "source", "--reason", "unavailable"},
		{"retire", "source", "--reason", "unavailable", "--revision", "9", "--request-id", ""},
		{"resume", "source", "--revision", "9", "--reason", "unavailable"},
	} {
		if code := e.main(append([]string{"usage", "collection"}, args...)...); code != exitUsage {
			t.Fatal(args, code, e.errOut)
		}
	}
	if requests != 1 {
		t.Fatal("invalid control sent a request", requests)
	}
}
