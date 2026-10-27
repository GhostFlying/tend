package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
)

const ownerLogin = "me@example.com"

func (s *server) startAPI() string {
	laptop := netx.Static{Identity: &netx.Identity{Node: "laptop", User: ownerLogin}}
	h := api.New(s.app, s.store, laptop, secret, api.Config{Owners: []string{ownerLogin}}, quiet())
	hs := httptest.NewServer(h.Handler())
	s.t.Cleanup(hs.Close)
	return hs.URL
}

type ownerCLI struct {
	t   *testing.T
	env *cli.Env
	out *bytes.Buffer
}

func newOwnerCLI(t *testing.T, dir string) *ownerCLI {
	a := &ownerCLI{t: t, out: &bytes.Buffer{}}
	a.env = &cli.Env{Stdin: strings.NewReader(""), Stdout: a.out, Stderr: &bytes.Buffer{}, Home: t.TempDir(), ConfigDir: dir, HTTP: &http.Client{Timeout: 10 * time.Second}, IDs: protocol.NewULIDGen(time.Now, nil)}
	return a
}
func (a *ownerCLI) run(stdin string, args ...string) string {
	a.t.Helper()
	a.out.Reset()
	a.env.Stdin = strings.NewReader(stdin)
	if code := cli.Main(context.Background(), a.env, args); code != 0 {
		a.t.Fatalf("tend %v = %d: %s", args, code, a.env.Stderr)
	}
	return a.out.String()
}
func TestEndToEnd_OwnerAnswersDecision(t *testing.T) {
	srv := newServer(t, time.Minute)
	url := srv.startAPI()
	configDir := filepath.Join(t.TempDir(), "tend")
	owner := newOwnerCLI(t, configDir)
	owner.run("", "login", "--server", url)

	n := newNode(t, srv, passing)
	n.ws.noChanges = true
	n.start()
	task := srv.createTask()
	eventually(t, "the task waits for a decision", func() bool { return srv.waitsForDecision(task) })
	var s protocol.Snapshot
	if err := json.Unmarshal([]byte(owner.run("", "status", "--json")), &s); err != nil || len(s.Decisions) != 1 {
		t.Fatalf("tend status = %+v, %v", s, err)
	}
	if out := owner.run("", "decide", s.Decisions[0].DecisionID, "cancel the task"); !strings.Contains(out, "tend carried it out: cancel") {
		t.Errorf("tend decide printed:\n%s", out)
	}
	if got := srv.task(task).Lifecycle; got != protocol.TaskCancelled {
		t.Errorf("task = %s, want cancelled", got)
	}
}
