//go:build linux

package updater

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type flockDevice struct{ major, minor uint64 }

func statFlockDevice(device uint64) flockDevice {
	return flockDevice{((device >> 8) & 0xfff) | ((device >> 32) & 0xfffff000), (device & 0xff) | ((device >> 12) & 0xffffff00)}
}

func parseFlockDevice(value string, base int) (flockDevice, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return flockDevice{}, false
	}
	major, e1 := strconv.ParseUint(parts[0], base, 64)
	minor, e2 := strconv.ParseUint(parts[1], base, 64)
	return flockDevice{major, minor}, e1 == nil && e2 == nil
}

// fdOwnsFlock binds a kernel lock observation to one exact open file, including
// Btrfs's distinct subvolume stat device and superblock lock device.
func fdOwnsFlock(fdinfo, mountinfo string, pid int, device, inode uint64) bool {
	mountID := ""
	var locks [][]string
	for _, line := range strings.Split(fdinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "mnt_id:" {
			mountID = fields[1]
		}
		if len(fields) == 9 && fields[0] == "lock:" {
			locks = append(locks, fields)
		}
	}
	if mountID == "" {
		return false
	}
	var mountDevice flockDevice
	filesystem := ""
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[0] != mountID {
			continue
		}
		d, ok := parseFlockDevice(fields[2], 10)
		if !ok {
			return false
		}
		for i := 6; i+1 < len(fields); i++ {
			if fields[i] == "-" {
				mountDevice = d
				filesystem = fields[i+1]
				break
			}
		}
		break
	}
	if filesystem == "" {
		return false
	}
	for _, fields := range locks {
		if fields[2] != "FLOCK" || fields[3] != "ADVISORY" || fields[4] != "WRITE" || fields[5] != strconv.Itoa(pid) || fields[7] != "0" || fields[8] != "EOF" {
			continue
		}
		parts := strings.Split(fields[6], ":")
		if len(parts) != 3 {
			continue
		}
		lockDevice, ok := parseFlockDevice(strings.Join(parts[:2], ":"), 16)
		lockInode, err := strconv.ParseUint(parts[2], 10, 64)
		if !ok || err != nil || lockInode != inode {
			continue
		}
		if lockDevice == mountDevice && (lockDevice == statFlockDevice(device) || filesystem == "btrfs") {
			return true
		}
	}
	return false
}

func processOwnsFlock(ctx context.Context, procRoot string, pid int, device, inode uint64) (bool, error) {
	root := filepath.Join(procRoot, strconv.Itoa(pid))
	mounts, err := os.ReadFile(filepath.Join(root, "mountinfo"))
	if err != nil {
		return false, fmt.Errorf("updater: read lock owner mounts: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "fd"))
	if err != nil {
		return false, fmt.Errorf("updater: read lock owner descriptors: %w", err)
	}
	matches := func(info os.FileInfo) bool {
		st, ok := info.Sys().(*syscall.Stat_t)
		return ok && info.Mode().IsRegular() && uint64(st.Dev) == device && st.Ino == inode
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if _, err := strconv.ParseUint(entry.Name(), 10, 32); err != nil {
			continue
		}
		path := filepath.Join(root, "fd", entry.Name())
		before, err := os.Stat(path)
		if err != nil || !matches(before) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, "fdinfo", entry.Name()))
		if err != nil {
			return false, fmt.Errorf("updater: read exact lock descriptor: %w", err)
		}
		after, err := os.Stat(path)
		if err != nil || !matches(after) || !os.SameFile(before, after) {
			return false, ErrUnknown
		}
		if fdOwnsFlock(string(raw), string(mounts), pid, device, inode) {
			return true, nil
		}
	}
	return false, nil
}
