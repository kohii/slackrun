package cli

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

// TestHTTPTransportUsesExplicitRoots pins the sandbox-safety contract:
// httpTransport must set RootCAs to a non-nil pool whenever any candidate
// bundle exists. A nil pool would send Go's TLS back to the platform
// verifier (SecTrust on darwin), which is what breaks under Claude Code's
// Bash sandbox with `x509: OSStatus -26276`.
func TestHTTPTransportUsesExplicitRoots(t *testing.T) {
	pool := loadCLIRoots()
	if pool == nil {
		// None of the known bundle paths are present in this test env.
		// The transport is still well-formed — nothing to assert beyond
		// "we did not panic".
		t.Skip("no CA bundle available on this host; nothing to verify")
	}

	tr := httpTransport()
	if tr.TLSClientConfig == nil {
		t.Fatalf("TLSClientConfig is nil; RootCAs assignment would be lost")
	}
	if tr.TLSClientConfig.RootCAs == nil {
		t.Fatalf("RootCAs is nil; Go would fall back to SecTrust and fail under sandbox")
	}
	if tr.Proxy == nil {
		t.Fatalf("Proxy is nil; HTTPS_PROXY (used by Claude Code sandbox egress) would be ignored")
	}
}

// TestLoadCLIRootsPrefersFirstReadable exercises the bundle-selection
// order without depending on the host's actual /etc/ssl contents. It
// swaps cliRootPaths for a temp-dir pair so both branches (skip-missing
// and accept-first) are covered.
func TestLoadCLIRootsPrefersFirstReadable(t *testing.T) {
	// A self-signed PEM is enough for AppendCertsFromPEM to succeed.
	// Reusing the standard-library test cert would be nicer but pulls in
	// crypto/tls's internal fixtures; a static minimal PEM keeps the
	// test hermetic.
	const pem = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----
`
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.pem")
	present := filepath.Join(dir, "present.pem")
	if err := os.WriteFile(present, []byte(pem), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := cliRootPaths
	cliRootPaths = []string{missing, present}
	t.Cleanup(func() { cliRootPaths = orig })

	pool := loadCLIRoots()
	if pool == nil {
		t.Fatalf("expected pool from %q, got nil", present)
	}
	// x509.CertPool has no public "count" API; a nil-check plus successful
	// build is the strongest assertion we can make without opening the
	// pool by dialling — that would be an integration test.

	// Downstream: the pool should attach cleanly to a tls.Config without
	// panicking. That mirrors how httpTransport uses it.
	_ = &tls.Config{RootCAs: pool}
	_ = &x509.CertPool{}
}
