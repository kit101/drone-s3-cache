package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func commitFiles(t *testing.T, repo *git.Repository) string {
	t.Helper()
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatal(err)
	}
	hash, err := wt.Commit("snapshot", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	return hash.String()
}

func TestGitSnapshotIgnoresWorktreeAndHeadChanges(t *testing.T) {
	inWorkspace(t)
	workspace, _ := os.Getwd()
	writeFile(t, "pom.xml", "root")
	writeFile(t, "modules/a/pom.xml", "module")
	writeFile(t, "modules/deep/b/pom.xml", "deep")
	repo, err := git.PlainInit(workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	sha := commitFiles(t, repo)
	files, err := openKeyFiles(workspace, true, sha)
	if err != nil {
		t.Fatal(err)
	}
	before, err := files.digest([]string{"./**/pom.xml"})
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := openKeyFiles(workspace, false, "")
	if err != nil {
		t.Fatal(err)
	}
	same, err := worktree.digest([]string{"pom.xml", "**/pom.xml", "./**/pom.xml"})
	if err != nil || same != before {
		t.Fatalf("sources differ: %s %s %v", before, same, err)
	}
	writeFile(t, "unrelated.txt", "next commit")
	sha2 := commitFiles(t, repo)
	other, err := openKeyFiles(workspace, true, sha2)
	if err != nil {
		t.Fatal(err)
	}
	same, err = other.digest([]string{"**/pom.xml"})
	if err != nil || same != before {
		t.Fatalf("unrelated commit changed key: %v", err)
	}
	writeFile(t, "pom.xml", "modified")
	if err := os.Remove("modules/a/pom.xml"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, "target/generated/pom.xml", "generated")
	t.Setenv("PATH", "")
	original, err := openKeyFiles(workspace, true, sha)
	if err != nil {
		t.Fatal(err)
	}
	after, err := original.digest([]string{"**/pom.xml"})
	if err != nil || after != before {
		t.Fatalf("Git snapshot drifted: %s %s %v", before, after, err)
	}
	changed, err := worktree.digest([]string{"**/pom.xml"})
	if err != nil || changed == before {
		t.Fatalf("worktree did not change: %v", err)
	}
	if _, err := openKeyFiles(workspace, true, strings.Repeat("0", 40)); err == nil {
		t.Fatal("missing commit accepted")
	}
	if _, err := openKeyFiles(workspace, true, ""); err == nil {
		t.Fatal("missing SHA accepted")
	}
}

func TestFilePatternsAndDigest(t *testing.T) {
	inWorkspace(t)
	workspace, _ := os.Getwd()
	for name, body := range map[string]string{"pom.xml": "root", "modules/pom.xml": "parent", "modules/a/pom.xml": "a", "modules/a/b/pom.xml": "b"} {
		writeFile(t, name, body)
	}
	for _, tc := range []struct {
		pattern string
		matches []string
	}{
		{"./**/pom.xml", []string{"pom.xml", "modules/pom.xml", "modules/a/pom.xml", "modules/a/b/pom.xml"}},
		{"modules/*/pom.xml", []string{"modules/a/pom.xml"}},
		{"modules/**/pom.xml", []string{"modules/pom.xml", "modules/a/pom.xml", "modules/a/b/pom.xml"}},
	} {
		matcher, err := filePattern(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range tc.matches {
			if !matcher.MatchString(name) {
				t.Errorf("%s missed %s", tc.pattern, name)
			}
		}
		if tc.pattern == "modules/*/pom.xml" && matcher.MatchString("modules/a/b/pom.xml") {
			t.Fatal("* crossed directory")
		}
	}
	files, _ := openKeyFiles(workspace, false, "")
	before, err := files.digest([]string{"**/pom.xml"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := files.digest([]string{"modules/**/pom.xml", "pom.xml", "./**/pom.xml"})
	if err != nil || same != before {
		t.Fatal("pattern order or overlap changed digest")
	}
	if err := os.Rename("modules/a/b/pom.xml", "modules/a/b/renamed.xml"); err != nil {
		t.Fatal(err)
	}
	after, err := files.digest([]string{"**/pom.xml"})
	if err != nil || after == before {
		t.Fatal("file removal did not change digest")
	}
	for _, pattern := range []string{"absent.xml", "../pom.xml", "/pom.xml", "x/**foo", "**/{pom,package}.xml", "**/pom?.xml"} {
		if _, err := files.digest([]string{pattern}); err == nil {
			t.Errorf("accepted %s", pattern)
		}
	}
}

func TestKeyTemplates(t *testing.T) {
	t.Setenv("CACHE_VERSION", "v1")
	got, err := expandKey("go-{{ .OS }}-{{ .ARCH }}-{{ .Env.CACHE_VERSION }}")
	if err != nil || got != "go-"+runtime.GOOS+"-"+runtime.GOARCH+"-v1" {
		t.Fatalf("%s %v", got, err)
	}
	t.Setenv("EMPTY", "")
	for _, key := range []string{"{{ .Missing }}", "{{ .Env.EMPTY }}", "{{ .Env.NOT_SET_FOR_CACHE_TEST }}", "{{ .OS }", "{{if .OS}}x{{end}}", "../x", "..", "a/b"} {
		if _, err := expandKey(key); err == nil {
			t.Errorf("accepted %s", key)
		}
	}
}
