package domain

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

const sha40 = "0123456789abcdef0123456789abcdef01234567"

func TestWorkspaceValidate(t *testing.T) {
	cases := []struct {
		name string
		w    Workspace
		ok   bool
	}{
		{"single mount", Workspace{Mounts: []Mount{{Repo: "tend"}}}, true},
		{"two repos", Workspace{Mounts: []Mount{{Repo: "douyin"}, {Repo: "template-assembler", Base: Base{Branch: "release/3.2"}}}}, true},
		{"pinned commit", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Commit: sha40}}}}, true},
		{"no mounts", Workspace{}, false},
		{"same repo twice", Workspace{Mounts: []Mount{{Repo: "tend"}, {Repo: "tend", Path: "tend2"}}}, false},
		{"same path twice", Workspace{Mounts: []Mount{{Repo: "a", Path: "x"}, {Repo: "b", Path: "x"}}}, false},
		{"nested path", Workspace{Mounts: []Mount{{Repo: "tend", Path: "a/b"}}}, false},
		{"dot path", Workspace{Mounts: []Mount{{Repo: "tend", Path: ".."}}}, false},
		{"uppercase repo", Workspace{Mounts: []Mount{{Repo: "Tend"}}}, false},
		{"branch and commit", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: "main", Commit: sha40}}}}, false},
		{"short commit", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Commit: "0123abc"}}}}, false},
		{"option-like branch", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: "-delete"}}}}, false},
		{"revision expression", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: "main@{1}"}}}}, false},
		{"branch with space", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: "my branch"}}}}, false},
		{"hidden component", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: "release/.hidden"}}}}, false},
		{"leading dot", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: ".hidden"}}}}, false},
		{"lock component", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: "topic.lock/fix"}}}}, false},
		{"nested branch", Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: "release/3.2"}}}}, true},
		{"bad profile", Workspace{Mounts: []Mount{{Repo: "tend"}}, Profile: "a b"}, false},
	}
	for _, tc := range cases {
		err := tc.w.Normalize().Validate()
		if (err == nil) != tc.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestWorkspaceNormalizeDefaultsPath(t *testing.T) {
	w := Workspace{Mounts: []Mount{{Repo: "douyin"}, {Repo: "template-assembler", Path: "lynx"}}}
	n := w.Normalize()
	if n.Mounts[0].Path != "douyin" || n.Mounts[1].Path != "lynx" {
		t.Errorf("paths = %q, %q", n.Mounts[0].Path, n.Mounts[1].Path)
	}
	if w.Mounts[0].Path != "" {
		t.Error("Normalize modified its receiver")
	}
	if !w.Normalize().Equal(n) {
		t.Error("normalized workspace differs from the original")
	}
}

func TestUpdateSpecWorkspaceRules(t *testing.T) {
	two := execSpec
	two.Workspace = Workspace{Mounts: []Mount{{Repo: "tend"}, {Repo: "codex"}}}
	moved := execSpec
	moved.Workspace = Workspace{Mounts: []Mount{{Repo: "tend", Base: Base{Branch: "release"}}}}
	other := execSpec
	other.Workspace = Workspace{Mounts: []Mount{{Repo: "codex"}}}
	linux := execSpec
	linux.Requirements = Requirements{OS: "linux"}
	renamed := execSpec
	renamed.Workspace.Template = "tend-only"

	cases := []struct {
		name     string
		next     Spec
		assigned bool
		ok       bool
	}{
		{"add a mount before assignment", two, false, true},
		{"add a mount after assignment", two, true, true},
		{"change base before assignment", moved, false, true},
		{"change base after assignment", moved, true, false},
		{"replace repo after assignment", other, true, false},
		{"change requirements after assignment", linux, true, false},
		{"template provenance only", renamed, true, true},
	}
	for _, tc := range cases {
		task := taskIn(protocol.TaskRunning)
		task.WorkspaceAssigned = tc.assigned
		_, err := UpdateSpec(task, tc.next, execSpec, task.ControlRevision)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
		if err != nil && protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Errorf("%s: code = %s, want invalid_request", tc.name, protocol.CodeOf(err))
		}
	}
	// Removing a mount after assignment is rejected too.
	task := taskIn(protocol.TaskRunning)
	task.WorkspaceAssigned = true
	if _, err := UpdateSpec(task, execSpec, two, task.ControlRevision); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Errorf("remove mount after assignment: err = %v, want invalid_request", err)
	}
}
