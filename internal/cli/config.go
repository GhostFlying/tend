package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

// config is config.json: where the server is.
type config struct {
	Server string `json:"server"`
}

// credentials is credentials.json, readable by the owner only. ClientID
// identifies this installation and stays the same across logins.
type credentials struct {
	DeploymentID string `json:"deployment_id"`
	ClientID     string `json:"client_id"`
	Owner        string `json:"owner_token,omitempty"`
	Agent        string `json:"agent_token,omitempty"`
}

var errNotLoggedIn = errors.New("not logged in; run: tend login --server <url>")

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Each write gets its own temporary file: several agent sessions may
	// write the same file at once, and a reader must see a whole one.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func (e *Env) loadConfig() (config, credentials, error) {
	var cfg config
	var cred credentials
	err := readJSON(filepath.Join(e.ConfigDir, "config.json"), &cfg)
	if err == nil {
		err = readJSON(filepath.Join(e.ConfigDir, "credentials.json"), &cred)
	}
	if errors.Is(err, fs.ErrNotExist) || (err == nil && cfg.Server == "") {
		return cfg, cred, errNotLoggedIn
	}
	return cfg, cred, err
}

func (e *Env) saveConfig(cfg config, cred credentials) error {
	if err := writeJSON(filepath.Join(e.ConfigDir, "config.json"), cfg, 0o600); err != nil {
		return err
	}
	return writeJSON(filepath.Join(e.ConfigDir, "credentials.json"), cred, 0o600)
}

func newClientID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "cli-" + hex.EncodeToString(b)
}

// client returns an API client with the token of the given scope.
func (e *Env) client(scope protocol.TokenScope) (*client, error) {
	cfg, cred, err := e.loadConfig()
	if err != nil {
		return nil, err
	}
	tok := cred.Owner
	if scope == protocol.ScopeAgent {
		tok = cred.Agent
	}
	if tok == "" {
		return nil, errNotLoggedIn
	}
	return &client{http: e.HTTP, server: cfg.Server, token: tok, deploymentID: cred.DeploymentID}, nil
}
