package slackapp

import (
	"github.com/kohii/slackrun/internal/runmgr"
	"github.com/kohii/slackrun/internal/runner"
)

// runnerHandleAdapter adapts a runner.Handle (owned by the caller of Run)
// to the minimal Handle surface runmgr requires. Keeping the interface tiny
// keeps runmgr free of any process-management concepts (like Done channels)
// that only the run's owner should touch.
type runnerHandleAdapter struct{ h runner.Handle }

func (a runnerHandleAdapter) Kill()    { a.h.Kill() }
func (a runnerHandleAdapter) PID() int { return a.h.Pid }

// resultToCause maps a runner.Result to a runmgr.ExitCause. The Killed
// branch is only picked when Manager.Kill has not already stamped
// CauseKilled — first-writer-wins is enforced inside Manager.Complete.
func resultToCause(r runner.Result) runmgr.ExitCause {
	switch {
	case r.TimedOut:
		return runmgr.CauseTimedOut
	case r.NotFound:
		return runmgr.CauseNotFound
	case r.StartErr != nil:
		return runmgr.CauseStartError
	case r.Killed:
		return runmgr.CauseKilled
	default:
		return runmgr.CauseExit
	}
}
