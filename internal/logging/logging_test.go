package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestTextLoggerFiltersActualRecordsAtConfiguredLevel(t *testing.T) {
	for _, tc := range []struct {
		value string
		level slog.Level
	}{{"", slog.LevelInfo}, {"debug", slog.LevelDebug}, {"verbose", slog.LevelDebug}, {"info", slog.LevelInfo}, {"warn", slog.LevelWarn}, {"error", slog.LevelError}, {" DEBUG ", slog.LevelDebug}} {
		t.Run(tc.value, func(t *testing.T) {
			var out bytes.Buffer
			log, err := NewText(&out, tc.value)
			if err != nil {
				t.Fatal(err)
			}
			for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
				log.Log(context.Background(), level, "record-"+level.String())
				if log.Enabled(context.Background(), level) != (level >= tc.level) {
					t.Fatalf("handler threshold %s ignored %q", level, tc.value)
				}
				if strings.Contains(out.String(), "record-"+level.String()) != (level >= tc.level) {
					t.Fatalf("actual output threshold %s ignored %q: %s", level, tc.value, out.String())
				}
			}
		})
	}
}

func TestInvalidLogLevelDoesNotCreateLogger(t *testing.T) {
	for _, value := range []string{"quiet", "trace", "debug+1", "private-invalid-configuration"} {
		log, err := NewText(&bytes.Buffer{}, value)
		if log != nil || err == nil || !strings.Contains(err.Error(), LevelEnv) || strings.Contains(err.Error(), value) {
			t.Fatalf("invalid environment value accepted or echoed: %q: %v", value, err)
		}
	}
}
