package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestObservationReaderRunCredentialScopeAndCancellation(t *testing.T) {
	calls := 0
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "GET" || q.URL.Path != "/runner/v1/observation" || q.URL.RawQuery != "" || q.Header.Get(headerRun) != "run_one" || q.Header.Get("Authorization") != "Bearer run-secret" {
			t.Error("unexpected request identity")
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte("run-secret"))
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.RunObservation{Version: 1, RunID: "run_one", Seq: int64(calls)})
	}))
	defer srv.Close()
	r := Runner{c: &client{base: srv.URL, http: srv.Client()}}
	as := protocol.Assignment{RunID: "run_one", RunToken: "run-secret", Spec: json.RawMessage(`{}`)}
	if r.observationReader(t.Context(), as) != nil {
		t.Fatal("scope-less reader")
	}
	as.Spec = json.RawMessage(`{"observation":{"deployment":true}}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	read := r.observationReader(ctx, as)
	for i := 1; i <= 2; i++ {
		got, err := read(t.Context())
		if err != nil || got.Seq != int64(i) {
			t.Fatalf("snapshot: %+v %v", got, err)
		}
	}
	status = http.StatusForbidden
	if _, err := read(t.Context()); err == nil || strings.Contains(err.Error(), "run-secret") {
		t.Fatalf("unsafe rejection %v", err)
	}
	cancel()
	if _, err := read(t.Context()); err == nil {
		t.Fatal("read after run cancellation")
	}
	if calls != 3 {
		t.Fatalf("requests=%d", calls)
	}
}
