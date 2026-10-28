package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drone-plugins/drone-plugin-lib/drone"
	"github.com/urfave/cli/v2"
)

func TestFileKeyIdentity(t *testing.T) {
	inWorkspace(t)
	for name, content := range map[string]string{"go.mod": "module example\n", "go.sum": "checksum\n"} {
		if err := os.WriteFile(name, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	first, err := resolveKey("deps-v1", []string{"go.mod", "go.sum"})
	if err != nil {
		t.Fatal(err)
	}
	if first != "deps-v1-c60515abc71806732243e0dfea3d1db9ea631333701ba4aeb7ac55a46f558dbd" {
		t.Fatalf("digest format changed: %s", first)
	}
	reordered, err := resolveKey("deps-v1", []string{"./go.sum", "go.mod", "go.mod"})
	if err != nil || first != reordered {
		t.Fatalf("ordering changed key: %s %s %v", first, reordered, err)
	}
	if err = os.Chtimes("go.mod", time.Unix(0, 0), time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	unchanged, err := resolveKey("deps-v1", []string{"go.mod", "go.sum"})
	if err != nil || first != unchanged {
		t.Fatalf("mtime changed key: %v", err)
	}
	if err = os.WriteFile("go.sum", []byte("different checksum"), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := resolveKey("deps-v1", []string{"go.mod", "go.sum"})
	if err != nil || first == changed {
		t.Fatalf("content change not reflected: %v", err)
	}
	stable, err := resolveKey("build-v1", nil)
	if err != nil || stable != "build-v1" {
		t.Fatalf("plain key changed: %s %v", stable, err)
	}
}
func TestFileKeyRejectsInvalidInputs(t *testing.T) {
	root := inWorkspace(t)
	external := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(external, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, "outside"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(external), "outside-dir"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("directory", 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`dir\lock`, "missing", "../secret", external, "outside", "outside-dir/secret", "directory", "*.lock", root} {
		if _, err := resolveKey("deps", []string{name}); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, key := range []string{"", ".", "..", "a/b", `a\b`, "line\nkey", strings.Repeat("x", 513)} {
		if _, err := resolveKey(key, nil); err == nil {
			t.Errorf("accepted key %q", key)
		}
	}
	if err := os.WriteFile("lock", []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveKey(strings.Repeat("x", 500), []string{"lock"}); err == nil {
		t.Fatal("accepted oversized final key")
	}
}
func keyPlugin(mode string) *Plugin {
	p := &Plugin{settings: Settings{Mode: mode, Root: "bucket/prefix", Key: "deps-v1"}}
	p.pipeline.Repo = drone.Repo{Owner: "team/subgroup", Name: "repo", Branch: "main"}
	p.pipeline.Commit.Branch = "feature"
	if mode == rebuildMode {
		p.settings.Mount = *cli.NewStringSlice("cache")
	}
	return p
}
func TestKeyConfigurationAndNamespace(t *testing.T) {
	for _, branch := range []string{"feature", "main", "tag"} {
		p := keyPlugin(restoreMode)
		p.pipeline.Commit.Branch = branch
		if err := p.validateMode(); err != nil {
			t.Fatal(err)
		}
		if got := p.keyPath(p.settings.resolvedKey); got != "bucket/prefix/team/subgroup/repo/keys/deps-v1/archive.tar" {
			t.Fatalf("unexpected path %s", got)
		}
		if p.settings.FallbackPath != "" {
			t.Fatal("new key mode added implicit branch fallback")
		}
	}
	for name, mutate := range map[string]func(*Plugin){
		"path":             func(p *Plugin) { p.settings.Path = "old" },
		"fallback_path":    func(p *Plugin) { p.settings.FallbackPath = "old" },
		"flush_path":       func(p *Plugin) { p.settings.FlushPath = "old" },
		"flush_mode":       func(p *Plugin) { p.settings.Mode = flushMode },
		"missing_root":     func(p *Plugin) { p.settings.Root = "" },
		"root_escape":      func(p *Plugin) { p.settings.Root = "bucket/../escape" },
		"owner_escape":     func(p *Plugin) { p.pipeline.Repo.Owner = "../other" },
		"missing_owner":    func(p *Plugin) { p.pipeline.Repo.Owner = "" },
		"repo_escape":      func(p *Plugin) { p.pipeline.Repo.Name = "../other" },
		"filename_escape":  func(p *Plugin) { p.settings.Filename = "../archive.tar" },
		"fallback_escape":  func(p *Plugin) { p.settings.FallbackKeys = *cli.NewStringSlice("../other") },
		"orphan_files":     func(p *Plugin) { p.settings.Key = ""; p.settings.KeyFiles = *cli.NewStringSlice("lock") },
		"orphan_fallbacks": func(p *Plugin) { p.settings.Key = ""; p.settings.FallbackKeys = *cli.NewStringSlice("other") },
	} {
		t.Run(name, func(t *testing.T) {
			p := keyPlugin(restoreMode)
			mutate(p)
			if err := p.validateMode(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
func TestLegacyDefaultPaths(t *testing.T) {
	p := keyPlugin(restoreMode)
	p.settings.Key = ""
	if err := p.validateMode(); err != nil {
		t.Fatal(err)
	}
	if p.settings.Path != "team/subgroup/repo/feature" || p.settings.FallbackPath != "team/subgroup/repo/main" {
		t.Fatalf("legacy defaults changed: %#v", p.settings)
	}
	p = keyPlugin(flushMode)
	p.settings.Key = ""
	if err := p.validateMode(); err != nil {
		t.Fatal(err)
	}
	if p.settings.FlushPath != "team/subgroup/repo" {
		t.Fatal("legacy flush path changed")
	}
}
