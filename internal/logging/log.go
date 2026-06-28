// Package logging emits one JSON object per record to stderr. PII redaction
// is applied to every string field so secrets cannot leak via accidental
// %v-style formatting.
package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/kohii/slackrun/internal/util"
)

// Level is the threshold above which records are emitted.
type Level int

const (
	LevelDebug Level = iota * 10
	LevelInfo  Level = 20
	LevelWarn  Level = 30
	LevelError Level = 40
)

// Logger is the global slackrun logger. Concurrent-safe.
var (
	mu sync.RWMutex
	cfg = Config{Level: LevelInfo, AllowRawEventText: false}
)

// Config controls runtime behavior. AllowRawEventText is an unsafe debug
// switch: even when on, redaction still runs over the captured text.
type Config struct {
	Level             Level
	AllowRawEventText bool
}

// Configure swaps the active config. Safe to call at startup before any
// goroutines log.
func Configure(c Config) {
	mu.Lock()
	cfg = c
	mu.Unlock()
}

// ParseLevel converts a textual level to its numeric constant. Unknown levels
// default to info.
func ParseLevel(s string) Level {
	switch s {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	}
	return LevelInfo
}

// Field is one key/value attached to a log record.
type Field struct {
	Key   string
	Value any
}

// F is a convenience for building Field{} inline.
func F(key string, value any) Field {
	return Field{Key: key, Value: value}
}

// Debug / Info / Warn / Error emit a record at the corresponding level.
func Debug(msg string, fields ...Field) { emit(LevelDebug, msg, fields) }
func Info(msg string, fields ...Field)  { emit(LevelInfo, msg, fields) }
func Warn(msg string, fields ...Field)  { emit(LevelWarn, msg, fields) }
func Error(msg string, fields ...Field) { emit(LevelError, msg, fields) }

func emit(level Level, msg string, fields []Field) {
	mu.RLock()
	threshold := cfg.Level
	allowRaw := cfg.AllowRawEventText
	mu.RUnlock()
	if level < threshold {
		return
	}

	rec := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"level": levelName(level),
		"msg":   util.RedactPII(msg),
	}
	for _, f := range fields {
		rec[f.Key] = serializeValue(f.Key, f.Value, allowRaw)
	}
	enc, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"level":"error","msg":"log encode failed: %v"}`+"\n", err)
		return
	}
	enc = append(enc, '\n')
	_, _ = os.Stderr.Write(enc)
}

func levelName(l Level) string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "info"
}

// serializeValue applies the same scrubbing rules as kohii-ai's logger:
//   - String values are PII-redacted
//   - Errors are flattened to {message, type}
//   - Anything keyed "event" or "slackEvent" is shape-stripped
//     (text/blocks/attachments masked unless AllowRawEventText is on)
func serializeValue(key string, v any, allowRaw bool) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case error:
		return map[string]any{"type": fmt.Sprintf("%T", v), "message": util.RedactPII(x.Error())}
	case string:
		return util.RedactPII(x)
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = util.RedactPII(s)
		}
		return out
	}
	if key == "event" || key == "slackEvent" {
		return sanitizeEvent(v, allowRaw)
	}
	return v
}

// sanitizeEvent recursively strips text/blocks/attachments from a Slack-event
// shaped value. Keeps everything else verbatim so we still see channel/ts/etc.
func sanitizeEvent(v any, allowRaw bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			switch k {
			case "text":
				if allowRaw {
					if s, ok := vv.(string); ok {
						out[k] = util.RedactPII(s)
						continue
					}
				}
				out[k] = "[stripped]"
			case "blocks", "attachments":
				out[k] = "[stripped]"
			case "message":
				out[k] = sanitizeEvent(vv, allowRaw)
			default:
				out[k] = sanitizeEvent(vv, allowRaw)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = sanitizeEvent(vv, allowRaw)
		}
		return out
	case string:
		return util.RedactPII(x)
	}
	return v
}
