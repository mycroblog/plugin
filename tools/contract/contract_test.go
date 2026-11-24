package contract

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const report = `warning: address is plain TCP: ...
PASS reachable
PASS remux_audio
FAIL remux_video: media plugin: media: corrupt image (does the plugin serve this RPC?)
SKIP slow_case: needs -long
1 passed, 1 failed, 0 skipped
`

func TestParse(t *testing.T) {
	got := Parse("media_video", report)
	want := []Case{
		{"media_video", "reachable", Pass, "", ""},
		{"media_video", "remux_audio", Pass, "", ""},
		{"media_video", "remux_video", Fail, "media plugin: media: corrupt image (does the plugin serve this RPC?)", ""},
		{"media_video", "slow_case", Skip, "needs -long", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse =\n%v\nwant\n%v", got, want)
	}
}

func TestJudge(t *testing.T) {
	m := Manifest{Kinds: []string{"media_video"}, KnownFailures: []KnownFailure{{"media_video/remux_video", "https://github.com/mycroblog/my/issues/3494"}}}
	for _, tt := range []struct {
		name  string
		cases []Case
		want  []string
	}{
		{"the known failure fails", []Case{{"media_video", "remux_video", Fail, "x", "unix"}, {"media_video", "reachable", Pass, "", "unix"}}, nil},
		{"another case fails", []Case{{"media_video", "remux_video", Fail, "x", "unix"}, {"media_video", "reachable", Fail, "down", "unix"}}, []string{"media_video/reachable fails over unix: down"}},
		{"the known failure passes", []Case{{"media_video", "remux_video", Pass, "", "unix"}}, []string{"media_video/remux_video passes, though plugin.yaml lists it as failing for https://github.com/mycroblog/my/issues/3494: remove it"}},
		{"the known failure is gone", []Case{{"media_video", "reachable", Pass, "", "unix"}}, []string{"media_video/remux_video, listed in plugin.yaml, did not run"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Judge(m, tt.cases); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Judge = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	for _, tt := range []struct {
		name, yaml, wantErr string
	}{
		{"valid", "kinds: [media_image]\n", ""},
		{"no kind", "kinds: []\n", "kinds is empty"},
		{"unknown kind", "kinds: [media_picture]\n", `"media_picture" is not a kind`},
		{"probe listed", "kinds: [probe]\n", "is probe, which is always checked"},
		{"unknown field", "kinds: [media_image]\nkind: x\n", "field kind not found"},
		{"failure of another kind", "kinds: [media_image]\nknown_failures:\n  - case: media_video/remux_video\n    issue: https://github.com/mycroblog/my/issues/1\n", "is not <kind>/<case> of a kind of the plugin"},
		{"failure without its issue", "kinds: [media_image]\nknown_failures:\n  - case: media_image/process_photo\n    issue: later\n", "is not an issue of mycroblog/my"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "plugin.yaml")
			os.WriteFile(p, []byte(tt.yaml), 0o600)
			_, err := Load(p)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("Load = %v, want an error with %q", err, tt.wantErr)
			}
		})
	}
}

// TestManifests loads the plugin.yaml of every plugin of the repository.
func TestManifests(t *testing.T) {
	paths, _ := filepath.Glob("../../plugins/*/plugin.yaml")
	dirs, _ := filepath.Glob("../../plugins/*/go.mod")
	if len(paths) != len(dirs) {
		t.Errorf("%d plugins, %d plugin.yaml: every plugin needs one", len(dirs), len(paths))
	}
	for _, p := range paths {
		if _, err := Load(p); err != nil {
			t.Error(err)
		}
	}
}
