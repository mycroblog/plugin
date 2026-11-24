// Package depcheck is a vet analyzer that keeps the imports between the packages of this repository in their
// direction. CI decides what to test from the import graph (tools/cmd/affected), so the graph must keep the shape
// these rules give it:
//
//	R1  The root module does not import plugins/.
//	R2  A plugin does not import another plugin.
//	R3  internal/server, the transport, imports no generated protocol package.
//	R4  internal/<kind> imports, of gen/, only gen/my/plugin/<kind>, and no other internal/ package but those in
//	    sharedInternal.
//	R5  gen/ imports nothing of this repository but gen/.
//	R6  Nothing outside tools/ imports tools/.
//	R7  An internal/ package is either internal/<kind> of a protocol kind, or listed in sharedInternal.
//
// A package shared between kinds is named by what it does (calllog, scratch), never common or util, and added to
// sharedInternal on purpose.
package depcheck

import (
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Module is the module path of this repository.
const Module = "github.com/mycroblog/plugin"

// kinds are the protocol packages of gen/my/plugin/, one per kind (media.proto and the others of mycroblog/my).
var kinds = map[string]bool{
	"probe": true, "translator": true, "search": true, "billing": true, "mailer": true,
	"media": true, "key_manager": true, "hook": true,
}

// sharedInternal are the internal/ packages that are not of one kind, and which internal/<kind> packages may import.
// server is the transport every plugin runs on; internal/<kind> packages have no need of it.
var sharedInternal = map[string]struct {
	importableByKinds bool
}{
	"server": {importableByKinds: false},
}

var Analyzer = &analysis.Analyzer{
	Name: "depcheck",
	Doc:  "check the direction of imports between the packages of github.com/mycroblog/plugin",
	Run:  run,
}

// class is where a package of this repository sits.
type class struct {
	area string // "plugin", "gen", "internal", "tools", "root", or "" for a package outside the repository
	name string // the plugin, the kind of gen/, or the internal/ package
}

func classify(path string) class {
	rest, ok := strings.CutPrefix(path, Module)
	if !ok || (rest != "" && rest[0] != '/') {
		return class{}
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	switch parts[0] {
	case "plugins":
		if len(parts) > 1 {
			return class{"plugin", parts[1]}
		}
	case "gen":
		// gen/my/plugin/<kind>/v1/...
		if len(parts) > 4 && parts[1] == "my" && parts[2] == "plugin" {
			return class{"gen", parts[3]}
		}
		return class{"gen", ""}
	case "internal":
		if len(parts) > 1 {
			return class{"internal", parts[1]}
		}
	case "tools":
		return class{"tools", ""}
	}
	return class{"root", ""}
}

func run(pass *analysis.Pass) (any, error) {
	// A test package of an external test (package x_test) has the path of x; vet passes its own path with a suffix.
	from := classify(strings.TrimSuffix(strings.TrimSuffix(pass.Pkg.Path(), "_test"), ".test"))
	if from.area == "" {
		return nil, nil
	}
	if from.area == "internal" && !kinds[from.name] && !isShared(from.name) && len(pass.Files) > 0 {
		pass.Reportf(pass.Files[0].Package, "%s: R7: internal/%s is neither a protocol kind nor listed in depcheck's sharedInternal", pass.Pkg.Path(), from.name)
	}
	for _, file := range pass.Files {
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if msg := violation(from, classify(path)); msg != "" {
				pass.Reportf(imp.Pos(), "%s imports %s: %s", pass.Pkg.Path(), path, msg)
			}
		}
	}
	return nil, nil
}

// violation returns why an import from one class to another breaks the rules, or "".
func violation(from, to class) string {
	switch {
	case to.area == "":
		return ""
	case to.area == "tools" && from.area != "tools":
		return "R6: nothing outside tools/ imports tools/"
	case to.area == "internal" && !kinds[to.name] && !isShared(to.name):
		return "R7: internal/" + to.name + " is neither a protocol kind nor listed in depcheck's sharedInternal"
	}

	switch from.area {
	case "gen":
		if to.area != "gen" {
			return "R5: generated code imports only generated code"
		}
	case "internal":
		if from.name == "server" {
			if to.area == "gen" {
				return "R3: internal/server, the transport, imports no protocol package"
			}
			if to.area == "internal" {
				return "R3: internal/server imports no other internal/ package"
			}
		} else if kinds[from.name] {
			if to.area == "gen" && to.name != from.name {
				return "R4: internal/" + from.name + " imports only gen/my/plugin/" + from.name
			}
			if to.area == "internal" && to.name != from.name && !sharedInternal[to.name].importableByKinds {
				return "R4: internal/" + from.name + " imports no other internal/ package but the shared ones"
			}
		}
	case "plugin":
		if to.area == "plugin" && to.name != from.name {
			return "R2: a plugin does not import another plugin"
		}
	}
	if from.area != "plugin" && to.area == "plugin" {
		return "R1: the root module does not import plugins/"
	}
	return ""
}

func isShared(name string) bool {
	_, ok := sharedInternal[name]
	return ok
}
