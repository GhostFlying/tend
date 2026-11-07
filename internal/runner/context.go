package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) contextReader(runCtx context.Context, as protocol.Assignment) func(context.Context, protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
	return func(ctx context.Context, query protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
		callCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(runCtx, cancel)
		defer stop()
		if runCtx.Err() != nil {
			cancel()
		}
		values := url.Values{}
		for name, value := range map[string]string{"kind": query.Kind, "id": query.ID, "after": query.After} {
			if value != "" {
				values.Set(name, value)
			}
		}
		if query.Limit != 0 {
			values.Set("limit", strconv.Itoa(query.Limit))
		}
		path := "/runner/v1/context"
		if len(values) > 0 {
			path += "?" + values.Encode()
		}
		var out protocol.RunContext
		if err := r.readRunContextJSON(callCtx, as, path, &out); err != nil {
			return out, err
		}
		if out.RunID != as.RunID || out.TaskID != as.TaskID || out.Role != as.Role {
			return protocol.RunContext{}, fmt.Errorf("issue context does not match the current run")
		}
		return out, nil
	}
}

func (r *Runner) readIssueFileMetadata(ctx context.Context, as protocol.Assignment, id string) (protocol.IssueMaterialFile, error) {
	var out protocol.IssueMaterialFile
	if err := r.readRunContextJSON(ctx, as, "/runner/v1/files/"+url.PathEscape(id)+"/metadata", &out); err != nil {
		return out, err
	}
	if out.FileID != id {
		return protocol.IssueMaterialFile{}, fmt.Errorf("issue file metadata does not match the requested original")
	}
	return out, nil
}

func (r *Runner) readRunContextJSON(ctx context.Context, as protocol.Assignment, path string, out any) error {
	if r.c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.c.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.c.base+path, nil)
	if err != nil {
		return fmt.Errorf("build Issue context request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+as.RunToken)
	req.Header.Set(headerRun, as.RunID)
	response, err := r.c.http.Do(req)
	if err != nil {
		return fmt.Errorf("read Issue context: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure rpcResponse
		if json.NewDecoder(response.Body).Decode(&failure) == nil && failure.Error != nil && failure.Error.Code != "" {
			message := failure.Error.Message
			if failure.Error.Code == protocol.CodeInternal {
				message = "server reported an internal failure; result is unconfirmed"
			}
			return protocol.Errorf(failure.Error.Code, "Issue context HTTP %d: %s", response.StatusCode, message)
		}
		code, message := protocol.CodeUnknownOutcome, "server response is unavailable or unconfirmed"
		switch response.StatusCode {
		case http.StatusBadRequest:
			code, message = protocol.CodeInvalidRequest, "server rejected the context query"
		case http.StatusUnauthorized:
			code, message = protocol.CodeUnauthenticated, "current run identity was not accepted"
		case http.StatusForbidden:
			code, message = protocol.CodeForbidden, "requested material could not be read by this run"
		case http.StatusNotFound:
			code, message = protocol.CodeNotFound, "this server does not provide the requested context interface or original"
		case http.StatusTooManyRequests:
			message = "server rate limit prevented this read; result is unconfirmed"
		}
		return protocol.Errorf(code, "Issue context HTTP %d: %s", response.StatusCode, message)
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode Issue context: %w", err)
	}
	return nil
}
