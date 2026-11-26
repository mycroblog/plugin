// Command release prepares the releases of the plugins (see package release):
//
//	release plan      print the plugins with a change since their last release, and their next versions
//	release prepare   write those versions to their CHANGELOG.md, and the release pull request's title and body
//	release tags      print the tags of the versions at the top of the changelogs that are not tagged yet
//
// Every command takes -root, the repository, checked out with its tags.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mycroblog/plugin/tools/release"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet("release "+cmd, flag.ExitOnError)
	root := fs.String("root", ".", "the repository, checked out with its tags")
	repoURL := fs.String("repo-url", "https://github.com/mycroblog/plugin", "the repository's URL, for the links to commits")
	date := fs.String("date", time.Now().UTC().Format(time.DateOnly), "the date of the releases, YYYY-MM-DD")
	body := fs.String("body", "", "prepare: write the pull request's body to this file")
	githubOutput := fs.Bool("github-output", false, "prepare: also write count and title to $GITHUB_OUTPUT")
	_ = fs.Parse(args)
	ctx := context.Background()

	switch cmd {
	case "plan", "prepare":
		d, err := time.Parse(time.DateOnly, *date)
		check(err)
		plans, err := release.Pending(ctx, *root)
		check(err)
		if cmd == "plan" {
			type row struct {
				Plugin  string `json:"plugin"`
				Last    string `json:"last,omitempty"`
				Next    string `json:"next"`
				Commits int    `json:"commits"`
			}
			rows := []row{}
			for _, p := range plans {
				r := row{Plugin: p.Plugin, Next: p.Next.String(), Commits: len(p.Commits)}
				if p.Last != nil {
					r.Last = p.Last.String()
				}
				rows = append(rows, r)
			}
			out, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(out))
			return
		}
		check(release.Write(*root, plans, *repoURL, d))
		for _, p := range plans {
			fmt.Printf("%s %s\n", p.Plugin, p.Next)
		}
		if *body != "" {
			check(os.WriteFile(*body, []byte(release.Body(plans, *repoURL, d)), 0o644))
		}
		if *githubOutput {
			f, err := os.OpenFile(os.Getenv("GITHUB_OUTPUT"), os.O_APPEND|os.O_WRONLY, 0)
			check(err)
			defer f.Close()
			_, err = fmt.Fprintf(f, "count=%d\ntitle=%s\n", len(plans), release.Title(plans))
			check(err)
		}
	case "tags":
		tags, err := release.Tags(ctx, *root)
		check(err)
		for _, t := range tags {
			fmt.Println(t)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: release plan|prepare|tags [-root dir] [-repo-url url] [-date YYYY-MM-DD] [-body file] [-github-output]")
	os.Exit(2)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}
