package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFileTextUnicodeWindows(t *testing.T) {
	data := []byte("甲乙丙丁")
	var text string
	for offset := 0; offset < len(data); {
		end := min(len(data), offset+4)
		chunk, skip, consumed, binary := fileText(data[offset:end], int64(offset), int64(len(data)))
		if binary || skip != 0 || consumed == 0 {
			t.Fatalf("offset %d: %q skip=%d consumed=%d binary=%v", offset, chunk, skip, consumed, binary)
		}
		text += chunk
		offset += consumed
	}
	if text != string(data) {
		t.Fatalf("read %q, want %q", text, data)
	}
	text, skip, _, binary := fileText(data[1:7], 1, int64(len(data)))
	if text != "乙" || skip != 2 || binary {
		t.Fatalf("unaligned read = %q skip=%d binary=%v", text, skip, binary)
	}
}
func TestFileTextBinaryNeverReturnsRawContent(t *testing.T) {
	for _, data := range [][]byte{{0, 1, 2, 255}, []byte("invalid\xfftext"), []byte("nul\x00text")} {
		text, _, consumed, binary := fileText(data, 0, int64(len(data)))
		if text != "" || !binary || consumed != len(data) {
			t.Fatalf("binary window = %q consumed=%d binary=%v", text, consumed, binary)
		}
	}
}
func TestFileReadRejectsUnboundedOrInvalidInputs(t *testing.T) {
	e := newEnv(t)
	for _, a := range []fileArgs{{FileID: "file", Offset: -1}, {FileID: "file", MaxBytes: new(65537)}, {FileID: "file", MaxBytes: new(1)}, {}} {
		r := e.call("files_read", a, "")
		if r.OK || r.Error == nil || r.Error.Code != "invalid_request" {
			t.Fatalf("args %+v: %+v", a, r)
		}
	}
}

func TestFilesReadFinalizedWindowsAndBinaryMetadata(t *testing.T) {
	e := newEnv(t)
	taskID, _ := createdTask(t, e.call("tasks_create", createArgs{Title: "files", Spec: spec, CommitSeq: 1}, ""))
	content := []byte(strings.Repeat("甲", 30000))
	file := protocol.ArtifactFile{FileID: "file_text", Name: "调查.txt", Size: int64(len(content)), SHA256: strings.Repeat("a", 64)}
	binaryFile := protocol.ArtifactFile{FileID: "file_binary", Name: "bytes.bin", Size: 4, SHA256: strings.Repeat("b", 64)}
	ctx := context.Background()
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		actions, err := store.ListActions(ctx, tx.Q(), taskID)
		if err != nil {
			return err
		}
		if len(actions) == 0 {
			return fmt.Errorf("no implementation action")
		}
		if err := tx.InsertRun(ctx, store.RunRow{RunID: "run_file", TaskID: taskID, ActionID: actions[0].ActionID, Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunFinished}); err != nil {
			return err
		}
		if err := tx.InsertDeliverableFile(ctx, store.DeliverableFileRow{ArtifactFile: file, TaskID: taskID, RunID: "run_file", Content: content}); err != nil {
			return err
		}
		return tx.InsertDeliverableFile(ctx, store.DeliverableFileRow{ArtifactFile: binaryFile, TaskID: taskID, RunID: "run_file", Content: []byte{0, 1, 2, 255}})
	}); err != nil {
		t.Fatal(err)
	}
	if r := e.call("files_read", fileArgs{FileID: file.FileID}, ""); r.OK || r.Error == nil || r.Error.Code != protocol.CodeNotFound {
		t.Fatalf("unfinalized upload readable: %+v", r)
	}
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		ref, err := json.Marshal(protocol.ArtifactPayload{Files: []protocol.ArtifactFile{file, binaryFile}})
		if err != nil {
			return err
		}
		return tx.InsertArtifact(ctx, "artifact_files", taskID, "run_file", "files", string(ref), "digest", 0)
	}); err != nil {
		t.Fatal(err)
	}
	read := func(a fileArgs) fileWindow {
		t.Helper()
		r := e.call("files_read", a, "")
		var result fileWindow
		if !r.OK || json.Unmarshal(r.Result, &result) != nil {
			t.Fatalf("read failed %+v", r)
		}
		return result
	}
	first := read(fileArgs{FileID: file.FileID, MaxBytes: new(65536)})
	if first.Size != file.Size || len(first.Text) != 65535 || first.NextOffset != 65535 || first.EOF || first.Binary {
		t.Fatalf("first window size=%d text=%d next=%d eof=%v binary=%v", first.Size, len(first.Text), first.NextOffset, first.EOF, first.Binary)
	}
	next := read(fileArgs{FileID: file.FileID, Offset: first.NextOffset, MaxBytes: new(65536)})
	if !next.EOF || first.Text+next.Text != string(content) {
		t.Fatal("UTF-8 text did not reconstruct through bounded windows")
	}
	binary := read(fileArgs{FileID: binaryFile.FileID})
	if !binary.Binary || binary.Text != "" || binary.Size != 4 {
		t.Fatalf("binary returned content %+v", binary)
	}
}

func TestReportTaskToolsAndContextWithoutRepository(t *testing.T) {
	e := newEnv(t)
	if err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), "UPDATE hosts SET workspace_protocol=1 WHERE host_id=?", "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reportSpec := domain.Spec{Goal: "investigate resource reuse", Acceptance: []string{"report names findings and uncertainties"}, Deliverables: []string{domain.DeliveryReport}, Workspace: domain.Workspace{Mode: domain.WorkspaceTemporary}}
	result := e.call("tasks_create", createArgs{Title: "investigation", Spec: reportSpec, CommitSeq: 1}, "")
	if !result.OK {
		t.Fatalf("report create: %+v", result.Error)
	}
	taskID, _ := createdTask(t, result)
	read := e.call("tasks_read", taskArgs{TaskID: taskID}, "")
	var detail protocol.TaskDetail
	if !read.OK || json.Unmarshal(read.Result, &detail) != nil {
		t.Fatalf("read task: %+v", read)
	}
	var stored domain.Spec
	if err := json.Unmarshal(detail.Spec, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Delivery != "" || !slices.Equal(stored.Deliverables, []string{domain.DeliveryReport}) || stored.Workspace.Mode != domain.WorkspaceTemporary || len(stored.Workspace.Mounts) != 0 {
		t.Fatalf("report spec changed: %+v", stored)
	}
	contextResult := e.call("tend_context", noArgs{}, "")
	var text string
	if !contextResult.OK || json.Unmarshal(contextResult.Result, &text) != nil {
		t.Fatalf("context: %+v", contextResult)
	}
	if !strings.Contains(text, "workspace mode temporary") || !strings.Contains(text, "deliverables report") {
		t.Fatalf("missing workspace/delivery context: %s", text)
	}
}
