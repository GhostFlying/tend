package coordination

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestIssueOwnerUsesDeviceToolDefaultsWithPinnedExecution(t *testing.T) {
	for _, method := range []string{"start", "resume"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := testkit.NewHarness()
			native, err := fake.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			d := Driver{NativeOwnerTools: true, Session: permissionSession{native.(*testkit.FakeSession)}, Directory: t.TempDir(), Profile: protocol.ExecutionProfile{Model: "pinned", Effort: "high"}, Emit: func(context.Context, Event) error { return nil }}
			defer func() { cancel(); _, _ = d.Handle(context.Background(), "close", json.RawMessage(`{}`)) }()
			cfg := harness.ThreadConfig{Purpose: NativeOwnerPurpose, Model: "caller-model", Config: map[string]any{}}
			cfg.Config["device_setting"] = "retained"
			threadID := ""
			if method == "resume" {
				raw, _ := json.Marshal(Request{Config: cfg})
				result, err := d.Handle(ctx, "start", raw)
				if err != nil {
					t.Fatal(err)
				}
				threadID = result.(ThreadInfo).ID
			}
			raw, _ := json.Marshal(Request{ThreadID: threadID, Config: cfg})
			if _, err := d.Handle(ctx, method, raw); err != nil {
				t.Fatal(err)
			}
			for _, th := range d.threads {
				got := th.(permissionThread).Config
				if got.Cwd != d.Directory || got.Model != "pinned" || got.Config["model_reasoning_effort"] != "high" {
					t.Fatalf("execution pin changed: %+v", got)
				}
				if got.Sandbox != "" || got.ApprovalPolicy != "" || got.Config["device_setting"] != "retained" {
					t.Fatalf("device defaults overridden: %+v", got)
				}
				for key := range Isolation() {
					if _, exists := got.Config[key]; exists {
						t.Fatalf("native tool override retained: %s", key)
					}
				}
			}
		})
	}
}

func TestConfiguredNativeRuntimeDoesNotDependOnRoleMarker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		marker  bool
		purpose string
	}{
		{name: "old_assignment", purpose: NativeOwnerPurpose},
		{name: "old_server", marker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			fake := testkit.NewHarness()
			session, err := fake.Open(ctx)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			d := Driver{NativeOwnerTools: tc.marker, Session: permissionSession{session.(*testkit.FakeSession)}, Profile: protocol.ExecutionProfile{Model: "fixed"}, Emit: func(context.Context, Event) error { return nil }}
			defer func() { cancel(); _, _ = d.Handle(context.Background(), "close", json.RawMessage(`{}`)) }()
			raw, _ := json.Marshal(Request{Config: harness.ThreadConfig{Purpose: tc.purpose, Sandbox: "read-only", ApprovalPolicy: "never"}})
			if _, err := d.Handle(ctx, "start", raw); err != nil {
				t.Fatal(err)
			}
			for _, th := range d.threads {
				got := th.(permissionThread).Config
				if tc.marker {
					if got.Sandbox != "read-only" || got.ApprovalPolicy != "never" || got.Config["features.shell_tool"] != nil {
						t.Fatal("configured native runtime was narrowed by missing role marker", got)
					}
				} else if got.Sandbox != "read-only" || got.ApprovalPolicy != "never" || got.Config["features.shell_tool"] != false {
					t.Fatal("legacy transport did not preserve accepted behavior", got)
				}
			}
		})
	}
}

func TestNativeRuntimeRetainsExplicitDeviceAndTaskConfiguration(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	native, err := testkit.NewHarness().Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := Driver{NativeOwnerTools: true, Session: permissionSession{native.(*testkit.FakeSession)}, Profile: protocol.ExecutionProfile{Model: "pinned"}, Emit: func(context.Context, Event) error { return nil }}
	defer func() { cancel(); _, _ = d.Handle(context.Background(), "close", json.RawMessage(`{}`)) }()
	cfg := harness.ThreadConfig{Purpose: "reviewer", Sandbox: "workspace-write", ApprovalPolicy: "never", Config: map[string]any{"features.shell_tool": false, "features.apply_patch": false, "features.multi_agent": false, "features.image_gen": false}}
	raw, _ := json.Marshal(Request{Config: cfg})
	if _, err := d.Handle(ctx, "start", raw); err != nil {
		t.Fatal(err)
	}
	for _, th := range d.threads {
		got := th.(permissionThread).Config
		if got.Sandbox != cfg.Sandbox || got.ApprovalPolicy != cfg.ApprovalPolicy {
			t.Fatal("explicit runtime configuration was erased", got)
		}
		for key, value := range cfg.Config {
			if got.Config[key] != value {
				t.Fatal("explicit tool configuration was erased", key, got)
			}
		}
	}
}
