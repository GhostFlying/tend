package extensions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDistributionSeparatesDownloadedPreparedAndAdopted(t *testing.T) {
	p := fixturePackage(t)
	identity := protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "worker"}
	r, manifest, err := NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{p.Resource}, Categories: []protocol.ExtensionBinding{{CategoryID: "implement", Choices: []protocol.ExtensionChoice{{Kind: "skill", ID: p.Resource.ID, Version: p.Resource.Version}}}}})
	if err != nil {
		t.Fatal(err)
	}
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Tend-Host") != "host" || req.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing host authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "/manifest"):
			_, _ = w.Write(manifest)
		case strings.HasSuffix(req.URL.Path, p.Resource.ContentSHA256):
			downloads++
			_, _ = w.Write(p.Content)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	consumer := &Consumer{UserHome: t.TempDir(), Config: Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]Runtime{"native": {Home: filepath.Join(root, "state")}}}, Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	a := Agent{Consumer: consumer, HTTP: server.Client(), ServerURL: server.URL, HostID: "host", Token: "fixture-token"}
	target := protocol.ExtensionTarget{Release: protocol.ExtensionReleaseRef{ID: r.ID, ManifestSHA256: Hash(manifest), ManifestSize: int64(len(manifest))}, ManifestPath: "/runner/v1/extensions/" + r.ID + "/manifest", ContentBasePath: "/runner/v1/extensions/" + r.ID + "/content"}
	if err = a.SetTarget(&target); err != nil {
		t.Fatal(err)
	}
	if err = a.sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	status := a.Status()
	if status.DownloadedRelease != r.ID || len(status.Prepared) != 0 || len(status.Adoptions) != 0 {
		t.Fatal("download falsely marked an execution adopted", status)
	}
	if err = a.PrepareCurrent(t.Context(), "native", "implement", identity); err != nil {
		t.Fatal(err)
	}
	pin, ready := a.Current("native", "implement", identity)
	if !ready {
		t.Fatal("prepared view unavailable")
	}
	status = a.Status()
	if len(status.Prepared) != 1 || len(status.Adoptions) != 0 {
		t.Fatal("preparation falsely marked adoption", status)
	}
	binding, err := a.Binding(t.Context(), pin, "native")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := binding.Pin(t.Context(), "run:fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = ref.Record(t.Context(), "ready"); err != nil {
		t.Fatal(err)
	}
	a.RefreshAdoptions(t.Context())
	status = a.Status()
	if len(status.Adoptions) != 1 || status.Adoptions[0].Pin != pin || status.Adoptions[0].State != "ready" {
		t.Fatal("missing actual lifecycle receipt", status)
	}
	if err = a.sync(t.Context()); err != nil || downloads != 1 {
		t.Fatal("unchanged release downloaded content again", downloads, err)
	}
	bad := target
	bad.Release.ManifestSHA256 = strings.Repeat("b", 64)
	if err = a.SetTarget(&bad); err != nil {
		t.Fatal(err)
	}
	if err = a.sync(t.Context()); err == nil {
		t.Fatal("corrupt manifest was accepted")
	}
	restarted := Consumer{Config: consumer.Config}
	retained, err := restarted.Release("")
	if err != nil || retained.ID != r.ID {
		t.Fatal("restart lost retained release", err)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "fixture-token") {
		t.Fatal("distribution status leaked credential")
	}
}

func TestDistributionDoesNotForwardCredentialsThroughRedirects(t *testing.T) {
	calls := 0
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer server.Close()
	a := Agent{ServerURL: server.URL, Token: "fixture-token"}
	if body, err := a.download(t.Context(), "/runner/v1/extensions/fixture/manifest"); err == nil {
		body.Close()
		t.Fatal("followed redirect")
	}
	if calls != 0 {
		t.Fatal("credentials reached another server")
	}
	for _, route := range []string{"https://other.invalid/manifest", "//other.invalid/manifest", "/runner/v1/extensions/../../private", "/runner/v1/extensions/%2e%2e/private"} {
		if body, err := a.download(t.Context(), route); err == nil {
			body.Close()
			t.Fatal("accepted unsafe route", route)
		}
	}
}
