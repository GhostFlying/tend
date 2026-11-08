package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Runtime only describes local state placement and authentication. Portable
// releases never contain these values and never select the executable.
type Runtime struct {
	Environment *codexenv.Binding `json:"environment,omitempty"`
	Home        string            `json:"home"`
	AuthSource  string            `json:"auth_source"`
	BaseConfig  string            `json:"base_config,omitempty"`
}
type Configuration struct {
	Directory string             `json:"directory"`
	Runtimes  map[string]Runtime `json:"runtimes"`
}

func (c *Configuration) ResolvePaths(resolve func(string) (string, error)) error {
	var err error
	c.Directory, err = resolve(c.Directory)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(c.Directory) || len(c.Runtimes) == 0 {
		return fmt.Errorf("managed extensions require a cache directory and runtime mappings")
	}
	for id, r := range c.Runtimes {
		if !protocol.AgentInventoryID(id) || r.Home == "" {
			return fmt.Errorf("invalid managed runtime mapping")
		}
		r.Home, err = resolve(r.Home)
		if err != nil {
			return err
		}
		if r.AuthSource != "" {
			r.AuthSource, err = resolve(r.AuthSource)
			if err != nil {
				return err
			}
		}
		if r.Environment != nil {
			r.Environment.Directory, err = resolve(r.Environment.Directory)
			if err != nil {
				return err
			}
			if r.Environment.ID == "" {
				return fmt.Errorf("mapped runtime environment requires an ID")
			}
		}
		if r.BaseConfig != "" {
			r.BaseConfig, err = resolve(r.BaseConfig)
			if err != nil {
				return err
			}
		}
		c.Runtimes[id] = r
	}
	return nil
}

type Consumer struct {
	// ValidationOnly checks portable catalogs without requiring another host's credentials.
	ValidationOnly bool
	Config         Configuration
	UserHome       string
	Now            func() time.Time
	Validate       func(context.Context, codexenv.Profile, string) error
	mu             sync.Mutex
}

func (c *Consumer) Enabled(runtime string) bool { _, ok := c.Config.Runtimes[runtime]; return ok }

func atomicFile(file string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".staging-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), file); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(file))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// Receive verifies and stages a complete release. A failure leaves the previous
// downloaded pointer intact. Source is supplied by the authenticated transport.
func (c *Consumer) Receive(ctx context.Context, r protocol.ExtensionRelease, source func(context.Context, protocol.ExtensionResource) (io.ReadCloser, error)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !filepath.IsAbs(c.Config.Directory) {
		return fmt.Errorf("managed extension cache requires an absolute directory")
	}
	if err := ValidateRelease(r); err != nil {
		return err
	}
	files, err := blobstore.New(filepath.Join(c.Config.Directory, "objects"))
	if err != nil {
		return err
	}
	for _, res := range r.Catalog.Resources {
		if res.Kind == "mcp" {
			continue
		}
		var raw []byte
		if f, err := files.Open(res.ContentSHA256); err == nil {
			raw, err = io.ReadAll(io.LimitReader(f, MaxArchiveBytes+1))
			_ = f.Close()
			if err != nil {
				return err
			}
		}
		if VerifyContent(res, raw) != nil {
			f, err := source(ctx, res)
			if err != nil {
				return err
			}
			raw, err = io.ReadAll(io.LimitReader(f, MaxArchiveBytes+1))
			_ = f.Close()
			if err != nil {
				return err
			}
			if err = VerifyContent(res, raw); err != nil {
				return err
			}
			if err = ValidatePackage(ctx, res.Kind, raw); err != nil {
				return err
			}
			if _, err = files.Put(ctx, bytes.NewReader(raw)); err != nil {
				return err
			}
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = atomicFile(filepath.Join(c.Config.Directory, "releases", r.ID+".json"), raw); err != nil {
		return err
	}
	return atomicFile(filepath.Join(c.Config.Directory, "downloaded.json"), []byte(fmt.Sprintf("%q", r.ID)))
}

func (c *Consumer) Release(id string) (protocol.ExtensionRelease, error) {
	var r protocol.ExtensionRelease
	if id == "" {
		raw, err := os.ReadFile(filepath.Join(c.Config.Directory, "downloaded.json"))
		if err != nil {
			return r, err
		}
		if err = json.Unmarshal(raw, &id); err != nil {
			return r, err
		}
	}
	if len(id) != 68 || id[:4] != "ext-" || !ValidHash(id[4:]) {
		return r, fmt.Errorf("invalid release identity")
	}
	raw, err := os.ReadFile(filepath.Join(c.Config.Directory, "releases", id+".json"))
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	if r.ID != id {
		return r, fmt.Errorf("cached release identity mismatch")
	}
	return r, ValidateRelease(r)
}

// Prepare creates a host-local view without changing session, database, auth or
// rollout roots. Different category/Agent pairs own distinct snapshot histories.
func (c *Consumer) Prepare(ctx context.Context, pin protocol.ExtensionExecutionPin, runtime string) (*codexenv.Binding, protocol.ExtensionPrepared, error) {
	return c.PrepareScoped(ctx, pin, runtime, "")
}

func (c *Consumer) PrepareScoped(ctx context.Context, pin protocol.ExtensionExecutionPin, runtime, scope string) (*codexenv.Binding, protocol.ExtensionPrepared, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := protocol.ExtensionPrepared{RuntimeID: runtime, Pin: pin, MCP: []protocol.ExtensionMCPStatus{}}
	r, err := c.Release(pin.ReleaseID)
	if err != nil {
		return nil, status, err
	}
	combo, err := VerifyCombination(r, pin)
	if err != nil {
		return nil, status, err
	}
	local, ok := c.Config.Runtimes[runtime]
	if !ok {
		return nil, status, fmt.Errorf("runtime has not enabled managed extensions")
	}
	if !filepath.IsAbs(local.Home) || !filepath.IsAbs(c.UserHome) || local.AuthSource != "" && !filepath.IsAbs(local.AuthSource) {
		return nil, status, fmt.Errorf("managed runtime requires fixed absolute local paths")
	}
	viewCategory := pin.CategoryID
	if scope != "" {
		viewCategory = ""
	}
	viewKey, _ := json.Marshal(struct {
		Scope    string
		Category string
		Agent    protocol.AgentIdentity
		Runtime  string
	}{scope, viewCategory, pin.Agent, runtime})
	root := filepath.Join(c.Config.Directory, "views", Hash(viewKey))
	config := map[string]any{}
	if local.BaseConfig != "" {
		if _, err = toml.DecodeFile(local.BaseConfig, &config); err != nil {
			return nil, status, fmt.Errorf("independent base configuration is invalid")
		}
	}
	// Managed resources own user-level extension policy. Project overlays are
	// still loaded by Codex from the unchanged working directory.
	delete(config, "plugins")
	delete(config, "marketplaces")
	delete(config, "skills")
	mcps := map[string]any{}
	config["mcp_servers"] = mcps

	stamp, _ := json.Marshal(struct {
		Local    Runtime
		UserHome string
		Config   map[string]any
	}{local, c.UserHome, config})
	variant := Hash(stamp)[:16]
	inputs := filepath.Join(root, "inputs", pin.EffectiveSHA256+"-"+variant)
	if err = os.MkdirAll(inputs, 0700); err != nil {
		return nil, status, err
	}
	p := codexenv.Profile{StrictNames: true, ManagedRuntimeID: runtime, ExtensionPin: pin, ID: "managed", Home: local.Home, UserHome: c.UserHome, AuthSource: local.AuthSource, BaseConfig: filepath.Join(inputs, "config.toml"), Skills: codexenv.Selection{Mode: "clean"}, Plugins: codexenv.Selection{Mode: "clean"}, Agents: codexenv.Agents{Mode: "sync", Source: filepath.Join(inputs, "AGENTS.md")}, BundledSkills: &combo.BundledSkills, AutoSync: new(bool), Enabled: true}

	for _, selected := range combo.Resources {
		res := selected.Resource
		if res.Kind != "mcp" {
			continue
		}
		mcps[res.ID] = mcpConfiguration(*res.MCP)
		diagnostic := diagnoseMCP(res.ID, *res.MCP)
		status.MCP = append(status.MCP, diagnostic)
		if !c.ValidationOnly && res.MCP.Required && diagnostic.State != "connection_unverified" {
			return nil, status, fmt.Errorf("required MCP local prerequisites are unavailable")
		}
	}
	environmentRoot := filepath.Join(root, "environments")
	if local.Environment != nil {
		environmentRoot = local.Environment.Directory
		p.ID = local.Environment.ID
	}
	s := codexenv.Service{Managed: true, Root: environmentRoot, UserHome: c.UserHome, Now: c.Now, Validate: c.Validate}
	current, readErr := s.Read(p.ID)
	if local.Environment != nil && readErr == nil && p.AuthSource == "" {
		p.AuthSource = current.Profile.AuthSource
	}
	if readErr != nil && !errors.Is(readErr, codexenv.ErrNotFound) {
		return nil, status, readErr
	}
	// A durable combination receipt avoids repeatedly probing native processes.
	receipt := filepath.Join(inputs, "prepared.json")
	if raw, err := os.ReadFile(receipt); err == nil {
		var binding codexenv.Binding
		if json.Unmarshal(raw, &binding) == nil && binding.Directory == s.Root && binding.ID == p.ID && binding.Extensions == pin && slices.ContainsFunc(current.Snapshots, func(snapshot codexenv.Snapshot) bool { return snapshot.ID == binding.Generation }) {
			status.Ready = true
			status.Generation = binding.Generation
			return &binding, status, nil
		}
	}
	files, err := blobstore.New(filepath.Join(c.Config.Directory, "objects"))
	if err != nil {
		return nil, status, err
	}
	for _, selected := range combo.Resources {
		res := selected.Resource
		if res.Kind == "mcp" {
			continue
		}

		f, err := files.Open(res.ContentSHA256)
		if err != nil {
			return nil, status, err
		}
		raw, err := io.ReadAll(io.LimitReader(f, MaxArchiveBytes+1))
		_ = f.Close()
		if err != nil {
			return nil, status, err
		}
		if err = VerifyContent(res, raw); err != nil {
			return nil, status, err
		}
		packageDir := filepath.Join(inputs, res.ContentSHA256)
		if _, err = os.Stat(packageDir); os.IsNotExist(err) {
			if err = ExtractPackage(ctx, raw, packageDir); err != nil {
				return nil, status, err
			}
		} else if err != nil {
			return nil, status, err
		}
		item := codexenv.Item{ID: res.ID, Path: packageDir}
		if res.Kind == "skill" {
			p.Skills.Installed = append(p.Skills.Installed, item)
		} else {
			p.Plugins.Installed = append(p.Plugins.Installed, item)
		}
	}
	var encoded bytes.Buffer
	if err = toml.NewEncoder(&encoded).Encode(config); err != nil {
		return nil, status, err
	}
	if err = atomicFile(p.BaseConfig, encoded.Bytes()); err != nil {
		return nil, status, err
	}
	if err = atomicFile(p.Agents.Source, []byte(combo.AgentsMD)); err != nil {
		return nil, status, err
	}
	q := codexenv.Request{Action: "create", ID: p.ID, RequestID: "prepare-" + pin.EffectiveSHA256, Profile: &p}
	if readErr == nil {
		q.Action = "update"
		q.ExpectedRevision = current.Revision
	}
	attemptFile := filepath.Join(inputs, "attempt.json")
	var attempt uint64
	if raw, err := os.ReadFile(attemptFile); err == nil {
		if err = json.Unmarshal(raw, &attempt); err != nil {
			return nil, status, err
		}
	}
	q.RequestID = fmt.Sprintf("prepare-%s-%s-%d", pin.EffectiveSHA256, variant, attempt)
	result, err := s.Execute(ctx, codexenv.Request{Action: "reconcile", ID: p.ID, RequestID: q.RequestID})
	if errors.Is(err, codexenv.ErrNotFound) {
		result, err = s.Execute(ctx, q)
	}
	if err != nil && result.RequestState == "failed" {
		raw, _ := json.Marshal(attempt + 1)
		if writeErr := atomicFile(attemptFile, raw); writeErr != nil {
			return nil, status, writeErr
		}
	}
	if err != nil {
		return nil, status, err
	}
	if result.Status == nil || result.Status.Latest == "" {
		return nil, status, fmt.Errorf("view preparation produced no valid generation")
	}
	binding := codexenv.Binding{Extensions: pin, Directory: s.Root, ID: p.ID, Generation: result.Status.Latest}
	raw, _ := json.Marshal(binding)
	if err = atomicFile(receipt, raw); err != nil {
		return nil, status, err
	}
	status.Ready = true
	status.Generation = binding.Generation
	return &binding, status, nil
}

func mcpConfiguration(m protocol.ExtensionMCP) map[string]any {
	out := map[string]any{"enabled": true, "required": m.Required}
	if m.Transport == "stdio" {
		out["command"] = m.Command
		if len(m.Args) > 0 {
			out["args"] = m.Args
		}
		if len(m.EnvVars) > 0 {
			out["env_vars"] = m.EnvVars
		}
	} else {
		out["url"] = m.URL
		if m.BearerTokenEnvVar != "" {
			out["bearer_token_env_var"] = m.BearerTokenEnvVar
		}
		if len(m.EnvHTTPHeaders) > 0 {
			out["env_http_headers"] = m.EnvHTTPHeaders
		}
	}
	if m.StartupTimeoutSec > 0 {
		out["startup_timeout_sec"] = m.StartupTimeoutSec
	}
	if m.ToolTimeoutSec > 0 {
		out["tool_timeout_sec"] = m.ToolTimeoutSec
	}
	if len(m.EnabledTools) > 0 {
		out["enabled_tools"] = m.EnabledTools
	}
	if len(m.DisabledTools) > 0 {
		out["disabled_tools"] = m.DisabledTools
	}
	return out
}

func diagnoseMCP(id string, m protocol.ExtensionMCP) protocol.ExtensionMCPStatus {
	s := protocol.ExtensionMCPStatus{ID: id, State: "connection_unverified", Reason: "definition is prepared; no authenticated connection or tool catalog has been observed"}
	if m.Transport == "stdio" {
		if _, err := exec.LookPath(m.Command); err != nil {
			s.State = "dependency_missing"
			s.Reason = "configured command is unavailable on this execution host"
			return s
		}
	}
	refs := slices.Clone(m.EnvVars)
	if m.BearerTokenEnvVar != "" {
		refs = append(refs, m.BearerTokenEnvVar)
	}
	for _, v := range m.EnvHTTPHeaders {
		refs = append(refs, v)
	}
	for _, name := range refs {
		if os.Getenv(name) == "" {
			s.State = "authentication_missing"
			s.Reason = "a referenced local environment variable is unavailable"
			return s
		}
	}
	return s
}

// Adoptions reads native lifecycle receipts, rather than inferring adoption
// from successful downloads or view preparation. Historical references remain.
func (c *Consumer) Adoptions(ctx context.Context) ([]protocol.ExtensionAdoption, error) {
	out := []protocol.ExtensionAdoption{}
	environments, err := c.environments()
	if err != nil {
		return out, err
	}
	for _, environment := range environments {
		s := codexenv.Service{Root: environment.Directory}
		after := ""
		for {
			result, err := s.Execute(ctx, codexenv.Request{Action: "show", ID: environment.ID, After: after, Limit: 100})
			if errors.Is(err, codexenv.ErrNotFound) {
				break
			}
			if err != nil {
				return out, err
			}
			if result.Status == nil {
				break
			}
			status := result.Status
			for _, adoption := range status.Adoptions {
				if adoption.Extensions == (protocol.ExtensionExecutionPin{}) && adoption.DesiredExtensions == (protocol.ExtensionExecutionPin{}) {
					continue
				}
				row := protocol.ExtensionAdoption{Execution: adoption.Execution, Pin: adoption.Extensions, State: adoption.State, MCP: adoption.MCP}
				target := status.Profile.ExtensionPin
				if adoption.DesiredGeneration != "" {
					target = adoption.DesiredExtensions
				}
				if target != adoption.Extensions {
					row.PendingRelease = target.ReleaseID
					row.PendingReason = "awaiting_confirmed_idle_and_settled_receipts"
					if strings.HasPrefix(adoption.Execution, "run:") {
						row.PendingReason = "run_combination_pinned_for_recovery"
					}
					if adoption.State == "unknown" {
						row.PendingReason = "native_receipt_requires_reconciliation"
					}
				}
				out = append(out, row)
			}
			if !status.AdoptionsMore {
				break
			}
			after = status.AdoptionsNext
		}
	}
	slices.SortFunc(out, func(a, b protocol.ExtensionAdoption) int { return strings.Compare(a.Execution, b.Execution) })
	return out, nil
}

func (c *Consumer) Status(ctx context.Context) (protocol.ExtensionHostStatus, error) {
	out := protocol.ExtensionHostStatus{Enabled: true, Prepared: []protocol.ExtensionPrepared{}, Adoptions: []protocol.ExtensionAdoption{}}
	out.SkillExclusions = codexenv.ExclusionSummary(c.UserHome)
	for id := range c.Config.Runtimes {
		out.Runtimes = append(out.Runtimes, id)
	}
	slices.Sort(out.Runtimes)
	r, err := c.Release("")
	if err != nil && !os.IsNotExist(err) {
		return out, err
	}
	if err == nil {
		out.DownloadedRelease = r.ID
	}
	environments, err := c.environments()
	if err != nil {
		return out, err
	}
	for _, environment := range environments {
		s := codexenv.Service{Root: environment.Directory}
		status, err := s.Read(environment.ID)
		if errors.Is(err, codexenv.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		out.Prepared = append(out.Prepared, protocol.ExtensionPrepared{RuntimeID: status.Profile.ManagedRuntimeID, Pin: status.Profile.ExtensionPin, Ready: status.Latest != "", Generation: status.Latest, Reason: status.LastError})
	}
	out.Adoptions, err = c.Adoptions(ctx)
	return out, err
}

func (c *Consumer) environments() ([]codexenv.Binding, error) {
	out := []codexenv.Binding{}
	seen := map[string]bool{}
	add := func(b codexenv.Binding) {
		key := b.Directory + "/" + b.ID
		if !seen[key] {
			out = append(out, b)
			seen[key] = true
		}
	}
	for _, local := range c.Config.Runtimes {
		if local.Environment != nil {
			add(*local.Environment)
		}
	}
	views, err := os.ReadDir(filepath.Join(c.Config.Directory, "views"))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, view := range views {
		if view.IsDir() {
			add(codexenv.Binding{Directory: filepath.Join(c.Config.Directory, "views", view.Name(), "environments"), ID: "managed"})
		}
	}
	return out, nil
}
