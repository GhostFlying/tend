package maintenance

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestNativeProtocolIdentityAndOrdinaryTools(t *testing.T) {
	for _, agent := range []string{"codex", "traex"} {
		t.Run(agent, func(t *testing.T) {
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
						result = map[string]any{"thread": map[string]any{"status": map[string]string{"type": "idle"}, "turns": []any{map[string]any{"id": "turn", "status": "completed", "items": []any{map[string]string{"type": "userMessage", "id": "original", "clientId": "original"}}}}}}
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
			id, _, err := n.NewThread(context.Background(), b, dir, "Configured maintenance scope")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := n.Start(context.Background(), b, id, "original", "Check once"); err != nil {
				t.Fatal(err)
			}
			thread, turn := <-messages, <-messages
			if _, ok := thread["dynamicTools"]; ok {
				t.Fatal("maintenance injected tools")
			}
			if _, ok := thread["config"]; ok {
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
			s, err := n.Read(context.Background(), b, id, true)
			if err != nil || s.Busy || len(s.Turns) != 1 || s.Turns[0].InputID != "original" {
				t.Fatal(s, err)
			}
		})
	}
}
