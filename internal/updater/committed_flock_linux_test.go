//go:build linux

package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCommittedFlockBtrfsDeviceIdentity(t *testing.T) {
	const info = "pos:\t0\nflags:\t02100002\nmnt_id:\t127\nino:\t1024\nlock:\t1: FLOCK  ADVISORY  WRITE 1234 00:31:1024 0 EOF\n"
	const mounts = "127 25 0:49 / /mounted rw - btrfs /dev/example rw\n"
	cases := []struct {
		name, fdinfo, mountinfo string
		want                    bool
	}{
		{"subvolume", info, mounts, true},
		{"ordinary", strings.ReplaceAll(info, "00:31:", "00:32:"), strings.ReplaceAll(strings.ReplaceAll(mounts, "0:49", "0:50"), "btrfs", "ext4"), true},
		{"wrong owner", strings.ReplaceAll(info, "WRITE 1234", "WRITE 1235"), mounts, false},
		{"wrong inode", strings.ReplaceAll(info, ":1024 0", ":1025 0"), mounts, false},
		{"wrong lock device", strings.ReplaceAll(info, "00:31:", "00:33:"), mounts, false},
		{"wrong mount device", info, strings.ReplaceAll(mounts, "0:49", "0:48"), false},
		{"escaped mount path", info, strings.ReplaceAll(mounts, "/mounted", `/mounted\040space`), true},
		{"malformed mount", info, "127 25 malformed / /mounted rw - btrfs /dev/example rw\n", false},
		{"missing namespace mount", info, "", false},
		{"wrong mount id", strings.ReplaceAll(info, "127", "128"), mounts, false},
		{"non btrfs mismatch", info, strings.ReplaceAll(mounts, "btrfs", "ext4"), false},
		{"read lock", strings.ReplaceAll(info, "WRITE", "READ"), mounts, false},
		{"other lock type", strings.ReplaceAll(info, "FLOCK", "POSIX"), mounts, false},
		{"no lock", strings.Split(info, "lock:")[0], mounts, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fdOwnsFlock(c.fdinfo, c.mountinfo, 1234, 50, 1024); got != c.want {
				t.Fatalf("ownership=%v want %v", got, c.want)
			}
		})
	}
}

func TestCommittedFlockRequiresExactDescriptorStat(t *testing.T) {
	root := t.TempDir()
	pid := 1234
	dir := filepath.Join(root, fmt.Sprint(pid))
	for _, d := range []string{"fd", "fdinfo"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "fd", "5")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	device := statFlockDevice(uint64(st.Dev))
	mount := fmt.Sprintf("127 25 %d:%d / /mounted rw - ext4 /dev/example rw\n", device.major, device.minor)
	fdinfo := fmt.Sprintf("mnt_id:\t127\nlock:\t1: FLOCK ADVISORY WRITE %d %02x:%02x:%d 0 EOF\n", pid, device.major, device.minor, st.Ino)
	for name, data := range map[string]string{"mountinfo": mount, "fdinfo/5": fdinfo} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name          string
		device, inode uint64
		want          bool
	}{{"exact", uint64(st.Dev), st.Ino, true}, {"wrong stat device", uint64(st.Dev) + 1, st.Ino, false}, {"wrong stat inode", uint64(st.Dev), st.Ino + 1, false}} {
		t.Run(c.name, func(t *testing.T) {
			got, err := processOwnsFlock(t.Context(), root, pid, c.device, c.inode)
			if err != nil || got != c.want {
				t.Fatalf("ownership=%v err=%v", got, err)
			}
		})
	}
}

func TestCommittedFlockActualKernelOwner(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	check := func(want bool) {
		t.Helper()
		got, err := processOwnsFlock(t.Context(), "/proc", os.Getpid(), uint64(st.Dev), st.Ino)
		if err != nil || got != want {
			t.Fatalf("ownership=%v want %v err=%v", got, want, err)
		}
	}
	check(false)
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	check(false)
}
