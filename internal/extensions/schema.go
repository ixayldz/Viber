package extensions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const jsonSchema202012 = "https://json-schema.org/draft/2020-12/schema"

// ValidateJSONSchema admits a deliberately small, bounded JSON Schema 2020-12
// subset. The manifest pins the exact schema bytes; unsupported keywords fail
// closed rather than being silently ignored.
func ValidateJSONSchema(raw []byte) error {
	return validateSchema(raw)
}

func validateSchema(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxSchemaBytes {
		return c.Fail(c.UnsupportedCapability, "extension JSON schema exceeds its size bound")
	}
	schema, err := decodeSchemaObject(raw)
	if err != nil || schema == nil {
		return c.Fail(c.InvalidArgument, "extension JSON schema must be an object")
	}
	if err := validateSchemaNode(schema, 0); err != nil {
		return c.Fail(c.UnsupportedCapability, "extension JSON schema uses an unsupported or invalid construct")
	}
	canonical, err := c.CanonicalV1(json.RawMessage(raw))
	if err != nil || len(canonical) > MaxSchemaBytes {
		return c.Fail(c.UnsupportedCapability, "extension JSON schema is not bounded canonical JSON")
	}
	return nil
}

func validateSchemaNode(schema map[string]any, depth int) error {
	if depth > 16 {
		return fmt.Errorf("schema depth")
	}
	allowed := map[string]bool{
		"$schema": true, "type": true, "description": true, "properties": true,
		"required": true, "additionalProperties": true, "items": true, "enum": true,
		"minimum": true, "maximum": true, "minLength": true, "maxLength": true,
		"minItems": true, "maxItems": true,
	}
	for key := range schema {
		if !allowed[key] {
			return fmt.Errorf("unsupported keyword")
		}
	}
	if version, ok := schema["$schema"]; ok && version != jsonSchema202012 {
		return fmt.Errorf("unknown schema draft")
	}
	kind, ok := schema["type"].(string)
	if !ok {
		return fmt.Errorf("explicit type required")
	}
	if description, ok := schema["description"]; ok {
		text, isString := description.(string)
		if !isString || len(text) > 1024 || !utf8.ValidString(text) {
			return fmt.Errorf("invalid description")
		}
	}
	if enum, ok := schema["enum"]; ok {
		values, isArray := enum.([]any)
		if !isArray || len(values) == 0 || len(values) > 64 {
			return fmt.Errorf("invalid enum")
		}
		for _, value := range values {
			if !schemaValueMatchesType(value, kind) {
				return fmt.Errorf("enum type mismatch")
			}
		}
	}
	switch kind {
	case "object":
		properties, ok := schema["properties"].(map[string]any)
		if !ok || len(properties) > 64 {
			return fmt.Errorf("bounded object properties required")
		}
		additional, ok := schema["additionalProperties"].(bool)
		if !ok || additional {
			return fmt.Errorf("additional properties must be denied")
		}
		required, ok := schema["required"].([]any)
		if !ok || len(required) > len(properties) {
			return fmt.Errorf("bounded required list required")
		}
		seen := map[string]bool{}
		for _, item := range required {
			name, isString := item.(string)
			if !isString || name == "" || len(name) > 128 || properties[name] == nil || seen[name] {
				return fmt.Errorf("invalid required property")
			}
			seen[name] = true
		}
		for _, rawChild := range properties {
			child, ok := rawChild.(map[string]any)
			if !ok || validateSchemaNode(child, depth+1) != nil {
				return fmt.Errorf("invalid property schema")
			}
		}
		if hasAnyKeyword(schema, "items", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems") {
			return fmt.Errorf("type-incompatible keyword")
		}
	case "string":
		if hasAnyKeyword(schema, "properties", "required", "additionalProperties", "items", "minimum", "maximum", "minItems", "maxItems") {
			return fmt.Errorf("type-incompatible keyword")
		}
		if err := validateIntegerRange(schema, "minLength", "maxLength", 0, 1<<16); err != nil {
			return err
		}
	case "integer":
		if hasAnyKeyword(schema, "properties", "required", "additionalProperties", "items", "minLength", "maxLength", "minItems", "maxItems") {
			return fmt.Errorf("type-incompatible keyword")
		}
		min, hasMin := schema["minimum"].(int64)
		max, hasMax := schema["maximum"].(int64)
		if hasMin && hasMax && min > max {
			return fmt.Errorf("invalid number bounds")
		}
		_, hasMinKey := schema["minimum"]
		_, hasMaxKey := schema["maximum"]
		if hasMinKey && !hasMin || hasMaxKey && !hasMax {
			return fmt.Errorf("integer bounds required")
		}
	case "boolean":
		if hasAnyKeyword(schema, "properties", "required", "additionalProperties", "items", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems") {
			return fmt.Errorf("type-incompatible keyword")
		}
	case "array":
		if hasAnyKeyword(schema, "properties", "required", "additionalProperties", "minimum", "maximum", "minLength", "maxLength") {
			return fmt.Errorf("type-incompatible keyword")
		}
		items, ok := schema["items"].(map[string]any)
		if !ok || validateSchemaNode(items, depth+1) != nil {
			return fmt.Errorf("bounded item schema required")
		}
		if err := validateIntegerRange(schema, "minItems", "maxItems", 0, 1<<12); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported type")
	}
	return nil
}

func hasAnyKeyword(schema map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := schema[key]; ok {
			return true
		}
	}
	return false
}

func validateIntegerRange(schema map[string]any, lowKey, highKey string, minAllowed, maxAllowed int64) error {
	low, hasLow := schema[lowKey].(int64)
	high, hasHigh := schema[highKey].(int64)
	_, hasLowKey := schema[lowKey]
	_, hasHighKey := schema[highKey]
	if (hasLowKey && !hasLow) || (hasHighKey && !hasHigh) {
		return fmt.Errorf("integer bound required")
	}
	if hasLow && (low < minAllowed || low > maxAllowed) || hasHigh && (high < minAllowed || high > maxAllowed) || hasLow && hasHigh && low > high {
		return fmt.Errorf("integer bounds outside supported range")
	}
	return nil
}

func schemaValueMatchesType(value any, kind string) bool {
	switch kind {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		_, ok := value.(int64)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

// ValidateJSON checks one JSON value against the manifest's accepted schema
// subset. It rejects unknown object keys and unsupported/coercive behavior.
func ValidateJSON(schemaRaw, valueRaw []byte) error {
	if err := validateSchema(schemaRaw); err != nil {
		return err
	}
	schema, err := decodeSchemaObject(schemaRaw)
	if err != nil {
		return c.Fail(c.InvalidArgument, "invalid pinned extension schema")
	}
	value, err := decodeSchemaObject(valueRaw)
	if err != nil || value == nil {
		return c.Fail(c.InvalidArgument, "extension JSON value must be an object")
	}
	if err := validateValue(schema, value, 0); err != nil {
		return c.Fail(c.InvalidArgument, "extension JSON value does not satisfy its pinned schema")
	}
	return nil
}

// Strict decoding into map[string]any would otherwise produce float64 and
// lose integer precision. Validate canonical shape first, then retain and
// normalize exact int64 JSON numbers for schema and value comparisons.
func decodeSchemaObject(raw []byte) (map[string]any, error) {
	var validated json.RawMessage
	if err := c.DecodeStrict(raw, &validated); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(validated))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	_, err := normalizeSchemaNumbers(object, 0)
	return object, err
}

func normalizeSchemaNumbers(value any, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("schema/value nesting exceeds supported bound")
	}
	switch item := value.(type) {
	case json.Number:
		return item.Int64()
	case map[string]any:
		for key, child := range item {
			normalized, err := normalizeSchemaNumbers(child, depth+1)
			if err != nil {
				return nil, err
			}
			item[key] = normalized
		}
	case []any:
		for i, child := range item {
			normalized, err := normalizeSchemaNumbers(child, depth+1)
			if err != nil {
				return nil, err
			}
			item[i] = normalized
		}
	}
	return value, nil
}

func validateValue(schema map[string]any, value any, depth int) error {
	if depth > 16 {
		return fmt.Errorf("value depth")
	}
	kind, _ := schema["type"].(string)
	if !schemaValueMatchesType(value, kind) {
		return fmt.Errorf("type mismatch")
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		valueDigest, _ := c.Digest(value)
		for _, candidate := range enum {
			candidateDigest, _ := c.Digest(candidate)
			matched = matched || candidateDigest == valueDigest
		}
		if !matched {
			return fmt.Errorf("enum mismatch")
		}
	}
	switch kind {
	case "object":
		properties := schema["properties"].(map[string]any)
		for _, item := range schema["required"].([]any) {
			name := item.(string)
			if _, ok := value.(map[string]any)[name]; !ok {
				return fmt.Errorf("missing required key")
			}
		}
		actual := value.(map[string]any)
		for name, childValue := range actual {
			childRaw, ok := properties[name]
			if !ok {
				return fmt.Errorf("additional key")
			}
			if err := validateValue(childRaw.(map[string]any), childValue, depth+1); err != nil {
				return err
			}
		}
	case "string":
		text := value.(string)
		runes := int64(utf8.RuneCountInString(text))
		if min, ok := schema["minLength"].(int64); ok && runes < min {
			return fmt.Errorf("string length")
		}
		if max, ok := schema["maxLength"].(int64); ok && runes > max {
			return fmt.Errorf("string length")
		}
	case "integer":
		number := value.(int64)
		if min, ok := schema["minimum"].(int64); ok && number < min {
			return fmt.Errorf("integer range")
		}
		if max, ok := schema["maximum"].(int64); ok && number > max {
			return fmt.Errorf("integer range")
		}
	case "array":
		items := value.([]any)
		if min, ok := schema["minItems"].(int64); ok && int64(len(items)) < min {
			return fmt.Errorf("array length")
		}
		if max, ok := schema["maxItems"].(int64); ok && int64(len(items)) > max {
			return fmt.Errorf("array length")
		}
		for _, item := range items {
			if err := validateValue(schema["items"].(map[string]any), item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
