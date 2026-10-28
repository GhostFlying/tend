package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/runner/gitws"
)

func TestLocalRepositoryControlPublishesWithoutRestart(t *testing.T) {
	home := t.TempDir()
	workspaces := gitws.New(gitws.Config{Home: home})
	closeControl, err := startRepositoryControl(t.Context(), home, workspaces, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControl()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(home, "repositories.sock"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	input := gitws.BindRepositoryRequest{RequestID: "req_01ARZ3NDEKTSV4RRFFQ69G5FAV", Binding: gitws.RepositoryBinding{Repo: "new", RemoteURL: "https://example.com/new.git"}}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		response, err := client.Post("http://localhost/repositories/bind", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		var status repositoryStatus
		err = json.NewDecoder(response.Body).Decode(&status)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || status.Revision != 1 || len(status.Repositories) != 1 {
			t.Fatal(response.StatusCode, status, err)
		}
	}
	if revision := workspaces.BindingRevision(); revision != 1 {
		t.Fatal("retry repeated mutation", revision)
	}
	if _, err := startRepositoryControl(t.Context(), home, gitws.New(gitws.Config{Home: home}), nil); err == nil {
		t.Fatal("another process took over the live control socket")
	}
}

func TestLocalRepositoryControlSupportsLongHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), strings.Repeat("long-directory-", 12))
	workspaces := gitws.New(gitws.Config{Home: home})
	closeControl, err := startRepositoryControl(t.Context(), home, workspaces, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControl()
	socket, err := repositorySocketPath(home)
	if err != nil || len(socket) > 90 {
		t.Fatal(socket, err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get("http://localhost/repositories")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
}
