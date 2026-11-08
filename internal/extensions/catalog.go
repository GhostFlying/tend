// Package extensions owns portable Codex content and deterministic composition.
// It never accesses the Tend ledger, selects models, or grants role permissions.
package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

var resourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@+-]{0,159}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func Hash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

func ValidHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}

func key(kind, id string) string                      { return kind + ":" + id }
func resourceKey(r protocol.ExtensionResource) string { return key(r.Kind, r.ID) + ":" + r.Version }

func ValidateResource(r protocol.ExtensionResource) error {
	if !slices.Contains([]string{"skill", "plugin", "mcp"}, r.Kind) || !resourceID.MatchString(r.ID) || !resourceID.MatchString(r.Version) {
		return fmt.Errorf("invalid resource kind, identity or version")
	}
	if r.Kind == "mcp" {
		if r.ContentSHA256 != "" || r.Size != 0 || r.MCP == nil {
			return fmt.Errorf("MCP requires a definition, not package content")
		}
		return ValidateMCP(*r.MCP)
	}
	if r.MCP != nil || !ValidHash(r.ContentSHA256) || r.Size <= 0 || r.Size > MaxArchiveBytes {
		return fmt.Errorf("invalid package content reference")
	}
	if err := ValidateSource(r.Source); err != nil {
		return err
	}
	return nil
}

func ValidateSource(s protocol.ExtensionSource) error {
	if !slices.Contains([]string{"local", "git", "npm"}, s.Type) {
		return fmt.Errorf("unsupported package source")
	}
	if s.Subdir != "" && (path.IsAbs(s.Subdir) || path.Clean(s.Subdir) != s.Subdir || s.Subdir == ".." || strings.HasPrefix(s.Subdir, "../") || strings.Contains(s.Subdir, `\`)) {
		return fmt.Errorf("source subdirectory must stay inside the package")
	}
	if s.Type == "git" && (s.URL == "" || s.Ref == "" || !commitID.MatchString(s.Commit)) {
		return fmt.Errorf("git source requires a resolved commit and explicit ref")
	}
	if s.Type == "npm" && (s.Package == "" || s.Version == "" || s.Integrity == "" || strings.ContainsAny(s.Version, "*^~<>= ")) {
		return fmt.Errorf("npm source requires an exact version and integrity")
	}
	if s.Type == "local" && (s.URL != "" || s.Ref != "" || s.Commit != "" || s.Subdir != "" || s.Package != "" || s.Version != "" || s.Integrity != "") {
		return fmt.Errorf("local provenance must not publish a host path")
	}
	if s.Type == "git" && (s.Package != "" || s.Version != "" || s.Integrity != "") {
		return fmt.Errorf("unexpected git provenance fields")
	}
	if s.Type == "npm" && (s.URL != "" || s.Ref != "" || s.Commit != "") {
		return fmt.Errorf("unexpected npm provenance fields")
	}
	if s.Type == "git" {
		u, err := url.Parse(s.URL)
		if err != nil || u.Host == "" || !slices.Contains([]string{"https", "ssh"}, u.Scheme) {
			return fmt.Errorf("git provenance requires an HTTPS or SSH repository URL")
		}
	}
	if strings.Contains(s.URL, "://") {
		u, err := url.Parse(s.URL)
		if err != nil || u.User != nil && !(u.Scheme == "ssh" && u.User.String() == "git") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("source URL must not contain credentials or query parameters")
		}
	}
	return nil
}

func ValidateMCP(m protocol.ExtensionMCP) error {
	if m.Transport == "stdio" {
		if m.Command == "" || strings.ContainsAny(m.Command, `/\`) || m.URL != "" || len(m.EnvHTTPHeaders) != 0 || m.BearerTokenEnvVar != "" {
			return fmt.Errorf("stdio MCP requires a PATH command and local environment references")
		}
	} else if m.Transport == "http" {
		u, err := url.Parse(m.URL)
		if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https"}, u.Scheme) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || m.Command != "" || len(m.Args) != 0 || len(m.EnvVars) != 0 {
			return fmt.Errorf("HTTP MCP requires a credential-free endpoint")
		}
	} else {
		return fmt.Errorf("MCP transport must be stdio or http")
	}
	for _, name := range m.EnvVars {
		if !envName.MatchString(name) {
			return fmt.Errorf("invalid MCP environment reference")
		}
	}
	for header, name := range m.EnvHTTPHeaders {
		if strings.TrimSpace(header) == "" || strings.ContainsAny(header, "\r\n:") || !envName.MatchString(name) {
			return fmt.Errorf("invalid MCP header environment reference")
		}
	}
	if m.BearerTokenEnvVar != "" && !envName.MatchString(m.BearerTokenEnvVar) {
		return fmt.Errorf("invalid bearer token environment reference")
	}
	if m.StartupTimeoutSec < 0 || m.StartupTimeoutSec > 600 || m.ToolTimeoutSec < 0 || m.ToolTimeoutSec > 3600 {
		return fmt.Errorf("MCP timeout is outside the supported range")
	}
	return nil
}

func ValidateBinding(b protocol.ExtensionBinding) error {
	category := b.CategoryID != ""
	if category && (!protocol.AgentInventoryID(b.CategoryID) || b.Agent != (protocol.AgentIdentity{})) || !category && (!protocol.AgentInventoryID(b.Agent.ExecutionEnvironmentID) || !protocol.AgentInventoryID(b.Agent.LocalID)) || b.Revision < 0 || len(b.AgentsMD) > 1<<20 {
		return fmt.Errorf("invalid category or Agent binding")
	}
	seen := map[string]bool{}
	for _, c := range b.Choices {
		k := key(c.Kind, c.ID)
		if !slices.Contains([]string{"skill", "plugin", "mcp"}, c.Kind) || !resourceID.MatchString(c.ID) || seen[k] || (c.Enabled == nil || *c.Enabled || c.Version != "") && !resourceID.MatchString(c.Version) {
			return fmt.Errorf("invalid or duplicate resource choice")
		}
		seen[k] = true
	}
	return nil
}

// CanonicalCatalog detaches input and sorts sets, without touching instruction text.
func CanonicalCatalog(in protocol.ExtensionCatalog) (protocol.ExtensionCatalog, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return in, err
	}
	var c protocol.ExtensionCatalog
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if c.Revision < 0 {
		return c, fmt.Errorf("invalid catalog revision")
	}
	if c.Resources == nil {
		c.Resources = []protocol.ExtensionResource{}
	}
	if c.Categories == nil {
		c.Categories = []protocol.ExtensionBinding{}
	}
	if c.Agents == nil {
		c.Agents = []protocol.ExtensionBinding{}
	}
	seen := map[string]bool{}
	for _, r := range c.Resources {
		if err = ValidateResource(r); err != nil {
			return c, err
		}
		k := resourceKey(r)
		if seen[k] {
			return c, fmt.Errorf("duplicate resource version")
		}
		seen[k] = true
	}
	slices.SortFunc(c.Resources, func(a, b protocol.ExtensionResource) int { return strings.Compare(resourceKey(a), resourceKey(b)) })
	seen = map[string]bool{}
	for scope, bs := range [][]protocol.ExtensionBinding{c.Categories, c.Agents} {
		for i := range bs {
			b := &bs[i]
			if (scope == 0) != (b.CategoryID != "") {
				return c, fmt.Errorf("binding is in the wrong scope")
			}
			if err = ValidateBinding(*b); err != nil {
				return c, err
			}
			k := bindingKey(*b)
			if seen[k] {
				return c, fmt.Errorf("duplicate binding")
			}
			seen[k] = true
			if b.Choices == nil {
				b.Choices = []protocol.ExtensionChoice{}
			}
			slices.SortFunc(b.Choices, func(a, b protocol.ExtensionChoice) int { return strings.Compare(key(a.Kind, a.ID), key(b.Kind, b.ID)) })
		}
		slices.SortFunc(bs, func(a, b protocol.ExtensionBinding) int { return strings.Compare(bindingKey(a), bindingKey(b)) })
	}
	return c, nil
}

func bindingKey(b protocol.ExtensionBinding) string {
	if b.CategoryID != "" {
		return "category:" + b.CategoryID
	}
	return "agent:" + b.Agent.ExecutionEnvironmentID + "/" + b.Agent.LocalID
}

func NewRelease(c protocol.ExtensionCatalog) (protocol.ExtensionRelease, []byte, error) {
	c, err := CanonicalCatalog(c)
	if err != nil {
		return protocol.ExtensionRelease{}, nil, err
	}
	r := protocol.ExtensionRelease{CompositionVersion: protocol.ExtensionCompositionVersion, Catalog: c}
	body, _ := json.Marshal(r)
	r.ID = "ext-" + Hash(body)
	body, err = json.Marshal(r)
	if len(body) > 4<<20 {
		return protocol.ExtensionRelease{}, nil, fmt.Errorf("release manifest exceeds 4 MiB")
	}
	return r, body, err
}

func ValidateRelease(r protocol.ExtensionRelease) error {
	if r.CompositionVersion != protocol.ExtensionCompositionVersion {
		return fmt.Errorf("unsupported extension composition version")
	}
	want, _, err := NewRelease(r.Catalog)
	if err != nil {
		return err
	}
	if want.ID != r.ID {
		return fmt.Errorf("extension release identity mismatch")
	}
	return nil
}

func Resolve(r protocol.ExtensionRelease, category string, agent protocol.AgentIdentity) (protocol.ExtensionCombination, error) {
	out := protocol.ExtensionCombination{Resources: []protocol.ResolvedExtension{}, Disabled: []protocol.ExtensionChoice{}, BundledSkills: true}
	if !protocol.AgentInventoryID(category) || !protocol.AgentInventoryID(agent.ExecutionEnvironmentID) || !protocol.AgentInventoryID(agent.LocalID) {
		return out, fmt.Errorf("one category and full Agent identity are required")
	}
	out.Pin = protocol.ExtensionExecutionPin{ReleaseID: r.ID, CategoryID: category, Agent: agent}
	chosen := map[string]protocol.ExtensionChoice{}
	origins := map[string]string{}
	texts := []string{}
	for _, layer := range []struct {
		bindings []protocol.ExtensionBinding
		origin   string
	}{{r.Catalog.Categories, "category"}, {r.Catalog.Agents, "agent"}} {
		for _, b := range layer.bindings {
			if layer.origin == "category" && b.CategoryID != category || layer.origin == "agent" && b.Agent != agent {
				continue
			}
			if layer.origin == "category" {
				out.Pin.CategoryRevision = b.Revision
			} else {
				out.Pin.AgentRevision = b.Revision
			}
			if b.BundledSkills != nil {
				out.BundledSkills = *b.BundledSkills
			}
			if strings.TrimSpace(b.AgentsMD) != "" {
				texts = append(texts, b.AgentsMD)
			}
			for _, v := range b.Choices {
				k := key(v.Kind, v.ID)
				chosen[k] = v
				origins[k] = layer.origin
			}
		}
	}
	out.AgentsMD = strings.Join(texts, "\n\n")
	resources := map[string]protocol.ExtensionResource{}
	for _, res := range r.Catalog.Resources {
		resources[resourceKey(res)] = res
	}
	keys := make([]string, 0, len(chosen))
	for k := range chosen {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := chosen[k]
		if v.Enabled != nil && !*v.Enabled {
			out.Disabled = append(out.Disabled, v)
			continue
		}
		res, ok := resources[k+":"+v.Version]
		if !ok {
			return out, fmt.Errorf("selected resource is not installed: %s@%s", k, v.Version)
		}
		out.Resources = append(out.Resources, protocol.ResolvedExtension{Resource: res, Origin: origins[k]})
	}
	body, _ := json.Marshal(out)
	out.Pin.EffectiveSHA256 = Hash(body)
	return out, nil
}

func VerifyCombination(r protocol.ExtensionRelease, pin protocol.ExtensionExecutionPin) (protocol.ExtensionCombination, error) {
	c, err := Resolve(r, pin.CategoryID, pin.Agent)
	if err != nil {
		return c, err
	}
	if c.Pin != pin {
		return c, fmt.Errorf("execution combination differs from the retained pin")
	}
	return c, nil
}
