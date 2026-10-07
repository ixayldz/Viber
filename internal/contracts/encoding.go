package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CanonicalV1 uses UTF-8 JSON, sorted object keys, ordered arrays, decimal
// signed 64-bit integers, explicit null, and no floating point numbers.
// The version/domain prefix separates metadata from exact-byte file hashes.
func CanonicalV1(value any) ([]byte, error) {
	if err := validateUnicode(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	parsed, err := parseDocument(raw)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := encodeCanonical(&out, parsed); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func Digest(value any) (string, error) {
	data, err := CanonicalV1(value)
	if err != nil {
		return "", err
	}
	return HashBytes(append([]byte("viber:metadata:v1\x00"), data...)), nil
}

func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func ValidDigest(d string) bool {
	if len(d) != 64 {
		return false
	}
	_, err := hex.DecodeString(d)
	return err == nil && d == strings.ToLower(d)
}

// DecodeStrict rejects duplicate keys, unknown fields, unsupported number
// encodings and trailing documents before deserializing an authoritative type.
func DecodeStrict(data []byte, target any) error {
	if len(data) > 8<<20 {
		return Fail(InvalidArgument, "JSON document exceeds 8 MiB")
	}
	parsed, err := parseDocument(data)
	if err != nil {
		return Fail(InvalidArgument, err.Error())
	}
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer || reflect.ValueOf(target).IsNil() {
		return Fail(InvalidArgument, "non-nil decode target pointer required")
	}
	if err := validateShape(parsed, targetType.Elem(), 0); err != nil {
		return Fail(InvalidArgument, err.Error())
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return Fail(InvalidArgument, err.Error())
	}
	return nil
}

func parseDocument(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	// Reject surrogate escapes altogether in v1. Literal UTF-8 Unicode remains
	// supported, so JSON decoder replacement behavior cannot collapse identities.
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if i+1 < len(data) && data[i+1] == '\\' {
			i++
			continue
		}
		if i+5 < len(data) && data[i+1] == 'u' {
			n, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
			if err == nil && n >= 0xd800 && n <= 0xdfff {
				return nil, fmt.Errorf("surrogate escape unsupported in canonical v1")
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := parseValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON content")
	}
	return v, nil
}

func parseValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("JSON nesting exceeds 64")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("invalid object key")
				}
				if _, exists := m[k]; exists {
					return nil, fmt.Errorf("duplicate key %q", k)
				}
				v, err := parseValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("unclosed object")
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := parseValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("unclosed array")
			}
			return a, nil
		default:
			return nil, fmt.Errorf("unexpected delimiter")
		}
	case json.Number:
		s := string(t)
		if strings.ContainsAny(s, ".eE") || s == "-0" {
			return nil, fmt.Errorf("only canonical decimal integers supported")
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer outside signed 64-bit range")
		}
		return n, nil
	case string, bool, nil:
		return t, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value")
	}
}

func encodeCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			b, _ := json.Marshal(k)
			out.Write(b)
			out.WriteByte(':')
			if err := encodeCanonical(out, v[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := encodeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out.Write(b)
	}
	return nil
}

func validateUnicode(v reflect.Value, depth int) error {
	if depth > 64 {
		return fmt.Errorf("metadata nesting exceeds 64")
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return fmt.Errorf("floating point metadata unsupported")
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return fmt.Errorf("invalid UTF-8 metadata string")
		}
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return validateUnicode(v.Elem(), depth+1)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if err := validateUnicode(iter.Key(), depth+1); err != nil {
				return err
			}
			if err := validateUnicode(iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := validateUnicode(v.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath == "" && field.Tag.Get("json") != "-" {
				if err := validateUnicode(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateShape(value any, t reflect.Type, depth int) error {
	if depth > 64 {
		return fmt.Errorf("schema nesting exceeds 64")
	}
	if t.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		return validateShape(value, t.Elem(), depth+1)
	}
	if value == nil {
		switch t.Kind() {
		case reflect.Slice, reflect.Map, reflect.Interface:
			return nil
		}
		return fmt.Errorf("null cannot replace a required scalar or object")
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("expected JSON object")
		}
		known := map[string]reflect.StructField{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			name := tag[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			known[name] = field
			optional := false
			for _, option := range tag[1:] {
				if option == "omitempty" {
					optional = true
				}
			}
			if _, exists := object[name]; !exists && !optional {
				return fmt.Errorf("required field %q absent", name)
			}
		}
		for name, v := range object {
			field, ok := known[name]
			if !ok {
				return fmt.Errorf("unknown field %q", name)
			}
			if err := validateShape(v, field.Type, depth+1); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("byte array must be base64 string")
			}
			return nil
		}
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected JSON array")
		}
		for _, v := range items {
			if err := validateShape(v, t.Elem(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok || t.Key().Kind() != reflect.String {
			return fmt.Errorf("expected string-keyed object")
		}
		for _, v := range object {
			if err := validateShape(v, t.Elem(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Interface:
		return nil
	case reflect.String:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected JSON string")
		}
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected JSON boolean")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if _, ok := value.(int64); !ok {
			return fmt.Errorf("expected decimal integer")
		}
	default:
		return fmt.Errorf("unsupported authoritative schema type")
	}
	return nil
}
