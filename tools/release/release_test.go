package release

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const goVersion = "1.27.4"

func v(s string) Version {
	ver, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return ver
}

func TestParseVersion(t *testing.T) {
	for _, s := range []string{"0.1.0", "1.2.3", "0.2.0-rc.1", "1.0.0-alpha.beta-1"} {
		got, err := ParseVersion(s)
		if err != nil || got.String() != s {
			t.Errorf("ParseVersion(%q) = %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "v0.1.0", "01.0.0", "1.2", "1.2.3-", "1.2.3+build", "1.2.3-rc..1"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) succeeded", s)
		}
	}
}

func TestCompare(t *testing.T) {
	// In order of precedence, as in the example of Semantic Versioning 2.0.0, §11.
	order := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := range order {
		for j := range order {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(v(order[i]), v(order[j])); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestNext(t *testing.T) {
	for _, tt := range []struct {
		from string
		bump Bump
		want string
	}{
		{"0.1.0", BumpPatch, "0.1.1"},
		{"0.1.3", BumpMinor, "0.2.0"},
		{"0.1.3", BumpMajor, "0.2.0"}, // before 1.0.0, a breaking change moves the minor version
		{"1.2.3", BumpPatch, "1.2.4"},
		{"1.2.3", BumpMinor, "1.3.0"},
		{"1.2.3", BumpMajor, "2.0.0"},
	} {
		if got := v(tt.from).Next(tt.bump).String(); got != tt.want {
			t.Errorf("%s.Next(%d) = %s, want %s", tt.from, tt.bump, got, tt.want)
		}
	}
}

func TestParseCommit(t *testing.T) {
	for _, tt := range []struct {
		subject, body string
		want          Commit
	}{
		{"feat(libvips): add a plugin", "", Commit{Type: "feat", Scope: "libvips", Description: "add a plugin"}},
		{"fix: a crash", "", Commit{Type: "fix", Description: "a crash"}},
		{"feat(server)!: drop http://", "", Commit{Type: "feat", Scope: "server", Description: "drop http://", Breaking: true}},
		{"refactor: move code", "Some text.\n\nBREAKING CHANGE: the flag -x is gone\n", Commit{Type: "refactor", Description: "move code", Breaking: true}},
		{"Update the README", "", Commit{Description: "Update the README"}},
	} {
		got := parseCommit("", tt.subject, tt.body)
		if got != tt.want {
			t.Errorf("parseCommit(%q) = %+v, want %+v", tt.subject, got, tt.want)
		}
	}
}

var date = time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)

func TestSection(t *testing.T) {
	last := v("0.1.0")
	p := Plan{Plugin: "my-media-ffmpeg", Last: &last, Next: v("0.2.0"), Commits: []Commit{
		{Hash: "1111111aaaa", Type: "fix", Scope: "ffmpeg", Description: "keep the rotation"},
		{Hash: "2222222bbbb", Type: "feat", Scope: "server", Description: "serve over vsock"},
		{Hash: "3333333cccc", Type: "chore", Scope: "deps", Description: "bump the Debian image"},
		{Hash: "4444444dddd", Type: "fix", Description: "close the socket"},
	}}
	want := `## 0.2.0 (2026-11-27)

### Features

- **server:** serve over vsock ([2222222](https://example.com/r/commit/2222222bbbb))

### Fixes

- keep the rotation ([1111111](https://example.com/r/commit/1111111aaaa))
- close the socket ([4444444](https://example.com/r/commit/4444444dddd))

### Other changes

- **deps:** bump the Debian image ([3333333](https://example.com/r/commit/3333333cccc))
`
	if got := Section(p, "https://example.com/r", date); got != want {
		t.Errorf("Section =\n%s\nwant\n%s", got, want)
	}
}

func TestWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plugins", "p1"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := Plan{Plugin: "p1", Next: v("0.1.0")}
	if err := Write(root, []Plan{first}, "https://example.com/r", date); err != nil {
		t.Fatal(err)
	}
	last := v("0.1.0")
	second := Plan{Plugin: "p1", Last: &last, Next: v("0.1.1"), Commits: []Commit{{Hash: "abcdef0123", Type: "fix", Description: "a fix"}}}
	if err := Write(root, []Plan{second}, "https://example.com/r", date.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "plugins", "p1", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := `# Changelog of p1

## 0.1.1 (2026-11-28)

### Fixes

- a fix ([abcdef0](https://example.com/r/commit/abcdef0123))

## 0.1.0 (2026-11-27)

First release.
`
	if string(got) != want {
		t.Errorf("CHANGELOG.md =\n%s\nwant\n%s", got, want)
	}
	if top, ok, err := TopVersion(root, "p1"); err != nil || !ok || top.String() != "0.1.1" {
		t.Errorf("TopVersion = %v, %v, %v", top, ok, err)
	}
}

// fixture is a repository shaped like this one: plugin p1 imports internal/a, and p2 imports nothing of the root.
var fixture = map[string]string{
	"go.mod":               "module github.com/mycroblog/plugin\n\ngo " + goVersion + "\n",
	"internal/a/a.go":      "package a\n\nfunc A() int { return 1 }\n",
	"internal/a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
	"plugins/p1/go.mod":    pluginGoMod("p1"),
	"plugins/p1/main.go":   "package main\n\nimport \"github.com/mycroblog/plugin/internal/a\"\n\nfunc main() { _ = a.A() }\n",
	"plugins/p2/go.mod":    pluginGoMod("p2"),
	"plugins/p2/main.go":   "package main\n\nfunc main() {}\n",
}

func pluginGoMod(name string) string {
	return "module github.com/mycroblog/plugin/plugins/" + name + "\n\ngo " + goVersion + "\n\n" +
		"require github.com/mycroblog/plugin v0.0.0-00010101000000-000000000000\n\nreplace github.com/mycroblog/plugin => ../..\n"
}

func TestPendingAndTags(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	ctx := context.Background()
	root := t.TempDir()
	for f, content := range fixture {
		write(t, root, f, content)
	}
	gitRun(t, root, "init", "-q", "-b", "main")
	commit(t, root, "chore: the repository")

	// Never released: every plugin, at 0.1.0.
	plans, err := Pending(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(plans); !reflect.DeepEqual(got, []string{"p1 -> 0.1.0", "p2 -> 0.1.0"}) {
		t.Fatalf("Pending = %v", got)
	}

	// The release pull request, merged: its versions get their tags, and nothing is pending.
	if err := Write(root, plans, "https://example.com/r", date); err != nil {
		t.Fatal(err)
	}
	commit(t, root, "chore: release p1 0.1.0, p2 0.1.0")
	if plans, err := Pending(ctx, root); err != nil || len(plans) != 0 {
		t.Fatalf("Pending before the tags = %v, %v; want none: the versions wait for their tags", summary(plans), err)
	}
	tags, err := Tags(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tags, []string{"plugins/p1/v0.1.0", "plugins/p2/v0.1.0"}) {
		t.Fatalf("Tags = %v", tags)
	}
	for _, tag := range tags {
		gitRun(t, root, "tag", tag)
	}
	if tags, err := Tags(ctx, root); err != nil || len(tags) != 0 {
		t.Fatalf("Tags after tagging = %v, %v", tags, err)
	}
	if plans, err := Pending(ctx, root); err != nil || len(plans) != 0 {
		t.Fatalf("Pending after tagging = %v, %v", summary(plans), err)
	}

	// A test, a fix of internal/a, and a feature of p2: p1 gets a patch, p2 a minor version.
	write(t, root, "internal/a/a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n")
	commit(t, root, "test: call A")
	write(t, root, "internal/a/a.go", "package a\n\nfunc A() int { return 2 }\n")
	commit(t, root, "fix(a): return 2")
	write(t, root, "plugins/p2/main.go", "package main\n\nfunc main() { println() }\n")
	commit(t, root, "feat(p2): print a line")
	plans, err = Pending(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(plans); !reflect.DeepEqual(got, []string{"p1 0.1.0 -> 0.1.1", "p2 0.1.0 -> 0.2.0"}) {
		t.Fatalf("Pending = %v", got)
	}
	if got := descriptions(plans[0]); !reflect.DeepEqual(got, []string{"return 2"}) {
		t.Errorf("p1's commits = %v, want the fix only", got)
	}
	if got := descriptions(plans[1]); !reflect.DeepEqual(got, []string{"print a line"}) {
		t.Errorf("p2's commits = %v, want the feature only", got)
	}

	// A pre-release of p2, then a fix: the fix goes into the release of that pre-release.
	gitRun(t, root, "tag", "plugins/p2/v0.2.0-rc.1")
	write(t, root, "plugins/p2/main.go", "package main\n\nfunc main() { println(1) }\n")
	commit(t, root, "fix(p2): print 1")
	plans, err = Pending(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(plans); !reflect.DeepEqual(got, []string{"p1 0.1.0 -> 0.1.1", "p2 0.2.0-rc.1 -> 0.2.0"}) {
		t.Fatalf("Pending = %v", got)
	}

	// A changelog edited by hand to a version before the last release is refused.
	write(t, root, "plugins/p1/CHANGELOG.md", "# Changelog of p1\n\n## 0.0.9 (2026-11-27)\n\nOld.\n")
	if _, err := Tags(ctx, root); err == nil || !strings.Contains(err.Error(), "before its last release") {
		t.Errorf("Tags with an older version = %v, want an error", err)
	}
}

func summary(plans []Plan) []string {
	var out []string
	for _, p := range plans {
		if p.Last == nil {
			out = append(out, p.Plugin+" -> "+p.Next.String())
		} else {
			out = append(out, p.Plugin+" "+p.Last.String()+" -> "+p.Next.String())
		}
	}
	return out
}

func descriptions(p Plan) []string {
	var out []string
	for _, c := range p.Commits {
		out = append(out, c.Description)
	}
	return out
}

func commit(t *testing.T, dir, message string) {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", message)
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
	cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}
