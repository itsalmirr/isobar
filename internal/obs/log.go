// Package obs configures the application's structured logger.
//
// Logs go to stderr in JSON, with level controlled by ISOBAR_LOG_LEVEL
// (debug | info | warn | error). The returned logger is also installed as
// the slog default so library code that calls slog.Info works coherently.
package obs

import (
	"log/slog"
	"os"
	"strings"
)

// Init builds a JSON slog.Logger writing to stderr at the given level
// ("" defaults to info). It also sets the slog default logger.
func Init(levelStr string) *slog.Logger {
	level := parseLevel(levelStr)

	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		// Rename the default "time" key to "ts" so log shippers can normalize.
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Key = "ts"
			}
			return a
		},
	})
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
