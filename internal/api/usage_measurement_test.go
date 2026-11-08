package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Explicit measurement keeps benchmarks out of ordinary correctness checks.
func TestMeasureTokenUsageHTTP(t *testing.T) {
	if os.Getenv("TEND_MEASURE_USAGE") != "1" {
		t.Skip("set TEND_MEASURE_USAGE=1 for the HTTP cost probe")
	}
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	at := e.clock.Now().Add(-1).UnixMilli()
	total := int64(110)
	samples := make([]protocol.TokenUsageSample, 2347)
	for i := range samples {
		id := fmt.Sprint(i)
		session := fmt.Sprint(i % 554)
		samples[i] = protocol.TokenUsageSample{Source: "native-ledger/traex/" + session, RecordID: id, RequestID: id, ThreadID: session, SessionID: session, TurnID: id, Model: "model-" + session, Revision: 1, Settlement: "final", OccurredAt: &at, Total: &total}
	}
	if err := e.app.RecordNativeConversationUsage(t.Context(), "main", samples); err != nil {
		t.Fatal(err)
	}
	handler := e.server.Handler()
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/token-usage?window=total&group_by=session", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set(deploymentHeader, deployment)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				b.Fatal(response.Code, response.Body.String())
			}
		}
	})
	t.Logf("2347 facts, 554 model/session dimensions; authenticated handler + SQLite + grouping + encoding: %s; %s", result.String(), result.MemString())
}
