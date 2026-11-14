//go:build linux

package updater

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSelfPreparationRejectionRetainsFirstDiagnosticAfterRestart(t *testing.T) {
	cfg := Config{InstanceID: "server", StateDir: t.TempDir()}
	r := SelfUpdateRequest{UpgradeID: "original", ReleaseSetSHA256: strings.Repeat("a", 64)}
	r.Target.RequestID, r.Target.ExpiresAtMillis = "original-request", 200000
	cause := errors.New("updater: candidate source identity changed")
	_, _ = persistSelfRejection(cfg, r, time.Unix(100, 0), cause)
	s := &Supervisor{cfg: cfg}
	w := httptest.NewRecorder()
	s.selfHandler(w, httptest.NewRequest("GET", "/self-update/status?request_id=original-request", nil))
	var got SelfUpdateStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Error, cause.Error()) {
		t.Fatalf("immutable rejection lost first cause: %q", got.Error)
	}
}
