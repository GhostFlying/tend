package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runAttachmentUpload(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "attachment upload")
	fs.Bool("json", true, "print complete operation result")
	dry := fs.Bool("dry-run", false, "print upload identity, metadata and digest without uploading")
	nameFlag := fs.String("name", "", "original filename; required for stdin")
	mediaFlag := fs.String("media-type", "", "original media type; defaults from the filename")
	request := fs.String("request-id", "", "original ID when retrying the same original file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("attachment upload takes one original file path")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("invalid request-id")
	}

	var f io.ReadCloser
	name := *nameFlag
	if pos[0] == "-" {
		if name == "" {
			return usagef("stdin upload requires --name")
		}
		// Let the HTTP transport close an interruptible stdin reader when
		// cancellation ends an upload whose producer has not reached EOF.
		if input, ok := e.Stdin.(io.ReadCloser); ok {
			f = input
		} else {
			f = io.NopCloser(e.Stdin)
		}
	} else {
		opened, err := os.Open(pos[0])
		if err != nil {
			return err
		}
		f = opened
		if name == "" {
			name = filepath.Base(pos[0])
		}
	}
	defer f.Close()
	stopInput := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stopInput()
	media := *mediaFlag
	if media == "" {
		media = mime.TypeByExtension(filepath.Ext(name))
	}
	if media == "" {
		media = "application/octet-stream"
	}
	query := url.Values{"request_id": {*request}, "name": {name}, "media_type": {media}}
	if *dry {
		hash := sha256.New()
		size, err := io.Copy(hash, f)
		if err != nil {
			return err
		}
		return (owner{e: e, json: true}).printJSON(map[string]any{"method": "POST", "path": "/api/v1/conversation/attachments", "request_id": *request, "name": name, "media_type": media, "size": size, "sha256": hex.EncodeToString(hash.Sum(nil))})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.server, "/")+"/api/v1/conversation/attachments?"+query.Encode(), f)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.deploymentID != "" {
		req.Header.Set("X-Tend-Deployment-ID", c.deploymentID)
	}
	fmt.Fprintf(e.Stderr, "attachment request %s; retry only with this ID and identical file bytes/name/type\n", *request)
	transfer := *c.http
	transfer.Timeout = 0
	resp, err := transfer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result protocol.APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if result.Error != nil {
		return result.Error
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("attachment upload: HTTP %d", resp.StatusCode)
	}
	if result.Operation == nil {
		return fmt.Errorf("attachment upload returned no operation")
	}
	if result.Operation.Status != protocol.OpSucceeded && result.Operation.Status != protocol.OpAccepted {
		if result.Operation.Error != nil {
			return result.Operation.Error
		}
		return protocol.Errorf(protocol.CodeUnknownOutcome, "attachment request %s is %s; query the original request before uploading again", *request, result.Operation.Status)
	}
	if result.Operation.Error != nil {
		return result.Operation.Error
	}
	return json.NewEncoder(e.Stdout).Encode(result.Operation)
}
