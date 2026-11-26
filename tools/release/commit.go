package release

import (
	"regexp"
	"strings"
)

// Commit is a commit since a plugin's last release that reaches its image, read as a Conventional Commit.
type Commit struct {
	Hash        string
	Type        string // "feat", "fix", …; "" when the subject is not a Conventional Commit
	Scope       string
	Description string // the subject without its type and scope, or the whole subject
	Breaking    bool   // a "!" after the type or scope, or a BREAKING CHANGE footer
}

var subjectRE = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^()]*)\))?(!)?: (.+)$`)

func parseCommit(hash, subject, body string) Commit {
	c := Commit{Hash: hash, Description: strings.TrimSpace(subject)}
	if m := subjectRE.FindStringSubmatch(c.Description); m != nil {
		c.Type, c.Scope, c.Breaking, c.Description = strings.ToLower(m[1]), m[2], m[3] == "!", m[4]
	}
	for line := range strings.Lines(body) {
		if strings.HasPrefix(line, "BREAKING CHANGE:") || strings.HasPrefix(line, "BREAKING-CHANGE:") {
			c.Breaking = true
		}
	}
	return c
}

// bump is how much the commit moves the version.
func (c Commit) bump() Bump {
	switch {
	case c.Breaking:
		return BumpMajor
	case c.Type == "feat":
		return BumpMinor
	default:
		return BumpPatch
	}
}

// section is the heading of the changelog the commit is listed under.
func (c Commit) section() string {
	switch {
	case c.Breaking:
		return "Breaking changes"
	case c.Type == "feat":
		return "Features"
	case c.Type == "fix":
		return "Fixes"
	default:
		return "Other changes"
	}
}

var sections = []string{"Breaking changes", "Features", "Fixes", "Other changes"}
