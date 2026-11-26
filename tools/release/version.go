package release

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is the version of a plugin: Semantic Versioning, without build metadata.
type Version struct {
	Major, Minor, Patch int
	Pre                 string // the pre-release, without its "-": "rc.1", or "" for a release
}

var versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// ParseVersion parses "1.2.3" or "1.2.3-rc.1", without a leading v.
func ParseVersion(s string) (Version, error) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a version: <major>.<minor>.<patch>[-<pre-release>]", s)
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	v.Pre = m[4]
	return v, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Core returns the release that the version is a pre-release of, or the version itself.
func (v Version) Core() Version {
	return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch}
}

// Compare compares a and b by the precedence of Semantic Versioning: -1, 0, or +1.
func Compare(a, b Version) int {
	if c := cmp.Or(cmp.Compare(a.Major, b.Major), cmp.Compare(a.Minor, b.Minor), cmp.Compare(a.Patch, b.Patch)); c != 0 {
		return c
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1 // a release comes after its pre-releases
	case b.Pre == "":
		return -1
	}
	as, bs := strings.Split(a.Pre, "."), strings.Split(b.Pre, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		var c int
		switch {
		case aErr == nil && bErr == nil:
			c = cmp.Compare(an, bn)
		case aErr == nil:
			c = -1 // a numeric identifier comes before an alphanumeric one
		case bErr == nil:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

// Bump is how much a change moves the version.
type Bump int

const (
	BumpPatch Bump = iota // a fix, or any other change to the image
	BumpMinor             // a feature
	BumpMajor             // a breaking change
)

// Next returns the version after v for a change of bump. Before 1.0.0, a breaking change moves the minor version, as
// a feature does: 0.x versions make no promise of compatibility, and 1.0.0 is a decision, not a commit.
func (v Version) Next(bump Bump) Version {
	v = v.Core()
	switch {
	case bump == BumpMajor && v.Major > 0:
		return Version{Major: v.Major + 1}
	case bump >= BumpMinor:
		return Version{Major: v.Major, Minor: v.Minor + 1}
	default:
		return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
	}
}
