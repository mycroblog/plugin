// Package release prepares the releases of the plugins, one pull request for all of them:
//
//   - Pending lists the plugins whose image changed since their last release (affected.Release), each with its next
//     version, from the Conventional Commits that reach its image (affected.Scope);
//   - Write puts that version, with those commits, at the top of the plugin's CHANGELOG.md, and Body and Title describe
//     the pull request;
//   - Tags, once the pull request is merged, lists the tags of the versions at the top of the changelogs that are not
//     tagged yet. Each tag, plugins/<name>/v<version>, releases the plugin's image (.github/workflows/release.yml).
//
// The tags are what was released: the last release of a plugin is its highest tag, and the changelogs only carry the
// next version until it is tagged, so a version can be edited in the pull request before it is merged.
package release

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mycroblog/plugin/tools/affected"
)

// Plan is the next release of a plugin.
type Plan struct {
	Plugin  string   `json:"plugin"`
	Last    *Version `json:"-"` // its highest tag, or nil for its first release
	Next    Version  `json:"-"`
	Commits []Commit `json:"-"` // since Last, oldest first, that reach its image
}

// TagName returns the tag of a version of a plugin.
func TagName(plugin string, v Version) string {
	return "plugins/" + plugin + "/v" + v.String()
}

// Pending returns the next release of each plugin of the repository at root whose image changed since its last
// release, or that was never released, in the order of their names.
func Pending(ctx context.Context, root string) ([]Plan, error) {
	plugins, err := affected.Plugins(root)
	if err != nil {
		return nil, err
	}
	var plans []Plan
	for _, name := range plugins {
		p, ok, err := pending(ctx, root, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if ok {
			plans = append(plans, p)
		}
	}
	return plans, nil
}

func pending(ctx context.Context, root, name string) (Plan, bool, error) {
	tags, err := tagged(ctx, root, name)
	if err != nil {
		return Plan{}, false, err
	}
	top, hasTop, err := TopVersion(root, name)
	if err != nil {
		return Plan{}, false, err
	}
	if len(tags) == 0 {
		if hasTop {
			return Plan{}, false, nil // its first version waits for its tag
		}
		return Plan{Plugin: name, Next: Version{Minor: 1}}, true, nil
	}

	last := tags[len(tags)-1]
	if hasTop && Compare(top, last) > 0 {
		return Plan{}, false, nil // its next version waits for its tag
	}
	changed, err := affected.Release(ctx, root, TagName(name, last), name)
	if err != nil || !changed {
		return Plan{}, false, err
	}
	commits, err := commitsSince(ctx, root, TagName(name, last), name)
	if err != nil {
		return Plan{}, false, err
	}

	// The next version counts from the last release, and comes after the last pre-release: a fix after 0.2.0-rc.1
	// is 0.2.0, not 0.1.1.
	bump := BumpPatch
	for _, c := range commits {
		bump = max(bump, c.bump())
	}
	base := Version{}
	for _, v := range tags {
		if v.Pre == "" {
			base = v
		}
	}
	next := base.Next(bump)
	if Compare(last.Core(), next) > 0 || last.Pre != "" && Compare(last.Core(), next) == 0 {
		next = last.Core()
	}
	return Plan{Plugin: name, Last: &last, Next: next, Commits: commits}, true, nil
}

// Tags returns the tags to create for the versions at the top of the changelogs of the repository at root that are
// not tagged yet. A version that does not come after the plugin's last release is an error.
func Tags(ctx context.Context, root string) ([]string, error) {
	plugins, err := affected.Plugins(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range plugins {
		top, ok, err := TopVersion(root, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if !ok {
			continue
		}
		tags, err := tagged(ctx, root, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if slices.ContainsFunc(tags, func(v Version) bool { return Compare(v, top) == 0 }) {
			continue
		}
		if len(tags) > 0 && Compare(top, tags[len(tags)-1]) < 0 {
			return nil, fmt.Errorf("%s: CHANGELOG.md says %s, which comes before its last release, %s", name, top, tags[len(tags)-1])
		}
		out = append(out, TagName(name, top))
	}
	return out, nil
}

// tagged returns the versions of the plugin's tags, lowest first. A tag that is not a version is ignored.
func tagged(ctx context.Context, root, name string) ([]Version, error) {
	out, err := git(ctx, root, "tag", "--list", "plugins/"+name+"/v*")
	if err != nil {
		return nil, err
	}
	var vs []Version
	for t := range strings.Lines(out) {
		if v, err := ParseVersion(strings.TrimPrefix(strings.TrimSpace(t), "plugins/"+name+"/v")); err == nil {
			vs = append(vs, v)
		}
	}
	slices.SortFunc(vs, Compare)
	return vs, nil
}

// commitsSince returns the commits since base that reach the plugin's image, oldest first, merges left out.
func commitsSince(ctx context.Context, root, base, name string) ([]Commit, error) {
	inScope, err := affected.Scope(ctx, root, name)
	if err != nil {
		return nil, err
	}
	// One record per commit: \x1e hash \x00 subject \x00 body \x00, then the files it changed, one per line.
	out, err := git(ctx, root, "log", "--reverse", "--no-merges", "--no-renames", "--name-only",
		"--format=%x1e%H%x00%s%x00%b%x00", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for rec := range strings.SplitSeq(out, "\x1e") {
		fields := strings.SplitN(rec, "\x00", 4)
		if len(fields) < 4 {
			continue
		}
		if slices.ContainsFunc(strings.Fields(fields[3]), inScope) {
			commits = append(commits, parseCommit(fields[0], fields[1], fields[2]))
		}
	}
	return commits, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func changelogPath(root, name string) string {
	return filepath.Join(root, "plugins", name, "CHANGELOG.md")
}

// TopVersion returns the version of the first heading "## <version>" of the plugin's CHANGELOG.md, and whether there
// is one.
func TopVersion(root, name string) (Version, bool, error) {
	b, err := os.ReadFile(changelogPath(root, name))
	if os.IsNotExist(err) {
		return Version{}, false, nil
	}
	if err != nil {
		return Version{}, false, err
	}
	for line := range strings.Lines(string(b)) {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			f := strings.Fields(h)
			if len(f) == 0 {
				break
			}
			v, err := ParseVersion(f[0])
			if err != nil {
				return Version{}, false, fmt.Errorf("CHANGELOG.md: %w", err)
			}
			return v, true, nil
		}
	}
	return Version{}, false, nil
}
