package agentconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Decode accepts exact schema keys, unlike encoding/json's case-insensitive
// struct matching. Duplicate keys and trailing values are rejected as well.
// No decoder error text or unrecognized key is returned to the caller.
func Decode(data []byte) (Config, error) {
	if !utf8.Valid(data) {
		return Config{}, invalid("invalid_json", "$")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := checkJSON(dec, reflect.TypeFor[Config](), "$"); err != nil {
		return Config{}, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, invalid("invalid_json", "$")
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, invalid("invalid_json", "$")
	}
	return cfg, nil
}

func checkJSON(dec *json.Decoder, typ reflect.Type, path string) error {
	token, err := dec.Token()
	if err != nil {
		return invalid("invalid_json", path)
	}
	switch typ.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return invalid("invalid_shape", path)
		}
		fields := make(map[string]reflect.Type)
		for i := range typ.NumField() {
			field := typ.Field(i)
			fields[strings.Split(field.Tag.Get("json"), ",")[0]] = field.Type
		}
		seen := make(map[string]bool)
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return invalid("invalid_json", path)
			}
			name, ok := key.(string)
			fieldType, known := fields[name]
			if !ok || !known {
				return invalid("unknown_field", path)
			}
			fieldPath := path + "." + name
			if seen[name] {
				return invalid("duplicate_field", fieldPath)
			}
			seen[name] = true
			if err := checkJSON(dec, fieldType, fieldPath); err != nil {
				return err
			}
		}
		if end, err := dec.Token(); err != nil || end != json.Delim('}') {
			return invalid("invalid_json", path)
		}
	case reflect.Slice:
		if token != json.Delim('[') {
			return invalid("invalid_shape", path)
		}
		for i := 0; dec.More(); i++ {
			if err := checkJSON(dec, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		if end, err := dec.Token(); err != nil || end != json.Delim(']') {
			return invalid("invalid_json", path)
		}
	case reflect.String:
		if _, ok := token.(string); !ok {
			return invalid("invalid_shape", path)
		}
	case reflect.Bool:
		if _, ok := token.(bool); !ok {
			return invalid("invalid_shape", path)
		}
	case reflect.Uint64:
		n, ok := token.(json.Number)
		if !ok {
			return invalid("invalid_shape", path)
		}
		if _, err := strconv.ParseUint(n.String(), 10, 64); err != nil {
			return invalid("invalid_shape", path)
		}
	default:
		return invalid("invalid_shape", path)
	}
	return nil
}
