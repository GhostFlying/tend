package testkit

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestSocketDirIgnoresLongAndSymlinkedTemporaryRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory and symlink fixture")
	}
	short := SocketDir(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("long-data-directory-", 9))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(short, "alias")
	if err := os.Symlink(long, alias); err != nil {
		t.Fatal(err)
	}
	for _, first := range []string{long, alias} {
		dir, err := shortSocketDir([]string{first, short}, "control.sock")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || real != dir || filepath.Dir(dir) != short {
			t.Fatalf("did not select a real short root: %q, %q, %v", dir, real, err)
		}
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("fixture directory is not private: %v, %v", info, err)
		}
	}
}

func TestSocketDirParallelCommunicationAndCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket fixture")
	}
	var mu sync.Mutex
	var directories []string
	t.Run(strings.Repeat("long-test-name-", 12), func(t *testing.T) {
		for range 8 {
			t.Run("concurrent", func(t *testing.T) {
				t.Parallel()
				dir := SocketDir(t)
				mu.Lock()
				directories = append(directories, dir)
				mu.Unlock()
				path := filepath.Join(dir, "s")
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o600); err != nil {
					_ = listener.Close()
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					conn, err := listener.Accept()
					if err == nil {
						defer conn.Close()
						_, err = io.Copy(conn, conn)
					}
					done <- err
				}()
				t.Cleanup(func() { _ = listener.Close() })
				conn, err := net.Dial("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Write([]byte("ok")); err != nil {
					t.Fatal(err)
				}
				var got [2]byte
				if _, err := io.ReadFull(conn, got[:]); err != nil || string(got[:]) != "ok" {
					t.Fatalf("Unix fixture communication failed: %q, %v", got, err)
				}
				_ = conn.Close()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		}
	})
	seen := make(map[string]bool)
	for _, dir := range directories {
		if seen[dir] {
			t.Fatalf("parallel fixtures shared %s", dir)
		}
		seen[dir] = true
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("socket fixture cleanup incomplete: %v", err)
		}
	}
}

func TestSocketPathValidatesActualLeafAndIgnoresGoTempRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket fixture")
	}
	short := SocketDir(t)
	long := filepath.Join(short, strings.Repeat("d", 50-len(short)-1))
	if err := os.Mkdir(long, 0700); err != nil {
		t.Fatal(err)
	}
	leaf := strings.Repeat("x", 56) + ".sock"
	dir, err := shortSocketDir([]string{long, short}, leaf)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if filepath.Dir(dir) != short {
		t.Fatal("actual suffix did not select the fallback", dir)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, leaf))
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	for _, bad := range []string{"", ".", "..", "../outside.sock", "/absolute.sock", `nested\socket`} {
		if _, err := shortSocketDir([]string{short}, bad); err == nil {
			t.Fatal("invalid leaf accepted", bad)
		}
	}
	t.Setenv("GOTMPDIR", t.TempDir())
	path := SocketPath(t, "upgrades.sock")
	listener, err = net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	real, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || real != filepath.Dir(path) {
		t.Fatal("socket directory is not real", path, err)
	}
}
