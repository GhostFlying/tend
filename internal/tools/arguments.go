package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// unknownArgument adds the containing path and allowed fields which Go's
// strict decoder omits. Type errors remain the decoder's responsibility.
func unknownArgument(raw json.RawMessage, typ reflect.Type, path string) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return nil
		}
		fields := argumentFields(typ)
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		slices.Sort(names)
		keys := make([]string, 0, len(object))
		for name := range object {
			keys = append(keys, name)
		}
		slices.Sort(keys)
		for _, name := range keys {
			fieldPath := name
			if path != "" {
				fieldPath = path + "." + name
			}
			fieldType, ok := fields[name]
			if !ok {
				for key, candidate := range fields {
					if strings.EqualFold(key, name) {
						fieldType, ok = candidate, true
						break
					}
				}
			}
			if !ok {
				message := fmt.Sprintf("%s: unknown field; allowed fields: %s", fieldPath, strings.Join(names, ", "))
				if fieldPath == "spec.workspace.host" || fieldPath == "spec.workspace.host_id" {
					message += "; use spec.requirements.host_id to require an execution host from repos_list"
				}
				details, _ := json.Marshal(map[string]any{"field": fieldPath, "kind": "unknown_field", "allowed_fields": names})
				return &protocol.Error{Code: protocol.CodeInvalidRequest, Message: message, Details: details}
			}
			if err := unknownArgument(object[name], fieldType, fieldPath); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return nil
		}
		for i, value := range values {
			if err := unknownArgument(value, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func argumentFields(typ reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		ft := field.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if field.Anonymous && name == "" && ft.Kind() == reflect.Struct {
			for key, value := range argumentFields(ft) {
				fields[key] = value
			}
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}
