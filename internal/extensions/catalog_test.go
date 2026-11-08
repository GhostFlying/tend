package extensions

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/protocol"
)

func fixturePackage(t *testing.T) protocol.ExtensionPackage {
	t.Helper()
	root := t.TempDir()
	for name, text := range map[string]string{"SKILL.md": "---\nname: fixture\ndescription: A fixture\n---\nmanaged body", "scripts/run.sh": "#!/bin/sh\nprintf managed", "assets/data.txt": "complete asset"} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0700); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Import(t.Context(), protocol.ExtensionImport{Kind: "skill", ID: "fixture", Version: "1", Path: root, Source: protocol.ExtensionSource{Type: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCompositionUsesOneCategoryAndFullAgentIdentity(t *testing.T) {
	r := fixturePackage(t).Resource
	v2 := r
	v2.Version = "2"
	a := protocol.AgentIdentity{ExecutionEnvironmentID: "host-a", LocalID: "worker"}
	other := protocol.AgentIdentity{ExecutionEnvironmentID: "host-b", LocalID: "worker"}
	c := protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{v2, r}, Categories: []protocol.ExtensionBinding{{CategoryID: "implement", Revision: 1, AgentsMD: "category instructions", Choices: []protocol.ExtensionChoice{{Kind: r.Kind, ID: r.ID, Version: "1"}}}, {CategoryID: "review", Revision: 4, AgentsMD: "review only", Choices: []protocol.ExtensionChoice{}}}, Agents: []protocol.ExtensionBinding{{Agent: a, Revision: 2, AgentsMD: "agent instructions", Choices: []protocol.ExtensionChoice{{Kind: r.Kind, ID: r.ID, Version: "2"}}}}}
	release, raw, err := NewRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	combo, err := Resolve(release, "implement", a)
	if err != nil {
		t.Fatal(err)
	}
	if len(combo.Resources) != 1 || combo.Resources[0].Resource.Version != "2" || combo.Resources[0].Origin != "agent" || combo.AgentsMD != "category instructions\n\nagent instructions" || combo.Pin.CategoryRevision != 1 || combo.Pin.AgentRevision != 2 {
		t.Fatalf("wrong composition: %+v", combo)
	}
	peer, err := Resolve(release, "implement", other)
	if err != nil || len(peer.Resources) != 1 || peer.Resources[0].Resource.Version != "1" || peer.AgentsMD != "category instructions" {
		t.Fatalf("identity alias: %+v %v", peer, err)
	}
	review, err := Resolve(release, "review", other)
	if err != nil || len(review.Resources) != 0 || review.AgentsMD != "review only" {
		t.Fatalf("merged unrelated category: %+v %v", review, err)
	}
	slices.Reverse(c.Resources)
	second, again, err := NewRelease(c)
	if err != nil || second.ID != release.ID || !bytes.Equal(again, raw) {
		t.Fatal("set ordering changed release", err)
	}
	pin := combo.Pin
	pin.CategoryID = "review"
	if _, err = VerifyCombination(release, pin); err == nil {
		t.Fatal("accepted changed execution category")
	}
	c.Agents[0].Choices[0] = protocol.ExtensionChoice{Kind: r.Kind, ID: r.ID, Enabled: new(bool)}
	c.Agents[0].BundledSkills = new(bool)
	release, _, err = NewRelease(c)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := Resolve(release, "implement", a)
	if err != nil || len(disabled.Resources) != 0 || len(disabled.Disabled) != 1 || disabled.BundledSkills {
		t.Fatal("explicit disable was lost", disabled, err)
	}
	if combo.Pin.AgentRevision != 2 || combo.Resources[0].Resource.Version != "2" {
		t.Fatal("resolver mutated previous combination")
	}
}

func TestRejectsUnsafeDefinitionsAndScopes(t *testing.T) {
	for _, m := range []protocol.ExtensionMCP{{Transport: "http", URL: "https://example.test/mcp?token=secret"}, {Transport: "http", URL: "https://user:password@example.test"}, {Transport: "stdio", Command: "sh", EnvVars: []string{"TOKEN=secret"}}, {Transport: "stdio", Command: "/private/host/tool"}, {Transport: "http", URL: "https://example.test", EnvHTTPHeaders: map[string]string{"Authorization": "secret-value!"}}} {
		if ValidateMCP(m) == nil {
			t.Fatalf("accepted unsafe MCP %+v", m)
		}
	}
	m := protocol.ExtensionMCP{Transport: "http", URL: "https://example.test/mcp", BearerTokenEnvVar: "TEST_EXTENSION_TOKEN"}
	if err := ValidateMCP(m); err != nil {
		t.Fatal(err)
	}
	c := protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{Agent: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "worker"}}}}
	if _, err := CanonicalCatalog(c); err == nil {
		t.Fatal("accepted Agent in category collection")
	}
	for _, s := range []protocol.ExtensionSource{{Type: "local", URL: "private-path"}, {Type: "git", URL: "file:///private/repo", Ref: "main", Commit: strings.Repeat("a", 40)}, {Type: "npm", Package: "package", Version: "^1.0.0", Integrity: "checksum"}, {Type: "local", Subdir: "../escape"}} {
		if ValidateSource(s) == nil {
			t.Fatalf("accepted unsafe source %+v", s)
		}
	}
}

func TestArchiveMaterializesCompletePackageAndRejectsEscape(t *testing.T) {
	p := fixturePackage(t)
	entries, err := ReadPackage(t.Context(), p.Content)
	if err != nil || len(entries) != 3 {
		t.Fatal(entries, err)
	}
	dest := filepath.Join(t.TempDir(), "package")
	if err = ExtractPackage(t.Context(), p.Content, dest); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dest, "assets", "data.txt")); err != nil || string(raw) != "complete asset" {
		t.Fatal("missing asset", err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "source"), []byte("body"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("source", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	raw, err := Pack(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	unpacked := filepath.Join(t.TempDir(), "unpacked")
	if err = ExtractPackage(t.Context(), raw, unpacked); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(unpacked, "alias"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("link was not materialized", err)
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err = Pack(t.Context(), root); err == nil {
		t.Fatal("accepted external link")
	}
	for _, name := range []string{"../escape", "/absolute", "a/../escape", `a\escape`} {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		if err = tw.WriteHeader(&tar.Header{Name: name, Size: 1, Mode: 0600, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte("x"))
		_ = tw.Close()
		if _, err = ReadPackage(t.Context(), b.Bytes()); err == nil {
			t.Fatal("accepted path", name)
		}
	}
}

func TestConsumerRetainsPreviousReleaseOnIncompleteDownload(t *testing.T) {
	p := fixturePackage(t)
	r, _, err := NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{p.Resource}})
	if err != nil {
		t.Fatal(err)
	}
	c := Consumer{Config: Configuration{Directory: t.TempDir()}}
	source := func(context.Context, protocol.ExtensionResource) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(p.Content)), nil
	}
	if err = c.Receive(t.Context(), r, source); err != nil {
		t.Fatal(err)
	}
	second := p.Resource
	second.Version = "2"
	second.ContentSHA256 = strings.Repeat("b", 64)
	next, _, err := NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{second}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Receive(t.Context(), next, source); err == nil {
		t.Fatal("accepted corrupted download")
	}
	recovered := Consumer{Config: c.Config}
	current, err := recovered.Release("")
	if err != nil || current.ID != r.ID {
		t.Fatal("lost previous release", err)
	}
}

func TestConsumerBuildsManagedMCPAndOrderedInstructions(t *testing.T) {
	p := fixturePackage(t)
	a := protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "worker"}
	mcp := protocol.ExtensionResource{Kind: "mcp", ID: "tools", Version: "1", MCP: &protocol.ExtensionMCP{Transport: "http", URL: "https://example.test/mcp", BearerTokenEnvVar: "TEST_TEND_EXTENSION_CREDENTIAL"}}
	r, _, err := NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{p.Resource, mcp}, Categories: []protocol.ExtensionBinding{{CategoryID: "implement", Revision: 1, AgentsMD: "first", Choices: []protocol.ExtensionChoice{{Kind: "skill", ID: "fixture", Version: "1"}, {Kind: "mcp", ID: "tools", Version: "1"}}}}, Agents: []protocol.ExtensionBinding{{Agent: a, Revision: 1, AgentsMD: "second"}}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	c := Consumer{UserHome: filepath.Join(root, "user"), Config: Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]Runtime{"native": {Home: filepath.Join(root, "runtime")}}}, Validate: func(_ context.Context, _ codexenv.Profile, stage string) error {
		raw, err := os.ReadFile(filepath.Join(stage, "AGENTS.md"))
		if err != nil || string(raw) != "first\n\nsecond" {
			return fmtError("instruction ordering")
		}
		raw, err = os.ReadFile(filepath.Join(stage, "config.toml"))
		if err != nil || !bytes.Contains(raw, []byte("TEST_TEND_EXTENSION_CREDENTIAL")) {
			return fmtError("MCP definition missing")
		}
		return nil
	}}
	if err = os.MkdirAll(c.UserHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err = c.Receive(t.Context(), r, func(context.Context, protocol.ExtensionResource) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(p.Content)), nil
	}); err != nil {
		t.Fatal(err)
	}
	combo, err := Resolve(r, "implement", a)
	if err != nil {
		t.Fatal(err)
	}
	binding, status, err := c.Prepare(t.Context(), combo.Pin, "native")
	if err != nil || !status.Ready || binding.Generation == "" || len(status.MCP) != 1 || status.MCP[0].State != "authentication_missing" {
		t.Fatalf("prepare %+v %+v %v", binding, status, err)
	}
	raw, _ := json.Marshal(status)
	if bytes.Contains(raw, []byte("secret-value")) {
		t.Fatal("credential leaked")
	}
}

type fmtError string

func (e fmtError) Error() string { return string(e) }
