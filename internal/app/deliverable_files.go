package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const OpRunsUploadFile = "runs.upload_file"

// UploadFile registers an immutable stored file through normal request
// deduplication. Content is a compatibility helper for in-process callers;
// production uploads stream into Object before the ledger transaction.
type UploadFile struct {
	Content []byte
	Object  *blobstore.Object
	// Deprecated: accepted for source compatibility, but no upload limits apply.
	FileLimit   int64
	ResultLimit int64
}

// UploadDeliverableFile streams the original bytes before committing their
// manifest. The envelope retains the existing binary upload request identity.
func (a *App) UploadDeliverableFile(ctx context.Context, p protocol.Principal, requestID, name string, body io.Reader) (protocol.Operation, error) {
	if err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return authorizeFileUpload(ctx, tx.Q(), p, p.Subject)
	}); err != nil {
		return protocol.Operation{}, err
	}
	object, err := a.store.FileStore().Put(ctx, body)
	if err != nil {
		return protocol.Operation{}, err
	}
	manifest := protocol.ArtifactFile{Name: name, Size: object.Size, SHA256: object.SHA256}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return protocol.Operation{}, err
	}
	env := protocol.Envelope{RequestID: requestID, Operation: OpRunsUploadFile, Target: protocol.Target{Type: "run", ID: p.Subject}, Payload: payload}
	return a.Execute(ctx, p, &env, UploadFile{Object: &object})
}

func (UploadFile) Operation() string { return OpRunsUploadFile }
func (UploadFile) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	return authorizeFileUpload(ctx, q, x.Principal, x.Env.Target.ID)
}

// Upload permission is frozen by the original claim. A later capable runner
// must not grant new file-producing authority to an older acceptance.
func authorizeFileUpload(ctx context.Context, q store.Querier, p protocol.Principal, runID string) error {
	if p.Kind != protocol.PrincipalRun || p.Subject != runID || p.Role != protocol.RoleImplement && p.Role != protocol.RoleAccept && p.Role != protocol.RoleIssueManager {
		return protocol.Errorf(protocol.CodeForbidden, "only the producing run may upload files")
	}
	run, err := store.GetRun(ctx, q, runID)
	if err != nil {
		return errNotFound(err, "upload run does not exist")
	}
	if p.Role != run.Role {
		return protocol.Errorf(protocol.CodeForbidden, "upload role does not match the run")
	}
	if run.Role == protocol.RoleIssueManager {
		allowed, err := managerNativeBusinessAllowed(ctx, q, run)
		if err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(ctx, q, runID)
		if err != nil {
			return err
		}
		if !allowed || lease.Status != store.LeaseActive {
			return protocol.Errorf(protocol.CodeForbidden, "only the active Issue owner may upload its result")
		}
	}
	if run.Role == protocol.RoleAccept {
		contract, err := runAcceptanceEvidenceContract(ctx, q, runID)
		if err != nil {
			return err
		}
		if !contract.Allowed {
			return protocol.Errorf(protocol.CodeForbidden, "acceptance evidence uploads were not negotiated for this run")
		}
	}
	return nil
}

type acceptanceEvidenceContract struct {
	Found      bool
	Allowed    bool
	FeedbackID string
}

func runAcceptanceEvidenceContract(ctx context.Context, q store.Querier, runID string) (acceptanceEvidenceContract, error) {
	var c acceptanceEvidenceContract
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_acceptance_evidence_contracts WHERE run_id=?),
 COALESCE((SELECT files_allowed FROM run_acceptance_evidence_contracts WHERE run_id=?),0),
 COALESCE((SELECT feedback_acceptance_id FROM run_acceptance_evidence_contracts WHERE run_id=?),'')`, runID, runID, runID).Scan(&c.Found, &c.Allowed, &c.FeedbackID)
	return c, err
}
func (c UploadFile) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var manifest protocol.ArtifactFile
	if err := decodePayload(x.Env, &manifest); err != nil {
		return Outcome{}, err
	}
	size, digest := int64(len(c.Content)), ""
	if c.Object != nil {
		size, digest = c.Object.Size, c.Object.SHA256
	} else {
		sum := sha256.Sum256(c.Content)
		digest = hex.EncodeToString(sum[:])
	}
	if manifest.FileID != "" || strings.TrimSpace(manifest.Name) == "" || manifest.Name == "." || manifest.Name == ".." || path.Base(manifest.Name) != manifest.Name || strings.ContainsAny(manifest.Name, "\\\r\n\x00") || !utf8.ValidString(manifest.Name) || len(manifest.Name) > 255 || manifest.Size != size || manifest.SHA256 != digest {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid file manifest")
	}
	rc, err := loadRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if rc.run.ObservedState == protocol.RunFinished || rc.run.ObservedState == protocol.RunFailed {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "run has ended")
	}
	if c.Object == nil && c.Content == nil {
		c.Content = []byte{}
	}
	manifest.FileID = x.IDs.New(protocol.PrefixFile)
	if err := tx.InsertDeliverableFile(ctx, store.DeliverableFileRow{ArtifactFile: manifest, TaskID: rc.task.TaskID, RunID: rc.run.RunID, Content: c.Content, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: manifest, Events: []Event{{ObjectType: "file", ObjectID: manifest.FileID, Kind: "file.uploaded", Body: map[string]any{"task_id": rc.task.TaskID, "run_id": rc.run.RunID, "name": manifest.Name, "size": manifest.Size, "sha256": manifest.SHA256}}}}, nil
}

// DeliverableFile reads a file for the owner or the exact producing/judging run.
func (a *App) DeliverableFile(ctx context.Context, p protocol.Principal, id string) (store.DeliverableFileRow, error) {
	var f store.DeliverableFileRow
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		f, err = store.GetDeliverableFile(ctx, tx.Q(), id)
		if err != nil {
			return errNotFound(err, "file does not exist")
		}
		if p.Kind == protocol.PrincipalOwner {
			return nil
		}
		if p.Kind != protocol.PrincipalRun {
			return protocol.Errorf(protocol.CodeForbidden, "file access denied")
		}
		rc, err := loadRun(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		if p.Role != rc.run.Role {
			return protocol.Errorf(protocol.CodeForbidden, "run role does not match")
		}
		if rc.run.RunID == f.RunID {
			return nil
		}
		if allowed, err := acceptanceFeedbackFile(ctx, tx.Q(), rc, f); err != nil {
			return err
		} else if allowed {
			return nil
		}
		if rc.run.Role != protocol.RoleAccept || rc.task.TaskID != f.TaskID || rc.action.TargetArtifactID == "" {
			return protocol.Errorf(protocol.CodeForbidden, "file access denied")
		}
		ref, err := store.GetArtifactRef(ctx, tx.Q(), rc.action.TargetArtifactID)
		if err != nil {
			return err
		}
		var artifact protocol.ArtifactPayload
		if err := json.Unmarshal([]byte(ref), &artifact); err != nil {
			return err
		}
		for _, candidate := range artifact.Files {
			if candidate.FileID == id {
				return nil
			}
		}
		return protocol.Errorf(protocol.CodeForbidden, "file is not part of this acceptance")
	})
	if err != nil {
		return store.DeliverableFileRow{}, err
	}
	return f, nil
}

// Only the acceptance selected by this implementation's original assignment
// can authorize feedback downloads. Other files of the same Task stay private.
func acceptanceFeedbackFile(ctx context.Context, q store.Querier, rc runContext, file store.DeliverableFileRow) (bool, error) {
	if rc.run.Role != protocol.RoleImplement {
		return false, nil
	}
	contract, err := runAcceptanceEvidenceContract(ctx, q, rc.run.RunID)
	if err != nil || !contract.Allowed || contract.FeedbackID == "" {
		return false, err
	}
	stored, acceptance, err := store.GetFinalizedAcceptanceFile(ctx, q, file.FileID)
	if err != nil {
		// An unselected upload has no finalized acceptance provenance.
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return stored.ArtifactFile == file.ArtifactFile && acceptance.AcceptanceID == contract.FeedbackID &&
		acceptance.TaskID == rc.task.TaskID && acceptance.SpecVersion == rc.action.TargetSpecVersion &&
		acceptance.Verdict == protocol.VerdictChangesRequired, nil
}

// OpenDeliverableFile authorizes metadata before opening the immutable bytes.
func (a *App) OpenDeliverableFile(ctx context.Context, p protocol.Principal, id string) (store.DeliverableFileRow, *os.File, error) {
	file, err := a.DeliverableFile(ctx, p, id)
	if err != nil {
		return store.DeliverableFileRow{}, nil, err
	}
	body, err := a.store.FileStore().Open(file.SHA256)
	if err != nil {
		return store.DeliverableFileRow{}, nil, err
	}
	return file, body, nil
}
