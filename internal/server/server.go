// Package server serves the handlers of a plugin on the address the server of mycroblog/my connects to.
//
// It owns what every plugin shares: the listener (unix://, http://, https://), HTTP/2 with and without TLS (the media
// kinds stream and need it), mutual TLS, the bearer token, and a graceful shutdown.
package server

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config is how a plugin listens and whom it lets in.
type Config struct {
	// Address is unix:///path/to.sock, http://host:port, or https://host:port.
	Address string
	// TLSCertFile and TLSKeyFile are required for https://.
	TLSCertFile string
	TLSKeyFile  string
	// TLSClientCAFile turns on mutual TLS: only clients with a certificate signed by it get in.
	TLSClientCAFile string
	// TokenFile holds the bearer token the server sends. The server sends it over unix:// and https:// only.
	TokenFile string
	// ShutdownTimeout bounds how long calls in flight may finish after the context is done.
	ShutdownTimeout time.Duration
}

// RegisterFlags binds the fields of c to flags of fs.
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.Address, "address", "", "address to listen on: unix:///path, http://host:port, or https://host:port")
	fs.StringVar(&c.TLSCertFile, "tls-cert-file", "", "certificate for https://")
	fs.StringVar(&c.TLSKeyFile, "tls-key-file", "", "private key for https://")
	fs.StringVar(&c.TLSClientCAFile, "tls-client-ca-file", "", "CA of client certificates; enables mutual TLS")
	fs.StringVar(&c.TokenFile, "token-file", "", "file holding the bearer token the server must send")
	fs.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", 30*time.Second, "how long calls in flight may finish on shutdown")
}

// Run serves h on the address of c until ctx is done, then shuts down gracefully.
func Run(ctx context.Context, c Config, h http.Handler) error {
	u, err := url.Parse(c.Address)
	if err != nil {
		return fmt.Errorf("server: address %q: %w", c.Address, err)
	}

	if c.TokenFile != "" {
		if u.Scheme == "http" {
			return errors.New("server: the server sends no token over http://; use unix:// or https://")
		}
		token, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return fmt.Errorf("server: token: %w", err)
		}
		h = requireToken(strings.TrimSpace(string(token)), h)
	}

	var protocols http.Protocols
	protocols.SetHTTP1(true)
	srv := &http.Server{Handler: h, Protocols: &protocols, ReadHeaderTimeout: 10 * time.Second}

	var ln net.Listener
	switch u.Scheme {
	case "unix":
		protocols.SetUnencryptedHTTP2(true)
		ln, err = listenUnix(u.Path)
	case "http":
		protocols.SetUnencryptedHTTP2(true)
		ln, err = net.Listen("tcp", u.Host)
	case "https":
		protocols.SetHTTP2(true)
		srv.TLSConfig, err = tlsConfig(c)
		if err == nil {
			ln, err = net.Listen("tcp", u.Host)
		}
	default:
		err = fmt.Errorf("scheme %q is not unix, http, or https", u.Scheme)
	}
	if err != nil {
		return fmt.Errorf("server: address %q: %w", c.Address, err)
	}

	errc := make(chan error, 1)
	go func() {
		if srv.TLSConfig != nil {
			errc <- srv.ServeTLS(ln, "", "")
		} else {
			errc <- srv.Serve(ln)
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// listenUnix listens on path, replacing a socket left behind by an earlier run but nothing else.
func listenUnix(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	return net.Listen("unix", path)
}

func tlsConfig(c Config) (*tls.Config, error) {
	if c.TLSCertFile == "" || c.TLSKeyFile == "" {
		return nil, errors.New("https:// needs -tls-cert-file and -tls-key-file")
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if c.TLSClientCAFile != "" {
		pem, err := os.ReadFile(c.TLSClientCAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no certificate", c.TLSClientCAFile)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

func requireToken(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
