package toml

import (
	"bufio"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

type MetaData struct{}

func DecodeFile(path string, v any) (MetaData, error) {
	f, err := os.Open(path)
	if err != nil {
		return MetaData{}, err
	}
	defer f.Close()

	root := map[string]any{}
	currentPath := []string{}

	s := bufio.NewScanner(f)
	for lineNo := 1; s.Scan(); lineNo++ {
		line := strings.TrimSpace(stripComment(s.Text()))
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return MetaData{}, fmt.Errorf("line %d: empty section", lineNo)
			}
			parts := splitPath(section)
			if len(parts) == 0 {
				return MetaData{}, fmt.Errorf("line %d: invalid section", lineNo)
			}
			currentPath = parts
			ensureMap(root, currentPath)
			continue
		}

		eq := strings.Index(line, "=")
		if eq <= 0 {
			return MetaData{}, fmt.Errorf("line %d: expected key=value", lineNo)
		}

		key := strings.TrimSpace(line[:eq])
		valText := strings.TrimSpace(line[eq+1:])
		if key == "" {
			return MetaData{}, fmt.Errorf("line %d: empty key", lineNo)
		}

		val, err := parseValue(valText)
		if err != nil {
			return MetaData{}, fmt.Errorf("line %d: %w", lineNo, err)
		}

		fullPath := append(append([]string{}, currentPath...), splitPath(key)...)
		if len(fullPath) == 0 {
			return MetaData{}, fmt.Errorf("line %d: invalid key path", lineNo)
		}

		setValue(root, fullPath, val)
	}
	if err := s.Err(); err != nil {
		return MetaData{}, err
	}

	if err := decodeInto(root, v); err != nil {
		return MetaData{}, err
	}
	return MetaData{}, nil
}

func stripComment(line string) string {
	inQuotes := false
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inQuotes {
			escaped = true
			continue
		}
		if ch == '"' {
			inQuotes = !inQuotes
			continue
		}
		if ch == '#' && !inQuotes {
			return line[:i]
		}
	}
	return line
}

func splitPath(s string) []string {
	parts := strings.Split(s, ".")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func ensureMap(root map[string]any, path []string) map[string]any {
	cur := root
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	return cur
}

func setValue(root map[string]any, path []string, val any) {
	if len(path) == 1 {
		root[path[0]] = val
		return
	}
	m := ensureMap(root, path[:len(path)-1])
	m[path[len(path)-1]] = val
}

func parseValue(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty value")
	}

	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return []string{}, nil
		}
		parts, err := splitArray(inner)
		if err != nil {
			return nil, err
		}
		arr := make([]string, 0, len(parts))
		for _, p := range parts {
			v, err := parseValue(p)
			if err != nil {
				return nil, err
			}
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("only string arrays are supported")
			}
			arr = append(arr, str)
		}
		return arr, nil
	}

	if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
		v, err := strconv.Unquote(s)
		if err != nil {
			return nil, err
		}
		return v, nil
	}

	if s == "true" {
		return true, nil
	}
	if s == "false" {
		return false, nil
	}

	n, err := strconv.Atoi(s)
	if err == nil {
		return n, nil
	}

	return nil, fmt.Errorf("unsupported value %q", s)
}

func splitArray(s string) ([]string, error) {
	parts := []string{}
	start := 0
	inQuotes := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inQuotes {
			escaped = true
			continue
		}
		if ch == '"' {
			inQuotes = !inQuotes
			continue
		}
		if ch == ',' && !inQuotes {
			parts = append(parts, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if inQuotes {
		return nil, fmt.Errorf("unterminated quoted string in array")
	}
	parts = append(parts, strings.TrimSpace(s[start:]))
	return parts, nil
}

func decodeInto(data map[string]any, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("toml: decode target must be a non-nil pointer")
	}
	return decodeStruct(data, rv.Elem())
}

func decodeStruct(data map[string]any, dst reflect.Value) error {
	if dst.Kind() != reflect.Struct {
		return fmt.Errorf("toml: decode target must point to a struct")
	}

	typ := dst.Type()
	for i := 0; i < dst.NumField(); i++ {
		field := dst.Field(i)
		if !field.CanSet() {
			continue
		}
		fieldType := typ.Field(i)
		key := fieldType.Tag.Get("toml")
		if key == "" {
			key = strings.ToLower(fieldType.Name)
		}

		raw, ok := data[key]
		if !ok {
			continue
		}

		if err := assignValue(field, raw); err != nil {
			return fmt.Errorf("field %s: %w", fieldType.Name, err)
		}
	}

	return nil
}

func assignValue(dst reflect.Value, raw any) error {
	switch dst.Kind() {
	case reflect.Struct:
		m, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("expected table")
		}
		return decodeStruct(m, dst)
	case reflect.Map:
		m, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("expected table")
		}
		if dst.IsNil() {
			dst.Set(reflect.MakeMap(dst.Type()))
		}
		for k, v := range m {
			keyVal := reflect.New(dst.Type().Key()).Elem()
			if keyVal.Kind() != reflect.String {
				return fmt.Errorf("only string map keys are supported")
			}
			keyVal.SetString(k)

			valVal := reflect.New(dst.Type().Elem()).Elem()
			if err := assignValue(valVal, v); err != nil {
				return err
			}
			dst.SetMapIndex(keyVal, valVal)
		}
		return nil
	case reflect.String:
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		dst.SetString(s)
		return nil
	case reflect.Bool:
		b, ok := raw.(bool)
		if !ok {
			return fmt.Errorf("expected bool")
		}
		dst.SetBool(b)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, ok := raw.(int)
		if !ok {
			return fmt.Errorf("expected int")
		}
		dst.SetInt(int64(n))
		return nil
	case reflect.Slice:
		if dst.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("only []string slices are supported")
		}
		arr, ok := raw.([]string)
		if !ok {
			return fmt.Errorf("expected string array")
		}
		out := reflect.MakeSlice(dst.Type(), len(arr), len(arr))
		for i := range arr {
			out.Index(i).SetString(arr[i])
		}
		dst.Set(out)
		return nil
	default:
		return fmt.Errorf("unsupported destination type %s", dst.Kind())
	}
}
