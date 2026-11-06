package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

type acceptanceEvidence struct {
	r        *Runner
	as       protocol.Assignment
	prepared Prepared
	mu       sync.Mutex
}

type sealedEvidenceFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	RequestID string `json:"request_id"`
}

type acceptanceEvidenceIntent struct {
	Format      int                     `json:"format"`
	RunID       string                  `json:"run_id"`
	TaskID      string                  `json:"task_id"`
	ActionID    string                  `json:"action_id"`
	SpecVersion int64                   `json:"spec_version"`
	Binding     string                  `json:"binding"`
	Input       json.RawMessage         `json:"input"`
	Files       []sealedEvidenceFile    `json:"files"`
	Uploaded    []protocol.ArtifactFile `json:"uploaded,omitempty"`
}

func evidenceDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func evidenceUploadState(p Prepared, name, digest string) string {
	return filepath.Join(p.StateDir, "uploads", evidenceDigest([]byte(name+"\x00"+digest))+".json")
}

func writeEvidenceState(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return replaceUploadState(path, func(f *os.File) error { _, err := f.Write(body); return err })
}

func hashEvidence(ctx context.Context, path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if !st.Mode().IsRegular() {
		return 0, "", errors.New("acceptance evidence must be a regular file")
	}
	sum := sha256.New()
	n, err := io.Copy(sum, transferReader{ctx: ctx, r: f})
	if err != nil {
		return 0, "", err
	}
	if n != st.Size() {
		return 0, "", errors.New("acceptance evidence changed while selecting original bytes")
	}
	return n, hex.EncodeToString(sum.Sum(nil)), nil
}

func canonicalEvidenceInput(input json.RawMessage) (json.RawMessage, error) {
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := decoder.Decode(&canonical); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("acceptance evidence input contains trailing data")
	}
	return json.Marshal(canonical)
}

// identity is a read-only proof that the original complete selection and upload
// identities are durable. Mutable upload receipts do not change that scope.
func (e *acceptanceEvidence) identity(ctx context.Context, binding string, input json.RawMessage) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if e.as.Role != protocol.RoleAccept || !e.as.AcceptanceEvidenceFiles || e.as.RunID == "" || e.prepared.StateDir == "" || binding == "" {
		return "", errors.New("original acceptance evidence authority is unavailable")
	}
	body, err := canonicalEvidenceInput(input)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(e.prepared.StateDir, "acceptance-evidence", evidenceDigest([]byte(binding)))
	raw, err := os.ReadFile(filepath.Join(dir, "intent.json"))
	if err != nil {
		return "", err
	}
	var intent acceptanceEvidenceIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return "", err
	}
	if intent.Format != 1 || intent.RunID != e.as.RunID || intent.TaskID != e.as.TaskID || intent.ActionID != e.as.ActionID || intent.SpecVersion != e.as.SpecVersion || intent.Binding != binding || !bytes.Equal(intent.Input, body) {
		return "", errors.New("original sealed acceptance evidence scope changed")
	}
	var declared struct {
		Files []harness.VerdictFileSelection `json:"files"`
	}
	if err := json.Unmarshal(body, &declared); err != nil || len(declared.Files) != len(intent.Files) {
		return "", errors.New("original sealed acceptance evidence selection changed")
	}
	for i, file := range intent.Files {
		selected := declared.Files[i]
		path := selected.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(e.prepared.Dir, path)
		}
		path, err = filepath.Abs(path)
		if err != nil || file.Path != path || file.Name != selected.Name || file.RequestID == "" {
			return "", errors.New("original sealed acceptance upload identity changed")
		}
		size, digest, err := hashEvidence(ctx, filepath.Join(dir, fmt.Sprintf("%06d.raw", i)))
		if err != nil || size != file.Size || digest != file.SHA256 {
			return "", errors.New("acceptance evidence is not completely sealed")
		}
	}
	intent.Uploaded = nil
	sealed, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	return evidenceDigest(sealed), ctx.Err()
}

// seal retains explicit selections independently of scratch. Once the intent
// exists, neither changed paths nor a lost upload reply can replace its bytes.
// Every selected source is sealed before any upload is sent.
func (e *acceptanceEvidence) seal(ctx context.Context, binding string, input json.RawMessage, selections []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.as.Role != protocol.RoleAccept || !e.as.AcceptanceEvidenceFiles || e.as.RunID == "" || e.prepared.StateDir == "" || binding == "" {
		return nil, errors.New("acceptance evidence requires negotiated run and original native binding")
	}
	body, err := canonicalEvidenceInput(input)
	if err != nil {
		return nil, err
	}
	var declared struct {
		Files []harness.VerdictFileSelection `json:"files"`
	}
	if err := json.Unmarshal(body, &declared); err != nil || len(declared.Files) != len(selections) {
		return nil, errors.New("acceptance evidence differs from the original verdict input")
	}
	paths, names := map[string]bool{}, map[string]bool{}
	wanted := make([]sealedEvidenceFile, len(selections))
	for i, selected := range selections {
		if selected != declared.Files[i] || strings.TrimSpace(selected.Path) == "" || strings.TrimSpace(selected.Name) == "" || selected.Name == "." || selected.Name == ".." || pathpkg.Base(selected.Name) != selected.Name || strings.ContainsAny(selected.Name, "\\\r\n\x00") || !utf8.ValidString(selected.Name) || len(selected.Name) > 255 {
			return nil, errors.New("acceptance evidence requires an original path and valid unique name")
		}
		path := selected.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(e.prepared.Dir, path)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if paths[path] || names[selected.Name] {
			return nil, errors.New("acceptance evidence paths and names must be unique")
		}
		paths[path], names[selected.Name] = true, true
		wanted[i] = sealedEvidenceFile{Path: path, Name: selected.Name}
	}
	base := filepath.Join(e.prepared.StateDir, "acceptance-evidence")
	dir := filepath.Join(base, evidenceDigest([]byte(binding)))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := errors.Join(syncUploadDirectory(e.prepared.StateDir), syncUploadDirectory(base)); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "intent.json")
	intent := acceptanceEvidenceIntent{Format: 1, RunID: e.as.RunID, TaskID: e.as.TaskID, ActionID: e.as.ActionID, SpecVersion: e.as.SpecVersion, Binding: binding, Input: body, Files: wanted}
	old, err := os.ReadFile(path)
	if err == nil {
		var saved acceptanceEvidenceIntent
		if err := json.Unmarshal(old, &saved); err != nil {
			return nil, err
		}
		if saved.Format != 1 || saved.RunID != intent.RunID || saved.TaskID != intent.TaskID || saved.ActionID != intent.ActionID || saved.SpecVersion != intent.SpecVersion || saved.Binding != binding || !bytes.Equal(saved.Input, body) || len(saved.Files) != len(wanted) {
			return nil, errors.New("original acceptance evidence intent changed; reconcile the original verdict")
		}
		for i, file := range saved.Files {
			if file.Path != wanted[i].Path || file.Name != wanted[i].Name || file.Size < 0 || len(file.SHA256) != 64 || file.RequestID == "" {
				return nil, errors.New("original acceptance evidence selection changed")
			}
		}
		intent = saved
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		for i := range intent.Files {
			file := &intent.Files[i]
			file.Size, file.SHA256, err = hashEvidence(ctx, file.Path)
			if err != nil {
				// No intent has ever been written for this binding, so this
				// definite missing-source rejection precedes every upload.
				// Persistence and other I/O failures remain unknown.
				if errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("%w: %v", harness.ErrVerdictSelectionRejected, err)
				}
				return nil, err
			}
		}
		for i := range intent.Files {
			file := &intent.Files[i]
			var pending struct {
				RequestID string `json:"request_id"`
			}
			if raw, err := os.ReadFile(evidenceUploadState(e.prepared, file.Name, file.SHA256)); err == nil {
				if err := json.Unmarshal(raw, &pending); err != nil {
					return nil, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			file.RequestID = pending.RequestID
			if file.RequestID == "" {
				file.RequestID = e.r.cfg.IDs.New(protocol.PrefixRequest)
			}
		}
		if err := writeEvidenceState(path, intent); err != nil {
			return nil, err
		}
	}
	sealedPaths := make([]string, len(intent.Files))
	for i, file := range intent.Files {
		sealed := filepath.Join(dir, fmt.Sprintf("%06d.raw", i))
		sealedPaths[i] = sealed
		if _, err := os.Stat(sealed); errors.Is(err, os.ErrNotExist) {
			if err := replaceUploadState(sealed, func(dst *os.File) error {
				src, err := os.Open(file.Path)
				if err != nil {
					return err
				}
				defer src.Close()
				st, err := src.Stat()
				if err != nil || !st.Mode().IsRegular() {
					return errors.New("original acceptance evidence is no longer a regular file")
				}
				sum := sha256.New()
				n, err := io.Copy(io.MultiWriter(dst, sum), transferReader{ctx: ctx, r: src})
				if err != nil {
					return err
				}
				if n != file.Size || hex.EncodeToString(sum.Sum(nil)) != file.SHA256 {
					return errors.New("original acceptance evidence bytes changed before sealing")
				}
				return dst.Chmod(0o400)
			}); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		size, digest, err := hashEvidence(ctx, sealed)
		if err != nil || size != file.Size || digest != file.SHA256 {
			return nil, errors.New("sealed acceptance evidence is missing or changed")
		}
	}
	if len(intent.Uploaded) == len(intent.Files) && len(intent.Files) > 0 {
		ids := map[string]bool{}
		for i, manifest := range intent.Uploaded {
			file := intent.Files[i]
			if manifest.FileID == "" || ids[manifest.FileID] || manifest.Name != file.Name || manifest.Size != file.Size || manifest.SHA256 != file.SHA256 {
				return nil, errors.New("original acceptance upload receipt changed")
			}
			ids[manifest.FileID] = true
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return append([]protocol.ArtifactFile(nil), intent.Uploaded...), nil
	}
	files := make([]protocol.ArtifactFile, 0, len(intent.Files))
	for i, file := range intent.Files {
		state := evidenceUploadState(e.prepared, file.Name, file.SHA256)
		if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
			return nil, err
		}
		if raw, err := os.ReadFile(state); err == nil {
			var pending struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(raw, &pending); err != nil || pending.RequestID != file.RequestID {
				return nil, errors.New("original acceptance upload request identity changed")
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if err := writeEvidenceState(state, map[string]string{"request_id": file.RequestID}); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
		manifest, err := e.r.uploadFile(ctx, e.as, e.prepared, sealedPaths[i], file.Name)
		if err != nil {
			return nil, err
		}
		if manifest.FileID == "" || manifest.Name != file.Name || manifest.Size != file.Size || manifest.SHA256 != file.SHA256 {
			return nil, errors.New("acceptance upload manifest does not match the sealed original")
		}
		files = append(files, manifest)
	}
	intent.Uploaded = files
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeEvidenceState(path, intent); err != nil {
		return nil, err
	}
	return files, nil
}
