package upgradectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUpgradeHealthSeparatesInstallationFromSessionDrain(t *testing.T) {
	c, _ := testController(t)
	ready := true
	clock := time.Unix(1000, 0)
	c.cfg.Now = func() time.Time { clock = clock.Add(time.Millisecond); return clock }
	c.cfg.HealthDiagnostics = func(context.Context) *protocol.UpgradeHealthDiagnostics {
		return &protocol.UpgradeHealthDiagnostics{ServiceReady: &ready}
	}
	c.observe = func(context.Context) (app.ServerObservation, error) {
		return app.ServerObservation{Healthy: true, MainDrained: false}, nil
	}
	binding := protocol.UpgradeBinding{InstanceID: c.cfg.Instance, ProcessInstanceID: c.cfg.ProcessInstance, ArtifactSHA256: c.cfg.BinarySHA256}
	raw, _ := json.Marshal(binding)
	read := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		out := httptest.NewRecorder()
		c.Handler().ServeHTTP(out, httptest.NewRequest(http.MethodPost, "/updater/health"+query, bytes.NewReader(raw)))
		return out
	}
	legacy := read("")
	if legacy.Code != http.StatusOK || bytes.Contains(legacy.Body.Bytes(), []byte(`"diagnostics"`)) {
		t.Fatal("legacy strict client received unsolicited new fields", legacy.Body.String())
	}
	out := read("?diagnostics=1")
	var value protocol.UpgradeHealth
	if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &value) != nil || !value.Healthy || !value.ServerConfirmed {
		t.Fatal("session drain blocked a healthy installation", out.Body.String())
	}
	facts := value.Diagnostics
	if facts == nil || facts.ServiceReady == nil || !*facts.ServiceReady || facts.MainDrained == nil || *facts.MainDrained || facts.AuthorityHealthy == nil || !*facts.AuthorityHealthy || facts.PhaseMillis["server_authority"] <= 0 || len(facts.Failures) != 0 {
		t.Fatal("ready/drain/authority facts were conflated", facts)
	}
	// Original binding mismatch remains rejected before the authority executes.
	binding.ProcessInstanceID = "other"
	raw, _ = json.Marshal(binding)
	if out = read("?diagnostics=1"); out.Code != http.StatusConflict {
		t.Fatal("diagnostics admitted a different process", out.Body.String())
	}
}

func TestOptionalHealthDiagnosticsFailureCannotChangeInstallationGate(t *testing.T) {
	c, _ := testController(t)
	c.cfg.HealthDiagnostics = func(ctx context.Context) *protocol.UpgradeHealthDiagnostics {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("diagnostic observation has no bounded budget")
		}
		<-ctx.Done()
		return &protocol.UpgradeHealthDiagnostics{}
	}
	binding := protocol.UpgradeBinding{InstanceID: c.cfg.Instance, ProcessInstanceID: c.cfg.ProcessInstance, ArtifactSHA256: c.cfg.BinarySHA256}
	health, err := c.normalHealth(t.Context(), binding, true)
	if err != nil || !health.Healthy || health.Diagnostics == nil || len(health.Diagnostics.Failures) == 0 {
		t.Fatal("diagnostic failure changed established health", health, err)
	}
}

func TestUpgradeHealthObservationFailurePreservesUnknownDrain(t *testing.T) {
	c, _ := testController(t)
	c.observe = func(context.Context) (app.ServerObservation, error) {
		return app.ServerObservation{}, errors.New("observation unavailable")
	}
	binding := protocol.UpgradeBinding{InstanceID: c.cfg.Instance, ProcessInstanceID: c.cfg.ProcessInstance, ArtifactSHA256: c.cfg.BinarySHA256}
	health, err := c.normalHealth(t.Context(), binding, true)
	if err != nil || health.Healthy || health.Diagnostics == nil || health.Diagnostics.MainDrained != nil || !slices.Contains(health.Diagnostics.Failures, "local_observation_unavailable") || slices.Contains(health.Diagnostics.Failures, "main_not_drained") {
		t.Fatal("unavailable observation became a false drain fact", health, err)
	}
}
