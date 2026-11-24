#!/bin/sh
# Runs go vet and the depcheck analyzer (tools/depcheck) over each module of the repository on its own (GOWORK=off):
# the root module, each plugin, and tools/.
set -eu
cd "$(dirname "$0")/.."
export GOWORK=off

bin="$(mktemp -d)"
trap 'rm -rf "$bin"' EXIT
go build -C tools -o "$bin/depcheck" ./cmd/depcheck

status=0
for dir in . plugins/*/ tools; do
	[ -f "$dir/go.mod" ] || continue
	(cd "$dir" && go vet ./... && go vet -vettool="$bin/depcheck" ./...) || status=1
done
exit $status
