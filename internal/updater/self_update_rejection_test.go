//go:build linux

package updater

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelfPreparationRejectionSurvivesLostReplyAndRestart(t *testing.T) {
	cfg := Config{InstanceID: "server", StateDir: t.TempDir()}
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{cfg: cfg, opts: Options{ConfigFile: config, Now: func() time.Time { return time.Unix(100, 0) }}}
	r := SelfUpdateRequest{UpgradeID: "original", ReleaseSetSHA256: strings.Repeat("a", 64), Release: "original-bundle"}
	r.Target.RequestID, r.Target.ExpiresAtMillis = "original-request", 200000
	status, err := s.requestSelf(t.Context(), r)
	if err == nil || status.Observation.Rejection == nil {
		t.Fatal("definite preparation failure lacked its receipt", status, err)
	}
	if _, err := readSelf(cfg); !os.IsNotExist(err) {
		t.Fatal("rejection created an active installer journal", err)
	}
	original, err := os.ReadFile(selfRejectionPath(cfg, r.Target.RequestID))
	if err != nil {
		t.Fatal(err)
	}
	// A restarted supervisor can resolve the receipt before live supervision
	// is available. No stop or installer operation is needed to prove rejection.
	restarted := &Supervisor{cfg: cfg}
	w := httptest.NewRecorder()
	restarted.selfHandler(w, httptest.NewRequest("GET", "/self-update/status?request_id=original-request", nil))
	var recovered SelfUpdateStatus
	if err := json.Unmarshal(w.Body.Bytes(), &recovered); err != nil || recovered.Observation.Rejection == nil || *recovered.Observation.Rejection != *status.Observation.Rejection {
		t.Fatal("lost rejection response was not recoverable", recovered, err)
	}
	if err := os.Chmod(config, 0o600); err != nil {
		t.Fatal(err)
	}
	// Correcting configuration does not repurpose a permanently rejected
	// identity. A later upgrade must use a new original request.
	if again, err := s.requestSelf(t.Context(), r); err == nil || again.Observation.Rejection == nil || *again.Observation.Rejection != *status.Observation.Rejection {
		t.Fatal("rejection identity was retried as new work", again, err)
	}
	r.Release = "changed-payload"
	if conflict, err := s.requestSelf(t.Context(), r); err == nil || conflict.Observation.Rejection != nil {
		t.Fatal("rejected request accepted a different payload", conflict, err)
	}
	current, err := os.ReadFile(selfRejectionPath(cfg, r.Target.RequestID))
	if err != nil || !bytes.Equal(original, current) {
		t.Fatal("immutable rejection changed", err)
	}
}
