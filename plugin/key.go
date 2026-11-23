package plugin

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var placeholder = regexp.MustCompile(`\{\{\s*(\.OS|\.ARCH|\.Env\.[A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

func expandKey(value string) (string, error) {
	var missing string
	value = placeholder.ReplaceAllStringFunc(value, func(match string) string {
		name := placeholder.FindStringSubmatch(match)[1]
		switch name {
		case ".OS":
			return runtime.GOOS
		case ".ARCH":
			return runtime.GOARCH
		default:
			v, ok := os.LookupEnv(strings.TrimPrefix(name, ".Env."))
			if !ok || v == "" {
				missing = name
			}
			return v
		}
	})
	if missing != "" {
		return "", fmt.Errorf("template environment variable is unset or empty: %s", missing)
	}
	if strings.ContainsAny(value, "{}") {
		return "", fmt.Errorf("unsupported or malformed key template")
	}
	if err := validKey(value); err != nil {
		return "", err
	}
	return value, nil
}

func filePattern(pattern string) (*regexp.Regexp, error) {
	pattern, err := relativePath(pattern)
	if err != nil || strings.ContainsAny(pattern, "?[]{}") {
		return nil, fmt.Errorf("files supports relative filenames, * and ** only")
	}
	parts := strings.Split(pattern, "/")
	var b strings.Builder
	b.WriteString("^")
	for i, part := range parts {
		if part == "**" {
			if i == len(parts)-1 {
				b.WriteString(".*")
			} else {
				b.WriteString("(?:[^/]+/)*")
			}
			continue
		}
		if strings.Contains(part, "**") {
			return nil, fmt.Errorf("** must be a complete path segment")
		}
		b.WriteString(strings.ReplaceAll(regexp.QuoteMeta(part), `\*`, "[^/]*"))
		if i < len(parts)-1 {
			b.WriteString("/")
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

type keyFiles struct {
	workspace string
	tree      *object.Tree
}

func openKeyFiles(workspace string, fromGit bool, sha string) (*keyFiles, error) {
	files := &keyFiles{workspace: workspace}
	if !fromGit {
		return files, nil
	}
	decoded, err := hex.DecodeString(sha)
	if err != nil || len(decoded) != 20 {
		return nil, fmt.Errorf("commit-sha must be a complete 40-character Git commit SHA")
	}
	repo, err := git.PlainOpenWithOptions(workspace, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, fmt.Errorf("open Git repository: %w", err)
	}
	commit, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, fmt.Errorf("read commit: %w", err)
	}
	files.tree, err = commit.Tree()
	return files, err
}

func (f *keyFiles) names() ([]string, error) {
	var names []string
	if f.tree != nil {
		err := f.tree.Files().ForEach(func(file *object.File) error {
			if file.Mode == filemode.Regular || file.Mode == filemode.Executable || file.Mode == filemode.Deprecated {
				names = append(names, file.Name)
			}
			return nil
		})
		return names, err
	}
	err := filepath.WalkDir(f.workspace, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(f.workspace, name)
			if err != nil {
				return err
			}
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	return names, err
}

func (f *keyFiles) reader(name string) (io.ReadCloser, error) {
	if f.tree != nil {
		file, err := f.tree.File(name)
		if err != nil {
			return nil, err
		}
		return file.Reader()
	}
	// Root prevents a concurrent workspace link from escaping the input scope.
	root, err := os.OpenRoot(f.workspace)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Open(filepath.FromSlash(name))
}

func (f *keyFiles) digest(patterns []string) (string, error) {
	names, err := f.names()
	if err != nil {
		return "", err
	}
	selected := make(map[string]bool)
	for _, pattern := range patterns {
		matcher, err := filePattern(pattern)
		if err != nil {
			return "", fmt.Errorf("files pattern %q: %w", pattern, err)
		}
		matched := false
		for _, name := range names {
			if matcher.MatchString(name) {
				if _, err := relativePath(name); err != nil {
					return "", err
				}
				selected[name] = true
				matched = true
			}
		}
		if !matched {
			return "", fmt.Errorf("files pattern %q matched no regular files", pattern)
		}
	}
	names = names[:0]
	for name := range selected {
		names = append(names, name)
	}
	slices.Sort(names)
	hash := sha256.New()
	for _, name := range names {
		reader, err := f.reader(name)
		if err != nil {
			return "", fmt.Errorf("read key file %s: %w", name, err)
		}
		content := sha256.New()
		_, readErr := io.Copy(content, reader)
		closeErr := reader.Close()
		if readErr != nil {
			return "", readErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		// Frame names before fixed-size content hashes to avoid concatenation ambiguity.
		hash.Write(binary.BigEndian.AppendUint64(nil, uint64(len(name))))
		hash.Write([]byte(name))
		hash.Write(content.Sum(nil))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func resolveKeys(entries []cacheEntry, workspace string, fromGit bool, sha string) error {
	var files *keyFiles
	seen := make(map[string]bool)
	for i := range entries {
		e := &entries[i]
		var err error
		if e.Key.value != "" {
			e.resolved, err = expandKey(e.Key.value)
		} else {
			prefix, prefixErr := expandKey(e.Key.Prefix)
			if prefixErr != nil {
				return fmt.Errorf("cache[%d].key.prefix: %w", i, prefixErr)
			}
			if len(e.Key.Files) == 0 {
				return fmt.Errorf("cache[%d].key.files must not be empty", i)
			}
			if files == nil {
				files, err = openKeyFiles(workspace, fromGit, sha)
				if err != nil {
					return fmt.Errorf("cache[%d].key.files: %w", i, err)
				}
			}
			digest, digestErr := files.digest(e.Key.Files)
			if digestErr != nil {
				return fmt.Errorf("cache[%d].key.files: %w", i, digestErr)
			}
			e.resolved = prefix + "-" + digest
		}
		if err != nil {
			return fmt.Errorf("cache[%d].key: %w", i, err)
		}
		if seen[e.resolved] {
			return fmt.Errorf("cache[%d]: duplicate primary key", i)
		}
		seen[e.resolved] = true
		for j := range e.FallbackKeys {
			e.FallbackKeys[j], err = expandKey(e.FallbackKeys[j])
			if err != nil {
				return fmt.Errorf("cache[%d].fallback_keys[%d]: %w", i, j, err)
			}
		}
	}
	return nil
}
