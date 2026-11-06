package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBoundedMaintenanceStatusUsesIndependentInstallerAndPreservesFacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	root := t.TempDir()
	installer := filepath.Join(root, "installer")
	body := []byte("#!/bin/sh\n[ \"$1\" = bounded ] && [ \"$2\" = status ] || exit 8\nprintf '%s\\n' '{\"outcome\":\"timed_out\",\"drained\":false,\"unknown\":[\"old-work\"],\"next_action\":\"reconcile\"}'\n")
	if err := os.WriteFile(installer, body, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	e := newTestEnv(t, "")
	if code := e.main("updater", "bounded", "status", "--installer", installer, "--installer-sha256", hex.EncodeToString(sum[:]), "--config", filepath.Join(root, "fixed.json"), "--timeout", "500ms", "--json"); code != exitOK {
		t.Fatal("bounded status has no independent durable-state entry", code, e.errOut.String())
	}
	var result struct {
		Outcome    string   `json:"outcome"`
		Drained    bool     `json:"drained"`
		Unknown    []string `json:"unknown"`
		NextAction string   `json:"next_action"`
	}
	if err := json.Unmarshal(e.out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "timed_out" || result.Drained || len(result.Unknown) != 1 || result.NextAction != "reconcile" {
		t.Fatalf("status converted missing recovery into readiness: %+v", result)
	}
}

func boundedCLIInputs(t *testing.T) (installer, digest, config, plan, authorization string, auth protocol.BoundedMaintenanceAuthorization) {
	t.Helper()
	root := t.TempDir()
	installer = filepath.Join(root, "installer")
	body := []byte("#!/bin/sh\nfor arg do\n  if [ \"$previous\" = --authorization ]; then auth=\"$arg\"; fi\n  previous=\"$arg\"\ndone\ncat \"$auth\"\n")
	if err := os.WriteFile(installer, body, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	digest = hex.EncodeToString(sum[:])
	config = filepath.Join(root, "config.json")
	plan = filepath.Join(root, "plan.json")
	authorization = filepath.Join(root, "authorization.json")
	if err := os.WriteFile(plan, []byte(`{"maintenance":{"maintenance_id":"original","receipt_recovery_stop":{"runs":[]}},"other_original_field":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	auth = protocol.BoundedMaintenanceAuthorization{
		RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", ExpectedGeneration: 17,
		ExpectedPhase: "draining", PlanSHA256: strings.Repeat("a", 64), Role: "server",
		Reason: "Keep original unknown work", ExpiresAtMillis: 9999999999999,
		Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(), ForceOnDrainTimeout: true,
	}
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorization, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return
}

func TestBoundedMaintenanceMutationsPreserveTypedAuthorityAndCAS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	installer, digest, config, plan, authorization, auth := boundedCLIInputs(t)
	for _, action := range []string{"apply", "force", "extend", "cancel", "takeover"} {
		t.Run(action, func(t *testing.T) {
			e := newTestEnv(t, "")
			args := []string{"updater", "bounded", action, "--installer", installer, "--installer-sha256", digest, "--config", config, "--plan", plan, "--authorization", authorization, "--request-id", auth.RequestID, "--expected-generation", "17", "--expected-phase", "draining", "--json"}
			if code := e.main(args...); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
			var got protocol.BoundedMaintenanceAuthorization
			if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || got != auth {
				t.Fatalf("authority/CAS changed before the installer: %+v, %v", got, err)
			}
			for _, conflict := range [][]string{{"--request-id", "req_01M6F01JSS6Q9BFV6DJNSSBHEZ"}, {"--expected-generation", "18"}, {"--expected-generation", "0"}, {"--expected-phase", "stopped"}, {"--expected-phase", ""}} {
				e.out.Reset()
				e.errOut.Reset()
				if code := e.main(append(append([]string(nil), args...), conflict...)...); code != exitUsage || e.out.Len() != 0 {
					t.Fatalf("conflicting authorization reached installer: %v, %d, %s", conflict, code, e.out.String())
				}
			}
		})
	}
}

func TestBoundedMaintenanceDryRunDoesNotExecuteAndRejectsUnknownAuthority(t *testing.T) {
	installer, digest, config, plan, authorization, auth := boundedCLIInputs(t)
	if err := os.Remove(installer); err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, "")
	args := []string{"updater", "bounded", "force", "--installer", installer, "--installer-sha256", digest, "--config", config, "--plan", plan, "--authorization", authorization, "--dry-run"}
	if code := e.main(args...); code != exitOK {
		t.Fatal("dry run attempted the missing executable", code, e.errOut.String())
	}
	var exported struct {
		Arguments     []string                                 `json:"arguments"`
		Plan          json.RawMessage                          `json:"plan"`
		Authorization protocol.BoundedMaintenanceAuthorization `json:"authorization"`
	}
	if err := json.Unmarshal(e.out.Bytes(), &exported); err != nil || exported.Authorization != auth || !strings.Contains(string(exported.Plan), "other_original_field") || len(exported.Arguments) == 0 {
		t.Fatalf("dry run lost original typed inputs: %+v, %v", exported, err)
	}
	raw, err := os.ReadFile(authorization)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"allow_any_source":true}`)...)
	if err := os.WriteFile(authorization, raw, 0600); err != nil {
		t.Fatal(err)
	}
	e.out.Reset()
	if code := e.main(args...); code != exitUsage || e.out.Len() != 0 {
		t.Fatal("unknown authorization field was silently discarded", code, e.out.String())
	}
}

func TestBoundedMaintenancePinsOriginalAuthorizationBeforeInstallerReadsIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	installer, _, _, plan, authorization, auth := boundedCLIInputs(t)
	// The fake installer changes the original source file before reading the
	// received authorization path. It must still receive the validated bytes.
	body := []byte("#!/bin/sh\nfor arg do\n  if [ \"$previous\" = --authorization ]; then auth=\"$arg\"; fi\n  if [ \"$previous\" = --config ]; then original=\"$arg\"; fi\n  previous=\"$arg\"\ndone\nprintf 'replaced\\n' > \"$original\"\ncat \"$auth\"\n")
	if err := os.WriteFile(installer, body, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	e := newTestEnv(t, "")
	if code := e.main("updater", "bounded", "force", "--installer", installer, "--installer-sha256", hex.EncodeToString(sum[:]), "--config", authorization, "--plan", plan, "--authorization", authorization); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var got protocol.BoundedMaintenanceAuthorization
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || got != auth {
		t.Fatalf("installer reread mutable authorization input: %+v, %v", got, err)
	}
	raw, err := os.ReadFile(authorization)
	if err != nil || string(raw) != "replaced\n" {
		t.Fatal("replacement fixture did not run", string(raw), err)
	}
}

func TestBoundedMaintenancePlanAcceptsDraftButMutationRequiresBindings(t *testing.T) {
	installer, digest, config, plan, authorization, auth := boundedCLIInputs(t)
	auth.ExpectedGeneration, auth.ExpectedPhase, auth.PlanSHA256 = 0, "", ""
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorization, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"plan", "force"} {
		e := newTestEnv(t, "")
		args := []string{"updater", "bounded", action, "--installer", installer, "--installer-sha256", digest, "--config", config, "--plan", plan, "--authorization", authorization, "--dry-run"}
		want := exitOK
		if action == "force" {
			want = exitUsage
		}
		if code := e.main(args...); code != want {
			t.Fatal(action, code, e.errOut.String())
		}
	}
}

func TestBoundedMaintenanceAcceptsExplicitLegacyZeroAndRejectsMissingGeneration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	installer, digest, config, plan, authorization, auth := boundedCLIInputs(t)
	auth.ExpectedGeneration = 0
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorization, raw, 0600); err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, "")
	args := []string{"updater", "bounded", "force", "--installer", installer, "--installer-sha256", digest, "--config", config, "--plan", plan, "--authorization", authorization, "--expected-generation", "0"}
	if code := e.main(args...); code != exitOK {
		t.Fatal("explicit legacy zero CAS was rejected", code, e.errOut.String())
	}
	var got protocol.BoundedMaintenanceAuthorization
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || got.ExpectedGeneration != 0 || got.RequestID != auth.RequestID {
		t.Fatal("legacy CAS was replaced", got, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, value := range []json.RawMessage{nil, json.RawMessage("null")} {
		if value == nil {
			delete(fields, "expected_generation")
		} else {
			fields["expected_generation"] = value
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authorization, raw, 0600); err != nil {
			t.Fatal(err)
		}
		e.out.Reset()
		if code := e.main(args...); code != exitUsage || e.out.Len() != 0 {
			t.Fatal("missing/null generation was silently converted to a fresh zero CAS", code, e.out.String())
		}
	}
}

func TestBoundedMaintenanceHelpCoversRecoveryAndDeadlines(t *testing.T) {
	e := newTestEnv(t, "")
	if code := e.main("updater", "bounded", "--help"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	for _, required := range []string{"force_on_drain_timeout", "overall_ms", "360000", "340 seconds", "unknown", "takeover", "extend", "reconcile"} {
		if !strings.Contains(e.out.String(), required) {
			t.Fatal("bounded recovery help omitted", required)
		}
	}
	for _, action := range protocol.BoundedMaintenanceActions() {
		e.out.Reset()
		if code := e.main("updater", "bounded", action, "--help"); code != exitOK {
			t.Fatal(action, code, e.errOut.String())
		}
		for _, flag := range []string{"authorization", "expected-generation", "expected-phase", "request-id", "timeout", "dry-run"} {
			if !strings.Contains(e.out.String(), flag) {
				t.Fatal("bounded leaf help omitted", action, flag)
			}
		}
	}
}
