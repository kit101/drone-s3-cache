package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

type cacheKey struct {
	value  string
	Prefix string   `json:"prefix"`
	Files  []string `json:"files"`
}

func decodeJSON(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func (k *cacheKey) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &k.value)
	}
	type fields cacheKey
	return decodeJSON(data, (*fields)(k))
}

type cacheEntry struct {
	Key          cacheKey `json:"key"`
	Paths        []string `json:"paths"`
	FallbackKeys []string `json:"fallback_keys"`
	resolved     string
}

func parseCache(raw string) ([]cacheEntry, error) {
	data := bytes.TrimSpace([]byte(raw))
	if len(data) == 0 {
		return nil, fmt.Errorf("cache is required (JSON object or array)")
	}
	var items []json.RawMessage
	switch data[0] {
	case '{':
		items = []json.RawMessage{data}
	case '[':
		if err := decodeJSON(data, &items); err != nil {
			return nil, fmt.Errorf("cache: %w", err)
		}
	default:
		return nil, fmt.Errorf("cache must be an object or array")
	}
	entries := make([]cacheEntry, len(items))
	for i, item := range items {
		if len(item) == 0 || item[0] != '{' {
			return nil, fmt.Errorf("cache[%d] must be an object", i)
		}
		if err := decodeJSON(item, &entries[i]); err != nil {
			return nil, fmt.Errorf("cache[%d]: %w", i, err)
		}
	}
	return entries, nil
}

func validKey(s string) error {
	if strings.TrimSpace(s) == "" || strings.Trim(s, ".") == "" || strings.ContainsAny(s, `/\`) || strings.ContainsFunc(s, unicode.IsControl) {
		return fmt.Errorf("key must be nonempty, not only dots, and contain no slash or control character")
	}
	return nil
}

func relativePath(s string) (string, error) {
	if s == "" || path.IsAbs(s) || strings.ContainsAny(s, `\:`) || strings.ContainsFunc(s, unicode.IsControl) {
		return "", fmt.Errorf("expected a workspace-relative path")
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." || strings.EqualFold(strings.TrimRight(part, ". "), ".git") {
			return "", fmt.Errorf("path cannot traverse parents or .git")
		}
	}
	s = path.Clean(s)
	if s == "." {
		return "", fmt.Errorf("workspace root is not a cache path")
	}
	return s, nil
}

func within(name, dir string) bool {
	return name == dir || strings.HasPrefix(name, dir+"/")
}

// Mounted paths must not redirect extraction through existing workspace links.
func safeParents(base, rel string) error {
	current := base
	for _, component := range strings.Split(rel, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("cache directory or ancestor is not a real directory: %s", rel)
		}
	}
	return nil
}

func validateEntries(entries []cacheEntry, workspace string) error {
	var dirs []string
	for i := range entries {
		e := &entries[i]
		if len(e.Paths) == 0 {
			return fmt.Errorf("cache[%d].paths must not be empty", i)
		}
		for j, dir := range e.Paths {
			clean, err := relativePath(dir)
			if err != nil || strings.ContainsAny(clean, "*?[]{}") {
				return fmt.Errorf("cache[%d].paths[%d]: expected an explicit relative directory", i, j)
			}
			if err := safeParents(workspace, clean); err != nil {
				return fmt.Errorf("cache[%d].paths[%d]: %w", i, j, err)
			}
			for _, existing := range dirs {
				if within(clean, existing) || within(existing, clean) {
					return fmt.Errorf("cache paths overlap: %s and %s", clean, existing)
				}
			}
			e.Paths[j] = clean
			dirs = append(dirs, clean)
		}
	}
	return nil
}
