package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// object is a JSON object that keeps its keys in order, so that tend setup
// changes an agent's configuration file without reordering what the owner
// wrote.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(k string) any { return o.vals[k] }

func (o *object) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// child returns the object under k, creating it if absent. It fails when
// k holds something other than an object.
func (o *object) child(k string) (*object, error) {
	switch v := o.get(k).(type) {
	case nil:
		c := newObject()
		o.set(k, c)
		return c, nil
	case *object:
		return v, nil
	default:
		return nil, fmt.Errorf("%q is not an object", k)
	}
}

// list returns the array under k, or nil if absent. It fails when k holds
// something other than an array.
func (o *object) list(k string) ([]any, error) {
	switch v := o.get(k).(type) {
	case nil:
		return nil, nil
	case []any:
		return v, nil
	default:
		return nil, fmt.Errorf("%q is not an array", k)
	}
}

// parseOrdered decodes JSON keeping object key order and numbers as
// written. Empty input is an empty object.
func parseOrdered(b []byte) (*object, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return newObject(), nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the top-level value")
	}
	o, ok := v.(*object)
	if !ok {
		return nil, fmt.Errorf("the top-level value is not an object")
	}
	return o, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newObject()
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(k.(string), v)
			}
			_, err := dec.Token()
			return o, err
		case '[':
			a := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := dec.Token()
			return a, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

// formatOrdered writes v as indented JSON with a final newline.
func formatOrdered(v any) string {
	var b strings.Builder
	writeValue(&b, v, "")
	b.WriteString("\n")
	return b.String()
}

func writeValue(b *strings.Builder, v any, indent string) {
	inner := indent + "  "
	switch t := v.(type) {
	case *object:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			fmt.Fprintf(b, "%s%s: ", inner, marshalScalar(k))
			writeValue(b, t.vals[k], inner)
			if i < len(t.keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(inner)
			writeValue(b, e, inner)
			if i < len(t)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent + "]")
	default:
		b.WriteString(marshalScalar(t))
	}
}

// marshalScalar encodes a scalar without escaping &, < and >, as the
// agents' own tools write them: shell commands in hook settings are full
// of them, and escaping would rewrite settings tend does not own.
func marshalScalar(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "null"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// diff renders the line changes from a to b with two lines of context.
func diff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	// lcs[i][j] is the longest common subsequence of x[i:] and y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		mark byte
		text string
	}
	var lines []line
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			lines = append(lines, line{' ', x[i]})
			i++
			j++
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			// A replaced line shows the old text first.
			lines = append(lines, line{'-', x[i]})
			i++
		default:
			lines = append(lines, line{'+', y[j]})
			j++
		}
	}
	var out strings.Builder
	last := -1
	for k, l := range lines {
		near := false
		for d := -2; d <= 2; d++ {
			if n := k + d; n >= 0 && n < len(lines) && lines[n].mark != ' ' {
				near = true
			}
		}
		if !near {
			continue
		}
		if last >= 0 && k > last+1 {
			out.WriteString("  ...\n")
		}
		fmt.Fprintf(&out, "%c %s\n", l.mark, l.text)
		last = k
	}
	return out.String()
}
