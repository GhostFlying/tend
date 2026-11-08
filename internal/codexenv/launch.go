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
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Reference pins immutable capabilities, not session/auth/database state.
type Reference struct {
	ProcessHome string
	Extensions  protocol.ExtensionExecutionPin
	ResumeOnly  bool
	Profile     Profile
	Generation  string
	Directory   string
	Execution   string
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
	if b.Generation != "" && !retained {
		found := false
		for _, snapshot := range r.Status.Snapshots {
			if snapshot.ID == b.Generation {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("requested environment generation is unavailable")
		}
		gen = b.Generation
	}
	if execution != "" {
		if retained {
			gen = prior.Generation
		} else {
			if err = writeAdoption(ctx, dir, Adoption{Execution: execution, Generation: gen, State: "pinned", Extensions: b.Extensions}); err != nil {
				return nil, err
			}
		}
	}
	resumeOnly := retained && prior.ResumeOnly
	extensionPin := b.Extensions
	if retained {
		extensionPin = prior.Extensions
	}
	return &Reference{Extensions: extensionPin, ResumeOnly: resumeOnly, Profile: r.Status.Profile, Generation: gen, Directory: dir, Execution: execution}, nil
}

func (r *Reference) VerifyHome(home string) error {
	if r == nil {
		return nil
	}
	expected := r.Profile.Home
	if r.ProcessHome != "" {
		expected = r.ProcessHome
	}
	if !samePath(home, expected) {
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
		prior.Extensions = prior.PreviousExtensions
		prior.PreviousExtensions = protocol.ExtensionExecutionPin{}
		prior.Previous = ""
		state = "rollback_pending"
	}
	prior.State = state
	if r.ProcessHome != "" {
		prior.ConfigurationHome = r.ProcessHome
	}
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

	prior, _, err := readAdoption(context.Background(), r.Directory, r.Execution)
	if err != nil {
		return false
	}
	target, pin := rec.Status.Latest, rec.Status.Profile.ExtensionPin
	if prior.DesiredGeneration != "" {
		target, pin = prior.DesiredGeneration, prior.DesiredExtensions
	} else if pin != (protocol.ExtensionExecutionPin{}) {
		return true
	}
	if target == r.Generation && pin == r.Extensions {
		return true
	}
	if rec.Status.Deleted || !rec.Status.Profile.Enabled {
		return true
	}
	return prior.BlockedGeneration == target && rec.Status.Revision <= prior.BlockedRevision
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
	target, pin := rec.Status.Latest, rec.Status.Profile.ExtensionPin
	if prior.DesiredGeneration != "" {
		target, pin = prior.DesiredGeneration, prior.DesiredExtensions
	}
	if prior.BlockedGeneration == target && rec.Status.Revision <= prior.BlockedRevision {
		return fmt.Errorf("latest generation failed to start")
	}
	prior.Previous = r.Generation
	prior.PreviousExtensions = prior.Extensions
	prior.Extensions = pin
	prior.MCP = nil
	prior.ResumeOnly = true
	prior.Generation = target
	prior.State = "pending_restart"
	prior.BlockedGeneration = ""
	prior.BlockedRevision = 0
	return writeAdoption(ctx, r.Directory, prior)
}
func (r *Reference) Command(ctx context.Context, bin string, args []string, cwd string, env []string) (*exec.Cmd, func(), error) {
	cmd, cleanup, err := prepareCommandForExecution(ctx, r.Profile, filepath.Join(r.Directory, "generations", r.Generation), bin, args, cwd, env, r.Execution)
	if err == nil {
		for _, entry := range cmd.Env {
			if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
				r.ProcessHome = value
			}
		}
	}
	return cmd, cleanup, err
}
func prepareCommand(ctx context.Context, p Profile, shot, bin string, args []string, cwd string, env []string) (*exec.Cmd, func(), error) {
	return prepareCommandForExecution(ctx, p, shot, bin, args, cwd, env, "")
}

func prepareCommandForExecution(ctx context.Context, p Profile, shot, bin string, args []string, cwd string, env []string, execution string) (*exec.Cmd, func(), error) {
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
	exclusions, err := refreshExclusions(ctx, p.UserHome, time.Now(), scanPersonalSkills)
	if err != nil {
		return nil, nil, err
	}
	work := filepath.Join(p.Home, ".tend-processes", "execution-"+digest([]byte(execution)))
	if execution == "" {
		work, err = os.MkdirTemp(filepath.Join(p.Home, ".tend-processes"), "probe-")
		if err != nil {
			return nil, nil, err
		}
	}
	// Rebuild an execution's capabilities only after its previous process has
	// closed. OAuth files are ordinary native files in this stable home.
	unlock, err := lock(ctx, work)
	if err != nil {
		return nil, nil, err
	}
	if err = prepareProcessHome(ctx, p, shot, work, exclusions); err != nil {
		unlock()
		return nil, nil, err
	}
	cmd, err := platformCommand(ctx, p, shot, work, bin, args, cwd)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	// Retain launched homes: SQLite rollouts and native history may contain
	// their session aliases. Deleting one would invalidate an existing thread.
	var cleaned sync.Once
	cleanup := func() {
		cleaned.Do(func() {
			unlock()
			if execution == "" {
				_ = os.RemoveAll(work)
			}
		})
	}
	cmd.Env = replaceEnv(env, "CODEX_HOME", work)
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
	for _, dir := range []string{p.Home, filepath.Join(p.Home, ".tend-processes")} {
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
	// Validation uses a disposable state home and the same native configuration.
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
	personal, err := readExclusions(p.UserHome)
	if err != nil {
		return fmt.Errorf("read personal exclusions for native probe: %w", err)
	}
	excluded := map[string]bool{}
	for _, path := range personal.Paths {
		excluded[path] = true
	}
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
						Name     string `json:"name"`
						Path     string `json:"path"`
						PluginID string `json:"plugin_id"`
						Enabled  bool   `json:"enabled"`
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
		if p.StrictNames && response.ID == 2 {
			names := map[string]string{}
			standalone := map[string]bool{}
			for _, entry := range response.Result.Data {
				for _, skill := range entry.Skills {
					if !skill.Enabled {
						continue
					}
					name := skill.Name
					if skill.PluginID != "" {
						plugin, _, _ := strings.Cut(skill.PluginID, "@")
						name = plugin + ":" + name
					}
					if prior, ok := names[name]; ok && prior != skill.Path {
						return fmt.Errorf("managed skills have conflicting native loading names")
					}
					names[name] = skill.Path
					for _, item := range p.Skills.Installed {
						if strings.Contains(filepath.ToSlash(skill.Path), "/skills/"+digest([]byte(item.ID))[:16]+"/SKILL.md") {
							standalone[item.ID] = true
						}
					}
				}
			}
			if len(standalone) != len(p.Skills.Installed) {
				return fmt.Errorf("a managed skill was absent or shadowed in the native catalog")
			}
		}
		for _, entry := range response.Result.Data {
			for _, raw := range entry.Errors {
				var failure struct {
					Path string `json:"path"`
				}
				// Codex parses personal files before applying enabled=false.
				// Ignore only native errors for explicitly excluded paths;
				// owned, project, admin and unknown errors still fail validation.
				if json.Unmarshal(raw, &failure) == nil && failure.Path != "" && excluded[canonicalPath(failure.Path)] {
					continue
				}
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
