#!/bin/sh
# The static checks of CI: formatting, go vet with depcheck, tidy go.mod files, and one Go version everywhere.
set -eu
cd "$(dirname "$0")/.."
status=0
fail() {
	echo "lint: $*" >&2
	status=1
}

unformatted="$(gofmt -l . | grep -v '^gen/' || true)"
[ -z "$unformatted" ] || fail "not gofmt-ed:
$unformatted"

./scripts/vet.sh || fail "go vet"

# The plugins' go.mod cannot be tidied on their own until the root module is published (they resolve it through a
# go.work), so only the root module and tools/ are checked.
GOWORK=off go mod tidy -diff || fail "go.mod of the root module is not tidy: run GOWORK=off go mod tidy"
(cd tools && GOWORK=off go mod tidy -diff) || fail "tools/go.mod is not tidy: run (cd tools && GOWORK=off go mod tidy)"

want="$(awk '$1 == "golang" { print $2 }' .tool-versions)"
for f in go.mod tools/go.mod plugins/*/go.mod; do
	got="$(awk '$1 == "go" { print $2; exit }' "$f")"
	[ "$got" = "$want" ] || fail "$f says go $got, .tool-versions golang $want"
done
for f in plugins/*/Dockerfile; do
	got="$(sed -n 's/^ARG GO_VERSION=//p' "$f")"
	[ "$got" = "$want" ] || fail "$f has GO_VERSION=$got, .tool-versions golang $want"
done
got="$(awk '$1 == "go" { print $2 }' go.work)"
[ "$got" = "$want" ] || fail "go.work says go $got, .tool-versions golang $want"

exit $status
