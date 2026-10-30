//go:build linux

package procx

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCoordinatorNativeProcessDiesWithRunner(t *testing.T) {
	if os.Getenv("TEND_TEST_PARENT_DEATH_HELPER") == "1" {
		child := exec.CommandContext(t.Context(), "sleep", "30")
		Contain(child)
		DieWithParent(child)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(30 * time.Second)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.CommandContext(t.Context(), executable, "-test.run=^TestCoordinatorNativeProcessDiesWithRunner$", "-test.timeout=35s")
	parent.Env = append(os.Environ(), "TEND_TEST_PARENT_DEATH_HELPER=1")
	out, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() {
		t.Fatal("helper did not report native child identity", scanner.Err())
	}
	pid, err := strconv.Atoi(scanner.Text())
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err = parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = parent.Wait(); err == nil {
		t.Fatal("helper did not crash")
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		// A container init can defer reaping the exited process; a zombie
		// has no native execution left and cannot issue business calls.
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) || (err == nil && strings.Contains(string(stat), ") Z ")) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("native process survived its runner's death")
		case <-tick.C:
		}
	}
}
