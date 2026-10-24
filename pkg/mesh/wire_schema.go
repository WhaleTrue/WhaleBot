package mesh

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// JSONSchemaFor describes JSON-encoded wire types at provider registration time.
// Structs are closed; map and RawMessage fields explicitly permit extension data.
// Semantic limits still belong to the business handler. Recursive data is bounded
// by the same 64-level ceiling as protocol decoding.
func JSONSchemaFor(value any) json.RawMessage {
	schema := wireTypeSchema(reflect.TypeOf(value), 0)
	data, _ := json.Marshal(schema) // schema contains only JSON-compatible standard values.
	return data
}
func wireTypeSchema(kind reflect.Type, depth int) any {
	if kind == nil {
		return map[string]string{"type": "null"}
	}
	if depth > 64 {
		return false
	}
	if kind == reflect.TypeOf(time.Time{}) {
		return map[string]string{"type": "string", "format": "date-time"}
	}
	if kind == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}
	switch kind.Kind() {
	case reflect.Pointer:
		return map[string]any{"anyOf": []any{wireTypeSchema(kind.Elem(), depth+1), map[string]string{"type": "null"}}}
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for i := range kind.NumField() {
			field := kind.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			if field.Anonymous && tag[0] == "" {
				embedded := field.Type
				if embedded.Kind() == reflect.Pointer {
					embedded = embedded.Elem()
				}
				if definition, ok := wireTypeSchema(embedded, depth+1).(map[string]any); ok {
					if values, ok := definition["properties"].(map[string]any); ok {
						for name, schema := range values {
							properties[name] = schema
						}
						required = append(required, definition["required"].([]string)...)
						continue
					}
				}
			}
			name := tag[0]
			if name == "" {
				name = field.Name
			}
			properties[name] = wireTypeSchema(field.Type, depth+1)
			optional := false
			for _, option := range tag[1:] {
				if option == "omitempty" || option == "omitzero" {
					optional = true
				}
			}
			if !optional {
				required = append(required, name)
			}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	case reflect.Slice:
		if kind.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": []string{"string", "null"}, "contentEncoding": "base64"}
		}
		return map[string]any{"type": []string{"array", "null"}, "items": wireTypeSchema(kind.Elem(), depth+1)}
	case reflect.Array:
		return map[string]any{"type": "array", "items": wireTypeSchema(kind.Elem(), depth+1), "minItems": kind.Len(), "maxItems": kind.Len()}
	case reflect.Map:
		if kind.Key().Kind() != reflect.String {
			return false
		}
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": wireTypeSchema(kind.Elem(), depth+1)}
	case reflect.Interface:
		return map[string]any{}
	case reflect.String:
		return map[string]string{"type": "string"}
	case reflect.Bool:
		return map[string]string{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]string{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]string{"type": "number"}
	default:
		return false
	}
}
