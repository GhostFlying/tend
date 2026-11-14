package serverkit

import (
	"log/slog"
	"os"
	"runtime"
)

// logServerProcess identifies the actual process and output streams. In
// particular, a configured historical log file is not evidence of where the
// currently supervised child is writing. No arguments or environment are read.
func logServerProcess(log *slog.Logger) {
	executable, err := os.Executable()
	fields := []any{"pid", os.Getpid(), "parent_pid", os.Getppid(), "goos", runtime.GOOS}
	if err == nil {
		fields = append(fields, "executable", executable)
	}
	if runtime.GOOS == "linux" {
		for _, stream := range []struct{ name, path string }{{"stdout", "/proc/self/fd/1"}, {"stderr", "/proc/self/fd/2"}} {
			if target, err := os.Readlink(stream.path); err == nil {
				fields = append(fields, stream.name, target)
			}
		}
	}
	log.Info("server process log provenance", fields...)
}
