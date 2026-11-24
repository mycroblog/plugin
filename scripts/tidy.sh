#!/bin/sh
# Tidies every module on its own (GOWORK=off): the root module, tools/, and each plugin. A plugin replaces the root module
# with ../.., so raising a dependency of the root module changes the plugins' go.mod too: run this after such a change,
# a Dependabot one included, and commit what it changes.
set -eu
cd "$(dirname "$0")/.."
export GOWORK=off
for dir in . tools plugins/*/; do
	[ -f "$dir/go.mod" ] || continue
	(cd "$dir" && go mod tidy)
done
