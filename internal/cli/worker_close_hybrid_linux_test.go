//go:build linux

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func newWorkerCloseHybridFixture(t *testing.T) workerCloseFixture {
	t.Helper()
	f := newWorkerCloseFixture(t)
	unitPath := "/user.slice/user-1000.slice/user@1000.service/app.slice/" + f.q.Unit
	mount := filepath.Join(t.TempDir(), "systemd mount")
	f.path = filepath.Join(mount, unitPath)
	f.write(filepath.Join(f.host.procRoot, "100/cgroup"), "11:memory:/user.slice/user-1000.slice/user@1000.service\n1:name=systemd:"+unitPath+"\n0::/\n")
	f.write(filepath.Join(f.host.procRoot, "self/mountinfo"), "30 29 0:26 / "+strings.ReplaceAll(mount, " ", `\040`)+" rw,nosuid,nodev,noexec,relatime shared:10 - cgroup cgroup rw,xattr,name=systemd\n")
	f.write(filepath.Join(f.path, "cgroup.procs"), "100\n")
	f.write(filepath.Join(f.path, "tasks"), "100\n101\n102\n")
	return f
}

func TestWorkerCloseHybridSystemdBoundaryRequiresKernelRemoval(t *testing.T) {
	f := newWorkerCloseHybridFixture(t)
	ctx := context.Background()
	prepared, err := executeWorkerClose(ctx, f.q, f.host)
	if err != nil {
		t.Fatal("hybrid root-only unified hierarchy hid the actual systemd unit", err)
	}
	boundary := prepared.Intent.Boundary
	if boundary.Hierarchy != protocol.WorkerCloseSystemdV1 || boundary.FilesystemRoot == f.host.cgroupRoot || filepath.Join(boundary.FilesystemRoot, boundary.CgroupPath) != f.path {
		t.Fatalf("did not bind actual named hierarchy mount: %+v", boundary)
	}
	// Even empty direct member files cannot prove recursive v1 closure. They
	// may hide descendants, and no v2 cgroup.events is available in this layout.
	f.stopped()
	if err := os.Remove(filepath.Join(f.path, "cgroup.events")); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(f.path, "tasks"), "")
	f.q.Action = "confirm"
	if _, err := executeWorkerClose(ctx, f.q, f.host); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("existing v1 directory was promoted to empty proof: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.q.StateDirectory, protocol.WorkerCloseProofFilename(f.q.OwnedSHA256))); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed v1 closure published proof: %v", err)
	}
	if err := os.RemoveAll(f.path); err != nil {
		t.Fatal(err)
	}
	confirmed, err := executeWorkerClose(ctx, f.q, f.host)
	if err != nil || confirmed.Phase != "confirmed" {
		t.Fatalf("removed original v1 kernel directory: %+v %v", confirmed, err)
	}
	for _, observation := range confirmed.Proof.Observations {
		if observation.State != "kernel_removed" {
			t.Fatalf("v1 received another closure scope: %+v", observation)
		}
	}
	if err := confirmed.Proof.Validate(); err != nil {
		t.Fatal(err)
	}
	// Repeated confirmation reads the historical proof after a successor
	// recreates the named unit; no new observation or overwrite is attempted.
	f.write(filepath.Join(f.path, "cgroup.procs"), "999\n")
	again, err := executeWorkerClose(ctx, f.q, f.host)
	if err != nil || again.ProofSHA256 != confirmed.ProofSHA256 {
		t.Fatalf("historical v1 proof replay changed: %+v %v", again, err)
	}
}

func TestWorkerCloseHybridRejectsChangedBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(workerCloseFixture)
	}{
		{"directory_replaced", func(f workerCloseFixture) {
			if err := os.Rename(f.path, f.path+".original"); err != nil {
				f.t.Fatal(err)
			}
			f.write(filepath.Join(f.path, "cgroup.procs"), "")
		}},
		{"unit_renamed_without_replacement", func(f workerCloseFixture) {
			if err := os.Rename(f.path, f.path+".renamed"); err != nil {
				f.t.Fatal(err)
			}
			f.write(filepath.Join(f.path+".renamed", "nested/cgroup.procs"), "999\n")
		}},
		{"ancestor_renamed_without_replacement", func(f workerCloseFixture) {
			parent := filepath.Dir(f.path)
			if err := os.Rename(parent, parent+".renamed"); err != nil {
				f.t.Fatal(err)
			}
			f.write(filepath.Join(parent+".renamed", filepath.Base(f.path), "nested/cgroup.procs"), "999\n")
		}},
		{"mountpoint_changed", func(f workerCloseFixture) {
			f.write(filepath.Join(f.host.procRoot, "self/mountinfo"), "30 29 0:26 / /other/systemd rw - cgroup cgroup rw,name=systemd\n")
		}},
		{"mount_hierarchy_changed", func(f workerCloseFixture) {
			f.write(filepath.Join(f.host.procRoot, "self/mountinfo"), "30 29 0:26 / /other/cpu rw - cgroup cgroup rw,cpu,cpuacct\n")
		}},
		{"mount_directory_replaced", func(f workerCloseFixture) {
			mount := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(f.path)))))
			if err := os.Rename(mount, mount+".original"); err != nil {
				f.t.Fatal(err)
			}
			if err := os.MkdirAll(mount, 0700); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"descendant_cgroup_exists", func(f workerCloseFixture) {
			f.write(filepath.Join(f.path, "nested/cgroup.procs"), "999\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkerCloseHybridFixture(t)
			if _, err := executeWorkerClose(context.Background(), f.q, f.host); err != nil {
				t.Fatal(err)
			}
			f.stopped()
			tc.edit(f)
			f.q.Action = "confirm"
			if _, err := executeWorkerClose(context.Background(), f.q, f.host); err == nil {
				t.Fatal("changed or remaining v1 boundary accepted")
			}
			if _, err := os.Stat(filepath.Join(f.q.StateDirectory, protocol.WorkerCloseProofFilename(f.q.OwnedSHA256))); !os.IsNotExist(err) {
				t.Fatalf("invalid boundary wrote proof: %v", err)
			}
		})
	}
}

func TestWorkerClosePrefersNamedUnifiedV2AndReadsLegacyIntent(t *testing.T) {
	f := newWorkerCloseFixture(t)
	f.write(filepath.Join(f.host.procRoot, "100/cgroup"), "1:name=systemd:/another/original.service\n0::/system.slice/original.service\n")
	// A valid named unified unit remains preferred without requiring the
	// fallback mountinfo. Its JSON still omits both optional extension fields.
	prepared, err := executeWorkerClose(context.Background(), f.q, f.host)
	if err != nil || prepared.Intent.Boundary.Hierarchy != "" || prepared.Intent.Boundary.FilesystemRoot != "" {
		t.Fatalf("v2 preference: %+v %v", prepared, err)
	}
	body, err := os.ReadFile(prepared.IntentPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"hierarchy"`) || strings.Contains(string(body), `"filesystem_root"`) {
		t.Fatalf("legacy v2 canonical bytes changed: %s", body)
	}
	f.stopped()
	f.q.Action = "confirm"
	confirmed, err := executeWorkerClose(context.Background(), f.q, f.host)
	if err != nil || confirmed.Proof.Observations[0].State != "empty" {
		t.Fatalf("legacy v2 confirmation: %+v %v", confirmed, err)
	}
	var old protocol.WorkerCloseProof
	proofBody, err := os.ReadFile(confirmed.ProofPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(proofBody, &old); err != nil {
		t.Fatal(err)
	}
	if err := old.Validate(); err != nil {
		t.Fatal("legacy v2 proof no longer readable", err)
	}
}

func TestWorkerCloseV1RemovalMetadataLookupRemainsBoundedAndUnknownOnError(t *testing.T) {
	for _, name := range []string{"directory_limit", "cancelled", "read_error", "cross_device"} {
		t.Run(name, func(t *testing.T) {
			f := newWorkerCloseHybridFixture(t)
			prepared, err := executeWorkerClose(context.Background(), f.q, f.host)
			if err != nil {
				t.Fatal(err)
			}
			boundary := prepared.Intent.Boundary
			ctx := context.Background()
			root := boundary.FilesystemRoot
			var expected error
			switch name {
			case "directory_limit":
				// Populate the tree while the original inode is still allocated;
				// otherwise mkdir could reuse it and correctly stop the lookup
				// before the directory-limit branch we intend to exercise.
				for i := 0; i < 4096; i++ {
					if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("lookup-%04d", i)), 0700); err != nil {
						t.Fatal(err)
					}
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				expected = context.Canceled
			case "read_error":
				root = filepath.Join(root, "missing-root")
				expected = os.ErrNotExist
			case "cross_device":
				boundary.RootDevice++
			}
			if err := os.RemoveAll(f.path); err != nil {
				t.Fatal(err)
			}
			err = confirmWorkerCloseV1InodeRemoved(ctx, root, boundary)
			if err == nil || expected != nil && !errors.Is(err, expected) {
				t.Fatalf("uncertain lookup accepted or lost its cause: %v", err)
			}
			if name == "directory_limit" && !strings.Contains(err.Error(), "4096 directories") {
				t.Fatalf("wrong directory bound: %v", err)
			}
		})
	}
}
