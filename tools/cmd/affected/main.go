// Command affected prints what CI tests for the change since a revision, as JSON:
//
//	affected -base origin/main
//	affected -all
//
// With -github-output, it also writes each field of the result to $GITHUB_OUTPUT, as the outputs of a step.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/mycroblog/plugin/tools/affected"
)

func main() {
	base := flag.String("base", "origin/main", "the revision the change is compared to, from its merge base with HEAD")
	root := flag.String("root", ".", "the repository, checked out at the head of the change")
	all := flag.Bool("all", false, "test everything, whatever changed")
	githubOutput := flag.Bool("github-output", false, "also write the result to $GITHUB_OUTPUT")
	flag.Parse()

	var res affected.Result
	var err error
	if *all {
		res, err = affected.Everything(context.Background(), *root)
	} else {
		res, err = affected.Compute(context.Background(), *root, *base)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "affected:", err)
		os.Exit(1)
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))

	if *githubOutput {
		f, err := os.OpenFile(os.Getenv("GITHUB_OUTPUT"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, "affected: $GITHUB_OUTPUT:", err)
			os.Exit(1)
		}
		defer f.Close()
		for k, v := range map[string]any{"all": res.All, "packages": res.Packages, "plugins": res.Plugins, "contract": res.Contract} {
			b, _ := json.Marshal(v)
			fmt.Fprintf(f, "%s=%s\n", k, b)
		}
	}
}
