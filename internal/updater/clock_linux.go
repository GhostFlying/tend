//go:build linux

package updater

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// BootClock persists a grant deadline against a Linux boot, not wall clocks.
func BootClock() (string, int64, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", 0, err
	}
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "", 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return "", 0, fmt.Errorf("updater: invalid boot clock")
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(string(boot)), int64(sec * 1000), nil
}
