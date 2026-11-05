package orca

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetainedAcceptanceRestoresOriginalBridgeVerdict(t *testing.T) {
	for _, change := range []string{"original", "missing", "other-run", "other-launch", "other-turn", "same-time-new-turn", "corrupt", "write-failure"} {
		t.Run(change, func(t *testing.T) {
			f, original, a := nativeFixture(t)
			a.RootTurn.State, a.RootTurn.Operations = "completed", "settled"
			a.RootTurn.ErrorCode, a.RootTurn.Retryable = "", false
			a.MainAgent.Outcome = ""
			if _, _, err := original.agent(t.Context()); err != nil {
				t.Fatal(err)
			}
			original.st.Worktree, original.st.SessionID = original.wt, a.ProviderSession.ID
			original.spec.Role, original.spec.StateDir = protocol.RoleAccept, t.TempDir()
			if err := original.openBridge(t.Context()); err != nil {
				t.Fatal(err)
			}
			if change == "write-failure" {
				if err := os.Mkdir(filepath.Join(original.spec.StateDir, "verdict.json.tmp"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"verdict":"passed","evidence":{"check":"original independent verification"}}}}` + "\n"
			if err := bridge.ServeMCP(t.Context(), filepath.Join(original.spec.StateDir, protocol.WorkerMarker), original.spec.RunID, strings.NewReader(in), &out); err != nil || strings.Contains(out.String(), "isError") != (change == "write-failure") {
				t.Fatalf("submit: %s %v", out.String(), err)
			}
			if change == "write-failure" && original.verdicts.Verdict() != nil {
				t.Fatal("failed write became a usable verdict")
			}
			original.closeBridge(t.Context())
			switch change {
			case "missing":
				if err := os.Remove(filepath.Join(original.spec.StateDir, "verdict.json")); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(filepath.Join(original.spec.StateDir, "verdict.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "other-launch":
				original.st.Marker = "another-native-launch"
			case "other-turn":
				original.st.Turn++
			case "same-time-new-turn":
				original.st.VerdictTurn = "different-journaled-turn"
			}
			spec := original.spec
			if change == "other-run" {
				spec.RunID = "another-run"
			}
			spec.Attempt = 2
			spec.Thread = mustState(original.st)
			spec.RetainedResume = &protocol.RetainedRunResume{RequestID: "exact-original-authority", Mode: "attach"}
			spec.OnThread = func(string) error { return nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rebuilt, err := f.ws.Start(ctx, spec)
			var result harness.WorkerResult
			if rebuilt != nil {
				for range rebuilt.Events() {
				}
				result, err = rebuilt.Wait()
			}
			if change == "original" {
				if err != nil || result.Verdict == nil || result.Verdict.Verdict != protocol.VerdictPassed || string(result.Verdict.Evidence) != `{"check":"original independent verification"}` {
					t.Fatalf("original verdict lost after reconstruction: result=%+v err=%v", result, err)
				}
			} else if err == nil || result.Verdict != nil {
				t.Fatalf("unconfirmed verdict completed: result=%+v err=%v", result, err)
			}
			if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("retained completion mutated native execution")
			}
		})
	}
}
