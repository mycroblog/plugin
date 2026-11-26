// Package affected decides what CI tests for a change: the packages of the root module whose code changed, directly
// or through what they import, and the plugins that build any such package or whose own files changed.
//
// It reads the import graph with go list in the builds CI and the container images run: each module on its own
// (GOWORK=off). A plugin's go.mod replaces the root module with ../.., so a plugin builds with the root module of the
// same commit and nothing else, and a dependency that one plugin raises never reaches another, nor the root module.
//
// The code of a package changed when one of its code files changed (Go, cgo, ignored, embedded, and other files), a
// package its code imports changed code, a module it belongs to changed version in that build, or the go or toolchain
// directive of its module changed. A package is affected, and its tests run, when its code changed, one of its test
// files or a file of its testdata/ changed, or a package its tests import changed code. A change to tests only does
// not reach the packages that import the package.
//
// A plugin is affected when its build holds an affected package, or a file under plugins/<name>/ changed but its
// Markdown. Everything is affected when the harness itself changed (see harness). The contract tests also run for every
// plugin when buf.gen.yaml changed, since it pins the version of mycroblog/my whose contract they run.
package affected

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Result is what to test.
type Result struct {
	// All is true when the harness changed, and everything below lists everything.
	All bool `json:"all"`
	// Packages are the affected packages of the root module, for the unit tests.
	Packages []string `json:"packages"`
	// Plugins are the affected plugins (directory names under plugins/), for their integration tests.
	Plugins []string `json:"plugins"`
	// Contract are the plugins whose contract tests run.
	Contract []string `json:"contract"`
}

// harness are the files whose change affects everything: the CI, the tools and scripts it runs, the Go version, and
// what the images may copy.
var harness = []string{".github/workflows/", "tools/", "scripts/", ".tool-versions", ".dockerignore"}

// contractPin is the file that pins the version of the contract the plugins are tested against.
const contractPin = "buf.gen.yaml"

// Everything returns everything to test, as for a change of the harness: for the scheduled and manual runs.
func Everything(ctx context.Context, root string) (Result, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Result{}, err
	}
	plugins, err := pluginNames(root)
	if err != nil {
		return Result{}, err
	}
	return everything(ctx, root, plugins)
}

// Compute returns what to test in the repository at root, checked out at the head of the change, for the change since
// base: a revision, compared from its merge base with HEAD, working tree included.
func Compute(ctx context.Context, root, base string) (Result, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Result{}, err
	}
	mergeBase, changed, err := changedSince(ctx, root, base)
	if err != nil {
		return Result{}, err
	}

	plugins, err := pluginNames(root)
	if err != nil {
		return Result{}, err
	}
	if slices.ContainsFunc(changed, isHarness) {
		return everything(ctx, root, plugins)
	}

	baseDir, cleanup, err := worktree(ctx, root, mergeBase)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	var res Result
	rootBuild := build{root: root, dir: root}
	pkgs, err := rootBuild.affected(ctx, baseDir, changed)
	if err != nil {
		return Result{}, fmt.Errorf("root module: %w", err)
	}
	res.Packages = pkgs

	for _, name := range plugins {
		dir := filepath.Join(root, "plugins", name)
		b := build{root: root, dir: dir, plugin: name}
		pkgs, err := b.affected(ctx, baseDir, changed)
		if err != nil {
			return Result{}, fmt.Errorf("plugin %s: %w", name, err)
		}
		own := slices.ContainsFunc(changed, func(f string) bool {
			return strings.HasPrefix(f, "plugins/"+name+"/") && path.Ext(f) != ".md"
		})
		if len(pkgs) > 0 || own {
			res.Plugins = append(res.Plugins, name)
		}
	}
	res.Contract = res.Plugins
	if slices.Contains(changed, contractPin) {
		res.Contract = plugins
	}
	res.Packages, res.Plugins, res.Contract = nonNil(res.Packages), nonNil(res.Plugins), nonNil(res.Contract)
	return res, nil
}

// changedSince returns the merge base of base and HEAD, and the files changed since it, working tree included.
func changedSince(ctx context.Context, root, base string) (string, []string, error) {
	mergeBase, err := git(ctx, root, "merge-base", base, "HEAD")
	if err != nil {
		return "", nil, err
	}
	mergeBase = strings.TrimSpace(mergeBase)
	out, err := git(ctx, root, "diff", "--name-only", "--no-renames", mergeBase)
	if err != nil {
		return "", nil, err
	}
	untracked, err := git(ctx, root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", nil, err
	}
	return mergeBase, slices.Compact(slices.Sorted(slices.Values(append(lines(out), lines(untracked)...)))), nil
}

func isHarness(f string) bool {
	return slices.ContainsFunc(harness, func(h string) bool {
		return f == h || strings.HasSuffix(h, "/") && strings.HasPrefix(f, h)
	})
}

func everything(ctx context.Context, root string, plugins []string) (Result, error) {
	nodes, err := build{root: root, dir: root}.list(ctx)
	if err != nil {
		return Result{}, err
	}
	mainPath := moduleOf(root)
	var all []string
	for ip, n := range nodes {
		if n.module != nil && n.module.Path == mainPath {
			all = append(all, ip)
		}
	}
	slices.Sort(all)
	return Result{All: true, Packages: nonNil(all), Plugins: nonNil(plugins), Contract: nonNil(plugins)}, nil
}

// build is one of the builds CI runs: the root module alone, or a plugin with the root module.
type build struct {
	root   string // the repository
	dir    string // the main module: the root module, or the plugin's
	plugin string // the plugin's name, or "" for the root module alone
}

// env returns the environment of the build: each module on its own, never a go.work.
func (b build) env() []string {
	return append(os.Environ(), "GOWORK=off")
}

type module struct {
	Path    string
	Version string
	Dir     string
	GoMod   string
	Replace *module
	Main    bool
}

// node is a package of the build, with its code and its tests apart.
type node struct {
	importPath, dir string
	module          *module
	codeFiles       []string
	testFiles       []string
	codeImports     []string
	testImports     []string
}

// list returns the packages of the build: those of its main module, with their dependencies and those of their tests.
func (b build) list(ctx context.Context) (map[string]*node, error) {
	out, err := run(ctx, b.dir, b.env(), "go", "list", "-e", "-deps", "-test", "-json", "./...")
	if err != nil {
		return nil, err
	}
	type raw struct {
		ImportPath, Dir, ForTest                                       string
		Module                                                         *module
		Standard                                                       bool
		Imports                                                        []string
		GoFiles, CgoFiles, IgnoredGoFiles, OtherFiles, EmbedFiles      []string
		CFiles, CXXFiles, HFiles, SFiles, SysoFiles, SwigFiles, MFiles []string
		TestGoFiles, XTestGoFiles, TestEmbedFiles, XTestEmbedFiles     []string
	}
	strip := func(ip string) string { return strings.Split(ip, " ")[0] }
	nodes := map[string]*node{}
	get := func(ip string) *node {
		if n, ok := nodes[ip]; ok {
			return n
		}
		n := &node{importPath: ip}
		nodes[ip] = n
		return n
	}
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var r raw
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		ip := strip(r.ImportPath)
		var imports []string
		for _, imp := range r.Imports {
			imports = append(imports, strip(imp))
		}
		switch {
		case r.Standard, strings.HasSuffix(ip, ".test"):
			// The standard library does not change within a build, and a test main imports only its tests.
		case r.ForTest == "":
			// The package as its importers build it: its code.
			n := get(ip)
			n.dir, n.module, n.codeImports = r.Dir, r.Module, imports
			for _, files := range [][]string{r.GoFiles, r.CgoFiles, r.IgnoredGoFiles, r.OtherFiles, r.EmbedFiles,
				r.CFiles, r.CXXFiles, r.HFiles, r.SFiles, r.SysoFiles, r.SwigFiles, r.MFiles} {
				n.codeFiles = append(n.codeFiles, files...)
			}
			n.testFiles = slices.Concat(r.TestGoFiles, r.XTestGoFiles, r.TestEmbedFiles, r.XTestEmbedFiles)
		case ip == r.ForTest || ip == r.ForTest+"_test":
			// The package compiled with its tests, or its external tests: what its tests import.
			n := get(r.ForTest)
			for _, imp := range imports {
				if imp != r.ForTest {
					n.testImports = append(n.testImports, imp)
				}
			}
		default:
			// Another package recompiled for a test: the same code as its plain entry.
		}
	}
	return nodes, nil
}

// modules returns the build list of the build in a checkout at root: each module path with its version or
// replacement, and the go and toolchain directives of the main modules.
func (b build) modules(ctx context.Context, root string) (map[string]string, error) {
	dir := root
	if b.plugin != "" {
		dir = filepath.Join(root, "plugins", b.plugin)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil // the plugin is new
	}
	out, err := run(ctx, dir, b.env(), "go", "list", "-m", "-json", "all")
	if err != nil {
		return nil, err
	}
	mods := map[string]string{}
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var m module
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if m.Main {
			v, err := directives(m.GoMod)
			if err != nil {
				return nil, err
			}
			mods[m.Path] = "main " + v
			continue
		}
		v := m.Version
		if m.Replace != nil {
			v += " => " + m.Replace.Path + " " + m.Replace.Version
			if m.Replace.Version == "" {
				// A local replacement: its content is what counts, and it is a directory of this repository or
				// outside of it; name it relative to the checkout so that both checkouts compare.
				rel, err := filepath.Rel(root, m.Replace.Dir)
				if err == nil {
					v = m.Version + " => " + rel
				}
			}
		}
		mods[m.Path] = v
	}
	return mods, nil
}

// affected returns the affected packages of the main module of the build, given the files changed since the checkout
// of the merge base at baseDir.
func (b build) affected(ctx context.Context, baseDir string, changed []string) ([]string, error) {
	nodes, code, err := b.code(ctx, baseDir, changed)
	if err != nil {
		return nil, err
	}
	mainPath := moduleOf(b.dir)
	var out []string
	for ip, n := range nodes {
		// Only the packages of the build's own module: the root module's are tested in its own build, and a change of
		// their code reaches the plugin's packages through their imports.
		if n.module == nil || n.module.Path != mainPath {
			continue
		}
		if code[ip] || changedFiles(b.root, n, changed, n.testFiles, false, true) ||
			slices.ContainsFunc(n.testImports, func(imp string) bool { return code[imp] }) {
			out = append(out, ip)
		}
	}
	slices.Sort(out)
	return out, nil
}

// code returns the packages of the build, and those whose code changed since the checkout of the merge base at
// baseDir: one of their code files changed, a package their code imports changed code, or the module they belong to
// changed version in this build.
func (b build) code(ctx context.Context, baseDir string, changed []string) (map[string]*node, map[string]bool, error) {
	nodes, err := b.list(ctx)
	if err != nil {
		return nil, nil, err
	}
	headMods, err := b.modules(ctx, b.root)
	if err != nil {
		return nil, nil, err
	}
	baseMods, err := b.modules(ctx, baseDir)
	if err != nil {
		return nil, nil, fmt.Errorf("at the merge base: %w", err)
	}
	// The modules of the build whose version changed, and the main modules whose directives changed.
	moved := map[string]bool{}
	for p, v := range headMods {
		if baseMods[p] != v {
			moved[p] = true
		}
	}

	code := map[string]bool{}
	for ip, n := range nodes {
		if n.module != nil && moved[n.module.Path] || changedFiles(b.root, n, changed, n.codeFiles, true, false) {
			code[ip] = true
		}
	}
	// The code of a package changes with the code it imports.
	for grew := true; grew; {
		grew = false
		for ip, n := range nodes {
			if !code[ip] && slices.ContainsFunc(n.codeImports, func(imp string) bool { return code[imp] }) {
				code[ip], grew = true, true
			}
		}
	}
	return nodes, code, nil
}

// changedFiles reports whether a file of files in the directory of the package, in the repository at root, or of its
// testdata/ with testdata, is among changed; or, with goFiles, a Go file of the directory that no longer exists.
func changedFiles(root string, n *node, changed, files []string, goFiles, testdata bool) bool {
	rel, ok := repoDir(root, n)
	if !ok {
		return false
	}
	for _, f := range changed {
		switch {
		case path.Dir(f) == rel && slices.Contains(files, path.Base(f)):
			return true
		case goFiles && path.Dir(f) == rel && path.Ext(f) == ".go" && !strings.HasSuffix(f, "_test.go"):
			return true // a deleted file of the package's code
		case testdata && (strings.HasPrefix(f, rel+"/testdata/") || path.Dir(f) == rel && strings.HasSuffix(f, "_test.go")):
			return true // a file of its testdata, or a deleted test file
		}
	}
	return false
}

// repoDir returns the directory of the package relative to the repository at root, with slashes, and whether it is in
// the repository at all.
func repoDir(root string, n *node) (string, bool) {
	if n.dir == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, n.dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false // outside the repository
	}
	return filepath.ToSlash(rel), true
}

func moduleOf(dir string) string {
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(mod)) {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(p, `"`)
		}
	}
	return ""
}

// directives returns the go and toolchain directives of a go.mod.
func directives(gomod string) (string, error) {
	mod, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	var v []string
	for line := range strings.Lines(string(mod)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "go ") || strings.HasPrefix(line, "toolchain ") {
			v = append(v, line)
		}
	}
	return strings.Join(v, "; "), nil
}

func pluginNames(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "plugins"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(root, "plugins", e.Name(), "go.mod")); e.IsDir() && err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// worktree checks rev out in a temporary directory, and returns it with a function that removes it.
func worktree(ctx context.Context, root, rev string) (string, func(), error) {
	parent, err := os.MkdirTemp("", "affected-base-*")
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(parent, "base")
	if _, err := git(ctx, root, "worktree", "add", "--detach", "--quiet", dir, rev); err != nil {
		os.RemoveAll(parent)
		return "", nil, err
	}
	return dir, func() {
		_, _ = git(context.WithoutCancel(ctx), root, "worktree", "remove", "--force", dir)
		os.RemoveAll(parent)
	}, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, dir, nil, "git", args...)
}

func run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func lines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
