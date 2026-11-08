package maintenance

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestNativeProtocolIdentityAndOrdinaryTools(t *testing.T) {
	for _, agent := range []string{"codex", "traex"} {
		for _, threadStatus := range []string{"idle", "systemError", "active", "future"} {
			t.Run(agent+"/"+threadStatus, func(t *testing.T) {
				dir, err := os.MkdirTemp("", "tend-maint-protocol-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(dir)
				socket := filepath.Join(dir, "native.sock")
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				messages := make(chan map[string]json.RawMessage, 16)
				srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
					for {
						var req struct {
							ID     *int                       `json:"id"`
							Method string                     `json:"method"`
							Params map[string]json.RawMessage `json:"params"`
						}
						if conn.ReadJSON(&req) != nil {
							return
						}
						if req.ID == nil {
							continue
						}
						var result any = map[string]any{}
						switch req.Method {
						case "thread/start":
							messages <- req.Params
							result = map[string]any{"thread": map[string]any{"id": "native", "path": "rollout"}, "sandbox": map[string]any{"type": "workspaceWrite", "networkAccess": false, "writableRoots": []string{dir}, "excludeSlashTmp": true}}
						case "turn/start":
							messages <- req.Params
							result = map[string]any{"turn": map[string]any{"id": "turn"}}
						case "thread/read":
							thread := map[string]any{"status": map[string]string{"type": threadStatus}}
							if string(req.Params["includeTurns"]) == "true" {
								outcome := "completed"
								if threadStatus == "systemError" {
									outcome = "failed"
								}
								thread["turns"] = []any{map[string]any{"id": "turn", "status": outcome, "items": []any{map[string]string{"type": "userMessage", "id": "original", "clientId": "original"}}}}
							}
							result = map[string]any{"thread": thread}
						}
						if conn.WriteJSON(map[string]any{"id": req.ID, "result": result}) != nil {
							return
						}
					}
				})}
				done := make(chan struct{})
				go func() { defer close(done); _ = srv.Serve(listener) }()
				defer func() { _ = srv.Close(); <-done }()
				b := protocol.MaintenanceBackend{Agent: agent, Socket: socket, Sandbox: "workspace-write", ApprovalPolicy: "never"}
				b.NetworkAccess = true
				b.WritableRoots = []string{filepath.Join(dir, "staging")}
				n := &NativeClient{}
				defer n.Close()
				threadRef, err := n.NewThread(context.Background(), b, dir, "Configured maintenance scope")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := n.Start(context.Background(), b, threadRef, "original", "Check once"); err != nil {
					t.Fatal(err)
				}
				thread, turn := <-messages, <-messages
				if _, ok := thread["dynamicTools"]; ok {
					t.Fatal("maintenance injected tools")
				}
				if strings.Contains(string(thread["config"]), "mcp_servers") {
					t.Fatal("maintenance injected a hidden tool configuration")
				}
				field := "userMessageId"
				if agent == "codex" {
					field = "clientUserMessageId"
				}
				if string(turn[field]) != `"original"` {
					t.Fatal("native input identity was not forwarded", turn)
				}
				if _, ok := turn["effort"]; ok {
					t.Fatal("empty optional native effort was sent")
				}
				var policy map[string]any
				if json.Unmarshal(turn["sandboxPolicy"], &policy) != nil || policy["networkAccess"] != true || policy["excludeSlashTmp"] != true || len(policy["writableRoots"].([]any)) != 2 {
					t.Fatal("native filesystem or network scope changed", policy)
				}
				s, err := n.Read(context.Background(), b, threadRef.ID, ReadFullHistory)
				busy := threadStatus != "idle" && threadStatus != "systemError"
				if err != nil || s.Busy != busy || len(s.Turns) != 1 || s.Turns[0].InputID != "original" {
					t.Fatal(s, err)
				}
				idleRead, err := n.Read(t.Context(), b, threadRef.ID, ReadWhenIdle)
				if err != nil || idleRead.Busy != busy || (!busy && len(idleRead.Turns) != 1) || (busy && len(idleRead.Turns) != 0) {
					t.Fatal("quiescent history or active-thread fencing lost", idleRead, err)
				}
				if threadStatus == "systemError" && idleRead.Turns[0].Status != "failed" {
					t.Fatal("original failure was not read", idleRead)
				}
			})
		}
	}
}
