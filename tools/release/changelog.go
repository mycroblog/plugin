package release

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Section returns the section of the changelog for the plan: its heading, then its commits by kind, each with a link
// to the commit at repoURL.
func Section(p Plan, repoURL string, date time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s (%s)\n\n", p.Next, date.Format(time.DateOnly))
	if p.Last == nil {
		b.WriteString("First release.\n")
		return b.String()
	}
	if len(p.Commits) == 0 {
		// Release found a change of the build that no commit's files show, such as a module that moved.
		fmt.Fprintf(&b, "Built again, with the repository's changes since %s.\n", p.Last)
		return b.String()
	}
	var parts []string
	for _, s := range sections {
		var lines []string
		for _, c := range p.Commits {
			if c.section() == s {
				lines = append(lines, "- "+entry(p.Plugin, c, repoURL))
			}
		}
		if len(lines) > 0 {
			parts = append(parts, fmt.Sprintf("### %s\n\n%s\n", s, strings.Join(lines, "\n")))
		}
	}
	b.WriteString(strings.Join(parts, "\n"))
	return b.String()
}

// entry is the line of a commit: its description, its scope unless the scope names the plugin, and its link.
func entry(plugin string, c Commit, repoURL string) string {
	s := c.Description
	if c.Scope != "" && c.Scope != plugin && !strings.HasSuffix(plugin, "-"+c.Scope) {
		s = "**" + c.Scope + ":** " + s
	}
	short := c.Hash
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("%s ([%s](%s/commit/%s))", s, short, repoURL, c.Hash)
}

// Write puts the section of each plan at the top of its plugin's CHANGELOG.md, under its title, creating the file for
// a first release.
func Write(root string, plans []Plan, repoURL string, date time.Time) error {
	for _, p := range plans {
		path := changelogPath(root, p.Plugin)
		old, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		content := string(old)
		if content == "" {
			content = "# Changelog of " + p.Plugin + "\n"
		}
		section := Section(p, repoURL, date)
		// Before the first section, or at the end when there is none.
		var out string
		if i := strings.Index(content, "\n## "); i >= 0 {
			out = content[:i+1] + "\n" + section + "\n" + content[i+1:]
		} else {
			out = strings.TrimRight(content, "\n") + "\n\n" + section
		}
		out = collapseBlankLines(out)
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// collapseBlankLines leaves at most one blank line between two lines.
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// Title is the title of the release pull request.
func Title(plans []Plan) string {
	var parts []string
	for _, p := range plans {
		parts = append(parts, p.Plugin+" "+p.Next.String())
	}
	return "chore: release " + strings.Join(parts, ", ")
}

// Body is the description of the release pull request.
func Body(plans []Plan, repoURL string, date time.Time) string {
	var b strings.Builder
	b.WriteString("Releases the plugins below. Merging this pull request tags each version on main, " +
		"`plugins/<name>/v<version>`, and each tag releases the plugin's image.\n\n")
	b.WriteString("| Plugin | Last release | Next |\n|---|---|---|\n")
	for _, p := range plans {
		last := "none"
		if p.Last != nil {
			last = p.Last.String()
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", p.Plugin, last, p.Next)
	}
	b.WriteString("\n- To release another version, edit the first heading of the plugin's `CHANGELOG.md` " +
		"in this pull request, such as `## 0.2.0-rc.1` for a pre-release.\n" +
		"- To leave a plugin out, revert its `CHANGELOG.md` in this pull request.\n" +
		"- This pull request is made again each time main moves, which drops such edits: make them just before merging.\n")
	for _, p := range plans {
		// The section's headings, two levels down, under the summary.
		var section strings.Builder
		for line := range strings.Lines(Section(p, repoURL, date)) {
			if strings.HasPrefix(line, "#") {
				line = "##" + line
			}
			section.WriteString(line)
		}
		fmt.Fprintf(&b, "\n<details><summary>%s %s</summary>\n\n%s\n</details>\n", p.Plugin, p.Next, section.String())
	}
	return b.String()
}
