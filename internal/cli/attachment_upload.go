package cli

import (
	"context"
	"encoding/json"
	"fmt"
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
	f, err := os.Open(pos[0])
	if err != nil {
		return err
	}
	defer f.Close()
	name := filepath.Base(pos[0])
	media := mime.TypeByExtension(filepath.Ext(name))
	if media == "" {
		media = "application/octet-stream"
	}
	query := url.Values{"request_id": {*request}, "name": {name}, "media_type": {media}}
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
	if result.Operation.Error != nil {
		return result.Operation.Error
	}
	return json.NewEncoder(e.Stdout).Encode(result.Operation)
}
