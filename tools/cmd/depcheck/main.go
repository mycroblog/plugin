// Command depcheck runs the depcheck analyzer as a vet tool:
//
//	go vet -vettool=$(path to depcheck) ./...
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/mycroblog/plugin/tools/depcheck"
)

func main() { unitchecker.Main(depcheck.Analyzer) }
