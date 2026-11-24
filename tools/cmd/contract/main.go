// Command contract runs the contract tests of mycroblog/my against the container image of a plugin, as its
// plugin.yaml declares them:
//
//	contract -plugin my-media-imagemagick -image my-media-imagemagick:ci -my ./my
//
// It fails on a case that fails and plugin.yaml does not list, and on a listed case that passes or no longer runs.
// With $GITHUB_STEP_SUMMARY set, it writes the result there too.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/mycroblog/plugin/tools/contract"
)

func main() {
	root := flag.String("root", ".", "the repository")
	plugin := flag.String("plugin", "", "the plugin, a directory under plugins/")
	image := flag.String("image", "", "the plugin's container image")
	my := flag.String("my", "my", "the my command of the version of mycroblog/my that buf.gen.yaml pins")
	flag.Parse()
	if *plugin == "" || *image == "" {
		fmt.Fprintln(os.Stderr, "contract: -plugin and -image are required")
		os.Exit(2)
	}

	m, err := contract.Load(filepath.Join(*root, "plugins", *plugin, "plugin.yaml"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "contract:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cases, err := contract.Run(ctx, contract.Options{
		Manifest: m, Image: *image, My: *my,
		Log: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "contract:", err)
		os.Exit(1)
	}
	problems := contract.Judge(m, cases)

	var sb strings.Builder
	fmt.Fprintf(&sb, "### Contract of %s\n\n| Case | Transport | Status | Detail |\n|---|---|---|---|\n", *plugin)
	for _, c := range cases {
		status := string(c.Status)
		if f, ok := m.Known(c); ok && c.Status == contract.Fail {
			status = "FAIL, known: " + f.Issue
		}
		fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", c.ID(), c.Transport, status, strings.ReplaceAll(c.Detail, "|", `\|`))
	}
	if len(problems) > 0 {
		sb.WriteString("\n**Problems**\n\n")
		for _, p := range problems {
			fmt.Fprintf(&sb, "- %s\n", p)
		}
	}
	fmt.Println(sb.String())
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0); err == nil {
			f.WriteString(sb.String())
			f.Close()
		}
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}
