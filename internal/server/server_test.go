package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serve runs Run in the background and returns a stop function that waits for it.
func serve(t *testing.T, c Config) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Proto) })
	go func() { done <- Run(ctx, c, h) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
}

// unixClient speaks HTTP/2 without TLS (h2c) over the Unix socket at path.
func unixClient(path string) *http.Client {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{
		Protocols: &protocols,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	for range 100 {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
}

func get(t *testing.T, c *http.Client, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://plugin/", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func TestUnixSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.sock")
	serve(t, Config{Address: "unix://" + path, SocketMode: 0o660, ShutdownTimeout: time.Second})
	waitFor(t, path)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o660 {
		t.Errorf("socket mode = %o, want 660", got)
	}
	if code, proto := get(t, unixClient(path), ""); code != http.StatusOK || proto != "HTTP/2.0" {
		t.Errorf("got %d over %s, want 200 over HTTP/2.0", code, proto)
	}
}

func TestToken(t *testing.T) {
	dir := t.TempDir()
	path, tokenFile := filepath.Join(dir, "plugin.sock"), filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serve(t, Config{Address: "unix://" + path, TokenFile: tokenFile, ShutdownTimeout: time.Second})
	waitFor(t, path)

	c := unixClient(path)
	if code, _ := get(t, c, ""); code != http.StatusUnauthorized {
		t.Errorf("without the token: %d, want 401", code)
	}
	if code, _ := get(t, c, "wrong"); code != http.StatusUnauthorized {
		t.Errorf("with a wrong token: %d, want 401", code)
	}
	if code, _ := get(t, c, "s3cret"); code != http.StatusOK {
		t.Errorf("with the token: %d, want 200", code)
	}
}

func TestRefusedConfigs(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte("t"), 0o600)
	for _, tt := range []struct {
		name string
		c    Config
		want string
	}{
		{"token over http", Config{Address: "http://127.0.0.1:0", TokenFile: tokenFile}, "no token over http://"},
		{"https without a certificate", Config{Address: "https://127.0.0.1:0"}, "-tls-cert-file"},
		{"unknown scheme", Config{Address: "tcp://127.0.0.1:0"}, "not unix, http, or https"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Run(context.Background(), tt.c, http.NotFoundHandler())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one saying %q", err, tt.want)
			}
		})
	}
}

func TestReplacesOnlyASocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.sock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), Config{Address: "unix://" + path}, http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Errorf("err = %v, want one saying the file is not a socket", err)
	}
}

func TestReplacesAStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.sock")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close() // a socket left behind by a run that died

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Address: "unix://" + path, ShutdownTimeout: time.Second}, http.NotFoundHandler())
	}()
	// The stale socket refuses connections; the new one, once renamed over it, accepts them.
	connected := false
	for range 100 {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			connected = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !connected {
		t.Error("the socket never accepted a connection")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("the socket is left after shutdown: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		t.Errorf("%s is left in the directory", e.Name())
	}
}

// runWith runs Run with h on a Unix socket in the background; stop ends its context and returns what Run returned.
func runWith(t *testing.T, timeout time.Duration, h http.Handler) (path string, stop func() error) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "plugin.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, Config{Address: "unix://" + path, ShutdownTimeout: timeout}, h) }()
	t.Cleanup(cancel)
	waitFor(t, path)
	return path, func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return")
			return nil
		}
	}
}

func TestShutdownLetsCallsFinish(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan bool, 1)
	path, stop := runWith(t, 5*time.Second, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			cancelled <- true
		case <-time.After(300 * time.Millisecond):
			cancelled <- false
		}
		io.WriteString(w, "done")
	}))
	got := make(chan string, 1)
	go func() { _, body := get(t, unixClient(path), ""); got <- body }()
	<-started
	if err := stop(); err != nil {
		t.Errorf("Run: %v", err)
	}
	if <-cancelled {
		t.Error("the call was cancelled within its grace")
	}
	if body := <-got; body != "done" {
		t.Errorf("the call answered %q, want done", body)
	}
}

func TestShutdownCancelsCallsAfterGrace(t *testing.T) {
	started := make(chan struct{})
	var cleaned atomic.Bool
	path, stop := runWith(t, 200*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done() // a call that never ends by itself, as a stuck conversion
		time.Sleep(50 * time.Millisecond)
		cleaned.Store(true) // what the handler removes, as its temporary directory
	}))
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://plugin/", nil)
		if res, err := unixClient(path).Do(req); err == nil {
			res.Body.Close()
		}
	}()
	<-started
	begin := time.Now()
	err := stop()
	if err == nil || !strings.Contains(err.Error(), "cut short") {
		t.Errorf("Run: %v, want one saying the calls were cut short", err)
	}
	if !cleaned.Load() {
		t.Error("Run returned before the cancelled call's handler did")
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("Run took %s to return", d)
	}
}
