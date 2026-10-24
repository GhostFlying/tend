package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	if p.Kind != protocol.PrincipalRun || p.Role != protocol.RoleImplement {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeForbidden, "only an implementation run may upload files")
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
func (UploadFile) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return authorizeRunSelf(x, protocol.RoleImplement)
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
		if rc.run.RunID == f.RunID {
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
