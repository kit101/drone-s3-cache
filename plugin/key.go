package plugin

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

func validateSegment(value string) error {
	if value == "" || value == "." || value == ".." || len(value) > 512 || strings.ContainsAny(value, `/\`) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid cache path segment %q", value)
	}
	return nil
}
func validateNamespace(value string) error {
	for _, part := range strings.Split(value, "/") {
		if err := validateSegment(part); err != nil {
			return err
		}
	}
	return nil
}

func resolveKey(key string, files []string) (string, error) {
	if err := validateSegment(key); err != nil {
		return "", err
	}
	if len(files) == 0 {
		return key, nil
	}
	root, err := filepath.EvalSymlinks(".")
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for _, name := range files {
		clean := filepath.Clean(name)
		if name == "" || filepath.IsAbs(name) || strings.Contains(name, `\`) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("key file must be inside the workspace: %q", name)
		}
		paths = append(paths, filepath.ToSlash(clean))
	}
	sort.Strings(paths)
	h := sha256.New()
	_, _ = h.Write([]byte("drone-s3-cache-key-v1\x00"))
	var size [8]byte
	for i, name := range paths {
		if i > 0 && name == paths[i-1] {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return "", fmt.Errorf("key file %q: %w", name, err)
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("key file escapes workspace: %q", name)
		}
		info, err := os.Stat(target)
		if err != nil {
			return "", fmt.Errorf("key file %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("key file must be a regular file: %q", name)
		}
		file, err := os.Open(target)
		if err != nil {
			return "", fmt.Errorf("key file %q: %w", name, err)
		}
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return "", fmt.Errorf("key file must be a readable regular file: %q", name)
		}
		binary.BigEndian.PutUint64(size[:], uint64(len(name)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(name))
		binary.BigEndian.PutUint64(size[:], uint64(info.Size()))
		_, _ = h.Write(size[:])
		n, copyErr := io.Copy(h, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", fmt.Errorf("hash key file %q: %w", name, copyErr)
		}
		if closeErr != nil {
			return "", closeErr
		}
		if n != info.Size() {
			return "", fmt.Errorf("key file changed while hashing: %q", name)
		}
	}
	resolved := fmt.Sprintf("%s-%x", key, h.Sum(nil))
	if err := validateSegment(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func (p *Plugin) validateKey() error {
	s := &p.settings
	if s.Key == "" {
		if len(s.KeyFiles.Value()) > 0 || len(s.FallbackKeys.Value()) > 0 {
			return fmt.Errorf("key_files and fallback_keys require key")
		}
		return nil
	}
	if s.Mode == flushMode {
		return fmt.Errorf("flush uses flush_path, not key parameters")
	}
	if s.Path != "" || s.FallbackPath != "" || s.FlushPath != "" {
		return fmt.Errorf("key cannot be combined with path, fallback_path or flush_path")
	}
	for _, name := range []string{s.Root, p.pipeline.Repo.Owner} {
		if err := validateNamespace(name); err != nil {
			return err
		}
	}
	for _, name := range append([]string{p.pipeline.Repo.Name, s.Filename}, s.FallbackKeys.Value()...) {
		if err := validateSegment(name); err != nil {
			return err
		}
	}
	resolved, err := resolveKey(s.Key, s.KeyFiles.Value())
	if err != nil {
		return err
	}
	s.resolvedKey = resolved
	return nil
}
func (p *Plugin) keyPath(key string) string {
	return cleanPath(p.settings.Root, p.pipeline.Repo.Owner, p.pipeline.Repo.Name, "keys", key, p.settings.Filename)
}
