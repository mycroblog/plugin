package affected

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// What changes the image of a plugin, for its releases, is narrower than what CI tests for a change:
//
//   - the tests: a test file or a file of testdata/ changes no image;
//   - the harness: the CI, tools/ and scripts/ change no image, but .dockerignore and .tool-versions (see imageInputs) do;
//   - plugin.yaml, which says what the contract tests check, is not in the image.
//
// The code a plugin builds counts as for CI: its own, that of the root module it imports, and the modules of its build.

// imageInputs are the files outside plugins/ and the Go code that the images are built with: what the Dockerfiles may
// copy, and the Go version they build with.
var imageInputs = []string{".dockerignore", ".tool-versions"}

// Release reports whether the image of the plugin changed since base, a revision (its last release), compared from its
// merge base with HEAD, working tree included.
func Release(ctx context.Context, root, base, plugin string) (bool, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	mergeBase, changed, err := changedSince(ctx, root, base)
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(changed, func(f string) bool { return slices.Contains(imageInputs, f) || ownImageFile(plugin, f) }) {
		return true, nil
	}
	if len(changed) == 0 {
		return false, nil
	}

	baseDir, cleanup, err := worktree(ctx, root, mergeBase)
	if err != nil {
		return false, err
	}
	defer cleanup()
	b := build{root: root, dir: filepath.Join(root, "plugins", plugin), plugin: plugin}
	nodes, code, err := b.code(ctx, baseDir, changed)
	if err != nil {
		return false, err
	}
	mainPath := moduleOf(b.dir)
	for ip, n := range nodes {
		if n.module != nil && n.module.Path == mainPath && code[ip] {
			return true, nil
		}
	}
	return false, nil
}

// Scope returns which files reach the image of the plugin, as the repository at root stands: the files of the plugin
// but its tests, its Markdown and plugin.yaml; the code files of the packages of the repository its code builds; the
// go.mod and go.sum of the modules of the repository in its build; and imageInputs.
//
// It tells which commits since a release went into the next one. Whether there is a next one is Release's to say, which
// compares the builds themselves: a commit can touch a go.sum without changing the build.
func Scope(ctx context.Context, root, plugin string) (func(file string) bool, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	b := build{root: root, dir: filepath.Join(root, "plugins", plugin), plugin: plugin}
	nodes, err := b.list(ctx)
	if err != nil {
		return nil, err
	}

	// The packages the plugin's code builds: those of its module, and what their code imports.
	reach := map[string]bool{}
	var visit func(ip string)
	visit = func(ip string) {
		n, ok := nodes[ip]
		if !ok || reach[ip] {
			return
		}
		reach[ip] = true
		for _, imp := range n.codeImports {
			visit(imp)
		}
	}
	mainPath := moduleOf(b.dir)
	for ip, n := range nodes {
		if n.module != nil && n.module.Path == mainPath {
			visit(ip)
		}
	}

	dirs, modFiles := map[string]bool{}, map[string]bool{}
	for ip := range reach {
		n := nodes[ip]
		if rel, ok := repoDir(root, n); ok {
			dirs[rel] = true
		}
		if n.module == nil {
			continue
		}
		dir := n.module.Dir
		if n.module.Replace != nil && n.module.Replace.Dir != "" {
			dir = n.module.Replace.Dir
		}
		if rel, ok := repoDir(root, &node{dir: dir}); ok {
			modFiles[path.Join(rel, "go.mod")], modFiles[path.Join(rel, "go.sum")] = true, true
		}
	}

	return func(f string) bool {
		switch {
		case slices.Contains(imageInputs, f), ownImageFile(plugin, f), modFiles[f]:
			return true
		case strings.HasPrefix(f, "plugins/"+plugin+"/"), path.Ext(f) == ".md", strings.HasSuffix(f, "_test.go"):
			// The plugin's own files are ownImageFile's to say.
			return false
		default:
			return dirs[path.Dir(f)]
		}
	}, nil
}

// Plugins returns the names of the plugins of the repository at root: the directories of plugins/ with a go.mod.
func Plugins(root string) ([]string, error) {
	return pluginNames(root)
}

// ownImageFile reports whether f is a file of the plugin that its image is made of: any file under plugins/<plugin>/
// but its Markdown, its tests and testdata/, and plugin.yaml.
func ownImageFile(plugin, f string) bool {
	rest, ok := strings.CutPrefix(f, "plugins/"+plugin+"/")
	if !ok {
		return false
	}
	switch {
	case path.Ext(rest) == ".md", strings.HasSuffix(rest, "_test.go"), rest == "plugin.yaml":
		return false
	case strings.HasPrefix(rest, "testdata/"), strings.Contains(rest, "/testdata/"):
		return false
	}
	return true
}
