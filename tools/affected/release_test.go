package affected

import (
	"context"
	"os/exec"
	"testing"
)

func TestRelease(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")

	for _, tt := range []struct {
		name   string
		change map[string]string
		want   map[string]bool // plugin → whether its image changed
	}{
		{
			name:   "code of a package: the plugins that build it",
			change: map[string]string{"internal/a/a.go": "package a\n\nfunc A() int { return 2 }\n"},
			want:   map[string]bool{"p1": true, "p2": false},
		},
		{
			name:   "code that only a test imports: the plugins that build it, not those whose tests do",
			change: map[string]string{"internal/c/c.go": "package c\n\nfunc C() int { return 4 }\n"},
			want:   map[string]bool{"p1": false, "p2": true},
		},
		{
			name:   "a test of a plugin: no image",
			change: map[string]string{"plugins/p1/main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) {}\n"},
			want:   map[string]bool{"p1": false, "p2": false},
		},
		{
			name:   "testdata of a plugin: no image",
			change: map[string]string{"plugins/p1/testdata/in.txt": "in\n"},
			want:   map[string]bool{"p1": false, "p2": false},
		},
		{
			name:   "plugin.yaml: no image",
			change: map[string]string{"plugins/p1/plugin.yaml": "kinds: [media_image]\n"},
			want:   map[string]bool{"p1": false, "p2": false},
		},
		{
			name:   "a plugin's Dockerfile: that plugin",
			change: map[string]string{"plugins/p1/Dockerfile": "FROM busybox\n"},
			want:   map[string]bool{"p1": true, "p2": false},
		},
		{
			name:   "a dependency of one plugin: that plugin",
			change: map[string]string{"plugins/p1/go.mod": p1GoMod("ext2")},
			want:   map[string]bool{"p1": true, "p2": false},
		},
		{
			name:   "the CI: no image",
			change: map[string]string{".github/workflows/ci.yml": "on: push\n", "tools/x.go": "package tools\n"},
			want:   map[string]bool{"p1": false, "p2": false},
		},
		{
			name:   "what the images may copy: every image",
			change: map[string]string{".dockerignore": "*.md\n"},
			want:   map[string]bool{"p1": true, "p2": true},
		},
		{
			name:   "Markdown: no image",
			change: map[string]string{"README.md": "# changed\n", "plugins/p1/CHANGELOG.md": "# p1\n"},
			want:   map[string]bool{"p1": false, "p2": false},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t)
			for f, content := range tt.change {
				write(t, dir, f, content)
			}
			gitRun(t, dir, "add", "-A")
			gitRun(t, dir, "commit", "-q", "-m", "change")
			for plugin, want := range tt.want {
				got, err := Release(context.Background(), dir, "base", plugin)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("Release(%s) = %v, want %v", plugin, got, want)
				}
			}
		})
	}
}

func TestReleaseNoChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GOPROXY", "off")
	dir := repo(t)
	got, err := Release(context.Background(), dir, "base", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("Release = true for no change")
	}
}

func TestScope(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GOPROXY", "off")
	dir := repo(t)
	in, err := Scope(context.Background(), dir, "p1")
	if err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]bool{
		"plugins/p1/main.go":         true,
		"plugins/p1/Dockerfile":      true,
		"plugins/p1/go.mod":          true,
		"plugins/p1/gone.go":         true, // a deleted file of the plugin
		"internal/a/a.go":            true,
		"internal/a/gone.go":         true, // a deleted file of a package it builds
		"third_party/ext1/ext.go":    true,
		"third_party/ext1/go.mod":    true,
		"go.mod":                     true,
		".dockerignore":              true,
		".tool-versions":             true,
		"plugins/p1/README.md":       false,
		"plugins/p1/plugin.yaml":     false,
		"plugins/p1/main_test.go":    false,
		"plugins/p1/testdata/in.txt": false,
		"internal/a/a_test.go":       false,
		"internal/a/testdata/in.txt": false,
		"internal/b/b.go":            false,
		"internal/c/c.go":            false, // imported by a test of internal/a only
		"third_party/ext2/ext.go":    false,
		"plugins/p2/main.go":         false,
		".github/workflows/ci.yml":   false,
		"README.md":                  false,
	} {
		if got := in(f); got != want {
			t.Errorf("Scope(p1)(%s) = %v, want %v", f, got, want)
		}
	}
}
