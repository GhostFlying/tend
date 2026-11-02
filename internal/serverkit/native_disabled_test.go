package serverkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/netx"
)

func TestDefaultServerDoesNotInitializeNativeWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	directory := t.TempDir()
	// A disabled broker must not require a usable CLI or central working directory.
	t.Setenv("PATH", t.TempDir())
	nativePath := filepath.Join(directory, "native-workers")
	if err := os.WriteFile(nativePath, []byte("disabled"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	finished := errors.New("disabled native workers verified")
	err = Run(ctx, Config{DataDir: directory, Owners: []string{"test-owner"}}, []byte("isolated-test-secret"), Dependencies{
		Listeners: []net.Listener{listener},
		WhoIs:     netx.Static{Identity: &netx.Identity{User: "test-owner"}},
		OpenSession: func(context.Context, SessionSpec) (harness.Session, error) {
			return nil, errors.New("default server unexpectedly opened a model session")
		},
		Background: func(ctx context.Context, _ *Runtime) error {
			for _, channel := range []string{"exec", "control", "tui"} {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listener.Addr().String()+"/runner/v1/native/"+channel, nil)
				if err != nil {
					return err
				}
				response, err := client.Do(request)
				if err != nil {
					return err
				}
				response.Body.Close()
				if response.StatusCode != http.StatusNotFound {
					return fmt.Errorf("disabled native channel %s returned %d", channel, response.StatusCode)
				}
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+listener.Addr().String()+"/api/v1/session", strings.NewReader(`{"client_id":"default-owner-test","scope":"owner"}`))
			if err != nil {
				return err
			}
			response, err := client.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("default owner session returned %d", response.StatusCode)
			}
			return finished
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, finished) {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(nativePath); err != nil || string(data) != "disabled" {
		t.Fatalf("disabled native working directory was touched: %q, %v", data, err)
	}
}
