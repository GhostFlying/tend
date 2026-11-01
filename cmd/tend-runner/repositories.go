package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/gitws"
)

type repositoryStatus = protocol.LocalRepositoryStatus

// repositorySocketPath is shared by the service and its local CLI. Long data
// directories use a short private socket directory and a private locator file.
func repositorySocketPath(home string) (string, error) {
	path := filepath.Join(home, "repositories.sock")
	if len(path) <= 90 {
		return path, nil
	}
	raw, err := os.ReadFile(filepath.Join(home, "repository-control.json"))
	if err != nil {
		return "", err
	}
	var locator struct {
		Socket string `json:"socket"`
	}
	if err := json.Unmarshal(raw, &locator); err != nil {
		return "", err
	}
	if !filepath.IsAbs(locator.Socket) || len(locator.Socket) > 90 {
		return "", errors.New("invalid repository control locator")
	}
	return locator.Socket, nil
}

func saveRepositorySocket(home, path string) error {
	raw, err := json.Marshal(struct {
		Socket string `json:"socket"`
	}{path})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(home, ".repository-control-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(home, "repository-control.json"))
}

// startRepositoryControl owns a private local socket, never a network listener.
func startRepositoryControl(ctx context.Context, home string, w *gitws.Workspaces, agents http.Handler) (func(), error) {
	if err := w.LoadBindings(ctx); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	path, err := repositorySocketPath(home)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSocket == 0 {
				return nil, errors.New("repository control path is not a socket")
			}
			if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
				c.Close()
				return nil, errors.New("repository control is already running")
			}
			if err := os.Remove(path); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	temporary := ""
	if len(filepath.Join(home, "repositories.sock")) > 90 {
		base := os.TempDir()
		if len(base) > 40 {
			base = "/tmp"
		}
		temporary, err = os.MkdirTemp(base, "tend-repositories-")
		if err != nil {
			return nil, err
		}
		path = filepath.Join(temporary, "control.sock")
	}
	cleanup := func() {
		if temporary != "" {
			_ = os.Remove(path)
			_ = os.Remove(temporary)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		cleanup()
		return nil, err
	}
	if temporary != "" {
		if err := saveRepositorySocket(home, path); err != nil {
			ln.Close()
			cleanup()
			return nil, err
		}
	}
	mux := http.NewServeMux()
	if agents != nil {
		mux.Handle("/agents/", agents)
	}
	mux.HandleFunc("GET /repositories", func(out http.ResponseWriter, _ *http.Request) {
		out.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(out).Encode(repositoryStatus{Revision: w.BindingRevision(), Repositories: w.RepositoryBindings()})
	})
	mux.HandleFunc("POST /repositories/bind", func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("Content-Type", "application/json")
		var request gitws.BindRepositoryRequest
		d := json.NewDecoder(http.MaxBytesReader(out, r.Body, 1<<20))
		d.DisallowUnknownFields()
		if err := d.Decode(&request); err != nil {
			out.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(out).Encode(repositoryStatus{Error: "invalid repository binding request"})
			return
		}
		if err := d.Decode(new(any)); err != io.EOF {
			out.WriteHeader(http.StatusBadRequest)
			return
		}
		bounded, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		revision, err := w.BindRepository(bounded, request)
		if err != nil {
			out.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(out).Encode(repositoryStatus{Revision: w.BindingRevision(), Error: err.Error()})
			return
		}
		_ = json.NewEncoder(out).Encode(repositoryStatus{Revision: revision, Repositories: w.RepositoryBindings()})
	})
	local, cancel := context.WithCancel(ctx)
	srv := &http.Server{Handler: mux, BaseContext: func(net.Listener) context.Context { return local }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	return func() {
		cancel()
		stop, doneShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer doneShutdown()
		_ = srv.Shutdown(stop)
		_ = srv.Close()
		<-done
		_ = os.Remove(path)
		if temporary != "" {
			if current, err := repositorySocketPath(home); err == nil && current == path {
				_ = os.Remove(filepath.Join(home, "repository-control.json"))
			}
			cleanup()
		}
	}, nil
}

func runRepositoryCommand(args []string) int {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "online repository binding requires a Unix host")
		return 1
	}
	if len(args) == 0 || (args[0] != "bind" && args[0] != "status") {
		fmt.Fprintln(os.Stderr, "usage: tend-runner repo bind|status --config <file> [--repo <name> --remote <url> --orca <selector>]")
		return 2
	}
	fs := flag.NewFlagSet("repo "+args[0], flag.ContinueOnError)
	configPath := fs.String("config", "", "runner configuration file")
	repo := fs.String("repo", "", "registered repository name")
	remote := fs.String("remote", "", "registered remote URL")
	orca := fs.String("orca", "", "existing Orca repository selector")
	requestID := fs.String("request-id", "", "reuse with the same --revision only to reconcile an earlier request")
	revision := fs.Int64("revision", -1, "local repository control revision; defaults to the observed revision")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *configPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--config is required; use named flags")
		return 2
	}
	var cfg config
	if err := cfgfile.Load(*configPath, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	home, err := cfgfile.Path(cfg.Home)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if home == "" {
		dir, e := os.UserHomeDir()
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 1
		}
		home = filepath.Join(dir, ".tend")
	}
	socket, err := repositorySocketPath(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "local runner control unavailable:", err)
		return 1
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 40 * time.Second}
	do := func(method, path string, input any) (repositoryStatus, error) {
		var body io.Reader
		if input != nil {
			raw, e := json.Marshal(input)
			if e != nil {
				return repositoryStatus{}, e
			}
			body = bytes.NewReader(raw)
		}
		req, e := http.NewRequest(method, "http://localhost"+path, body)
		if e != nil {
			return repositoryStatus{}, e
		}
		response, e := client.Do(req)
		if e != nil {
			return repositoryStatus{}, errors.New("local runner unavailable; preserve request ID and revision before retrying")
		}
		defer response.Body.Close()
		var result repositoryStatus
		if e := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); e != nil {
			return result, e
		}
		if response.StatusCode != http.StatusOK {
			return result, fmt.Errorf("repository binding rejected: %s", result.Error)
		}
		return result, nil
	}
	status, err := do("GET", "/repositories", nil)
	if err == nil && args[0] == "bind" {
		if strings.TrimSpace(*repo) == "" || strings.TrimSpace(*remote) == "" {
			fmt.Fprintln(os.Stderr, "--repo and --remote are required")
			return 2
		}
		if *requestID == "" {
			*requestID = protocol.NewULIDGen(time.Now, nil).New(protocol.PrefixRequest)
		} else if *revision < 0 {
			fmt.Fprintln(os.Stderr, "reconciliation requires the original --revision")
			return 2
		}
		if *revision < 0 {
			*revision = status.Revision
		}
		fmt.Fprintf(os.Stderr, "Repository request %s; revision %d\n", *requestID, *revision)
		input := gitws.BindRepositoryRequest{RequestID: *requestID, ExpectedRevision: *revision, Binding: gitws.RepositoryBinding{Repo: *repo, RemoteURL: *remote, Orca: *orca}}
		status, err = do("POST", "/repositories/bind", input)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
