package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/GhostFlying/tend/internal/protocol"
)

func requiresDelivery(as protocol.Assignment, kind string) bool {
	var s struct {
		Delivery     string   `json:"delivery"`
		Deliverables []string `json:"deliverables"`
	}
	if json.Unmarshal(as.Spec, &s) != nil {
		return kind == "commit"
	}
	if len(s.Deliverables) == 0 {
		return kind == "commit"
	}
	for _, d := range s.Deliverables {
		if d == kind {
			return true
		}
	}
	return false
}

func (r *Runner) uploadFile(ctx context.Context, as protocol.Assignment, p Prepared, path, name string) (protocol.ArtifactFile, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.Dir, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return protocol.ArtifactFile{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return protocol.ArtifactFile{}, err
	}
	if !st.Mode().IsRegular() {
		return protocol.ArtifactFile{}, errors.New("delivery file must be regular")
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, transferReader{ctx: ctx, r: f}); err != nil {
		return protocol.ArtifactFile{}, err
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	key := sha256.Sum256([]byte(name + "\x00" + digest))
	dir := filepath.Join(p.StateDir, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return protocol.ArtifactFile{}, err
	}
	state := filepath.Join(dir, hex.EncodeToString(key[:])+".json")
	var pending struct {
		RequestID string                 `json:"request_id"`
		File      *protocol.ArtifactFile `json:"file,omitempty"`
	}
	b, err := os.ReadFile(state)
	if err == nil {
		if err := json.Unmarshal(b, &pending); err != nil {
			return protocol.ArtifactFile{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocol.ArtifactFile{}, err
	}
	if pending.File != nil {
		return *pending.File, nil
	}
	if pending.RequestID == "" {
		pending.RequestID = r.cfg.IDs.New(protocol.PrefixRequest)
		b, _ := json.Marshal(pending)
		if err := replaceUploadState(state, func(f *os.File) error { _, err := f.Write(b); return err }); err != nil {
			return protocol.ArtifactFile{}, err
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return protocol.ArtifactFile{}, err
	}
	endpoint := r.c.base + "/runner/v1/files?" + url.Values{"request_id": {pending.RequestID}, "name": {name}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, f)
	if err != nil {
		return protocol.ArtifactFile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+as.RunToken)
	req.Header.Set(headerRun, as.RunID)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = st.Size()
	resp, err := r.c.http.Do(req)
	if err != nil {
		return protocol.ArtifactFile{}, err
	}
	defer resp.Body.Close()
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return protocol.ArtifactFile{}, err
	}
	if out.Error != nil {
		return protocol.ArtifactFile{}, out.Error
	}
	if out.Operation == nil {
		return protocol.ArtifactFile{}, errors.New("missing upload result")
	}
	if out.Operation.Error != nil {
		return protocol.ArtifactFile{}, out.Operation.Error
	}
	var file protocol.ArtifactFile
	if err := json.Unmarshal(out.Operation.Result, &file); err != nil {
		return file, err
	}
	if file.SHA256 != digest || file.Size != st.Size() {
		return file, errors.New("uploaded file changed during submission")
	}
	pending.File = &file
	b, _ = json.Marshal(pending)
	if err := replaceUploadState(state, func(f *os.File) error { _, err := f.Write(b); return err }); err != nil {
		return file, err
	}
	return file, nil
}

// replaceUploadState keeps the prior request identity readable until the complete
// next state has been flushed. A failed temporary write never truncates it.
func replaceUploadState(path string, write func(*os.File) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := write(f); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncUploadDirectory(filepath.Dir(path))
}

func syncUploadDirectory(path string) error {
	// Windows opens directory handles read-only; FlushFileBuffers requires
	// write access. Keep the file flush and rename without that unsupported step.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (r *Runner) downloadFile(ctx context.Context, as protocol.Assignment, p Prepared, id string) (string, error) {
	file, category, err := assignmentFile(as, id)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.c.base+"/runner/v1/files/"+url.PathEscape(id), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+as.RunToken)
	req.Header.Set(headerRun, as.RunID)
	resp, err := r.c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}
	dir := filepath.Join(p.ScratchDir, category)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id)
	f, err := os.CreateTemp(dir, ".download-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	digest := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, digest), io.LimitReader(transferReader{ctx: ctx, r: resp.Body}, file.Size+1))
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	if n != file.Size || hex.EncodeToString(digest.Sum(nil)) != file.SHA256 {
		return "", errors.New("downloaded file failed integrity verification")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	if err := syncUploadDirectory(dir); err != nil {
		return "", err
	}
	return path, nil
}

// assignmentFile resolves only the immutable inputs or submitted output of this
// run. No owner credentials or host-local attachment paths are involved.
func assignmentFile(as protocol.Assignment, id string) (protocol.ArtifactFile, string, error) {
	var spec struct {
		Inputs []protocol.TaskInput `json:"inputs"`
	}
	if len(as.Spec) > 0 {
		if err := json.Unmarshal(as.Spec, &spec); err != nil {
			return protocol.ArtifactFile{}, "", fmt.Errorf("decode task inputs: %w", err)
		}
	}
	for _, input := range spec.Inputs {
		if input.FileID == id {
			return protocol.ArtifactFile{FileID: input.FileID, Name: input.Name, Size: input.Size, SHA256: input.SHA256}, "inputs", nil
		}
	}
	var artifact protocol.ArtifactPayload
	if len(as.Artifact) > 0 {
		if err := json.Unmarshal(as.Artifact, &artifact); err != nil {
			return protocol.ArtifactFile{}, "", fmt.Errorf("decode submitted artifact: %w", err)
		}
	}
	for _, file := range artifact.Files {
		if file.FileID == id {
			return file, "deliverables", nil
		}
	}
	return protocol.ArtifactFile{}, "", errors.New("file is not a task input or submitted artifact")
}

// File transfers follow the caller's run lifetime rather than the short RPC
// deadline. Check cancellation during local hashing as well as network IO.
type transferReader struct {
	ctx context.Context
	r   io.Reader
}

func (r transferReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
