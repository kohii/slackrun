package adminapi

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SocketEnvVar names the environment variable operators set to override the
// default admin socket location. Set to "off" (case-insensitive) to disable
// the admin API entirely.
const SocketEnvVar = "SLACKRUN_ADMIN_SOCKET"

// ErrDisabled is returned by ResolveSocketPath when the operator has opted
// out of the admin API via SLACKRUN_ADMIN_SOCKET=off. Callers treat this as
// a signal to skip listener setup — it is not a failure.
var ErrDisabled = errors.New("admin socket disabled")

// unixPathMax is the platform limit on sun_path. Linux is 108, macOS/BSD is
// 104. Sockets exceeding this fail bind() with EINVAL; we surface it up-
// front with a clearer error.
const unixPathMax = 104 // conservative — safe on both

// ResolveSocketPath computes the admin socket path.
//
//   - If SLACKRUN_ADMIN_SOCKET is set to "off" (case-insensitive), returns
//     ("", ErrDisabled) so the caller can skip listener setup.
//   - Otherwise, the explicit path from the env wins.
//   - Default: ${XDG_RUNTIME_DIR}/slackrun/slackrun.sock on Linux (falls back
//     to $TMPDIR when XDG_RUNTIME_DIR is unset — normal on macOS).
//   - macOS: $TMPDIR/slackrun-<uid>.sock (per-user, short enough for 104B).
//
// Returns an absolute path. Callers use PrepareSocket to unlink stale
// sockets and validate length before Listen.
func ResolveSocketPath() (string, error) {
	if v := strings.TrimSpace(os.Getenv(SocketEnvVar)); v != "" {
		if strings.EqualFold(v, "off") {
			return "", ErrDisabled
		}
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("%s must be an absolute path, got %q", SocketEnvVar, v)
		}
		return v, nil
	}
	return defaultSocketPath()
}

func defaultSocketPath() (string, error) {
	uid := currentUID()
	name := "slackrun-" + uid + ".sock"

	if runtime.GOOS == "linux" {
		if xdg := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); xdg != "" {
			return filepath.Join(xdg, "slackrun", "slackrun.sock"), nil
		}
	}
	tmp := os.TempDir()
	// macOS TempDir sits under /var/folders/... and is user-scoped, so a
	// per-uid filename is defence-in-depth for shared /tmp fallbacks.
	return filepath.Join(tmp, name), nil
}

func currentUID() string {
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return u.Uid
	}
	// user.Current can fail in static/cgo-disabled builds or minimal
	// containers without a passwd entry. os.Getuid always works and gives
	// us a real UID — falling back to a literal "0" would collapse every
	// non-root user onto the same socket file.
	return strconv.Itoa(os.Getuid())
}

// PrepareSocket ensures the parent directory exists, unlinks any stale
// socket at path, and validates the path length. Returns an error the caller
// should log and fail startup on (per current design; ResolveSocketPath's
// ErrDisabled short-circuits before this).
func PrepareSocket(path string) error {
	if len(path) >= unixPathMax {
		return fmt.Errorf(
			"admin socket path is too long for the OS (%d bytes, max %d): %s\n"+
				"Set %s to a shorter absolute path.",
			len(path), unixPathMax-1, path, SocketEnvVar,
		)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create admin socket dir %s: %w", dir, err)
	}
	// If a file exists at path, decide: another slackrun on it? or stale?
	// Lstat (not Stat) so we don't follow a symlink into the wrong place;
	// only clean up when it's genuinely a socket — refuse to touch regular
	// files that happen to share the path (an operator-typoed
	// SLACKRUN_ADMIN_SOCKET pointing at, say, ~/.env would otherwise be
	// deleted here).
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("admin socket path %s exists and is not a UNIX socket; refusing to overwrite (mode=%s)", path, info.Mode())
		}
		if isSocketAlive(path) {
			return fmt.Errorf("another slackrun appears to be listening on %s (delete it manually if not)", path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale socket %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat admin socket %s: %w", path, err)
	}
	return nil
}

// isSocketAlive attempts a short-timeout dial. A successful connect implies
// something is listening; refusal or timeout implies stale. Kept to a short
// timeout so start-up isn't held hostage by an unresponsive host FS.
func isSocketAlive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ListenUnix wraps net.Listen("unix", ...) with a 0600 chmod so only the
// owner can connect. The listener's Close removes the socket file too — no
// separate unlink needed on shutdown as long as Close is reached.
//
// Umask is ratcheted to 077 across the Listen call so the socket is *born*
// with 0600 semantics; the follow-up Chmod is defence-in-depth. Without
// this, a permissive umask (e.g. 0002 on shared-group Linux hosts) would
// leave a brief window between Listen and Chmod during which the socket is
// group-readable — a same-group but different-UID process could connect.
func ListenUnix(path string) (net.Listener, error) {
	prev := syscall.Umask(0o077)
	ln, err := net.Listen("unix", path)
	syscall.Umask(prev)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod admin socket 0600: %w", err)
	}
	return ln, nil
}
