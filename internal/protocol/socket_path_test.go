package protocol

import (
	"strings"
	"testing"
)

func TestUnixSocketPathUsesCompleteByteLength(t *testing.T) {
	for _, path := range []string{"/" + strings.Repeat("x", UnixSocketPathLimit-1), "/" + strings.Repeat("界", (UnixSocketPathLimit-1)/3)} {
		if err := ValidateUnixSocketPath("control_socket", path); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"", "/bad\x00socket", "/" + strings.Repeat("x", UnixSocketPathLimit), "/" + strings.Repeat("界", UnixSocketPathLimit/3+1)} {
		if err := ValidateUnixSocketPath("control_socket", path); err == nil || !strings.Contains(err.Error(), "control_socket") {
			t.Fatalf("invalid path accepted or configuration omitted: %q, %v", path, err)
		}
	}
}
