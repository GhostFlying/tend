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
			cfg := harness.ThreadConfig{Purpose: NativeOwnerPurpose, Sandbox: "read-only", ApprovalPolicy: "never", Model: "caller-model", Config: Isolation()}
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

func TestIssueOwnerToolsDoNotWidenLegacyRequests(t *testing.T) {
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
				if got.Sandbox != "read-only" || got.ApprovalPolicy != "never" || got.Config["features.shell_tool"] != false {
					t.Fatal("legacy request changed its default permissions", got)
				}
			}
		})
	}
}
