package codexenv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/GhostFlying/tend/internal/procx"
)

// Reference pins immutable capabilities, not session/auth/database state.
type Reference struct {
	ResumeOnly bool
	Profile    Profile
	Generation string
	Directory  string
	Execution  string
}

func (b *Binding) Pin(ctx context.Context, execution string) (*Reference, error) {
	s := Service{Root: b.Directory}
	dir, err := s.directory(b.ID)
	if err != nil {
		return nil, err
	}
	unlock, err := lock(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	r, err := loadRecord(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	prior, retained, err := readAdoption(ctx, dir, execution)
	if err != nil {
		return nil, err
	}
	if (!retained && (r.Status.Deleted || !r.Status.Profile.Enabled)) || r.Status.Latest == "" {
		return nil, fmt.Errorf("environment is disabled or has no valid snapshot")
	}
	gen := r.Status.Latest
	if execution != "" {
		if retained {
			gen = prior.Generation
		} else {
			if err = writeAdoption(ctx, dir, Adoption{Execution: execution, Generation: gen, State: "pinned"}); err != nil {
				return nil, err
			}
		}
	}
	resumeOnly := retained && prior.ResumeOnly
	return &Reference{ResumeOnly: resumeOnly, Profile: r.Status.Profile, Generation: gen, Directory: dir, Execution: execution}, nil
}

func (r *Reference) VerifyHome(home string) error {
	if r != nil && !samePath(home, r.Profile.Home) {
		return fmt.Errorf("native Codex reported a different state home")
	}
	return nil
}
func (r *Reference) Record(ctx context.Context, state string) error {
	if r == nil || r.Execution == "" {
		return nil
	}
	unlock, err := lock(ctx, r.Directory)
	if err != nil {
		return err
	}
	defer unlock()
	prior, ok, err := readAdoption(ctx, r.Directory, r.Execution)
	if err != nil {
		return err
	}
	if !ok || prior.Generation != r.Generation {
		if ok && state == "retained" {
			return nil
		}
		return ErrConflict
	}
	if state == "startup_failed" && prior.State == "unknown" {
		return fmt.Errorf("unsettled environment process requires reconciliation")
	}
	if state == "startup_failed" && prior.Previous != "" {
		prior.BlockedGeneration = prior.Generation
		record, err := loadRecord(filepath.Join(r.Directory, "state.json"))
		if err != nil {
			return err
		}
		prior.BlockedRevision = record.Status.Revision
		prior.Generation = prior.Previous
		prior.Previous = ""
		state = "rollback_pending"
	}
	prior.State = state
	if state == "ready" {
		prior.Previous = ""
		prior.ResumeOnly = false
	}
	return writeAdoption(ctx, r.Directory, prior)
}
func (r *Reference) Current() bool {
	if r == nil {
		return true
	}
	rec, err := loadRecord(filepath.Join(r.Directory, "state.json"))
	if err != nil {
		return false
	}
	if rec.Status.Latest == r.Generation {
		return true
	}
	if rec.Status.Deleted || !rec.Status.Profile.Enabled {
		return true
	}
	prior, _, err := readAdoption(context.Background(), r.Directory, r.Execution)
	return err == nil && prior.BlockedGeneration == rec.Status.Latest && rec.Status.Revision <= prior.BlockedRevision
}

// Adopt is called only under the execution owner's confirmed idle gate.
func (r *Reference) Adopt(ctx context.Context) error {
	if r == nil || r.Execution == "" {
		return fmt.Errorf("persistent execution reference required")
	}
	unlock, err := lock(ctx, r.Directory)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(r.Directory, "state.json")
	rec, err := loadRecord(path)
	if err != nil {
		return err
	}
	if rec.Status.Deleted || !rec.Status.Profile.Enabled {
		return fmt.Errorf("disabled environment does not adopt new snapshots")
	}
	prior, ok, err := readAdoption(ctx, r.Directory, r.Execution)
	if err != nil {
		return err
	}
	if !ok || prior.Generation != r.Generation {
		return ErrConflict
	}
	if prior.BlockedGeneration == rec.Status.Latest && rec.Status.Revision <= prior.BlockedRevision {
		return fmt.Errorf("latest generation failed to start")
	}
	prior.Previous = r.Generation
	prior.ResumeOnly = true
	prior.Generation = rec.Status.Latest
	prior.State = "pending_restart"
	prior.BlockedGeneration = ""
	prior.BlockedRevision = 0
	return writeAdoption(ctx, r.Directory, prior)
}
func (r *Reference) Command(ctx context.Context, bin string, args []string, cwd string, env []string) (*exec.Cmd, func(), error) {
	return prepareCommand(ctx, r.Profile, filepath.Join(r.Directory, "generations", r.Generation), bin, args, cwd, env)
}
func prepareCommand(ctx context.Context, p Profile, shot, bin string, args []string, cwd string, env []string) (*exec.Cmd, func(), error) {
	actualHome := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok {
			actualHome = value
		}
	}
	if actualHome == "" {
		actualHome, _ = os.UserHomeDir()
	}
	if !samePath(p.UserHome, actualHome) {
		return nil, nil, fmt.Errorf("configured user home differs from the unchanged execution HOME")
	}
	if err := platformCheck(ctx); err != nil {
		return nil, nil, err
	}
	if err := ensureHome(p); err != nil {
		return nil, nil, err
	}
	work, err := os.MkdirTemp(filepath.Join(p.Home, ".tend-processes"), "launch-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(work) }
	if err = materialize(ctx, filepath.Join(shot, "plugins"), filepath.Join(work, "plugins")); err != nil {
		cleanup()
		return nil, nil, err
	}
	provenance, err := os.ReadFile(filepath.Join(shot, "provenance.json"))
	if err == nil {
		err = atomicWrite(filepath.Join(work, "provenance.json"), provenance)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err = os.MkdirAll(filepath.Join(work, "legacy-skills"), 0700); err != nil {
		cleanup()
		return nil, nil, err
	}
	cmd, err := platformCommand(ctx, p, shot, work, bin, args, cwd)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	cmd.Env = replaceEnv(env, "CODEX_HOME", p.Home)
	cmd.Env = replaceEnv(cmd.Env, "CODEX_SQLITE_HOME", "")
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"} {
		cmd.Env = replaceEnv(cmd.Env, key, "")
	}
	return cmd, cleanup, nil
}
func replaceEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, v := range env {
		if !strings.HasPrefix(v, key+"=") {
			out = append(out, v)
		}
	}
	if value != "" {
		out = append(out, key+"="+value)
	}
	return out
}
func ensureHome(p Profile) error {
	for _, dir := range []string{p.Home, filepath.Join(p.Home, ".tend-processes"), filepath.Join(p.Home, ".tend-capabilities"), filepath.Join(p.Home, "skills"), filepath.Join(p.Home, "plugins"), filepath.Join(p.UserHome, ".agents", "skills"), filepath.Join(p.UserHome, ".agents", "plugins")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	for _, name := range []string{"config.toml", "AGENTS.md", "AGENTS.override.md"} {
		f, err := os.OpenFile(filepath.Join(p.Home, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_ = f.Close()
		} else if !os.IsExist(err) {
			return err
		}
	}
	if p.AuthSource != "" {
		target := filepath.Join(p.Home, "auth.json")
		if _, err := os.Lstat(target); os.IsNotExist(err) {
			if err = os.Symlink(p.AuthSource, target); err != nil {
				return err
			}
		} else {
			link, err := os.Readlink(target)
			if err != nil || !samePath(link, p.AuthSource) {
				return fmt.Errorf("runtime auth is not the configured shared-auth link")
			}
		}
	}
	return nil
}

// Probe reads native catalogs only. No thread or model turn is created.
func (s *Service) validateNative(ctx context.Context, p Profile, stage string) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	defer func() {
		if resultErr != nil && ctx.Err() != nil {
			resultErr = errors.Join(resultErr, ctx.Err())
		}
	}()
	bin, err := exec.LookPath("codex")
	if err != nil {
		return fmt.Errorf("native Codex probe unavailable: %w", err)
	}
	// Validation uses a disposable state home and the same discovery mounts.
	tmp, err := os.MkdirTemp("", "tend-codex-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	originalHome := p.Home
	p.Home = filepath.Join(tmp, "home")
	p.AuthSource = ""
	probeStage := filepath.Join(tmp, "capabilities")
	if err = materialize(ctx, stage, probeStage); err != nil {
		return err
	}
	var config map[string]any
	if _, err = toml.DecodeFile(filepath.Join(probeStage, "config.toml"), &config); err != nil {
		return err
	}
	config["sqlite_home"] = p.Home
	config["log_dir"] = filepath.Join(p.Home, "logs")
	if markets, ok := config["marketplaces"].(map[string]any); ok {
		for _, value := range markets {
			if market, ok := value.(map[string]any); ok {
				if source, ok := market["source"].(string); ok {
					market["source"] = strings.Replace(source, filepath.Join(originalHome, ".tend-capabilities"), filepath.Join(p.Home, ".tend-capabilities"), 1)
				}
			}
		}
	}
	var encoded bytes.Buffer
	if err = toml.NewEncoder(&encoded).Encode(config); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(probeStage, "config.toml"), encoded.Bytes(), 0600); err != nil {
		return err
	}
	cmd, cleanup, err := prepareCommand(ctx, p, probeStage, bin, []string{"app-server"}, tmp, os.Environ())
	if err != nil {
		return err
	}
	defer cleanup()
	procx.Contain(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = input.Close(); procx.KillGroup(cmd); _ = cmd.Wait() }()
	requests := []map[string]any{{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "tend-environment-probe", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}}, {"method": "initialized"}, {"id": 2, "method": "skills/list", "params": map[string]any{"cwds": []string{tmp}, "forceReload": true}}, {"id": 3, "method": "plugin/list", "params": map[string]any{"cwds": []string{tmp}, "marketplaceKinds": []string{"local"}}}, {"id": 4, "method": "hooks/list", "params": map[string]any{"cwds": []string{tmp}}}}
	enc := json.NewEncoder(input)
	for _, q := range requests {
		if err = enc.Encode(q); err != nil {
			return err
		}
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	seen := map[int]bool{}
	for scanner.Scan() {
		var response struct {
			ID     int             `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result struct {
				MarketplaceLoadErrors []json.RawMessage `json:"marketplaceLoadErrors"`
				Marketplaces          []struct {
					Plugins []struct {
						ID        string `json:"id"`
						Enabled   bool   `json:"enabled"`
						Installed bool   `json:"installed"`
					} `json:"plugins"`
				} `json:"marketplaces"`
				Data []struct {
					Errors []json.RawMessage `json:"errors"`
					Skills []struct {
						Name    string `json:"name"`
						Enabled bool   `json:"enabled"`
					} `json:"skills"`
				} `json:"data"`
			} `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			continue
		}
		if response.ID < 2 || response.ID > 4 {
			continue
		}
		if len(response.Error) > 0 {
			return fmt.Errorf("native capability catalog rejected environment")
		}
		if len(response.Result.MarketplaceLoadErrors) > 0 {
			return fmt.Errorf("native marketplace loading reported errors")
		}
		if response.ID == 3 {
			expected := map[string]bool{}
			if plugins, ok := config["plugins"].(map[string]any); ok {
				for id := range plugins {
					if id != "_default" {
						expected[id] = false
					}
				}
			}
			for _, market := range response.Result.Marketplaces {
				for _, plugin := range market.Plugins {
					if _, ok := expected[plugin.ID]; ok {
						if !plugin.Enabled || !plugin.Installed {
							return fmt.Errorf("selected plugin is not installed and enabled")
						}
						expected[plugin.ID] = true
					}
				}
			}
			for _, found := range expected {
				if !found {
					return fmt.Errorf("selected plugin was absent from native inventory")
				}
			}
		}
		for _, entry := range response.Result.Data {
			if len(entry.Errors) > 0 {
				return fmt.Errorf("native skill or plugin loading reported errors")
			}
		}
		seen[response.ID] = true
		if len(seen) == 3 {
			return nil
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("native capability probe ended without a catalog")
}
