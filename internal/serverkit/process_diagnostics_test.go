package serverkit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"runtime"
	"testing"
)

func TestServerProcessLogIdentifiesActualOutputStreams(t *testing.T) {
	var output bytes.Buffer
	logServerProcess(slog.New(slog.NewJSONHandler(&output, nil)))
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if record["pid"] != float64(os.Getpid()) || record["parent_pid"] != float64(os.Getppid()) || record["executable"] != executable {
		t.Fatal("diagnostics are not bound to current process", record)
	}
	if runtime.GOOS == "linux" {
		for field, path := range map[string]string{"stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2"} {
			if target, err := os.Readlink(path); err == nil && record[field] != target {
				t.Fatalf("%s does not identify actual output stream: %+v", field, record)
			}
		}
	}
}
