package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// EnvName returns the variable for a key: "faces.url" -> SCHUHKARTON_FACES_URL.
func EnvName(key string) string {
	return "SCHUHKARTON_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// Keys lists every settable key ("server.listen", "library.folders", ...).
func Keys() []string {
	var out []string
	walk(reflect.ValueOf(Defaults()).Elem(), "", func(key string, _ reflect.Value) { out = append(out, key) })
	return out
}

func walk(v reflect.Value, prefix string, fn func(key string, f reflect.Value)) {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name := strings.Split(sf.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		f := v.Field(i)
		if f.Kind() == reflect.Struct {
			walk(f, key, fn)
			continue
		}
		fn(key, f)
	}
}

// applyEnv overlays SCHUHKARTON_* variables and locks what they set.
// Folders are "Name=/path,/other/path"; lists are comma separated.
func (c *Config) applyEnv(lookup func(string) (string, bool)) error {
	var errs []string
	walk(reflect.ValueOf(c).Elem(), "", func(key string, f reflect.Value) {
		env := EnvName(key)
		raw, ok := lookup(env)
		if !ok {
			return
		}
		if err := SetValue(c, key, raw); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", env, err))
			return
		}
		c.Lock(key, env)
	})
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// SetValue sets one key from text (used for the environment and flags).
func SetValue(c *Config, key, raw string) error {
	var target reflect.Value
	walk(reflect.ValueOf(c).Elem(), "", func(k string, f reflect.Value) {
		if k == key {
			target = f
		}
	})
	if !target.IsValid() {
		return fmt.Errorf("unknown key %q", key)
	}
	raw = strings.TrimSpace(raw)
	switch target.Kind() {
	case reflect.String:
		target.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		target.SetBool(b)
	case reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return err
		}
		target.SetInt(int64(n))
	case reflect.Float64:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		target.SetFloat(n)
	case reflect.Slice:
		parts := splitList(raw)
		if target.Type().Elem() == reflect.TypeOf(Folder{}) {
			var fs []Folder
			for _, p := range parts {
				name, path, ok := strings.Cut(p, "=")
				if !ok || strings.ContainsAny(name, `/\`) {
					name, path = "", p
				}
				fs = append(fs, Folder{Name: name, Path: ExpandHome(path)})
			}
			target.Set(reflect.ValueOf(fs))
			return nil
		}
		target.Set(reflect.ValueOf(parts))
	default:
		return fmt.Errorf("unsupported type %s", target.Kind())
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
