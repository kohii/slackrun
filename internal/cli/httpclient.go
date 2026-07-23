package cli

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"sync"
	"time"
)

// cliHTTPTimeout bounds Slack Web API calls made from CLI subcommands.
const cliHTTPTimeout = 30 * time.Second

// cliRootPaths lists candidate CA bundle files in preference order. First
// readable non-empty PEM wins. macOS ships LibreSSL's bundle at /etc/ssl,
// Linux distributions install one under /etc/ssl/certs or /etc/pki, and
// Homebrew/local OpenSSL builds place one under their prefix.
var cliRootPaths = []string{
	"/etc/ssl/cert.pem",
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/opt/homebrew/etc/ca-certificates/cert.pem",
	"/usr/local/etc/openssl/cert.pem",
}

var (
	cliTransportOnce sync.Once
	cliTransportPtr  *http.Transport
)

// httpTransport returns a *http.Transport with an explicit RootCAs pool.
//
// Setting RootCAs to a non-nil pool routes Go's TLS verification through
// the pure-Go x509 chain builder instead of darwin's SecTrust. macOS app
// sandboxes (e.g. the one Claude Code applies to Bash-invoked commands)
// block the SecTrust IPC path with `x509: OSStatus -26276`, so any Go
// binary that keeps the default nil pool fails there. When no local
// bundle is readable, RootCAs stays nil and Go falls back to the platform
// verifier — same behaviour as before this file existed.
//
// Proxy stays on http.ProxyFromEnvironment so HTTPS_PROXY (used by
// Claude Code's sandbox to funnel egress) is still honoured; slack-go's
// default client relied on that behaviour and the fix must not regress it.
func httpTransport() *http.Transport {
	cliTransportOnce.Do(func() {
		cliTransportPtr = &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{RootCAs: loadCLIRoots()},
		}
	})
	return cliTransportPtr
}

// httpClient wraps httpTransport with a Slack-Web-API timeout, suitable
// for slack.OptionHTTPClient.
var (
	cliHTTPClientOnce sync.Once
	cliHTTPClientPtr  *http.Client
)

func httpClient() *http.Client {
	cliHTTPClientOnce.Do(func() {
		cliHTTPClientPtr = &http.Client{
			Timeout:   cliHTTPTimeout,
			Transport: httpTransport(),
		}
	})
	return cliHTTPClientPtr
}

// loadCLIRoots returns the first CA pool parsed from cliRootPaths, or nil
// when nothing is readable. nil signals "keep Go's default behaviour" —
// used by callers to detect the no-bundle case for tests/diagnostics.
func loadCLIRoots() *x509.CertPool {
	for _, p := range cliRootPaths {
		pem, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(pem) {
			return pool
		}
	}
	return nil
}
