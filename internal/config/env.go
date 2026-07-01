// Package config loads slackrun's two on-disk inputs: the .env secrets file
// and the rules.yaml routing file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const (
	defaultMaxConcurrent       = 5
	defaultMinEventAgeMsAtBoot = 300_000
	defaultLogLevel            = "info"

	defaultEnvPath        = "~/.config/slackrun/.env"
	defaultConfigPathYAML = "~/.config/slackrun/rules.yaml"
)

// AppEnv is the validated set of environment variables slackrun consumes.
// Built from process.env after .env has been merged in.
type AppEnv struct {
	SlackBotToken        string
	SlackAppToken        string
	ConfigPath           string // resolved path to rules.yaml
	MaxConcurrent        int
	MinEventAgeMsAtBoot  int
	LogLevel             string // debug | info | warn | error
	AllowRawEventTextLog bool
}

// ExpandHome resolves a leading `~` to the user's home directory. Paths
// without `~` are returned unchanged (absolute) or made absolute against the
// current working dir (relative).
func ExpandHome(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// LoadDotenv merges the configured .env file into the process environment.
// Variables already set in process.env win (godotenv defaults to Overload=false).
// Returns the resolved path so the caller can log it.
func LoadDotenv() (string, error) {
	raw := os.Getenv("SLACKRUN_ENV_PATH")
	if raw == "" {
		raw = defaultEnvPath
	}
	path := ExpandHome(raw)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, fmt.Errorf("env file not found at %s. Set SLACKRUN_ENV_PATH to override, or copy .env.example to ~/.config/slackrun/.env", path)
		}
		return path, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := godotenv.Load(path); err != nil {
		return path, fmt.Errorf("load %s: %w", path, err)
	}
	return path, nil
}

// ParseEnv reads + validates env vars. Call only after LoadDotenv.
func ParseEnv() (AppEnv, error) {
	var errs []string

	bot := os.Getenv("SLACK_BOT_TOKEN")
	if bot == "" {
		errs = append(errs, "SLACK_BOT_TOKEN is required")
	} else if !strings.HasPrefix(bot, "xoxb-") {
		errs = append(errs, "SLACK_BOT_TOKEN must start with xoxb-")
	}

	app := os.Getenv("SLACK_APP_TOKEN")
	if app == "" {
		errs = append(errs, "SLACK_APP_TOKEN is required")
	} else if !strings.HasPrefix(app, "xapp-") {
		errs = append(errs, "SLACK_APP_TOKEN must start with xapp- (Socket Mode token)")
	}

	if strings.TrimSpace(os.Getenv("ALLOWED_USER_IDS")) != "" {
		errs = append(errs, "ALLOWED_USER_IDS env var was removed — move the list to `allowed_user_ids:` at the top of rules.yaml")
	}

	maxConc, err := parseIntDefault("MAX_CONCURRENT", defaultMaxConcurrent, func(n int) bool { return n > 0 }, "must be > 0")
	if err != nil {
		errs = append(errs, err.Error())
	}

	minAge, err := parseIntDefault("MIN_EVENT_AGE_MS_AT_BOOT", defaultMinEventAgeMsAtBoot, func(n int) bool { return n >= 0 }, "must be >= 0")
	if err != nil {
		errs = append(errs, err.Error())
	}

	level := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if level == "" {
		level = defaultLogLevel
	}
	switch level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Sprintf("LOG_LEVEL %q is not one of debug|info|warn|error", level))
	}

	allowRaw := strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_RAW_EVENT_TEXT_LOG")), "true")

	if len(errs) > 0 {
		return AppEnv{}, fmt.Errorf("invalid env: %s", strings.Join(errs, "; "))
	}

	cfg := os.Getenv("SLACKRUN_CONFIG_PATH")
	if cfg == "" {
		cfg = defaultConfigPathYAML
	}

	return AppEnv{
		SlackBotToken:        bot,
		SlackAppToken:        app,
		ConfigPath:           ExpandHome(cfg),
		MaxConcurrent:        maxConc,
		MinEventAgeMsAtBoot:  minAge,
		LogLevel:             level,
		AllowRawEventTextLog: allowRaw,
	}, nil
}

func parseIntDefault(name string, def int, ok func(int) bool, why string) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q: not an integer", name, raw)
	}
	if !ok(n) {
		return 0, fmt.Errorf("%s %d: %s", name, n, why)
	}
	return n, nil
}
