package runner

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Result is what each rule execution produces. ExitCode mirrors the OS exit
// status (negative when the process did not run to completion).
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// TimedOut is true if the command was terminated because its rule's
	// timeout fired. The exit code in that case is irrelevant.
	TimedOut bool
	// NotFound is true if the command binary itself could not be located
	// (ENOENT). Distinct from a runtime exit-code != 0.
	NotFound bool
	// StartErr is set if cmd.Start() failed (binary missing, cwd missing, ...).
	StartErr error
}

// Options is the per-invocation input to Run.
type Options struct {
	// Command is the argv. Command[0] is the program; the rest are arguments.
	// Each element has already been template-expanded by the caller.
	Command []string
	// Cwd is the working directory; required, absolute.
	Cwd string
	// Env overrides specific variables on top of the parent process env. The
	// child sees both unless a key in Env shadows a parent value.
	Env map[string]string
	// Timeout fires the SIGTERM → SIGKILL (5s) sequence.
	Timeout time.Duration
	// SigKillGrace overrides the default 5s grace between SIGTERM and SIGKILL.
	// Used by tests to keep them fast.
	SigKillGrace time.Duration
}

// Handle exposes the running job. Done fires exactly once with the final
// Result. Kill is idempotent and triggers the SIGTERM → SIGKILL sequence.
type Handle struct {
	Done <-chan Result
	Kill func()
}

const defaultSigKillGrace = 5 * time.Second

// Run spawns the configured command and returns a Handle the caller can wait
// on. The returned channel is buffered; the caller never blocks the runner.
func Run(opts Options) Handle {
	done := make(chan Result, 1)

	if len(opts.Command) == 0 {
		done <- Result{ExitCode: -1, StartErr: errors.New("command argv is empty")}
		return Handle{Done: done, Kill: func() {}}
	}
	grace := opts.SigKillGrace
	if grace <= 0 {
		grace = defaultSigKillGrace
	}

	// We manage timeout / kill ourselves rather than using
	// exec.CommandContext so we can do the SIGTERM → SIGKILL two-stage. The
	// stdlib's CommandContext jumps straight to SIGKILL on context cancel.
	cmd := exec.Command(opts.Command[0], opts.Command[1:]...)
	cmd.Dir = opts.Cwd
	cmd.Env = buildEnv(opts.Env)
	// Detach into its own process group so signals reach the whole tree
	// (matters when the user wraps things in `sh -c`).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		notFound := errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
		done <- Result{
			ExitCode: -1,
			Stderr:   err.Error(),
			NotFound: notFound,
			StartErr: err,
		}
		return Handle{Done: done, Kill: func() {}}
	}

	var (
		timedOut    atomic.Bool
		killed      atomic.Bool
		killOnce    sync.Once
		sigKillT    *time.Timer
		sigKillMu   sync.Mutex
		timeoutTmr *time.Timer
	)

	sendKill := func() {
		killOnce.Do(func() {
			killed.Store(true)
			signalGroup(cmd, syscall.SIGTERM)
			sigKillMu.Lock()
			sigKillT = time.AfterFunc(grace, func() {
				signalGroup(cmd, syscall.SIGKILL)
			})
			sigKillMu.Unlock()
		})
	}

	if opts.Timeout > 0 {
		timeoutTmr = time.AfterFunc(opts.Timeout, func() {
			timedOut.Store(true)
			sendKill()
		})
	}

	go func() {
		waitErr := cmd.Wait()
		if timeoutTmr != nil {
			timeoutTmr.Stop()
		}
		sigKillMu.Lock()
		if sigKillT != nil {
			sigKillT.Stop()
		}
		sigKillMu.Unlock()

		// ExitCode handling: if the process was signalled, ExitCode() returns -1.
		// Surface the original exit status when available.
		exitCode := -1
		if waitErr == nil {
			exitCode = 0
		} else if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ProcessState.ExitCode()
		}

		stderrText := stderr.String()
		if waitErr != nil && !timedOut.Load() && !killed.Load() {
			if _, isExit := waitErr.(*exec.ExitError); !isExit {
				// Non-exit error (e.g. broken pipe). Surface as stderr context.
				stderrText = fmt.Sprintf("%s\n%s", stderrText, waitErr.Error())
			}
		}

		done <- Result{
			ExitCode: exitCode,
			Stdout:   stdout.String(),
			Stderr:   stderrText,
			TimedOut: timedOut.Load(),
		}
	}()

	return Handle{Done: done, Kill: sendKill}
}

func buildEnv(overrides map[string]string) []string {
	parent := os.Environ()
	if len(overrides) == 0 {
		return parent
	}
	// Build a map for quick override.
	idx := make(map[string]int, len(parent))
	for i, kv := range parent {
		if eq := indexEq(kv); eq >= 0 {
			idx[kv[:eq]] = i
		}
	}
	out := make([]string, len(parent), len(parent)+len(overrides))
	copy(out, parent)
	for k, v := range overrides {
		entry := k + "=" + v
		if i, ok := idx[k]; ok {
			out[i] = entry
		} else {
			out = append(out, entry)
		}
	}
	return out
}

func indexEq(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return i
		}
	}
	return -1
}

// signalGroup sends sig to the process group leader (pgid). Falls back to the
// process itself if the group lookup fails (rare; e.g. the process exited
// between the check and the kill).
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err == nil && pgid > 0 {
		// Negative pid → kill entire process group.
		_ = syscall.Kill(-pgid, sig)
		return
	}
	_ = cmd.Process.Signal(sig)
}
