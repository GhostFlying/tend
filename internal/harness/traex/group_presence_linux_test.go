//go:build linux

package traex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The supervisor is a separate subreaper, so this test neither changes the
// test runner's process-wide child handling nor leaves zombies with PID 1.
func TestOwnedGroupPresenceSurvivesNonleaderForkHandoff(t *testing.T) {
	if os.Getenv("TEND_TEST_GROUP_PRESENCE_HELPER") == "1" {
		runGroupPresenceSupervisor(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	script := filepath.Join(dir, "handoff.sh")
	body := `dir=$1
remaining=$2
if [ "$remaining" -eq 400 ]; then
  while [ ! -f "$dir/go" ]; do sleep 0.001; done
fi
if [ "$remaining" -eq 0 ]; then
  echo "$$" > "$dir/final"
  while [ ! -f "$dir/finish" ]; do sleep 0.001; done
  exit 0
fi
sh "$0" "$dir" "$((remaining - 1))" &
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	supervisor := exec.CommandContext(ctx, binary, "-test.run=^TestOwnedGroupPresenceSurvivesNonleaderForkHandoff$")
	supervisor.Env = append(os.Environ(), "TEND_TEST_GROUP_PRESENCE_HELPER=1", "TEND_TEST_GROUP_PRESENCE_DIRECTORY="+dir)
	supervisor.Stdout, supervisor.Stderr = os.Stdout, os.Stderr
	if err := supervisor.Start(); err != nil {
		t.Fatal(err)
	}
	var group int
	waited := false
	groupReaped := false
	t.Cleanup(func() {
		if group > 1 && !groupReaped {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
		if !waited {
			// Let the subreaper reap killed descendants before its own exit.
			_ = supervisor.Wait()
		}
	})
	waitFile := func(name string) string {
		t.Helper()
		for {
			value, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil && strings.TrimSpace(string(value)) != "" {
				return strings.TrimSpace(string(value))
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
				t.Fatalf("waiting for %s: %v", name, ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	group, err = strconv.Atoi(waitFile("group"))
	if err != nil || group <= 1 {
		t.Fatalf("invalid fixture group %d: %v", group, err)
	}
	memberPID, err := strconv.Atoi(waitFile("member"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := inspectOwnedProcess(group)
	if err != nil {
		t.Fatal(err)
	}
	member, observedGroup, active, err := readOwnedProcess(memberPID)
	if err != nil || !active || observedGroup != group || memberPID == group {
		t.Fatalf("fixture did not create a nonleader: %+v group=%d active=%t error=%v", member, observedGroup, active, err)
	}
	member.Group = group
	writeMarker := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeMarker("reap-leader")
	waitFile("leader-reaped")
	writeMarker("go")
	passes := 0
	for {
		present, err := ownedGroupPresent(group)
		if err != nil || !present {
			t.Fatalf("fork/handoff lost a continuously existing kernel group: present=%t error=%v kernel=%v", present, err, syscall.Kill(-group, 0))
		}
		if err := confirmKnownOwnedProcessSetStopped(ctx, original, []ownedProcessIdentity{member}); err == nil {
			t.Fatal("nonleader fork/handoff was promoted to closed while its group still exists")
		}
		passes++
		if _, err := os.Stat(filepath.Join(dir, "final")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	finalPID, err := strconv.Atoi(waitFile("final"))
	if err != nil {
		t.Fatal(err)
	}
	_, finalGroup, active, err := readOwnedProcess(finalPID)
	if err != nil || !active || finalGroup != group || finalPID == memberPID || finalPID == group {
		t.Fatalf("handoff did not retain a living descendant: pid=%d group=%d active=%t error=%v", finalPID, finalGroup, active, err)
	}
	writeMarker("finish")
	waitFile("all-reaped")
	if err := supervisor.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true
	groupReaped = true
	if err := syscall.Kill(-group, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("subreaper did not remove the complete original group: %v", err)
	}
	if present, err := ownedGroupPresent(group); err != nil || present {
		t.Fatalf("disappeared kernel group remains present=%t error=%v", present, err)
	}
	if err := confirmKnownOwnedProcessSetStopped(ctx, original, []ownedProcessIdentity{member}); err != nil {
		t.Fatalf("fully reaped original group cannot reconcile: %v", err)
	}
	t.Logf("checked %d handoff observations; accepted only after all group members were reaped", passes)
}

func runGroupPresenceSupervisor(t *testing.T) {
	t.Helper()
	const prSetChildSubreaper = 36
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		t.Fatal(errno)
	}
	dir := os.Getenv("TEND_TEST_GROUP_PRESENCE_DIRECTORY")
	leader := exec.Command("sh", "-c", `sh "$1/handoff.sh" "$1" 400 & echo $! > "$1/member"; while [ ! -f "$1/reap-leader" ]; do sleep 0.001; done`, "fixture", dir)
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("group", strconv.Itoa(leader.Process.Pid))
	if err := leader.Wait(); err != nil {
		t.Fatal(err)
	}
	write("leader-reaped", "ready")
	for {
		var status syscall.WaitStatus
		_, err := syscall.Wait4(-1, &status, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.ECHILD) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	write("all-reaped", "ready")
}
