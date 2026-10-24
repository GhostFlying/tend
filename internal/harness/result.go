package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GhostFlying/tend/internal/protocol"
)

const ResultTool = "submit_result"

var ResultToolSpec = ToolSpec{
	Name:        ResultTool,
	Description: "Submit your completed implementation result. Report is optional unless required by the task. Upload only explicitly selected deliverable files. Call at the end; do not rely on your final chat message as delivery.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"report":{"type":"object","properties":{"body":{"type":"string"},"evidence":{"type":"array","items":{"type":"string"}},"uncertainties":{"type":"array","items":{"type":"string"}}},"required":["body"],"additionalProperties":false},"files":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"name":{"type":"string"}},"required":["path","name"],"additionalProperties":false}}},"additionalProperties":false}`),
}

var DownloadFileToolSpec = ToolSpec{
	Name:        "download_file",
	Description: "Download an original task input or a submitted artifact file by file_id into this run's scratch directory. Verifies its size and SHA-256 and returns its local path.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"file_id":{"type":"string"}},"required":["file_id"],"additionalProperties":false}`),
}

type Results struct {
	callMu sync.Mutex
	mu     sync.Mutex
	result *protocol.ArtifactPayload
	Spec   WorkerSpec
}

func (r *Results) Handle(ctx context.Context, c ToolCall) ToolResult {
	if c.Tool == AskOwnerTool {
		return r.askOwner(ctx, c)
	}
	if c.Tool == "download_file" && r.Spec.DownloadFile != nil {
		var p struct {
			FileID string `json:"file_id"`
		}
		if json.Unmarshal(c.Arguments, &p) != nil || p.FileID == "" {
			return ToolResult{Text: "file_id is required"}
		}
		path, err := r.Spec.DownloadFile(ctx, p.FileID)
		if err != nil {
			return ToolResult{Text: fmt.Sprintf("download failed: %v", err)}
		}
		return ToolResult{Success: true, Text: path}
	}
	if c.Tool != ResultTool {
		return ToolResult{Text: "unknown tool"}
	}
	r.callMu.Lock()
	defer r.callMu.Unlock()
	var p struct {
		Report *protocol.ResultReport `json:"report"`
		Files  []struct {
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(c.Arguments, &p); err != nil {
		return ToolResult{Text: "invalid result"}
	}
	// Reject input collisions before uploads so the worker can correct its call.
	names, paths := map[string]bool{}, map[string]bool{}
	for _, file := range p.Files {
		if strings.TrimSpace(file.Path) == "" || file.Name == "" {
			return ToolResult{Text: "files require a path and delivery name"}
		}
		path := file.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.Spec.Dir, path)
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return ToolResult{Text: err.Error()}
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		if names[file.Name] || paths[path] {
			return ToolResult{Text: "file paths and delivery names must be unique"}
		}
		names[file.Name], paths[path] = true, true
	}
	// Required report/files and report content can be checked before side effects.
	candidate := protocol.ArtifactPayload{Report: p.Report, Files: make([]protocol.ArtifactFile, len(p.Files))}
	if err := r.validateResult(candidate, false); err != nil {
		return ToolResult{Text: err.Error()}
	}
	out := protocol.ArtifactPayload{Report: p.Report}
	for _, f := range p.Files {
		if r.Spec.UploadFile == nil {
			return ToolResult{Text: "file upload is unavailable"}
		}
		file, err := r.Spec.UploadFile(ctx, f.Path, f.Name)
		if err != nil {
			return ToolResult{Text: fmt.Sprintf("upload failed: %v", err)}
		}
		out.Files = append(out.Files, file)
	}
	if err := r.validateResult(out, true); err != nil {
		return ToolResult{Text: err.Error()}
	}
	if r.Spec.StateDir != "" {
		if err := os.MkdirAll(r.Spec.StateDir, 0o700); err != nil {
			return ToolResult{Text: err.Error()}
		}
		b, err := json.Marshal(out)
		if err != nil {
			return ToolResult{Text: err.Error()}
		}
		path := filepath.Join(r.Spec.StateDir, "result.json")
		if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
			return ToolResult{Text: err.Error()}
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			return ToolResult{Text: err.Error()}
		}
	}
	r.mu.Lock()
	r.result = &out
	r.mu.Unlock()
	return ToolResult{Success: true, Text: "result recorded; finish your turn"}
}

func (r *Results) Result() *protocol.ArtifactPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

// Restore resumes an explicitly submitted result after a runner restart.
func (r *Results) Restore() error {
	if r.Spec.StateDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(r.Spec.StateDir, "result.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var p protocol.ArtifactPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	if err := r.validateResult(p, true); err != nil {
		return err
	}
	r.mu.Lock()
	r.result = &p
	r.mu.Unlock()
	return nil
}

// validateResult mirrors the server's report/file shape and uniqueness rules.
// Upload ownership and immutable manifest facts remain server-authoritative.
func (r *Results) validateResult(p protocol.ArtifactPayload, manifests bool) error {
	if p.Report != nil && strings.TrimSpace(p.Report.Body) == "" {
		return errors.New("report.body must not be empty")
	}
	var spec struct {
		Deliverables []string `json:"deliverables"`
	}
	if err := json.Unmarshal(r.Spec.Spec, &spec); err != nil && len(r.Spec.Spec) > 0 {
		return fmt.Errorf("invalid task spec: %w", err)
	}
	for _, required := range spec.Deliverables {
		if required == "report" && p.Report == nil {
			return errors.New("this task requires a report")
		}
		if required == "files" && len(p.Files) == 0 {
			return errors.New("this task requires deliverable files")
		}
	}
	if !manifests {
		return nil
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, file := range p.Files {
		if file.FileID == "" || file.Name == "" {
			return errors.New("uploaded file must have an ID and delivery name")
		}
		if ids[file.FileID] || names[file.Name] {
			return errors.New("file is listed twice; file IDs and delivery names must be unique")
		}
		ids[file.FileID], names[file.Name] = true, true
	}
	return nil
}
