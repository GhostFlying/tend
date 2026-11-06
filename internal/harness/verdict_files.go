package harness

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
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// VerdictFileSelection explicitly chooses a local ordinary file to preserve.
// It is an upload source, never a durable file reference or authorization.
type VerdictFileSelection struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// VerdictToolFor advertises file selections only for a negotiated assignment.
func VerdictToolFor(spec WorkerSpec) ToolSpec {
	tool := VerdictToolSpec
	if !spec.AcceptanceEvidenceFiles {
		return tool
	}
	var schema map[string]json.RawMessage
	_ = json.Unmarshal(tool.InputSchema, &schema)
	var properties map[string]json.RawMessage
	_ = json.Unmarshal(schema["properties"], &properties)
	properties["files"] = json.RawMessage(`{"type":"array","description":"Explicit ordinary evidence files to preserve before acknowledging any verdict, including blocked. Paths in evidence alone are not preserved.","items":{"type":"object","properties":{"path":{"type":"string"},"name":{"type":"string"}},"required":["path","name"],"additionalProperties":false}}`)
	schema["properties"], _ = json.Marshal(properties)
	tool.InputSchema, _ = json.Marshal(schema)
	tool.Description += " Select files explicitly with path and delivery name to preserve their original bytes."
	return tool
}

func decodeVerdictCall(raw json.RawMessage, allowFiles bool) (protocol.VerdictPayload, []VerdictFileSelection, json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return protocol.VerdictPayload{}, nil, nil, errors.New("invalid verdict")
	}
	for field := range fields {
		if field != "files" && strings.EqualFold(field, "files") {
			return protocol.VerdictPayload{}, nil, nil, errors.New("the evidence selection field must be named files")
		}
	}
	if files, ok := fields["files"]; ok {
		if !allowFiles {
			return protocol.VerdictPayload{}, nil, nil, errors.New("acceptance evidence files were not negotiated for this run")
		}
		if !bytes.HasPrefix(bytes.TrimSpace(files), []byte("[")) {
			return protocol.VerdictPayload{}, nil, nil, errors.New("files must be an array of explicit path and name selections")
		}
	}
	var call struct {
		Verdict  protocol.Verdict       `json:"verdict"`
		Evidence json.RawMessage        `json:"evidence"`
		Issues   json.RawMessage        `json:"issues,omitempty"`
		Files    []VerdictFileSelection `json:"files,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&call); err != nil {
		return protocol.VerdictPayload{}, nil, nil, fmt.Errorf("invalid verdict: %w", err)
	}
	if !call.Verdict.Valid() {
		return protocol.VerdictPayload{}, nil, nil, errors.New("the verdict must be passed, changes_required, blocked or inconclusive")
	}
	// Canonicalize the entire input, including nested evidence and the presence
	// of an empty files array, without interpreting any path in evidence JSON.
	var canonical any
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&canonical); err != nil {
		return protocol.VerdictPayload{}, nil, nil, err
	}
	input, err := json.Marshal(canonical)
	if err != nil {
		return protocol.VerdictPayload{}, nil, nil, err
	}
	return protocol.VerdictPayload{Verdict: call.Verdict, Evidence: call.Evidence, Issues: call.Issues}, call.Files, input, nil
}

func (v *Verdicts) validateVerdictSelections(files []VerdictFileSelection) error {
	names, paths := map[string]bool{}, map[string]bool{}
	for _, file := range files {
		if strings.TrimSpace(file.Path) == "" || strings.TrimSpace(file.Name) == "" {
			return errors.New("files require a path and delivery name")
		}
		path := file.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(v.Spec.Dir, path)
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		// Resolving existing aliases only detects collisions. The runner owns
		// sealing and may restore already selected bytes after scratch is gone.
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		if names[file.Name] || paths[path] {
			return errors.New("file paths and delivery names must be unique")
		}
		names[file.Name], paths[path] = true, true
	}
	return nil
}

func validateVerdictManifests(files []protocol.ArtifactFile, selections []VerdictFileSelection) error {
	if selections != nil && len(files) != len(selections) {
		return errors.New("not every selected evidence file has a confirmed manifest")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for i, file := range files {
		digest, err := hex.DecodeString(file.SHA256)
		if file.FileID == "" || strings.TrimSpace(file.Name) == "" || file.Size < 0 || err != nil || len(digest) != 32 {
			return errors.New("evidence file manifest requires an ID, name, size and SHA-256")
		}
		if selections != nil && file.Name != selections[i].Name {
			return errors.New("confirmed evidence file does not match its selected delivery name")
		}
		if ids[file.FileID] || names[file.Name] {
			return errors.New("evidence file IDs and delivery names must be unique")
		}
		ids[file.FileID], names[file.Name] = true, true
	}
	return nil
}

type pendingVerdict struct {
	NativeBinding  string          `json:"native_binding"`
	RecordBinding  string          `json:"record_binding,omitempty"`
	Input          json.RawMessage `json:"input"`
	SealedIdentity string          `json:"sealed_identity,omitempty"`
}

func (v *Verdicts) stateDir() string {
	if v.StateDir != "" {
		return v.StateDir
	}
	return v.Spec.StateDir
}

func (v *Verdicts) readPendingVerdict() (*pendingVerdict, error) {
	raw, err := os.ReadFile(filepath.Join(v.stateDir(), "pending-verdict.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending pendingVerdict
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pending); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("original pending verdict has trailing content")
	}
	if pending.NativeBinding == "" || !json.Valid(pending.Input) {
		return nil, errors.New("original pending verdict is invalid")
	}
	return &pending, nil
}

func (v *Verdicts) recordFileVerdict(ctx context.Context, binding string, input json.RawMessage, selections []VerdictFileSelection, p *protocol.VerdictPayload) error {
	if binding == "" || v.Spec.SealVerdictFiles == nil || v.Spec.VerdictFilesIdentity == nil || v.stateDir() == "" {
		return errors.New("original verdict binding, durable state and evidence sealing are required")
	}
	pending, err := v.readPendingVerdict()
	if err != nil {
		return err
	}
	if pending != nil {
		if pending.NativeBinding != binding || pending.RecordBinding != v.binding || !bytes.Equal(pending.Input, input) {
			return fmt.Errorf("%w: the original verdict submission must be reconciled first", ErrRetainedResumePending)
		}
		if pending.SealedIdentity != "" {
			identity, err := v.Spec.VerdictFilesIdentity(ctx, binding, input)
			if err != nil {
				return fmt.Errorf("read original sealed verdict identity before upload: %w", err)
			}
			if !validVerdictIdentity(identity) || identity != pending.SealedIdentity {
				return errors.New("fully sealed original verdict identity changed before upload")
			}
		}
	} else {
		if err := v.validateVerdictSelections(selections); err != nil {
			return err
		}
		pending = &pendingVerdict{NativeBinding: binding, RecordBinding: v.binding, Input: input}
		body, err := json.Marshal(pending)
		if err != nil {
			return err
		}
		if err := writeVerdictState(filepath.Join(v.stateDir(), "pending-verdict.json"), body); err != nil {
			return err
		}
	}
	files, uploadErr := v.Spec.SealVerdictFiles(ctx, binding, input, selections)
	identity, identityErr := v.Spec.VerdictFilesIdentity(ctx, binding, input)
	if identityErr == nil && !validVerdictIdentity(identity) {
		identityErr = errors.New("fully sealed verdict identity is invalid")
	}
	if identityErr == nil {
		if pending.SealedIdentity != "" && pending.SealedIdentity != identity {
			return errors.New("fully sealed original verdict identity changed")
		}
		pending.SealedIdentity = identity
		body, err := json.Marshal(pending)
		if err != nil {
			return err
		}
		if err := writeVerdictState(filepath.Join(v.stateDir(), "pending-verdict.json"), body); err != nil {
			return err
		}
	}
	if uploadErr != nil || identityErr != nil {
		return errors.Join(uploadErr, identityErr)
	}
	// Distinguish a no-file input from an unconstrained restore validation.
	if selections == nil {
		selections = []VerdictFileSelection{}
	}
	if err := validateVerdictManifests(files, selections); err != nil {
		return err
	}
	p.Files = files
	if err := v.persistVerdict(*p, binding, identity); err != nil {
		return err
	}
	if err := v.composeOwnedSessionClose(binding, identity); err != nil {
		return err
	}
	v.sealedIdentity = identity
	if err := os.Remove(filepath.Join(v.stateDir(), "pending-verdict.json")); err != nil {
		return err
	}
	return syncVerdictDirectory(v.stateDir())
}

func (v *Verdicts) restorePendingVerdict(ctx context.Context) error {
	pending, err := v.readPendingVerdict()
	if err != nil || pending == nil {
		return err
	}
	if !v.Spec.AcceptanceEvidenceFiles || pending.RecordBinding != v.binding || (v.RequireBinding && v.binding == "") {
		return errors.New("original evidence submission cannot be adopted by this worker binding")
	}
	p, files, canonical, err := decodeVerdictCall(pending.Input, true)
	if err != nil {
		return err
	}
	return v.recordFileVerdict(ctx, pending.NativeBinding, canonical, files, &p)
}

func (v *Verdicts) persistVerdict(p protocol.VerdictPayload, nativeBinding, sealedIdentity string) error {
	if v.stateDir() == "" {
		return nil
	}
	var record any = p
	if v.binding != "" || nativeBinding != "" {
		record = boundVerdict{Binding: v.binding, NativeBinding: nativeBinding, SealedIdentity: sealedIdentity, Payload: p}
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeVerdictState(filepath.Join(v.stateDir(), "verdict.json"), body)
}

func validVerdictIdentity(identity string) bool {
	digest, err := hex.DecodeString(identity)
	return err == nil && len(digest) == sha256.Size
}

type ownedSessionCloseReceipt struct {
	RunID          string `json:"run_id"`
	NativeBinding  string `json:"native_binding"`
	SealedIdentity string `json:"sealed_identity"`
	VerdictSHA256  string `json:"verdict_sha256,omitempty"`
}

// RecordOwnedSessionClose records only the successful close of the original
// locally owned process session. Its caller must have observed that close;
// disconnecting a remote transport or releasing a reference does not qualify.
// This receipt says nothing about external native effects or escaped children.
func (v *Verdicts) RecordOwnedSessionClose() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	scope, err := v.ownedSessionCloseScope()
	if err != nil {
		return err
	}
	body, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	path := filepath.Join(v.stateDir(), "owned-session-close.json")
	prior, err := os.ReadFile(path)
	if err == nil {
		if !bytes.Equal(prior, body) {
			return errors.New("original owned session close receipt already differs")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeVerdictState(path, body)
}

// ConfirmOwnedSessionClose composes the original close receipt with the same
// sealed submission after its upload receipts have been reconciled.
func (v *Verdicts) ConfirmOwnedSessionClose() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verdict == nil {
		return errors.New("original verdict is not confirmed")
	}
	want, err := v.ownedSessionCloseScope()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(v.stateDir(), "owned-session-close.json"))
	if err != nil {
		return err
	}
	var got ownedSessionCloseReceipt
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("owned session close receipt has trailing content")
	}
	if got.RunID != want.RunID || got.NativeBinding != want.NativeBinding || got.SealedIdentity != want.SealedIdentity || (got.VerdictSHA256 != "" && got.VerdictSHA256 != want.VerdictSHA256) {
		return errors.New("original owned session close receipt differs from the sealed verdict")
	}
	if got.VerdictSHA256 == "" {
		composition, err := os.ReadFile(filepath.Join(v.stateDir(), "owned-session-close-verdict.json"))
		if err != nil {
			return err
		}
		var composed ownedSessionCloseVerdict
		if err := json.Unmarshal(composition, &composed); err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		if composed.CloseSHA256 != hex.EncodeToString(digest[:]) || composed.VerdictSHA256 != want.VerdictSHA256 {
			return errors.New("confirmed verdict differs from the original owned close composition")
		}
	}
	return nil
}

type ownedSessionCloseVerdict struct {
	CloseSHA256   string `json:"close_receipt_sha256"`
	VerdictSHA256 string `json:"verdict_sha256"`
}

// composeOwnedSessionClose runs only after the original seal/upload hook has
// confirmed every manifest. It preserves the historical close receipt and
// binds that fact to the exact final verdict before removing the pending call.
func (v *Verdicts) composeOwnedSessionClose(binding, identity string) error {
	raw, err := os.ReadFile(filepath.Join(v.stateDir(), "owned-session-close.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var receipt ownedSessionCloseReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return err
	}
	if receipt.RunID != v.Spec.RunID || receipt.NativeBinding != binding || receipt.SealedIdentity != identity {
		return errors.New("original owned close differs from the confirmed sealed submission")
	}
	verdict, err := os.ReadFile(filepath.Join(v.stateDir(), "verdict.json"))
	if err != nil {
		return err
	}
	verdictDigest := sha256.Sum256(verdict)
	finalSHA := hex.EncodeToString(verdictDigest[:])
	if receipt.VerdictSHA256 != "" {
		if receipt.VerdictSHA256 != finalSHA {
			return errors.New("original owned close verdict changed")
		}
		return nil
	}
	closeDigest := sha256.Sum256(raw)
	composed := ownedSessionCloseVerdict{CloseSHA256: hex.EncodeToString(closeDigest[:]), VerdictSHA256: finalSHA}
	path := filepath.Join(v.stateDir(), "owned-session-close-verdict.json")
	prior, err := os.ReadFile(path)
	if err == nil {
		var old ownedSessionCloseVerdict
		if err := json.Unmarshal(prior, &old); err != nil {
			return err
		}
		if old != composed {
			return errors.New("original owned close composition changed")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body, err := json.Marshal(composed)
	if err != nil {
		return err
	}
	return writeVerdictState(path, body)
}

func (v *Verdicts) ownedSessionCloseScope() (ownedSessionCloseReceipt, error) {
	scope := ownedSessionCloseReceipt{RunID: v.Spec.RunID}
	if scope.RunID == "" || v.stateDir() == "" {
		return scope, errors.New("original run scope is unavailable")
	}
	pending, err := v.readPendingVerdict()
	if err != nil {
		return scope, err
	}
	if pending != nil {
		scope.NativeBinding, scope.SealedIdentity = pending.NativeBinding, pending.SealedIdentity
	} else {
		raw, err := os.ReadFile(filepath.Join(v.stateDir(), "verdict.json"))
		if err != nil {
			return scope, err
		}
		var record boundVerdict
		if err := json.Unmarshal(raw, &record); err != nil {
			return scope, err
		}
		if record.NativeBinding == "" || record.Binding != v.binding {
			return scope, errors.New("saved verdict binding is unavailable")
		}
		scope.NativeBinding, scope.SealedIdentity = record.NativeBinding, record.SealedIdentity
		digest := sha256.Sum256(raw)
		scope.VerdictSHA256 = hex.EncodeToString(digest[:])
	}
	if scope.NativeBinding == "" || !validVerdictIdentity(scope.SealedIdentity) {
		return scope, errors.New("original verdict input is not fully sealed")
	}
	return scope, nil
}

func writeVerdictState(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".verdict-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(body)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncVerdictDirectory(filepath.Dir(path))
}

func syncVerdictDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
