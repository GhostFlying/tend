// Package schema derives JSON Schema (draft-07) from Go types, following
// encoding/json's rules, so that the web client's TypeScript types are
// generated from the server's own types (D22).
//
// Every named struct and registered enum becomes a definition named after
// its Go type. A field without omitempty is required; if its Go value can
// be nil (a pointer, slice, map or interface), it may also be null, which
// is what encoding/json writes for nil.
package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var (
	rawMessage = reflect.TypeFor[json.RawMessage]()
	timeType   = reflect.TypeFor[time.Time]()
)

// Generator collects definitions.
type Generator struct {
	enums map[reflect.Type][]string
	defs  map[string]map[string]any
	names map[string]reflect.Type
	roots []string
	err   error
}

// New returns an empty Generator.
func New() *Generator {
	return &Generator{enums: map[reflect.Type][]string{}, defs: map[string]map[string]any{}, names: map[string]reflect.Type{}}
}

// Enum registers the values of a string enum. values is a slice of the
// enum type, such as []protocol.Verdict{...}.
func (g *Generator) Enum(values any) {
	v := reflect.ValueOf(values)
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.String {
		g.fail(fmt.Errorf("schema: enum values must be a slice of a string type, not %T", values))
		return
	}
	out := make([]string, v.Len())
	for i := range out {
		out[i] = v.Index(i).String()
	}
	g.enums[v.Type().Elem()] = out
}

// Add makes the type of v a root of the document.
func (g *Generator) Add(v any) {
	t := reflect.TypeOf(v)
	if t.Kind() != reflect.Struct || t.Name() == "" {
		g.fail(fmt.Errorf("schema: root %s is not a named struct", t))
		return
	}
	g.schema(t)
	g.roots = append(g.roots, t.Name())
}

func (g *Generator) fail(err error) {
	if g.err == nil {
		g.err = err
	}
}

// Document returns the schema: an object whose properties name every root,
// with every definition under "definitions".
func (g *Generator) Document(title string) ([]byte, error) {
	if g.err != nil {
		return nil, g.err
	}
	props := map[string]any{}
	for _, r := range g.roots {
		props[r] = ref(r)
	}
	doc := map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#", "title": title, "type": "object",
		"properties": props, "additionalProperties": false, "definitions": g.defs,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("schema: encode: %w", err)
	}
	return append(b, '\n'), nil
}

func ref(name string) map[string]any { return map[string]any{"$ref": "#/definitions/" + name} }

// define registers t under its name and reports whether it was new.
func (g *Generator) define(t reflect.Type) bool {
	name := t.Name()
	if prev, ok := g.names[name]; ok {
		if prev != t {
			g.fail(fmt.Errorf("schema: %s and %s share the name %s", prev.PkgPath(), t.PkgPath(), name))
		}
		return false
	}
	g.names[name] = t
	return true
}

func (g *Generator) schema(t reflect.Type) map[string]any {
	if values, ok := g.enums[t]; ok {
		if g.define(t) {
			g.defs[t.Name()] = map[string]any{"title": t.Name(), "type": "string", "enum": values}
		}
		return ref(t.Name())
	}
	switch {
	case t == rawMessage:
		return map[string]any{}
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return g.schema(t.Elem())
	case reflect.Struct:
		if t.Name() == "" {
			return g.object(t)
		}
		if g.define(t) {
			g.defs[t.Name()] = nil // reserve against recursion
			obj := g.object(t)
			obj["title"] = t.Name()
			g.defs[t.Name()] = obj
		}
		return ref(t.Name())
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": g.schema(t.Elem())}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			g.fail(fmt.Errorf("schema: map key of %s is not a string", t))
		}
		return map[string]any{"type": "object", "additionalProperties": g.schema(t.Elem())}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		return map[string]any{}
	}
	g.fail(fmt.Errorf("schema: unsupported type %s", t))
	return map[string]any{}
}

// object describes a struct's JSON object, flattening embedded structs as
// encoding/json does.
func (g *Generator) object(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	g.fields(t, props, &required)
	obj := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		obj["required"] = required
	}
	return obj
}

func (g *Generator) fields(t reflect.Type, props map[string]any, required *[]string) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				g.fields(ft, props, required)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		s := g.schema(f.Type)
		omit := strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,")
		if !omit {
			*required = append(*required, name)
			if nilable(f.Type) && len(s) > 0 {
				s = map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}
			}
		}
		props[name] = s
	}
}

func nilable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
		return true
	}
	return false
}
