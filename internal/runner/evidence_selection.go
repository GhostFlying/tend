package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (e *acceptanceEvidence) validate(ctx context.Context, binding string, input json.RawMessage, selections []harness.VerdictFileSelection) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	intent, dir, err := e.selectionIntent(ctx, binding, input, selections)
	if err != nil {
		return err
	}
	_, _, err = e.prepareEvidenceIntent(ctx, filepath.Join(dir, "intent.json"), intent)
	return err
}

func (e *acceptanceEvidence) selectionIntent(ctx context.Context, binding string, input json.RawMessage, selections []harness.VerdictFileSelection) (acceptanceEvidenceIntent, string, error) {
	var intent acceptanceEvidenceIntent
	if err := ctx.Err(); err != nil {
		return intent, "", err
	}
	if e.as.Role != protocol.RoleAccept || !e.as.AcceptanceEvidenceFiles || e.as.RunID == "" || e.prepared.StateDir == "" || binding == "" {
		return intent, "", errors.New("acceptance evidence requires negotiated run and original native binding")
	}
	body, err := canonicalEvidenceInput(input)
	if err != nil {
		return intent, "", err
	}
	var declared struct {
		Files []harness.VerdictFileSelection `json:"files"`
	}
	if err := json.Unmarshal(body, &declared); err != nil || len(declared.Files) != len(selections) {
		return intent, "", errors.New("acceptance evidence differs from the original verdict input")
	}
	paths, names := map[string]bool{}, map[string]bool{}
	wanted := make([]sealedEvidenceFile, len(selections))
	for i, selected := range selections {
		if selected != declared.Files[i] || strings.TrimSpace(selected.Path) == "" || strings.TrimSpace(selected.Name) == "" || selected.Name == "." || selected.Name == ".." || pathpkg.Base(selected.Name) != selected.Name || strings.ContainsAny(selected.Name, "\\\r\n\x00") || !utf8.ValidString(selected.Name) || len(selected.Name) > 255 {
			return intent, "", errors.New("acceptance evidence requires an original path and valid unique name")
		}
		path := selected.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(e.prepared.Dir, path)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return intent, "", err
		}
		if paths[path] || names[selected.Name] {
			return intent, "", errors.New("acceptance evidence paths and names must be unique")
		}
		paths[path], names[selected.Name] = true, true
		wanted[i] = sealedEvidenceFile{Path: path, Name: selected.Name}
	}
	intent = acceptanceEvidenceIntent{Format: 1, RunID: e.as.RunID, TaskID: e.as.TaskID, ActionID: e.as.ActionID, SpecVersion: e.as.SpecVersion, Binding: binding, Input: body, Files: wanted}
	return intent, filepath.Join(e.prepared.StateDir, "acceptance-evidence", evidenceDigest([]byte(binding))), nil
}

// prepareEvidenceIntent is read-only. Only a genuinely fresh selection can
// produce a definite input rejection; existing identity and I/O errors remain
// unresolved. Request IDs, directories and uploads all follow this phase.
func (e *acceptanceEvidence) prepareEvidenceIntent(ctx context.Context, path string, intent acceptanceEvidenceIntent) (acceptanceEvidenceIntent, bool, error) {
	st, err := os.Lstat(path)
	if err == nil {
		if !st.Mode().IsRegular() {
			return intent, false, errors.New("original acceptance evidence intent is not a regular record")
		}
		old, err := os.ReadFile(path)
		if err != nil {
			return intent, false, err
		}
		var saved acceptanceEvidenceIntent
		if err := json.Unmarshal(old, &saved); err != nil {
			return intent, false, err
		}
		if saved.Format != 1 || saved.RunID != intent.RunID || saved.TaskID != intent.TaskID || saved.ActionID != intent.ActionID || saved.SpecVersion != intent.SpecVersion || saved.Binding != intent.Binding || !bytes.Equal(saved.Input, intent.Input) || len(saved.Files) != len(intent.Files) {
			return intent, false, errors.New("original acceptance evidence intent changed; reconcile the original verdict")
		}
		for i, file := range saved.Files {
			if file.Path != intent.Files[i].Path || file.Name != intent.Files[i].Name || file.Size < 0 || len(file.SHA256) != 64 || file.RequestID == "" {
				return intent, false, errors.New("original acceptance evidence selection changed")
			}
		}
		return saved, false, ctx.Err()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return intent, false, err
	}
	// A missing intent cannot authorize recapture beneath an existing result
	// or closed-session proof. Failure to inspect that history is not fresh.
	for _, name := range []string{"verdict.json", "owned-session-close.json", "owned-session-close-verdict.json"} {
		_, err := os.Lstat(filepath.Join(e.prepared.StateDir, name))
		if err == nil {
			return intent, false, errors.New("original acceptance evidence intent is unavailable for its recorded result")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return intent, false, err
		}
	}
	for i := range intent.Files {
		file := &intent.Files[i]
		file.Size, file.SHA256, err = validateFreshEvidenceSource(ctx, file.Path)
		if err != nil {
			return intent, false, err
		}
	}
	return intent, true, ctx.Err()
}

func validateFreshEvidenceSource(ctx context.Context, path string) (int64, string, error) {
	if strings.IndexByte(path, 0) >= 0 {
		return 0, "", rejectFreshEvidenceSource(os.ErrInvalid)
	}
	f, st, err := openRegularEvidence(ctx, path)
	if err != nil {
		return 0, "", rejectFreshEvidenceSource(err)
	}
	defer f.Close()
	// Once an ordinary source was opened, read or hash failures are I/O
	// uncertainty, not deterministic path-validation failures.
	return hashEvidenceBytes(ctx, f, st)
}

func rejectFreshEvidenceSource(err error) error {
	// These errors determine that the selected source cannot name an ordinary
	// file. Permission, cancellation, read failures and storage faults do not.
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) || errors.Is(err, errEvidenceNonRegular) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG) || errors.Is(err, syscall.ELOOP) {
		return fmt.Errorf("%w: %v", harness.ErrVerdictSelectionRejected, err)
	}
	return err
}
