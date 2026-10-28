package agentconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const privateSentinel = "PRIVATE_SOURCE_AND_CREDENTIAL_SENTINEL"

func fixture() Config {
	return Config{FormatVersion: 1, Runtimes: []RuntimeBinding{
		{ID: "orca", Revision: 1, Kind: OrcaWorker, CLI: "codex", ExecutableRef: "/private/" + privateSentinel, DeclaredPurposes: []Purpose{WorkerRun}, Enabled: true},
		{ID: "appserver", Revision: 2, Kind: AppServerWorker, ExecutableRef: "local-installation", DeclaredPurposes: []Purpose{WorkerRun}, Enabled: true},
		{ID: "hosted", Revision: 1, Kind: HostedManager, ExecutableRef: "local-installation", DeclaredPurposes: []Purpose{Main, IssueManager, Consultation}, Enabled: true},
	}, Agents: []AgentDefinition{
		{LocalID: "implement-a", Revision: 3, DisplayName: "墨石", RuntimeID: "orca", Categories: []string{"implementer", "planner"}, Purposes: []Purpose{WorkerRun}, Model: "gpt-model", Effort: "high", Enabled: true},
		{LocalID: "review-a", Revision: 1, DisplayName: "白鹭", RuntimeID: "appserver", Categories: []string{"reviewer"}, Purposes: []Purpose{WorkerRun}, Model: "other-model", Enabled: true},
		{LocalID: "planner-chat", Revision: 1, DisplayName: "青竹", RuntimeID: "hosted", Categories: []string{"planner"}, Purposes: []Purpose{IssueManager, Consultation}, Model: "chat-model", Effort: "medium", Enabled: true},
	}}
}

func source() SourceReference {
	return SourceReference{Kind: "local_file", Reference: "/private/" + privateSentinel + "/agents.json"}
}

func requireValid(t *testing.T, env string, cfg Config) *Definitions {
	t.Helper()
	d, err := Validate(env, cfg, source())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func publicBytes(t *testing.T, d *Definitions) ([]byte, string) {
	t.Helper()
	b, hash, err := d.CanonicalPublic()
	if err != nil {
		t.Fatal(err)
	}
	return b, hash
}

func requireError(t *testing.T, err error, code, path string) {
	t.Helper()
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != code || v.Path != path {
		t.Fatalf("error = %v, want code=%s path=%s", err, code, path)
	}
	if strings.Contains(err.Error(), privateSentinel) || strings.Contains(err.Error(), "/private/") {
		t.Fatalf("private content in error: %s", err)
	}
}

func TestStrictDecode(t *testing.T) {
	raw, err := json.Marshal(fixture())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Decode(raw)
	if err != nil || !reflect.DeepEqual(cfg, fixture()) {
		t.Fatalf("round trip changed requested source: %v", err)
	}
	replace := func(old, next string) []byte { return bytes.Replace(raw, []byte(old), []byte(next), 1) }
	for _, tt := range []struct {
		name, code, path string
		data             []byte
	}{
		{"unknown-secret-key", "unknown_field", "$", []byte(`{"` + privateSentinel + `":"secret","format_version":1}`)},
		{"source-kind-cannot-be-claimed-in-config", "unknown_field", "$", []byte(`{"format_version":1,"source_kind":"inline"}`)},
		{"unknown-nested", "unknown_field", "$.runtimes[0]", replace(`"runtime_id":"orca"`, `"runtime_id":"orca","native_config":"`+privateSentinel+`"`)},
		{"case-variant", "unknown_field", "$.runtimes[0]", replace(`"runtime_id":"orca"`, `"Runtime_ID":"orca"`)},
		{"instructions-not-yet-supported", "unknown_field", "$.agents[0]", replace(`"local_id":"implement-a"`, `"local_id":"implement-a","instructions":"`+privateSentinel+`"`)},
		{"role-instructions-not-yet-supported", "unknown_field", "$.agents[0]", replace(`"local_id":"implement-a"`, `"local_id":"implement-a","role_instructions":"`+privateSentinel+`"`)},
		{"duplicate-root", "duplicate_field", "$.format_version", replace(`"format_version":1`, `"format_version":1,"format_version":1`)},
		{"duplicate-nested", "duplicate_field", "$.runtimes[0].runtime_id", replace(`"runtime_id":"orca"`, `"runtime_id":"orca","runtime_id":"other"`)},
		{"root-null", "invalid_shape", "$", []byte(`null`)},
		{"array-null", "invalid_shape", "$.runtimes", []byte(`{"format_version":1,"runtimes":null}`)},
		{"element-null", "invalid_shape", "$.agents[0]", []byte(`{"format_version":1,"runtimes":[],"agents":[null]}`)},
		{"model-null", "invalid_shape", "$.agents[0].model", replace(`"model":"gpt-model"`, `"model":null`)},
		{"string-bool", "invalid_shape", "$.runtimes[0].enabled", replace(`"enabled":true`, `"enabled":"true"`)},
		{"fractional-version", "invalid_shape", "$.format_version", replace(`"format_version":1`, `"format_version":1.0`)},
		{"overflow", "invalid_shape", "$.format_version", []byte(`{"format_version":18446744073709551616}`)},
		{"negative", "invalid_shape", "$.format_version", []byte(`{"format_version":-1}`)},
		{"trailing-object", "invalid_json", "$", append(slices.Clone(raw), []byte(`{}`)...)},
		{"trailing-secret", "invalid_json", "$", append(slices.Clone(raw), []byte(privateSentinel)...)},
		{"invalid-utf8", "invalid_json", "$", []byte{'{', '"', 0xff, '"', ':', '1', '}'}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(tt.data)
			requireError(t, err, tt.code, tt.path)
		})
	}
}

func TestTypedValidationRejectsUnsupportedDefinitions(t *testing.T) {
	for _, tt := range []struct {
		name, code, path string
		change           func(*Config)
	}{
		{"version", "unsupported_format", "$.format_version", func(c *Config) { c.FormatVersion = 2 }},
		{"duplicate-runtime", "duplicate_id", "$.runtimes[3].runtime_id", func(c *Config) { c.Runtimes = append(c.Runtimes, c.Runtimes[0]) }},
		{"duplicate-agent-different-runtime", "duplicate_id", "$.agents[3].local_id", func(c *Config) {
			a := c.Agents[0]
			a.RuntimeID = "appserver"
			c.Agents = append(c.Agents, a)
		}},
		{"empty-revision", "required", "$.agents[0].revision", func(c *Config) { c.Agents[0].Revision = 0 }},
		{"invalid-id", "invalid_id", "$.agents[0].local_id", func(c *Config) { c.Agents[0].LocalID = "/private/" + privateSentinel }},
		{"missing-runtime", "missing_runtime", "$.agents[0].runtime_id", func(c *Config) { c.Agents[0].RuntimeID = privateSentinel }},
		{"unknown-runtime-kind", "unsupported_runtime", "$.runtimes[0].kind", func(c *Config) { c.Runtimes[0].Kind = RuntimeKind(privateSentinel) }},
		{"orca-claude", "unsupported_cli", "$.runtimes[0].cli", func(c *Config) { c.Runtimes[0].CLI = "claude" }},
		{"appserver-cli-claim", "unsupported_cli", "$.runtimes[1].cli", func(c *Config) { c.Runtimes[1].CLI = "arbitrary-cli" }},
		{"effort-only-orca", "unsupported_effort_only", "$.agents[0].effort", func(c *Config) { c.Agents[0].Model = "" }},
		{"missing-model", "model_required", "$.agents[1].model", func(c *Config) { c.Agents[1].Model = "" }},
		{"whitespace-model", "model_required", "$.agents[0].model", func(c *Config) { c.Agents[0].Model = " gpt-model" }},
		{"unsupported-effort", "unsupported_effort", "$.agents[0].effort", func(c *Config) { c.Agents[0].Effort = privateSentinel }},
		{"skills", "unsupported_option", "$.agents[0].skills", func(c *Config) { c.Agents[0].Skills = []string{"/private/" + privateSentinel} }},
		{"mcp", "unsupported_option", "$.agents[0].mcp_servers", func(c *Config) { c.Agents[0].MCPServers = []string{privateSentinel} }},
		{"empty-category", "required", "$.agents[0].categories", func(c *Config) { c.Agents[0].Categories = nil }},
		{"duplicate-category", "duplicate_value", "$.agents[0].categories[1]", func(c *Config) { c.Agents[0].Categories = []string{"planner", "planner"} }},
		{"worker-cannot-consult", "unsupported_purpose", "$.agents[0].purposes[0]", func(c *Config) { c.Agents[0].Purposes = []Purpose{Consultation} }},
		{"hosted-cannot-execute", "unsupported_purpose", "$.runtimes[2].declared_purposes[0]", func(c *Config) { c.Runtimes[2].DeclaredPurposes = []Purpose{WorkerRun} }},
		{"undeclared-purpose", "purpose_mismatch", "$.agents[2].purposes[1]", func(c *Config) { c.Runtimes[2].DeclaredPurposes = []Purpose{IssueManager} }},
		{"duplicate-purpose", "duplicate_value", "$.agents[0].purposes[1]", func(c *Config) { c.Agents[0].Purposes = []Purpose{WorkerRun, WorkerRun} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := fixture()
			tt.change(&c)
			_, err := Validate("env-a", c, source())
			requireError(t, err, tt.code, tt.path)
		})
	}
	// Both launcher-supported Orca agents can be declared, but not called live.
	c := fixture()
	c.Runtimes[0].CLI = "traex"
	requireValid(t, "env-a", c)
}

func TestIdentityAndCategoryMembership(t *testing.T) {
	c := fixture()
	other := c.Agents[0]
	other.LocalID = "implement-b"
	other.RuntimeID = "appserver"
	c.Agents = append(c.Agents, other)
	d := requireValid(t, "env-a", c)
	identity, ok := d.Identity("implement-a")
	if !ok || identity != (Identity{ExecutionEnvironmentID: "env-a", LocalID: "implement-a"}) {
		t.Fatalf("identity = %+v, %v", identity, ok)
	}
	if _, found := d.Identity("absent"); found {
		t.Fatal("unregistered local ID found")
	}
	c.Agents[0].RuntimeID = "appserver"
	changed, _ := requireValid(t, "env-a", c).Identity("implement-a")
	if changed != identity {
		t.Fatal("runtime binding change changed stable Agent identity")
	}
	elsewhere, _ := requireValid(t, "env-b", c).Identity("implement-a")
	identities := map[Identity]bool{identity: true, elsewhere: true}
	if len(identities) != 2 {
		t.Fatal("same local ID collided across execution environments")
	}
}

func TestPublicProjectionCanonicalAndPrivate(t *testing.T) {
	c := fixture()
	d := requireValid(t, "env-a", c)
	b, digest := publicBytes(t, d)
	if bytes.Contains(b, []byte(privateSentinel)) || bytes.Contains(b, []byte("/private/")) {
		t.Fatal("private reference escaped into projection")
	}
	if d.Source() != source() {
		t.Fatal("typed local source was not retained independently")
	}
	slices.Reverse(c.Runtimes)
	slices.Reverse(c.Agents)
	for i := range c.Runtimes {
		slices.Reverse(c.Runtimes[i].DeclaredPurposes)
	}
	for i := range c.Agents {
		slices.Reverse(c.Agents[i].Categories)
		slices.Reverse(c.Agents[i].Purposes)
	}
	b2, digest2 := publicBytes(t, requireValid(t, "env-a", c))
	if !bytes.Equal(b, b2) || digest != digest2 {
		t.Fatal("equivalent set ordering changed canonical bytes or hash")
	}
	c.Runtimes[0].ExecutableRef = "/another/private/installation"
	privateSource := SourceReference{Kind: LocalFile, Reference: privateSentinel + "-different-source"}
	d2, err := Validate("env-a", c, privateSource)
	if err != nil {
		t.Fatal(err)
	}
	b2, digest2 = publicBytes(t, d2)
	if !bytes.Equal(b, b2) || digest != digest2 {
		t.Fatal("public hash exposed private source or installation changes")
	}
	c.Agents[0].Model = "new-requested-model"
	_, digest2 = publicBytes(t, requireValid(t, "env-a", c))
	if digest == digest2 {
		t.Fatal("requested model change missing from digest")
	}
	sum := sha256.Sum256(append([]byte("tend-agent-public-v1\n"), b...))
	if digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("public digest format/domain changed")
	}
	var projection map[string]any
	if err := json.Unmarshal(b, &projection); err != nil {
		t.Fatal(err)
	}
	for _, r := range projection["runtimes"].([]any) {
		m := r.(map[string]any)
		if _, exists := m["executable_ref"]; exists {
			t.Fatal("private executable field exists in public schema")
		}
	}
	for _, forbidden := range []string{"source", "credentials", "instructions", "raw_config", "private_digest", "skills", "mcp_servers"} {
		if bytes.Contains(b, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("unsupported/private field present: %s", forbidden)
		}
	}
}

func TestDefinitionsAndProjectionsAreDetached(t *testing.T) {
	c := fixture()
	d := requireValid(t, "env-a", c)
	before, hash := publicBytes(t, d)
	c.Runtimes[0].DeclaredPurposes[0] = Consultation
	c.Agents[0].Categories[0] = "mutated"
	c.Agents[0].Purposes[0] = Consultation
	c.Agents[0].Model = "mutated"
	p := d.Public()
	p.Agents[0].Categories[0] = "mutated-again"
	p.Agents[0].Purposes[0] = Consultation
	p.Runtimes[0].DeclaredPurposes[0] = Consultation
	p.Agents[0].Availability.Available = true
	p.SourceKind = Inline
	after, hash2 := publicBytes(t, d)
	if !bytes.Equal(before, after) || hash != hash2 {
		t.Fatal("caller mutation changed validated configuration")
	}
}

func TestConfigurationNeverProvesAvailabilityOrActualModel(t *testing.T) {
	c := fixture()
	p := requireValid(t, "env-a", c).Public()
	for _, r := range p.Runtimes {
		if r.Availability.Available || r.Registration != "staged" {
			t.Fatal("configured binding was called verified/available")
		}
		if r.Kind == HostedManager && r.Availability.Reason != "isolation_not_verified" {
			t.Fatal("hosted isolation gate absent")
		}
	}
	for _, a := range p.Agents {
		if a.Availability.Available || a.Registration != "staged" {
			t.Fatal("configured Agent was called verified/available")
		}
		if a.Identity.LocalID == "review-a" && a.RequestedEffort != "" {
			t.Fatal("omitted effort inherited another Agent's selection")
		}
	}
	c.Agents[0].Enabled = false
	c.Runtimes[1].Enabled = false
	for _, a := range requireValid(t, "env-a", c).Public().Agents {
		if a.Identity.LocalID == "implement-a" && a.Availability.Reason != "definition_disabled" {
			t.Fatal("disabled Agent reason lost")
		}
		if a.Identity.LocalID == "review-a" && a.Availability.Reason != "binding_disabled" {
			t.Fatal("disabled binding reason lost")
		}
	}
	for _, key := range []string{"available", "actual_model", "live_verified"} {
		raw := []byte(`{"format_version":1,"` + key + `":true}`)
		_, err := Decode(raw)
		requireError(t, err, "unknown_field", "$")
	}
}

func TestEmptyInventoryAndUntrustedSourceMetadata(t *testing.T) {
	d := requireValid(t, "env-a", Config{FormatVersion: 1})
	b, _ := publicBytes(t, d)
	const empty = `{"format_version":1,"execution_environment_id":"env-a","source_kind":"local_file","runtimes":[],"agents":[]}`
	if string(b) != empty {
		t.Fatalf("empty declaration canonical bytes = %s", b)
	}
	if _, ok := d.Identity("absent"); ok {
		t.Fatal("empty declaration created an identity")
	}
	_, err := Validate("/private/"+privateSentinel, fixture(), source())
	requireError(t, err, "invalid_id", "$environment_id")
	_, err = Validate("env-a", fixture(), SourceReference{Kind: SourceKind(privateSentinel), Reference: "/private/" + privateSentinel})
	requireError(t, err, "invalid_source", "$source.kind")
	_, err = Validate("env-a", fixture(), SourceReference{Kind: LocalFile})
	requireError(t, err, "required", "$source.reference")
}

func TestSourceKindIsPublicAndChangesDigest(t *testing.T) {
	c := fixture()
	fileSource := source()
	d := requireValid(t, "env-a", c)
	fileBytes, fileDigest := publicBytes(t, d)
	inlineSource := fileSource
	inlineSource.Kind = Inline
	inline, err := Validate("env-a", c, inlineSource)
	if err != nil {
		t.Fatal(err)
	}
	inlineBytes, inlineDigest := publicBytes(t, inline)
	if bytes.Equal(fileBytes, inlineBytes) || fileDigest == inlineDigest {
		t.Fatal("public source-kind change missing from canonical bytes or hash")
	}
	if d.Public().SourceKind != LocalFile || inline.Public().SourceKind != Inline {
		t.Fatal("validated typed source kind was not projected")
	}
	for _, v := range []*Definitions{d, inline} {
		b, _ := publicBytes(t, v)
		if bytes.Contains(b, []byte(privateSentinel)) || bytes.Contains(b, []byte("/private/")) {
			t.Fatal("projecting kind also leaked private source reference")
		}
		for _, a := range v.Public().Agents {
			if a.Availability.Available || a.Registration != "staged" {
				t.Fatal("source kind was treated as runtime verification")
			}
		}
	}
	fileID, _ := d.Identity("implement-a")
	inlineID, _ := inline.Identity("implement-a")
	if fileID != inlineID || !reflect.DeepEqual(d.Public().Agents, inline.Public().Agents) {
		t.Fatal("source kind change changed identity or Agent requests/availability")
	}
}
