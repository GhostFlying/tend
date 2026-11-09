//go:build linux

package nativeusage

import (
	"os"
	"strconv"
	"strings"
)

// WriterIdentity is captured from the process we just launched. Failure leaves
// identity unknown; a PID alone is never used as a durable process identity.
func WriterIdentity(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	i := strings.LastIndex(string(raw), ") ")
	if i < 0 {
		return ""
	}
	fields := strings.Fields(string(raw)[i+2:])
	if len(fields) < 20 {
		return ""
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(boot)) + "/" + strconv.Itoa(pid) + "/" + fields[19]
}
