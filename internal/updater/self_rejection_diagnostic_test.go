//go:build linux

package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
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

// The old supervisor may still be running when the new installer rejects.
func TestSelfRejectionDiagnosticDoesNotChangeLegacyReceiptOrGateProof(t *testing.T) {
	cfg := Config{InstanceID: "server", StateDir: t.TempDir()}
	r := SelfUpdateRequest{UpgradeID: "batch", ReleaseSetSHA256: strings.Repeat("a", 64)}
	r.Target.RequestID, r.Target.ExpiresAtMillis = "original", 200000
	// A diagnostic storage failure must not hide the pre-stop rejection proof.
	if err := os.Mkdir(selfRejectionPath(cfg, r.Target.RequestID)+".diagnostic.json", 0700); err != nil {
		t.Fatal(err)
	}
	result, err := persistSelfRejection(cfg, r, time.Unix(100, 0), errors.New("first rejection"))
	if err == nil || result.Observation.Rejection == nil {
		t.Fatal("diagnostic failure gated original proof", result, err)
	}
	raw, err := os.ReadFile(selfRejectionPath(cfg, r.Target.RequestID))
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Format  int               `json:"format"`
		Request SelfUpdateRequest `json:"request"`
		Proof   json.RawMessage   `json:"proof"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		t.Fatal("new installer broke the old supervisor receipt reader", err)
	}
	read, err := readSelfRejection(cfg, r.Target.RequestID)
	if err != nil || read == nil || read.Proof != *result.Observation.Rejection || read.Cause != "" {
		t.Fatal("missing diagnostic changed proof or invented old cause", read, err)
	}
}
