// Package logging constructs the service entrypoints' ordinary slog handler.
package logging

import (
	"errors"
	"io"
	"log/slog"
	"strings"
)

const LevelEnv = "TEND_LOG_LEVEL"

const EnvironmentHelp = "Logging: TEND_LOG_LEVEL=debug|info|warn|error (default: info; verbose aliases debug). Logs go to stderr and retain the configured supervisor rotation."

// NewText preserves the existing stderr text format and chooses its threshold.
// The caller supplies the environment value; this package has no process state.
func NewText(output io.Writer, value string) (*slog.Logger, error) {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" {
		text = "info"
	}
	if text == "verbose" {
		text = "debug"
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(text)); err != nil {
		return nil, errors.New("TEND_LOG_LEVEL must be debug, info, warn or error; verbose aliases debug")
	}
	switch level {
	case slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError:
	default:
		return nil, errors.New("TEND_LOG_LEVEL must be debug, info, warn or error; verbose aliases debug")
	}
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level})), nil
}
