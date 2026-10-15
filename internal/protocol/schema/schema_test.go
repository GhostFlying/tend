package schema

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type color string

type base struct {
	ID string `json:"id"`
}

type sample struct {
	base
	Name    string           `json:"name"`
	Color   color            `json:"color"`
	Note    string           `json:"note,omitempty"`
	Tags    []string         `json:"tags"`
	Attrs   map[string]int64 `json:"attrs,omitempty"`
	Raw     json.RawMessage  `json:"raw"`
	Child   *sample          `json:"child,omitempty"`
	Parent  *sample          `json:"parent"`
	Skipped string           `json:"-"`
	Plain   bool
	Nested  struct{ X int } `json:"nested"`
}

func TestDocument(t *testing.T) {
	g := New()
	g.Enum([]color{"red", "green"})
	g.Add(sample{})
	b, err := g.Document("Test")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties  map[string]map[string]string `json:"properties"`
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
			Enum       []string                   `json:"enum"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Properties["sample"]["$ref"] != "#/definitions/sample" {
		t.Errorf("roots = %v", doc.Properties)
	}
	s := doc.Definitions["sample"]
	for field, want := range map[string]string{
		"id":     `{"type":"string"}`,
		"color":  `{"$ref":"#/definitions/color"}`,
		"tags":   `{"anyOf":[{"items":{"type":"string"},"type":"array"},{"type":"null"}]}`,
		"attrs":  `{"additionalProperties":{"type":"integer"},"type":"object"}`,
		"raw":    `{}`,
		"child":  `{"$ref":"#/definitions/sample"}`,
		"parent": `{"anyOf":[{"$ref":"#/definitions/sample"},{"type":"null"}]}`,
		"Plain":  `{"type":"boolean"}`,
	} {
		var got bytes.Buffer
		if err := json.Compact(&got, s.Properties[field]); err != nil || got.String() != want {
			t.Errorf("%s = %s, want %s", field, got.String(), want)
		}
	}
	for _, absent := range []string{"Skipped"} {
		if _, ok := s.Properties[absent]; ok {
			t.Errorf("%s is in the schema", absent)
		}
	}
	if got := strings.Join(s.Required, ","); got != "id,name,color,tags,raw,parent,Plain,nested" {
		t.Errorf("required = %s", got)
	}
	if got := doc.Definitions["color"].Enum; len(got) != 2 || got[0] != "red" {
		t.Errorf("color enum = %v", got)
	}
}

func TestNameClash(t *testing.T) {
	type sample struct{}
	g := New()
	g.Add(outer{})
	g.Add(sample{})
	if _, err := g.Document("Test"); err == nil || !strings.Contains(err.Error(), "share the name sample") {
		t.Errorf("two types named sample: %v", err)
	}
}

// outer refers to the package's sample.
type outer struct {
	S sample `json:"s"`
}
