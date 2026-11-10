package coordination

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeOwnerUploadReadsActualLocalFileUnderSameExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")
	original := []byte("Original issue background\nActual accepted report\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	uploads := 0
	d := Driver{NativeOwnerTools: true, Upload: func(_ context.Context, local, name string) (protocol.ArtifactFile, error) {
		uploads++
		if local != path || name != "report.md" {
			t.Fatal(local, name)
		}
		body, err := os.ReadFile(local)
		if err != nil {
			return protocol.ArtifactFile{}, err
		}
		if string(body) != string(original) {
			t.Fatal("upload replaced local original")
		}
		sum := sha256.Sum256(body)
		return protocol.ArtifactFile{FileID: "actual-file", Name: name, SHA256: fmt.Sprintf("%x", sum), Size: int64(len(body))}, nil
	}}
	args, _ := json.Marshal(map[string]string{"path": path, "name": "report.md"})
	result := d.tool(t.Context(), harness.ToolCall{Tool: harness.UploadFileTool, Arguments: args})
	var decoded struct {
		OK     bool                  `json:"ok"`
		Result protocol.ArtifactFile `json:"result"`
	}
	if !result.Success || json.Unmarshal([]byte(result.Text), &decoded) != nil || !decoded.OK || decoded.Result.FileID != "actual-file" || decoded.Result.Size != int64(len(original)) || uploads != 1 {
		t.Fatal(result, decoded)
	}
	d.RequireReceiptReader = true
	if got := d.tool(t.Context(), harness.ToolCall{Tool: harness.UploadFileTool, Arguments: args}); got.Success || uploads != 1 {
		t.Fatal("receipt reconciliation mutated file storage", got, uploads)
	}
	d.RequireReceiptReader, d.NativeOwnerTools = false, false
	if got := d.tool(t.Context(), harness.ToolCall{Tool: harness.UploadFileTool, Arguments: args}); got.Success || uploads != 1 {
		t.Fatal("legacy receipt transport acquired native file upload", got, uploads)
	}
}
