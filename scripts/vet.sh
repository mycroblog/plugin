#!/bin/sh
# Runs go vet and the depcheck analyzer (tools/depcheck) over every module of the workspace, and over tools/.
set -eu
cd "$(dirname "$0")/.."

bin="$(mktemp -d)"
trap 'rm -rf "$bin"' EXIT
GOWORK=off go build -C tools -o "$bin/depcheck" ./cmd/depcheck

status=0
for dir in $(go list -m -f '{{.Dir}}'); do
	(cd "$dir" && go vet ./... && go vet -vettool="$bin/depcheck" ./...) || status=1
done
(cd tools && GOWORK=off go vet ./... && GOWORK=off go vet -vettool="$bin/depcheck" ./...) || status=1
exit $status
