package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/logging"
	"github.com/kohii/slackrun/internal/slackapp"
	"github.com/kohii/slackrun/internal/util"
)

// RunStart boots the Socket Mode app. The optional first positional arg
// overrides the rules file path; otherwise we honour SLACKRUN_CONFIG_PATH or
// the default ~/.config/slackrun/rules.yaml.
func RunStart(args []string, stdout, stderr io.Writer) int {
	_, _ = stdout, stderr // start writes via logging (stderr), not arg writers

	envPath, err := config.LoadDotenv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	env, err := config.ParseEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	logging.Configure(logging.Config{
		Level:             logging.ParseLevel(env.LogLevel),
		AllowRawEventText: env.AllowRawEventTextLog,
	})
	logging.Info("loaded env", logging.F("envPath", envPath))

	selfCheck := util.RunRedactSelfCheck()
	if !selfCheck.OK {
		logging.Warn("redact self-check failed", logging.F("failures", selfCheck.Failures))
	} else {
		logging.Info("redact self-check passed")
	}

	rulesPath := env.ConfigPath
	if len(args) >= 1 {
		rulesPath = config.ExpandHome(args[0])
	}
	rules, err := config.LoadRulesFile(rulesPath, config.CheckOptions{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, i := range rules.Issues {
		if i.Level == config.IssueError {
			logging.Error("rules error", logging.F("rule", i.RuleName), logging.F("message", i.Message))
		} else {
			logging.Warn("rules warning", logging.F("message", i.Message))
		}
	}
	if rules.HasErrors() {
		return 1
	}
	logging.Info("rules loaded", logging.F("rulesPath", rulesPath), logging.F("count", len(rules.Rules)))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	app, err := slackapp.New(ctx, slackapp.Options{Env: env, Rules: rules.Rules})
	if err != nil {
		logging.Error("startup failed", logging.F("error", err))
		return 1
	}

	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logging.Error("socketmode exit", logging.F("error", err))
		return 1
	}
	logging.Info("shutdown complete")
	return 0
}
