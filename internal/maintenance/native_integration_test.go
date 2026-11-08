//go:build integration && linux

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/gorilla/websocket"
)

// TestNativeMaintenanceSharedDaemon uses installed native binaries with a local
// deterministic provider. It neither loads personal credentials nor calls a model.
func TestNativeMaintenanceSharedDaemon(t *testing.T) {
	for _, agent := range []string{"codex", "traex"} {
		t.Run(agent, func(t *testing.T) {
			binary, err := exec.LookPath(agent)
			if err != nil {
				t.Skip("native installation required")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			root, err := os.MkdirTemp("", "tend-native-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			holding := make(chan struct{}, 1)
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				_ = json.NewDecoder(r.Body).Decode(&body)
				text := "Maintenance inspection complete."
				rawBody, _ := json.Marshal(body)
				if strings.Contains(string(rawBody), "Hold inspection.") && !strings.Contains(string(rawBody), "review-fixture") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"holding\"}}\n\n")
					w.(http.Flusher).Flush()
					select {
					case holding <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					return
				}
				if strings.Contains(string(rawBody), "Owner UI inspection.") {
					text = "Owner TUI interaction verified."
				}
				if strings.Contains(string(rawBody), "review-fixture") {
					text = `{"findings":[],"overall_correctness":"patch is correct","overall_explanation":"Fixture review completed","overall_confidence_score":1}`
				}
				item := map[string]any{"type": "message", "role": "assistant", "id": "message", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
				response := map[string]any{"id": "response", "status": "completed", "model": "probe-model", "output": []any{item}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []any{map[string]any{"type": "response.created", "response": map[string]any{"id": "response"}}, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}, map[string]any{"type": "response.completed", "response": response}} {
					raw, _ := json.Marshal(event)
					_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
				}
			}))
			defer model.Close()
			socket := filepath.Join(root, "native.sock")
			args := []string{"app-server", "--listen", "unix://" + socket, "-c", `model_provider="probe"`, "-c", `model="probe-model"`, "-c", `model_providers.probe.name="probe"`, "-c", `model_providers.probe.base_url="` + model.URL + `/v1"`, "-c", `model_providers.probe.wire_api="responses"`, "-c", `model_providers.probe.requires_openai_auth=false`}
			for _, feature := range []string{"hooks", "plugins", "apps", "memories", "browser_use", "computer_use", "image_generation", "code_mode_host", "in_app_updates"} {
				args = append(args, "-c", "features."+feature+"=false")
			}
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "CODEX_HOME=" + root, "TRAE_HOME=" + root, "TRAECLI_HOME=" + root, "TRAE_CLI_HOME=" + root, "TEND_ADDON=off", "NO_PROXY=127.0.0.1,localhost"}
			logPath := filepath.Join(root, "native.log")
			logFile, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()
			cmd.Stdout = logFile
			cmd.Stderr = logFile
			readLog := func() string { raw, _ := os.ReadFile(logPath); return string(raw) }
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			staging := filepath.Join(root, "staging")
			if err := os.MkdirAll(staging, 0700); err != nil {
				t.Fatal(err)
			}
			b := protocol.MaintenanceBackend{Agent: agent, Socket: socket, Model: "probe-model", ApprovalPolicy: "never", Sandbox: "workspace-write", NetworkAccess: true, WritableRoots: []string{staging}}
			for {
				if _, err := os.Stat(socket); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("native socket not ready: %s", readLog())
				case <-time.After(30 * time.Millisecond):
				}
			}
			n := NativeClient{}
			thread, err := n.NewThread(ctx, b, root, "Perform the one requested bounded inspection.")
			if err != nil {
				t.Fatal(err)
			}
			id, path := thread.ID, thread.Path
			if id == "" {
				t.Fatal("empty native thread")
			}
			// A controller restart loses the creation connection. Only metadata is
			// available until the first input materializes native history.
			if err := n.Close(); err != nil {
				t.Fatal(err)
			}
			n = NativeClient{}
			serialized, err := json.Marshal(thread)
			if err != nil || json.Unmarshal(serialized, &thread) != nil {
				t.Fatal("cannot restore resolved native permissions", err)
			}
			if s, err := n.Read(ctx, b, id, ReadMetadata); err != nil || s.Busy {
				t.Fatalf("unstarted native status: %+v %v", s, err)
			}
			turn, err := n.Start(ctx, b, thread, "maintenance-input-1", "Reply with the inspection result.")
			if err != nil {
				t.Fatal(err)
			}
			for {
				s, err := n.Read(ctx, b, id, ReadFullHistory)
				if err != nil {
					select {
					case <-ctx.Done():
						t.Fatal(err)
					case <-time.After(100 * time.Millisecond):
						continue
					}
				}
				for _, v := range s.Turns {
					if v.ID == turn && v.Status == "completed" {
						if v.InputID != "maintenance-input-1" {
							t.Fatalf("lost original input identity: %+v", v)
						}
						t.Log("first input survived a controller restart on the original empty thread")
						// Restart only this fixture's shared daemon, then recover the
						// same durable native thread with its pinned backend policy.
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						_ = os.Remove(socket)
						next := exec.CommandContext(ctx, binary, args...)
						next.Dir, next.Env = root, cmd.Env
						next.Stdout, next.Stderr = logFile, logFile
						cmd = next
						if err := cmd.Start(); err != nil {
							t.Fatal(err)
						}
						for {
							recovered, err := n.Read(ctx, b, id, ReadFullHistory)
							if err == nil && !recovered.Busy && len(recovered.Turns) > 0 {
								break
							}
							select {
							case <-ctx.Done():
								t.Fatal("native daemon restart did not recover", err, readLog())
							case <-time.After(100 * time.Millisecond):
							}
						}
						probe, err := connectNative(ctx, b)
						if err != nil {
							t.Fatal(err)
						}
						var resolved struct {
							Sandbox map[string]any `json:"sandbox"`
						}
						err = probe.call(ctx, "thread/resume", map[string]any{"threadId": id, "excludeTurns": true}, &resolved)
						_ = probe.conn.Close()
						if err != nil || resolved.Sandbox["type"] != "workspaceWrite" || resolved.Sandbox["networkAccess"] != true || !strings.Contains(fmt.Sprint(resolved.Sandbox["writableRoots"]), staging) {
							t.Fatal("daemon restart lost the pinned backend policy", resolved.Sandbox, err)
						}
						t.Log("daemon restart retained configured sandbox, network access and extra writable roots")
						t.Logf("%s same native thread readable after connection closes; rollout recorded=%t input identity=%s", agent, path != "", v.InputID)
						remoteURL := "unix://" + socket
						if agent == "traex" {
							gateway := nativeTUIGateway(t, socket)
							defer gateway.Close()
							remoteURL = "ws" + strings.TrimPrefix(gateway.URL, "http")
						}
						probeNativeTUI(t, ctx, binary, id, remoteURL, model.URL, cmd.Env)
						if _, err := n.Read(ctx, b, id, ReadFullHistory); err != nil {
							t.Fatal("TUI exit damaged shared daemon", err)
						}
						holdTurn, err := n.Start(ctx, b, thread, "maintenance-hold", "Hold inspection.")
						if err != nil {
							t.Fatal(err)
						}
						select {
						case <-holding:
						case <-ctx.Done():
							t.Fatal("native hold did not start")
						}
						if err := n.Interrupt(ctx, b, id, holdTurn); err != nil {
							t.Fatal(err)
						}
						for {
							snapshot, err := n.Read(ctx, b, id, ReadFullHistory)
							stopped := false
							if err == nil && !snapshot.Busy {
								for _, v := range snapshot.Turns {
									if v.ID == holdTurn && v.Status == "interrupted" {
										stopped = true
									}
								}
							}
							if stopped {
								break
							}
							select {
							case <-ctx.Done():
								t.Fatal("native interrupt was not confirmed", err)
							case <-time.After(100 * time.Millisecond):
							}
						}
						t.Log("native interrupt confirmed idle on the original thread")
						reviewTurn, err := n.Review(ctx, b, thread, "Independent review, request_id=review-fixture. Review this isolated fixture without editing it.")
						if err != nil {
							t.Fatal(err)
						}
						for {
							snapshot, err := n.Read(ctx, b, id, ReadFullHistory)
							if err == nil {
								for _, v := range snapshot.Turns {
									if v.ID == reviewTurn && v.Status == "completed" {
										if v.InputID != "review-fixture" {
											t.Fatalf("review request identity lost: %+v", v)
										}
										t.Log("independent native review completed inline in original thread")
										return
									}
								}
							}
							select {
							case <-ctx.Done():
								t.Fatal("native review did not finish", err, readLog())
							case <-time.After(100 * time.Millisecond):
							}
						}
					}
				}
				select {
				case <-ctx.Done():
					t.Fatalf("native turn did not finish: %s", readLog())
				case <-time.After(100 * time.Millisecond):
				}
			}
		})
	}
}

func probeNativeTUI(t *testing.T, ctx context.Context, binary, id, remoteURL, modelURL string, env []string) {
	t.Helper()
	script := `import os,pty,select,signal,sys,time,re,fcntl,termios,struct,json
pid,fd=pty.fork()
if pid==0:
 args=[sys.argv[1],"-c",'model_provider="probe"',"-c",'model="probe-model"',"-c",'model_providers.probe.name="probe"',"-c",'model_providers.probe.base_url='+json.dumps(sys.argv[4]+"/v1"),"-c",'model_providers.probe.wire_api="responses"',"-c",'model_providers.probe.requires_openai_auth=false',"-c",'features.in_app_updates=false',"resume",sys.argv[2],"--include-non-interactive","--remote",sys.argv[3],"--no-alt-screen"]
 os.execv(sys.argv[1],args)
fcntl.ioctl(fd,termios.TIOCSWINSZ,struct.pack("HHHH",40,120,0,0))
data=b"";deadline=time.monotonic()+15;found=False;sent=False
try:
 while time.monotonic()<deadline:
  if not select.select([fd],[],[],0.2)[0]: continue
  try: block=os.read(fd,65536)
  except OSError: break
  data+=block
  if b"\x1b[6n" in block: os.write(fd,b"\x1b[1;1R")
  text=re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]",b"",data.replace(b"\x1b\x1b",b"\x1b"))
  if not sent and b"Maintenance inspection complete" in text:
   os.write(fd,b"Owner UI inspection.");time.sleep(0.2);os.write(fd,b"\r");sent=True
  if sent and b"Owner TUI interaction verified" in text: found=True;break
finally:
 try: os.kill(pid,signal.SIGTERM)
 except ProcessLookupError: pass
 os.waitpid(pid,0);os.close(fd)
if not found:
 print(data[-6000:].decode(errors="replace"));sys.exit(1)
print("same-session native TUI rendered the persisted result and accepted owner input")
`
	cmd := exec.CommandContext(ctx, "python3", "-c", script, binary, id, remoteURL, modelURL)
	cmd.Env = append(env, "TERM=xterm-256color")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native TUI: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "same-session native TUI") {
		t.Fatal("missing TUI evidence")
	}
	t.Log(strings.TrimSpace(string(output)))
}

func nativeTUIGateway(t *testing.T, socket string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		up, _, err := dialer.DialContext(r.Context(), "ws://localhost/", nil)
		if err != nil {
			http.Error(w, "upstream unavailable", 502)
			return
		}
		defer up.Close()
		down, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer down.Close()
		done := make(chan struct{}, 2)
		copyFrames := func(to, from *websocket.Conn) {
			defer func() { done <- struct{}{} }()
			for {
				kind, body, err := from.ReadMessage()
				if err != nil {
					return
				}
				if to.WriteMessage(kind, body) != nil {
					return
				}
			}
		}
		go copyFrames(up, down)
		go copyFrames(down, up)
		<-done
		_ = up.Close()
		_ = down.Close()
		<-done
	}))
}
