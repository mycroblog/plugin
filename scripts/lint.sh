#!/bin/sh
# The static checks of CI: formatting, go vet with depcheck, tidy go.mod files, each plugin on the root module of
# its commit, and one Go version everywhere.
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

export GOWORK=off
[ ! -e go.work ] || fail "go.work is committed: CI and the images build each module on its own; keep it local"

for dir in . tools plugins/*/; do
	[ -f "$dir/go.mod" ] || continue
	(cd "$dir" && go mod tidy -diff) || fail "${dir%/}/go.mod is not tidy: run (cd ${dir%/} && GOWORK=off go mod tidy)"
done

# Each plugin builds with the root module of the same commit.
for dir in plugins/*/; do
	[ -f "$dir/go.mod" ] || continue
	go mod edit -json "$dir/go.mod" | grep -q '"Path": "\.\./\.\."' ||
		fail "$dir/go.mod does not replace github.com/mycroblog/plugin with ../.."
done

want="$(awk '$1 == "golang" { print $2 }' .tool-versions)"
for f in go.mod tools/go.mod plugins/*/go.mod; do
	got="$(awk '$1 == "go" { print $2; exit }' "$f")"
	[ "$got" = "$want" ] || fail "$f says go $got, .tool-versions golang $want"
done
for f in plugins/*/Dockerfile; do
	got="$(sed -n 's/^ARG GO_VERSION=//p' "$f")"
	[ "$got" = "$want" ] || fail "$f has GO_VERSION=$got, .tool-versions golang $want"
done

exit $status
