// Package contract runs the contract tests of mycroblog/my (`my plugin test`) against the container image of a
// plugin, as its plugin.yaml declares them, and judges the results against the known failures it lists.
package contract

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Manifest is a plugin's plugin.yaml.
type Manifest struct {
	// Kinds are the kinds the plugin serves, besides probe.
	Kinds []string `yaml:"kinds"`
	// KnownFailures are the cases that fail because of mycroblog/my.
	KnownFailures []KnownFailure `yaml:"known_failures"`
}

// KnownFailure is a case, "<kind>/<case>", that fails because of mycroblog/my, with the issue there.
type KnownFailure struct {
	Case  string `yaml:"case"`
	Issue string `yaml:"issue"`
}

// kinds are the kinds `my plugin test --kind` takes, and whether the server may reach each over plain TCP
// (docs/operations/plugins.md of mycroblog/my).
var kinds = map[string]bool{
	"probe": true, "translator": false, "search": false, "billing": true, "mailer": false,
	"media_image": true, "media_video": true, "media_audio": true, "key_manager": false,
	"inbound.status": false, "inbound.follow": true, "signup.check": false, "media.check": true,
	"local.status": false, "inbound.actor": true, "local.profile": true,
}

// Load reads and checks a plugin.yaml.
func Load(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, m.check()
}

func (m Manifest) check() error {
	var errs []error
	if len(m.Kinds) == 0 {
		errs = append(errs, errors.New("kinds is empty"))
	}
	for _, k := range m.Kinds {
		if _, ok := kinds[k]; !ok || k == "probe" {
			errs = append(errs, fmt.Errorf("kinds: %q is not a kind of my plugin test, or is probe, which is always checked", k))
		}
	}
	for _, f := range m.KnownFailures {
		kind, name, ok := strings.Cut(f.Case, "/")
		if !ok || name == "" || kind != "probe" && !slices.Contains(m.Kinds, kind) {
			errs = append(errs, fmt.Errorf("known_failures: %q is not <kind>/<case> of a kind of the plugin", f.Case))
		}
		if !strings.HasPrefix(f.Issue, "https://github.com/mycroblog/my/issues/") {
			errs = append(errs, fmt.Errorf("known_failures: %s: issue %q is not an issue of mycroblog/my", f.Case, f.Issue))
		}
	}
	return errors.Join(errs...)
}

// Status is the outcome of one case.
type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Skip Status = "SKIP"
)

// Case is one line of the report of `my plugin test`.
type Case struct {
	Kind, Name string
	Status     Status
	Detail     string
	// Transport is how the case reached the plugin; Run sets it.
	Transport string
}

// Known returns the known failure of the case, if plugin.yaml lists it.
func (m Manifest) Known(c Case) (KnownFailure, bool) {
	for _, f := range m.KnownFailures {
		if f.Case == c.ID() {
			return f, true
		}
	}
	return KnownFailure{}, false
}

// ID is the case as known_failures names it.
func (c Case) ID() string { return c.Kind + "/" + c.Name }

// Parse reads the cases of the output of `my plugin test --kind kind`.
func Parse(kind, output string) []Case {
	var cases []Case
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		status, rest, ok := strings.Cut(sc.Text(), " ")
		if !ok || !slices.Contains([]Status{Pass, Fail, Skip}, Status(status)) {
			continue
		}
		name, detail, _ := strings.Cut(rest, ":")
		cases = append(cases, Case{Kind: kind, Name: strings.TrimSpace(name), Status: Status(status), Detail: strings.TrimSpace(detail)})
	}
	return cases
}

// Judge returns the problems of a run: a case that fails and is not known to, a known failure that passes (its issue
// is fixed: remove it from plugin.yaml), and a known failure that never ran (its case is gone or renamed).
func Judge(m Manifest, cases []Case) []string {
	known := map[string]KnownFailure{}
	for _, f := range m.KnownFailures {
		known[f.Case] = f
	}
	ran := map[string]bool{}
	var problems []string
	for _, c := range cases {
		ran[c.ID()] = true
		f, isKnown := known[c.ID()]
		switch {
		case c.Status == Fail && !isKnown:
			problems = append(problems, fmt.Sprintf("%s fails over %s: %s", c.ID(), c.Transport, c.Detail))
		case c.Status == Pass && isKnown:
			problems = append(problems, fmt.Sprintf("%s passes, though plugin.yaml lists it as failing for %s: remove it", c.ID(), f.Issue))
		}
	}
	for _, f := range m.KnownFailures {
		if !ran[f.Case] {
			problems = append(problems, fmt.Sprintf("%s, listed in plugin.yaml, did not run", f.Case))
		}
	}
	slices.Sort(problems)
	return slices.Compact(problems)
}

// Options are those of Run.
type Options struct {
	Manifest Manifest
	// Image is the plugin's container image (the runtime target of its Dockerfile).
	Image string
	// My is the my command of the pinned version of mycroblog/my.
	My string
	// Log receives the report of each run of my plugin test.
	Log func(format string, args ...any)
}

// transport is one way to reach the plugin.
type transport struct {
	name     string
	protocol string // "" for the kind's default
	plain    bool   // over plain TCP
}

var transports = []transport{
	{name: "unix socket, default protocol"},
	{name: "unix socket, gRPC", protocol: "grpc"},
	{name: "plain TCP, default protocol", plain: true},
}

// Run starts the image twice, on a Unix socket and on plain TCP, runs `my plugin test` for probe and each kind over
// each transport the kind allows, and returns every case.
func Run(ctx context.Context, o Options) ([]Case, error) {
	sockDir, err := os.MkdirTemp("", "contract-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(sockDir)
	// The plugin runs as nobody in its image; the socket it makes must be reachable from here.
	if err := os.Chmod(sockDir, 0o777); err != nil {
		return nil, err
	}

	unixName, err := start(ctx, o.Image, "-v", sockDir+":/run/my", "--", "-address", "unix:///run/my/plugin.sock", "-socket-mode", "0666")
	if err != nil {
		return nil, err
	}
	defer stop(unixName)
	tcpName, err := start(ctx, o.Image, "-p", "127.0.0.1::8080", "--", "-address", "http://:8080")
	if err != nil {
		return nil, err
	}
	defer stop(tcpName)

	sock := filepath.Join(sockDir, "plugin.sock")
	if err := waitFor(ctx, func() bool { _, err := os.Stat(sock); return err == nil }); err != nil {
		return nil, fmt.Errorf("the plugin made no socket: %w: %s", err, logs(unixName))
	}
	port, err := exec.CommandContext(ctx, "docker", "port", tcpName, "8080/tcp").Output()
	if err != nil {
		return nil, fmt.Errorf("docker port: %w: %s", err, logs(tcpName))
	}
	hostPort := strings.TrimSpace(strings.SplitN(string(port), "\n", 2)[0])
	tcp := "http://" + hostPort
	if err := waitFor(ctx, func() bool {
		c, err := net.DialTimeout("tcp", hostPort, time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	}); err != nil {
		return nil, fmt.Errorf("the plugin does not listen on %s: %w: %s", hostPort, err, logs(tcpName))
	}

	var all []Case
	for _, kind := range append([]string{"probe"}, o.Manifest.Kinds...) {
		for _, t := range transports {
			if t.plain && !kinds[kind] {
				continue
			}
			addr := "unix://" + sock
			if t.plain {
				addr = tcp
			}
			args := []string{"plugin", "test", "--kind", kind}
			if t.protocol != "" {
				args = append(args, "-protocol", t.protocol)
			}
			out, runErr := exec.CommandContext(ctx, o.My, append(args, addr)...).CombinedOutput()
			if o.Log != nil {
				o.Log("== %s over %s\n%s", kind, t.name, out)
			}
			cases := Parse(kind, string(out))
			if runErr != nil && !slices.ContainsFunc(cases, func(c Case) bool { return c.Status == Fail }) {
				return nil, fmt.Errorf("my plugin test --kind %s over %s: %w: %s", kind, t.name, runErr, out)
			}
			for i := range cases {
				cases[i].Transport = t.name
			}
			all = append(all, cases...)
		}
	}
	return all, nil
}

func start(ctx context.Context, image string, args ...string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	name := "contract-" + hex.EncodeToString(b)
	i := slices.Index(args, "--")
	dockerArgs := append([]string{"run", "-d", "--rm", "--name", name}, args[:i]...)
	dockerArgs = append(append(dockerArgs, image), args[i+1:]...)
	if out, err := exec.CommandContext(ctx, "docker", dockerArgs...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker run %s: %w: %s", image, err, out)
	}
	return name, nil
}

func stop(name string) {
	_ = exec.Command("docker", "rm", "-f", name).Run()
}

func logs(name string) string {
	out, _ := exec.Command("docker", "logs", name).CombinedOutput()
	return string(out)
}

func waitFor(ctx context.Context, ok func() bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for !ok() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}
