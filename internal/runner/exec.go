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
	// Env overrides specific variables on top of the (filtered) parent process
	// env. See buildEnv for the protected-key list that overrides cannot
	// reintroduce.
	Env map[string]string
	// ExposeSlackToken passes SLACK_BOT_TOKEN through to the child. Without it
	// the child cannot call `slackrun post|react|upload`. Default false.
	ExposeSlackToken bool
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
//
// Caller responsibility: `SLACKRUN_*` keys in opts.Env are injected verbatim
// (they describe the triggering event). slackapp populates them; if you call
// Run from somewhere else, set them yourself or omit them.
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
	cmd.Env = buildEnv(opts.Env, opts.ExposeSlackToken)
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

// alwaysStripFromOverride lists env keys that are *secrets* — they must never
// be supplied by a rule. `SLACK_BOT_TOKEN` is the opt-in case (gated by
// Options.ExposeSlackToken from the parent env), `SLACK_APP_TOKEN` and
// `ALLOWED_USER_IDS` have no legitimate child use.
var alwaysStripFromOverride = map[string]struct{}{
	"SLACK_BOT_TOKEN":  {},
	"SLACK_APP_TOKEN":  {},
	"ALLOWED_USER_IDS": {},
}

// slackrunReservedKeys are vars slackrun injects on every spawn. Rules cannot
// shadow them via `action.env` (would lie to the child about the triggering
// event), but slackrun itself populates them through Options.Env.
var slackrunReservedKeys = map[string]struct{}{
	"SLACKRUN_CHANNEL":   {},
	"SLACKRUN_TS":        {},
	"SLACKRUN_THREAD_TS": {},
	"SLACKRUN_USER":      {},
}

// IsProtectedEnvKey reports whether `key` is reserved by slackrun, so the
// rules loader can reject `action.env` entries that would silently lose.
// runner.buildEnv applies looser rules at runtime (trusts the caller of
// runner.Run to have validated).
func IsProtectedEnvKey(key string) bool {
	if _, ok := alwaysStripFromOverride[key]; ok {
		return true
	}
	_, ok := slackrunReservedKeys[key]
	return ok
}

func buildEnv(overrides map[string]string, exposeSlackToken bool) []string {
	parent := os.Environ()
	filtered := make([]string, 0, len(parent))
	for _, kv := range parent {
		eq := indexEq(kv)
		if eq < 0 {
			filtered = append(filtered, kv)
			continue
		}
		key := kv[:eq]
		if key == "SLACK_BOT_TOKEN" {
			if exposeSlackToken {
				filtered = append(filtered, kv)
			}
			continue
		}
		if key == "SLACK_APP_TOKEN" || key == "ALLOWED_USER_IDS" {
			continue
		}
		filtered = append(filtered, kv)
	}
	if len(overrides) == 0 {
		return filtered
	}
	idx := make(map[string]int, len(filtered))
	for i, kv := range filtered {
		if eq := indexEq(kv); eq >= 0 {
			idx[kv[:eq]] = i
		}
	}
	out := make([]string, len(filtered), len(filtered)+len(overrides))
	copy(out, filtered)
	for k, v := range overrides {
		// Defence-in-depth: never let an override smuggle a parent secret back
		// in. SLACKRUN_* keys are legitimate system injections, so the
		// reserved-key check is rules-only, not here.
		if _, banned := alwaysStripFromOverride[k]; banned {
			continue
		}
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
