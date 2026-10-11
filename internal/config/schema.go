package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Field describes one editable setting for the web UI.
type Field struct {
	Key      string   `json:"key"` // dotted json path, e.g. "bonding.mode"
	Label    string   `json:"label"`
	Help     string   `json:"help,omitempty"`
	Type     string   `json:"type"` // string | int | float | bool | enum
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Step     float64  `json:"step,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	Options  []Option `json:"options,omitempty"`
	Advanced bool     `json:"advanced,omitempty"`
	Secret   bool     `json:"secret,omitempty"`
}

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Section groups fields of one nested struct.
type Section struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Help   string  `json:"help,omitempty"`
	Fields []Field `json:"fields"`
}

// Schema reflects over a config struct pointer. Top-level scalar fields go in
// a section with key "".
func Schema(v any) []Section {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	general := Section{Key: "", Label: "General"}
	var sections []Section
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("schema") == "-" {
			continue
		}
		key := jsonName(f)
		if f.Type.Kind() == reflect.Struct {
			s := Section{Key: key, Label: orDefault(f.Tag.Get("label"), key), Help: f.Tag.Get("help")}
			for j := 0; j < f.Type.NumField(); j++ {
				sf := f.Type.Field(j)
				if sf.Tag.Get("schema") == "-" {
					continue
				}
				s.Fields = append(s.Fields, field(key+"."+jsonName(sf), sf))
			}
			sections = append(sections, s)
			continue
		}
		general.Fields = append(general.Fields, field(key, f))
	}
	if len(general.Fields) > 0 {
		sections = append([]Section{general}, sections...)
	}
	return sections
}

func field(key string, f reflect.StructField) Field {
	fd := Field{
		Key:      key,
		Label:    orDefault(f.Tag.Get("label"), key),
		Help:     f.Tag.Get("help"),
		Unit:     f.Tag.Get("unit"),
		Advanced: f.Tag.Get("adv") == "1",
		Secret:   f.Tag.Get("secret") == "1",
	}
	switch f.Type.Kind() {
	case reflect.Bool:
		fd.Type = "bool"
	case reflect.Int, reflect.Int64, reflect.Int32:
		fd.Type = "int"
		fd.Step = 1
	case reflect.Float64, reflect.Float32:
		fd.Type = "float"
		fd.Step = 0.1
	default:
		fd.Type = "string"
	}
	if s := f.Tag.Get("step"); s != "" {
		fd.Step, _ = strconv.ParseFloat(s, 64)
	}
	if s := f.Tag.Get("min"); s != "" {
		v, _ := strconv.ParseFloat(s, 64)
		fd.Min = &v
	}
	if s := f.Tag.Get("max"); s != "" {
		v, _ := strconv.ParseFloat(s, 64)
		fd.Max = &v
	}
	if e := f.Tag.Get("enum"); e != "" {
		fd.Type = "enum"
		for _, opt := range strings.Split(e, ",") {
			val, label, ok := strings.Cut(opt, "=")
			if !ok {
				label = val
			}
			fd.Options = append(fd.Options, Option{val, label})
		}
	}
	return fd
}

// checkRanges enforces min/max/enum tags on every nested field.
func checkRanges(v any) []error {
	var errs []error
	rv := reflect.ValueOf(v).Elem()
	walk(rv, "", func(key string, f reflect.StructField, val reflect.Value) {
		var x float64
		switch val.Kind() {
		case reflect.Int, reflect.Int64, reflect.Int32:
			x = float64(val.Int())
		case reflect.Float64, reflect.Float32:
			x = val.Float()
		case reflect.String:
			if e := f.Tag.Get("enum"); e != "" {
				ok := false
				for _, opt := range strings.Split(e, ",") {
					v, _, _ := strings.Cut(opt, "=")
					if v == val.String() {
						ok = true
					}
				}
				if !ok {
					errs = append(errs, fmt.Errorf("%s: %q is not one of the allowed values", key, val.String()))
				}
			}
			return
		default:
			return
		}
		if s := f.Tag.Get("min"); s != "" {
			if m, _ := strconv.ParseFloat(s, 64); x < m {
				errs = append(errs, fmt.Errorf("%s: %v is below the minimum %v", key, x, m))
			}
		}
		if s := f.Tag.Get("max"); s != "" {
			if m, _ := strconv.ParseFloat(s, 64); x > m {
				errs = append(errs, fmt.Errorf("%s: %v is above the maximum %v", key, x, m))
			}
		}
	})
	return errs
}

func walk(v reflect.Value, prefix string, fn func(string, reflect.StructField, reflect.Value)) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("schema") == "-" {
			continue
		}
		key := jsonName(f)
		if prefix != "" {
			key = prefix + "." + key
		}
		if f.Type.Kind() == reflect.Struct {
			walk(v.Field(i), key, fn)
			continue
		}
		fn(key, f, v.Field(i))
	}
}

// fillZero copies numeric/enum fields from def into dst where dst has the
// zero value but the zero value would be invalid (has a min > 0 or an enum).
// Booleans and free-text strings are left alone: false/"" are legitimate.
func fillZero(dst, def any) {
	dv := reflect.ValueOf(dst).Elem()
	fv := reflect.ValueOf(def).Elem()
	var rec func(d, f reflect.Value)
	rec = func(d, f reflect.Value) {
		t := d.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if sf.Tag.Get("schema") == "-" {
				continue
			}
			if sf.Type.Kind() == reflect.Struct {
				rec(d.Field(i), f.Field(i))
				continue
			}
			if !d.Field(i).IsZero() {
				continue
			}
			needs := sf.Tag.Get("enum") != ""
			if m := sf.Tag.Get("min"); m != "" {
				if x, _ := strconv.ParseFloat(m, 64); x > 0 {
					needs = true
				}
			}
			if sf.Tag.Get("fill") == "1" {
				needs = true
			}
			if needs {
				d.Field(i).Set(f.Field(i))
			}
		}
	}
	rec(dv, fv)
}

func jsonName(f reflect.StructField) string {
	n, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if n == "" {
		return f.Name
	}
	return n
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
