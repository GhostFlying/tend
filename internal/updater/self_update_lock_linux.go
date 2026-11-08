//go:build linux

package updater

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
)

// selfOwnsFlock proves a positive ownership fact for an already inspected
// process. Global /proc/locks iteration can miss held locks during unrelated
// lock churn. Missing per-descriptor evidence or a changed process is unknown.
func selfOwnsFlock(ctx context.Context, expected Identity, device, inode uint64) bool {
	if expected.PID <= 1 || expected.StartTicks == 0 {
		return false
	}
	stat := filepath.Join("/proc", strconv.Itoa(expected.PID), "stat")
	before, err := os.ReadFile(stat)
	if err != nil || !sameStart(before, expected.StartTicks) {
		return false
	}
	owns, err := processOwnsFlock(ctx, "/proc", expected.PID, device, inode)
	if err != nil || !owns {
		return false
	}
	after, err := os.ReadFile(stat)
	return err == nil && sameStart(after, expected.StartTicks)
}
