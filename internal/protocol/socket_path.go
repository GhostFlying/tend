package protocol

import (
	"fmt"
	"strings"
	"syscall"
)

// UnixSocketPathLimit is the pathname byte budget, including room for the
// terminating NUL in the current platform's sockaddr_un.
const UnixSocketPathLimit = len(syscall.RawSockaddrUnix{}.Path) - 1

// ValidateUnixSocketPath validates a filesystem address before any stale socket
// removal or service startup. It does not change the configured endpoint.
func ValidateUnixSocketPath(field, path string) error {
	if path == "" || strings.ContainsRune(path, 0) {
		return fmt.Errorf("%s must be a nonempty Unix socket pathname without NUL bytes", field)
	}
	if len(path) > UnixSocketPathLimit {
		return fmt.Errorf("%s is %d bytes; Unix socket pathname limit is %d bytes; select a shorter runtime directory", field, len(path), UnixSocketPathLimit)
	}
	return nil
}
