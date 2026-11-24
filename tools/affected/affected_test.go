package affected

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const goVersion = "1.27.4"

// fixture is a repository shaped like this one, committed once as the base of every change:
//
//	internal/a          imported by internal/b and by plugin p1; its test imports internal/c
//	internal/b          imports internal/a
//	internal/c          imported by plugin p2
//	plugins/p1          imports internal/a and example.com/ext, which a replace points at third_party/ext1
//	plugins/p2          imports internal/c
var fixture = map[string]string{
	"go.mod":                     "module github.com/mycroblog/plugin\n\ngo " + goVersion + "\n",
	"go.work":                    "go " + goVersion + "\n\nuse (\n\t.\n\t./plugins/p1\n\t./plugins/p2\n)\n",
	"README.md":                  "# fixture\n",
	"internal/a/a.go":            "package a\n\nfunc A() int { return 1 }\n",
	"internal/a/a_test.go":       "package a\n\nimport (\n\t\"testing\"\n\n\t\"github.com/mycroblog/plugin/internal/c\"\n)\n\nfunc TestA(t *testing.T) { _ = c.C() }\n",
	"internal/a/testdata/in.txt": "in\n",
	"internal/b/b.go":            "package b\n\nimport \"github.com/mycroblog/plugin/internal/a\"\n\nfunc B() int { return a.A() }\n",
	"internal/c/c.go":            "package c\n\nfunc C() int { return 3 }\n",
	"third_party/ext1/go.mod":    "module example.com/ext\n\ngo " + goVersion + "\n",
	"third_party/ext1/ext.go":    "package ext\n\nfunc E() int { return 1 }\n",
	"third_party/ext2/go.mod":    "module example.com/ext\n\ngo " + goVersion + "\n",
	"third_party/ext2/ext.go":    "package ext\n\nfunc E() int { return 2 }\n",
	"plugins/p1/go.mod": "module github.com/mycroblog/plugin/plugins/p1\n\ngo " + goVersion + "\n\n" +
		"require example.com/ext v0.0.0\n\nreplace example.com/ext => ../../third_party/ext1\n",
	"plugins/p1/main.go":    "package main\n\nimport (\n\t\"example.com/ext\"\n\n\t\"github.com/mycroblog/plugin/internal/a\"\n)\n\nfunc main() { _, _ = a.A(), ext.E() }\n",
	"plugins/p1/Dockerfile": "FROM scratch\n",
	"plugins/p1/README.md":  "# p1\n",
	"plugins/p2/go.mod":     "module github.com/mycroblog/plugin/plugins/p2\n\ngo " + goVersion + "\n",
	"plugins/p2/main.go":    "package main\n\nimport \"github.com/mycroblog/plugin/internal/c\"\n\nfunc main() { _ = c.C() }\n",
}

const (
	pkgA = "github.com/mycroblog/plugin/internal/a"
	pkgB = "github.com/mycroblog/plugin/internal/b"
	pkgC = "github.com/mycroblog/plugin/internal/c"
)

func TestCompute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")

	for _, tt := range []struct {
		name   string
		change map[string]string // file → new content; "" removes the file
		want   Result
	}{
		{
			name:   "code of a package: it, its importers, and the plugins that build it",
			change: map[string]string{"internal/a/a.go": "package a\n\nfunc A() int { return 2 }\n"},
			want:   Result{Packages: []string{pkgA, pkgB}, Plugins: []string{"p1"}, Contract: []string{"p1"}},
		},
		{
			name:   "a test file: the package's tests only",
			change: map[string]string{"internal/a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n"},
			want:   Result{Packages: []string{pkgA}, Plugins: []string{}, Contract: []string{}},
		},
		{
			name:   "testdata: the package's tests only",
			change: map[string]string{"internal/a/testdata/in.txt": "changed\n"},
			want:   Result{Packages: []string{pkgA}, Plugins: []string{}, Contract: []string{}},
		},
		{
			name:   "code that a test imports: that package, the test's package, and the plugins that build the first",
			change: map[string]string{"internal/c/c.go": "package c\n\nfunc C() int { return 4 }\n"},
			want:   Result{Packages: []string{pkgA, pkgC}, Plugins: []string{"p2"}, Contract: []string{"p2"}},
		},
		{
			name:   "a plugin's code: that plugin only",
			change: map[string]string{"plugins/p2/main.go": "package main\n\nimport \"github.com/mycroblog/plugin/internal/c\"\n\nfunc main() { _ = c.C() + 1 }\n"},
			want:   Result{Packages: []string{}, Plugins: []string{"p2"}, Contract: []string{"p2"}},
		},
		{
			name: "a new plugin, with go.work: the new plugin only",
			change: map[string]string{
				"go.work":            "go " + goVersion + "\n\nuse (\n\t.\n\t./plugins/p1\n\t./plugins/p2\n\t./plugins/p3\n)\n",
				"plugins/p3/go.mod":  "module github.com/mycroblog/plugin/plugins/p3\n\ngo " + goVersion + "\n",
				"plugins/p3/main.go": "package main\n\nimport \"github.com/mycroblog/plugin/internal/c\"\n\nfunc main() { _ = c.C() }\n",
			},
			want: Result{Packages: []string{}, Plugins: []string{"p3"}, Contract: []string{"p3"}},
		},
		{
			name:   "a plugin's Dockerfile: that plugin",
			change: map[string]string{"plugins/p1/Dockerfile": "FROM busybox\n"},
			want:   Result{Packages: []string{}, Plugins: []string{"p1"}, Contract: []string{"p1"}},
		},
		{
			name:   "Markdown only: nothing",
			change: map[string]string{"README.md": "# changed\n", "plugins/p1/README.md": "# changed\n"},
			want:   Result{Packages: []string{}, Plugins: []string{}, Contract: []string{}},
		},
		{
			name: "a dependency of one plugin: that plugin only",
			change: map[string]string{"plugins/p1/go.mod": "module github.com/mycroblog/plugin/plugins/p1\n\ngo " + goVersion + "\n\n" +
				"require example.com/ext v0.0.0\n\nreplace example.com/ext => ../../third_party/ext2\n"},
			want: Result{Packages: []string{}, Plugins: []string{"p1"}, Contract: []string{"p1"}},
		},
		{
			name:   "the code of a dependency: the plugins that build it",
			change: map[string]string{"third_party/ext1/ext.go": "package ext\n\nfunc E() int { return 3 }\n"},
			want:   Result{Packages: []string{}, Plugins: []string{"p1"}, Contract: []string{"p1"}},
		},
		{
			name:   "a deleted code file: its package and its importers",
			change: map[string]string{"internal/b/extra.go": ""},
			want:   Result{Packages: []string{pkgB}, Plugins: []string{}, Contract: []string{}},
		},
		{
			name:   "the contract's version: the contract of every plugin",
			change: map[string]string{"buf.gen.yaml": "version: v2\n"},
			want:   Result{Packages: []string{}, Plugins: []string{}, Contract: []string{"p1", "p2"}},
		},
		{
			name:   "the harness: everything",
			change: map[string]string{".github/workflows/ci.yml": "on: push\n"},
			want:   Result{All: true, Packages: []string{pkgA, pkgB, pkgC}, Plugins: []string{"p1", "p2"}, Contract: []string{"p1", "p2"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t)
			if strings.Contains(tt.name, "deleted") {
				// The file to delete exists at the base.
				write(t, dir, "internal/b/extra.go", "package b\n\nfunc Extra() {}\n")
				gitRun(t, dir, "add", "-A")
				gitRun(t, dir, "commit", "-q", "-m", "extra")
				gitRun(t, dir, "branch", "-f", "base")
			}
			for f, content := range tt.change {
				if content == "" {
					if err := os.Remove(filepath.Join(dir, f)); err != nil {
						t.Fatal(err)
					}
					continue
				}
				write(t, dir, f, content)
			}
			gitRun(t, dir, "add", "-A")
			gitRun(t, dir, "commit", "-q", "-m", "change")

			got, err := Compute(context.Background(), dir, "base")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Compute =\n  %+v\nwant\n  %+v", got, tt.want)
			}
		})
	}
}

func TestComputeUncommitted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GOPROXY", "off")
	dir := repo(t)
	// A change still in the working tree counts, as when one runs the tool before committing.
	write(t, dir, "internal/c/c.go", "package c\n\nfunc C() int { return 5 }\n")
	got, err := Compute(context.Background(), dir, "base")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"p2"}; !reflect.DeepEqual(got.Plugins, want) {
		t.Errorf("Plugins = %v, want %v", got.Plugins, want)
	}
}

// repo writes the fixture in a new repository, commits it, and points the branch base at the commit.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for f, content := range fixture {
		write(t, dir, f, content)
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "branch", "base")
	return dir
}

func write(t *testing.T, dir, f, content string) {
	t.Helper()
	p := filepath.Join(dir, f)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}
