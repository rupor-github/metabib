package inpxutil

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
)

type RecordTemplate struct {
	template *template.Template
}

func NewRecordTemplate(name string, text string) (*RecordTemplate, error) {
	funcs, err := recordTemplateFuncs()
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(name).Funcs(funcs).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	return &RecordTemplate{template: tmpl}, nil
}

func (t *RecordTemplate) ExecuteString(value any) (string, error) {
	if t == nil {
		return "", nil
	}
	var buf bytes.Buffer
	if err := t.template.Execute(&buf, value); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func (t *RecordTemplate) ExecuteBool(value any) (bool, error) {
	out, err := t.ExecuteString(value)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(out) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("filter template returned %q; expected boolean output", out)
	}
}

func recordTemplateFuncs() (template.FuncMap, error) {
	funcs := sprig.FuncMap()
	custom := template.FuncMap{
		"oneOf":         oneOf,
		"containsValue": containsValue,
		"rangeName":     rangeName,
	}
	for name, fn := range custom {
		if _, exists := funcs[name]; exists {
			return nil, fmt.Errorf("INPX template function %q conflicts with slim-sprig", name)
		}
		funcs[name] = fn
	}
	return funcs, nil
}

func oneOf(value any, candidates ...any) bool {
	for _, candidate := range candidates {
		if valuesEqual(value, candidate) {
			return true
		}
	}
	return false
}

func containsValue(values any, target any) bool {
	rv := reflect.ValueOf(values)
	if !rv.IsValid() {
		return false
	}
	if rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return false
	}
	for idx := range rv.Len() {
		if valuesEqual(rv.Index(idx).Interface(), target) {
			return true
		}
	}
	return false
}

func valuesEqual(left any, right any) bool {
	if reflect.DeepEqual(left, right) {
		return true
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

func rangeName(value any, size any, fallback any) (string, error) {
	n, ok := positiveInt64(value)
	if !ok {
		return fmt.Sprint(fallback), nil
	}
	return rangeNameFor(n, size, fallback)
}

func rangeNameFor(value int64, size any, fallback any) (string, error) {
	bucketSize, ok := positiveInt64(size)
	if !ok {
		return "", fmt.Errorf("range size %q must be positive integer", size)
	}
	if value <= 0 {
		return fmt.Sprint(fallback), nil
	}
	start := ((value - 1) / bucketSize * bucketSize) + 1
	end := start + bucketSize - 1
	return fmt.Sprintf("%010d-%010d", start, end), nil
}

func positiveInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return positiveInt64Value(int64(v))
	case int8:
		return positiveInt64Value(int64(v))
	case int16:
		return positiveInt64Value(int64(v))
	case int32:
		return positiveInt64Value(int64(v))
	case int64:
		return positiveInt64Value(v)
	case uint:
		return positiveUint64Value(uint64(v))
	case uint8:
		return positiveUint64Value(uint64(v))
	case uint16:
		return positiveUint64Value(uint64(v))
	case uint32:
		return positiveUint64Value(uint64(v))
	case uint64:
		return positiveUint64Value(v)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, false
		}
		return positiveInt64Value(parsed)
	default:
		parsed, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(v)), 10, 64)
		if err != nil {
			return 0, false
		}
		return positiveInt64Value(parsed)
	}
}

func positiveInt64Value(value int64) (int64, bool) {
	if value <= 0 {
		return 0, false
	}
	return value, true
}

func positiveUint64Value(value uint64) (int64, bool) {
	if value == 0 || value > uint64(^uint64(0)>>1) {
		return 0, false
	}
	return int64(value), true
}
