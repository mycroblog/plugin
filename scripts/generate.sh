#!/bin/sh
# Regenerates gen/ from buf.gen.yaml. buf lives in tools/go.mod so that its dependencies stay out of the root module.
set -eu
cd "$(dirname "$0")/.."
GOWORK=off go tool -modfile=tools/go.mod buf generate
