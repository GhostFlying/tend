package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// DefaultFileLimit and DefaultResultFileLimit bound durable file uploads.
const (
	DefaultFileLimit       int64 = 20 << 20
	DefaultResultFileLimit int64 = 100 << 20
	OpRunsUploadFile             = "runs.upload_file"
)

// UploadFile persists binary data through normal request deduplication. Only
// the immutable manifest is hashed in the envelope, never a JSON base64 copy.
type UploadFile struct {
	Content     []byte
	FileLimit   int64
	ResultLimit int64
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
	digest := sha256.Sum256(c.Content)
	if manifest.FileID != "" || strings.TrimSpace(manifest.Name) == "" || manifest.Name == "." || manifest.Name == ".." || path.Base(manifest.Name) != manifest.Name || strings.ContainsAny(manifest.Name, "\\\r\n\x00") || !utf8.ValidString(manifest.Name) || len(manifest.Name) > 255 || manifest.Size != int64(len(c.Content)) || manifest.SHA256 != hex.EncodeToString(digest[:]) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid file manifest")
	}
	fileLimit, resultLimit := c.FileLimit, c.ResultLimit
	if fileLimit <= 0 {
		fileLimit = DefaultFileLimit
	}
	if resultLimit <= 0 {
		resultLimit = DefaultResultFileLimit
	}
	if manifest.Size > fileLimit {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "file exceeds configured limit")
	}
	rc, err := loadRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if rc.run.ObservedState == protocol.RunFinished || rc.run.ObservedState == protocol.RunFailed {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "run has ended")
	}
	used, err := store.RunFileBytes(ctx, tx.Q(), rc.run.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if used+manifest.Size > resultLimit {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "result files exceed configured limit")
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
