//go:build linux

package codexenv

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/GhostFlying/tend/internal/procx"
	"golang.org/x/sys/unix"
)

type nativeCommandFacts struct {
	UID          int
	GID          int
	Groups       []int
	Status       map[string]string
	Mounts       map[string]string
	Devices      bool
	PTY          bool
	DevnullChild bool
}

func commandFacts() nativeCommandFacts {
	groups, _ := os.Getgroups()
	out := nativeCommandFacts{UID: os.Getuid(), GID: os.Getgid(), Groups: groups, Status: map[string]string{}, Mounts: map[string]string{}}
	raw, _ := os.ReadFile("/proc/self/status")
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, _ := strings.Cut(line, ":")
		if key == "NoNewPrivs" || key == "CapBnd" || key == "CapEff" {
			out.Status[key] = strings.TrimSpace(value)
		}
	}
	raw, _ = os.ReadFile("/proc/self/mountinfo")
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 6 && (fields[4] == "/" || fields[4] == "/dev" || fields[4] == "/dev/pts") {
			out.Mounts[fields[4]] = fields[5]
		}
	}
	zero, err := os.Open("/dev/zero")
	if err == nil {
		b := make([]byte, 1)
		_, err = zero.Read(b)
		_ = zero.Close()
		if err == nil {
			null, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
			if err == nil {
				_, err = null.Write(b)
				_ = null.Close()
				out.Devices = err == nil
			}
		}
	}
	cmd := exec.Command("/bin/sh", "-c", "printf NATIVE_EXEC_OK")
	// A nil stdin makes os/exec open /dev/null inside this process.
	raw, err = cmd.Output()
	out.DevnullChild = err == nil && string(raw) == "NATIVE_EXEC_OK"
	master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err == nil {
		defer unix.Close(master)
		if unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0) == nil {
			number, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
			if err == nil {
				slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
				if err == nil {
					cmd := exec.Command("/bin/sh", "-c", "printf NATIVE_PTY_OK")
					cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
					err = cmd.Run()
					_ = slave.Close()
					fds := []unix.PollFd{{Fd: int32(master), Events: unix.POLLIN}}
					if n, pollErr := unix.Poll(fds, 1000); err == nil && pollErr == nil && n > 0 {
						b := make([]byte, 128)
						n, readErr := unix.Read(master, b)
						out.PTY = readErr == nil && strings.Contains(string(b[:n]), "NATIVE_PTY_OK")
					}
				}
			}
		}
	}
	return out
}

func TestNativeCommandHelper(t *testing.T) {
	if os.Getenv("TEND_TEST_NATIVE_COMMAND") != "1" {
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(commandFacts())
	os.Exit(0)
}

func TestNativeCommandPreservesHostExecutionAndRetainsSessionAliases(t *testing.T) {
	s, p := fixture(t)
	p.Skills.Installed = []Item{{ID: "owned", Path: skill(t, t.TempDir(), "owned", "owned")}}
	create(t, s, p)
	ref, err := (&Binding{Directory: s.Root, ID: p.ID}).Pin(t.Context(), "run:native-command")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := replaceEnv(os.Environ(), "HOME", p.UserHome)
	env = replaceEnv(env, "TEND_TEST_NATIVE_COMMAND", "1")
	cmd, cleanup, err := ref.Command(t.Context(), bin, []string{"-test.run=^TestNativeCommandHelper$"}, t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if cmd.Path != bin {
		t.Fatal("native command is still wrapped", cmd.Path)
	}
	baseline := commandFacts()
	if !baseline.Devices || !baseline.PTY || !baseline.DevnullChild {
		t.Skip("host does not support the execution fixture")
	}
	procx.Contain(cmd)
	raw, err := cmd.CombinedOutput()
	procx.KillGroup(cmd)
	if err != nil {
		t.Fatalf("native child: %v %s", err, raw)
	}
	var got nativeCommandFacts
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err, string(raw))
	}
	if !reflect.DeepEqual(got, baseline) {
		t.Fatalf("outer launch changed host execution: got=%+v want=%+v", got, baseline)
	}
	if err := ref.VerifyHome(ref.ProcessHome); err != nil || ref.VerifyHome(t.TempDir()) == nil {
		t.Fatal("native home identity not verified", err)
	}
	var config struct {
		SQLiteHome string `toml:"sqlite_home"`
	}
	if _, err := toml.DecodeFile(filepath.Join(ref.ProcessHome, "config.toml"), &config); err != nil || config.SQLiteHome != p.Home {
		t.Fatal("SQLite left the fixed home", config, err)
	}
	marker := filepath.Join(ref.ProcessHome, "sessions", "original-rollout.jsonl")
	if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	for _, path := range []string{marker, filepath.Join(p.Home, "sessions", "original-rollout.jsonl")} {
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "retained" {
			t.Fatal("cleanup invalidated native history or fixed usage root", path, err)
		}
	}
}

func TestNativeHomesKeepConcurrentGenerationsSeparate(t *testing.T) {
	s, p := fixture(t)
	instructions := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(instructions, []byte("CATEGORY_V1\nAGENT_V1"), 0600); err != nil {
		t.Fatal(err)
	}
	p.Agents = Agents{Mode: "sync", Source: instructions}
	create(t, s, p)
	binding := &Binding{Directory: s.Root, ID: p.ID}
	first, err := binding.Pin(t.Context(), "run:first")
	if err != nil {
		t.Fatal(err)
	}
	env := replaceEnv(os.Environ(), "HOME", p.UserHome)
	cmd, clean, err := first.Command(t.Context(), "/bin/true", nil, t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	defer clean()
	if err := os.WriteFile(instructions, []byte("CATEGORY_V2\nAGENT_V2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncAutomatic(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	second, err := binding.Pin(t.Context(), "run:second")
	if err != nil || second.Generation == first.Generation {
		t.Fatal("second generation missing", err)
	}
	cmd, cleanSecond, err := second.Command(t.Context(), "/bin/true", nil, t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanSecond()
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	for _, view := range []struct {
		ref  *Reference
		text string
	}{{first, "CATEGORY_V1\nAGENT_V1"}, {second, "CATEGORY_V2\nAGENT_V2"}} {
		raw, err := os.ReadFile(filepath.Join(view.ref.ProcessHome, "AGENTS.md"))
		if err != nil || string(raw) != view.text {
			t.Fatal("generation instructions crossed process homes", string(raw), err)
		}
	}
	if first.ProcessHome == second.ProcessHome {
		t.Fatal("writable plugin home is shared")
	}
	if err := os.WriteFile(filepath.Join(first.ProcessHome, "plugins", "first-only"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(second.ProcessHome, "plugins", "first-only")); !os.IsNotExist(err) {
		t.Fatal("plugin cache write crossed process boundary", err)
	}
}

func TestNativeExecutionHomeKeepsOAuthFileAcrossRestart(t *testing.T) {
	s, p := fixture(t)
	create(t, s, p)
	binding := &Binding{Directory: s.Root, ID: p.ID}
	env := replaceEnv(os.Environ(), "HOME", p.UserHome)
	first, err := binding.Pin(t.Context(), "coordinator:oauth-fixture")
	if err != nil {
		t.Fatal(err)
	}
	cmd, clean, err := first.Command(t.Context(), "/bin/true", nil, t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Run(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(first.ProcessHome, ".credentials.json")
	// Match native OAuth's no-follow regular-file write, with no real token.
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = unix.Write(fd, []byte("{}"))
	_ = unix.Close(fd)
	if err != nil {
		t.Fatal(err)
	}
	clean()
	second, err := binding.Pin(t.Context(), "coordinator:oauth-fixture")
	if err != nil {
		t.Fatal(err)
	}
	cmd, clean, err = second.Command(t.Context(), "/bin/true", nil, t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer clean()
	if err = cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if first.ProcessHome != second.ProcessHome {
		t.Fatal("native OAuth home changed across restart")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "{}" {
		t.Fatal("native OAuth state was overwritten", err)
	}
	if _, err := os.Lstat(filepath.Join(p.Home, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatal("OAuth file was copied into another home", err)
	}
}
