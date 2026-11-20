package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/gorilla/websocket"
)

// NativeClient connects to the existing daemon's WebSocket control socket.
// app-server proxy carries the same WebSocket bytes; it is not JSON-line stdio.
// No daemon process or global configuration is owned by this client.
type NativeClient struct {
	mu        sync.Mutex
	unstarted map[string]*nativeRPC
	sandboxes map[string]map[string]any
}

func (n *NativeClient) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, r := range n.unstarted {
		_ = r.conn.Close()
		delete(n.unstarted, id)
	}
	return nil
}

var errNativeMethodUnavailable = errors.New("native RPC method is unavailable")

type nativeRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e nativeRPCError) unknownTurnList() bool {
	if e.Code == -32601 {
		return true
	}
	// Some Codex versions use serde's unknown enum variant as Invalid Request.
	const prefix = "Invalid request: unknown variant `thread/turns/list`"
	suffix, ok := strings.CutPrefix(e.Message, prefix)
	return e.Code == -32600 && ok && (suffix == "" || strings.HasPrefix(suffix, ", expected "))
}

type nativeRPC struct {
	sandbox map[string]any
	conn    *websocket.Conn
	next    int
}

func connectNative(ctx context.Context, b protocol.MaintenanceBackend) (*nativeRPC, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", b.Socket)
	}}
	conn, resp, err := dialer.DialContext(ctx, "ws://localhost/", http.Header{})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("maintenance: native connect: %w", err)
	}
	conn.SetReadLimit(32 << 20)
	rpc := &nativeRPC{conn: conn}
	if err := rpc.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "tend_maintainer", "title": "Tend maintenance", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}, nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.WriteJSON(map[string]any{"method": "initialized"}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return rpc, nil
}

func (r *nativeRPC) call(ctx context.Context, method string, params any, result any) error {
	stop := context.AfterFunc(ctx, func() { _ = r.conn.Close() })
	defer stop()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := r.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := r.conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	r.next++
	if err := r.conn.WriteJSON(map[string]any{"id": r.next, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var response struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := r.conn.ReadJSON(&response); err != nil {
			return err
		}
		if response.Method != "" || response.ID == nil || *response.ID != r.next {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			var rejection nativeRPCError
			if json.Unmarshal(response.Error, &rejection) == nil {
				if method == "thread/turns/list" && rejection.unknownTurnList() {
					return fmt.Errorf("maintenance: native %s: %w: %w", method, ErrNotSent, errNativeMethodUnavailable)
				}
				if nativeThreadUnavailable(method, params, rejection) {
					return fmt.Errorf("maintenance: native %s: %w: %w", method, ErrNotSent, ErrThreadUnavailable)
				}
			}
			return fmt.Errorf("maintenance: native %s rejected the request: %w", method, ErrNotSent)
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	}
}

// Only exact provider replies for the addressed thread can retire a completed
// audit's idle session. In particular, a transport failure has no such meaning.
func nativeThreadUnavailable(method string, params any, rejection nativeRPCError) bool {
	if method != "thread/read" && method != "thread/resume" && method != "turn/start" {
		return false
	}
	values, ok := params.(map[string]any)
	if !ok {
		return false
	}
	id, _ := values["threadId"].(string)
	if id == "" || rejection.Code != -32600 {
		return false
	}
	return rejection.Message == "thread not found: "+id ||
		method == "thread/read" && rejection.Message == "thread not loaded: "+id ||
		(method == "thread/read" || method == "thread/resume") && rejection.Message == "no rollout found for thread id "+id
}

func backendParams(b protocol.MaintenanceBackend) map[string]any {
	params := map[string]any{"approvalPolicy": b.ApprovalPolicy, "sandbox": b.Sandbox}
	config := map[string]any{}
	if b.Model != "" {
		params["model"] = b.Model
		config["review_model"] = b.Model
	}
	if b.Effort != "" {
		config["model_reasoning_effort"] = b.Effort
	}
	if b.Sandbox == "workspace-write" {
		config["sandbox_workspace_write.network_access"] = b.NetworkAccess
		if len(b.WritableRoots) > 0 {
			config["sandbox_workspace_write.writable_roots"] = b.WritableRoots
		}
	}
	if len(config) > 0 {
		params["config"] = config
	}
	return params
}

func (n *NativeClient) resume(ctx context.Context, b protocol.MaintenanceBackend, id string, r *nativeRPC) error {
	params := backendParams(b)
	params["threadId"] = id
	params["excludeTurns"] = true
	var result struct {
		Sandbox map[string]any `json:"sandbox"`
	}
	if err := r.call(ctx, "thread/resume", params, &result); err != nil {
		return err
	}
	r.sandbox = result.Sandbox
	n.mu.Lock()
	if n.sandboxes == nil {
		n.sandboxes = map[string]map[string]any{}
	}
	n.sandboxes[id] = result.Sandbox
	n.mu.Unlock()
	return nil
}

func (n *NativeClient) NewThread(ctx context.Context, b protocol.MaintenanceBackend, cwd, instructions string) (NativeThread, error) {
	r, err := connectNative(ctx, b)
	if err != nil {
		return NativeThread{}, fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	var result struct {
		Sandbox map[string]any `json:"sandbox"`
		Thread  struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"thread"`
	}
	params := backendParams(b)
	params["cwd"] = cwd
	params["developerInstructions"] = instructions
	params["experimentalRawEvents"] = true
	params["persistExtendedHistory"] = true
	err = r.call(ctx, "thread/start", params, &result)
	if err == nil && result.Thread.ID == "" {
		err = errors.New("maintenance: native creation response omitted thread identity")
	}
	r.sandbox = result.Sandbox
	if err != nil {
		_ = r.conn.Close()
	} else {
		n.mu.Lock()
		if n.unstarted == nil {
			n.unstarted = map[string]*nativeRPC{}
		}
		n.unstarted[result.Thread.ID] = r
		n.mu.Unlock()
	}
	return NativeThread{ID: result.Thread.ID, Path: result.Thread.Path, Sandbox: result.Sandbox}, err
}

type nativeHistoryTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Items  []struct {
		Type, ID string
		ClientID string `json:"clientId"`
		Review   string `json:"review"`
	} `json:"items"`
}

func (t nativeHistoryTurn) identity() NativeTurn {
	turn := NativeTurn{ID: t.ID, Status: t.Status}
	for _, item := range t.Items {
		switch item.Type {
		case "enteredReviewMode":
			_, tail, ok := strings.Cut(item.Review, "request_id=")
			if fields := strings.Fields(tail); ok && len(fields) > 0 {
				turn.InputID = strings.TrimSuffix(fields[0], ".")
			}
			return turn
		case "userMessage":
			turn.InputID = item.ClientID
			if turn.InputID == "" {
				turn.InputID = item.ID
			}
			return turn
		}
	}
	return turn
}

func (n *NativeClient) Read(ctx context.Context, b protocol.MaintenanceBackend, id string, mode NativeReadMode) (NativeState, error) {
	return n.readThread(ctx, b, id, mode, nil, false)
}

// ReadTurn reconciles the current durable input without loading other turns'
// item payloads. Older providers can still complete a single-use session.
func (n *NativeClient) ReadTurn(ctx context.Context, b protocol.MaintenanceBackend, id string, mode NativeReadMode, target NativeTurn, review bool) (NativeState, error) {
	return n.readThread(ctx, b, id, mode, &target, review)
}

func (n *NativeClient) readThread(ctx context.Context, b protocol.MaintenanceBackend, id string, mode NativeReadMode, target *NativeTurn, review bool) (state NativeState, err error) {
	n.mu.Lock()
	r := n.unstarted[id]
	n.mu.Unlock()
	unstarted := r != nil
	defer func() {
		if err != nil && unstarted {
			n.ForgetUnstarted(id)
		}
	}()
	if r == nil {
		r, err = connectNative(ctx, b)
		if err != nil {
			return NativeState{}, err
		}
		defer r.conn.Close()
	}
	var result struct {
		Thread struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
			Turns []nativeHistoryTurn `json:"turns"`
		} `json:"thread"`
	}
	if err := r.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &result); err != nil {
		return NativeState{}, err
	}
	if result.Thread.Status.Type == "notLoaded" {
		if err := n.resume(ctx, b, id, r); err != nil {
			return NativeState{}, err
		}
		if err := r.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &result); err != nil {
			return NativeState{}, err
		}
	}
	// A quiescent systemError thread still needs identity-bound reconciliation.
	s := NativeState{Busy: result.Thread.Status.Type != "idle" && result.Thread.Status.Type != "systemError", Unusable: result.Thread.Status.Type == "systemError"}
	if unstarted || mode == ReadMetadata || mode == ReadWhenIdle && s.Busy {
		return s, nil
	}
	if target != nil {
		turns, readErr := r.readBoundedTurn(ctx, id, *target, review)
		if readErr == nil {
			s.Turns, s.HistoryRead = turns, nativeHistoryBounded
			return s, nil
		}
		if !errors.Is(readErr, errNativeMethodUnavailable) {
			return NativeState{}, readErr
		}
		s.HistoryRead = nativeHistoryLegacy
	}
	if err := r.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true}, &result); err != nil {
		return NativeState{}, err
	}
	s.Busy = result.Thread.Status.Type != "idle" && result.Thread.Status.Type != "systemError"
	s.Unusable = result.Thread.Status.Type == "systemError"
	for _, t := range result.Thread.Turns {
		s.Turns = append(s.Turns, t.identity())
	}
	return s, nil
}

func (r *nativeRPC) readBoundedTurn(ctx context.Context, threadID string, target NativeTurn, review bool) ([]NativeTurn, error) {
	items, limit := "notLoaded", 16
	if target.ID == "" {
		items, limit = "summary", 1
		if review {
			// The summary intentionally excludes enteredReviewMode.
			items = "full"
		}
	}
	params := map[string]any{"threadId": threadID, "sortDirection": "desc", "limit": limit, "itemsView": items}
	for pageNumber := range 32 {
		var page struct {
			Data       *[]nativeHistoryTurn `json:"data"`
			NextCursor string               `json:"nextCursor"`
		}
		if err := r.call(ctx, "thread/turns/list", params, &page); err != nil {
			if pageNumber > 0 && errors.Is(err, errNativeMethodUnavailable) {
				return nil, errors.New("maintenance: native turn pagination became unavailable before finding the original identity")
			}
			return nil, err
		}
		if page.Data == nil {
			return nil, errors.New("maintenance: native turn page omitted data")
		}
		var matches []NativeTurn
		for _, record := range *page.Data {
			turn := record.identity()
			if target.ID != "" && turn.ID == target.ID || target.ID == "" && turn.InputID == target.InputID {
				matches = append(matches, turn)
			}
		}
		if len(matches) > 0 || page.NextCursor == "" {
			return matches, nil
		}
		if page.NextCursor == params["cursor"] {
			return nil, errors.New("maintenance: native turn page did not advance")
		}
		params["cursor"] = page.NextCursor
	}
	return nil, errors.New("maintenance: original input not found within bounded native history")
}

func (n *NativeClient) threadRPC(ctx context.Context, b protocol.MaintenanceBackend, thread NativeThread) (*nativeRPC, error) {
	id := thread.ID
	n.mu.Lock()
	r := n.unstarted[id]
	sandbox := n.sandboxes[id]
	n.mu.Unlock()
	if r == nil {
		var err error
		r, err = connectNative(ctx, b)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotSent, err)
		}
		var result struct {
			Thread struct {
				Status struct {
					Type string `json:"type"`
				} `json:"status"`
			} `json:"thread"`
		}
		if err := r.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &result); err != nil {
			_ = r.conn.Close()
			return nil, fmt.Errorf("%w: %w", ErrNotSent, err)
		}
		if result.Thread.Status.Type == "notLoaded" {
			if err := n.resume(ctx, b, id, r); err != nil {
				_ = r.conn.Close()
				return nil, fmt.Errorf("%w: %w", ErrNotSent, err)
			}
			if thread.RequireIdle {
				if err := r.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &result); err != nil {
					_ = r.conn.Close()
					return nil, fmt.Errorf("%w: %w", ErrNotSent, err)
				}
			}
		} else {
			r.sandbox = sandbox
			if r.sandbox == nil {
				r.sandbox = thread.Sandbox
			}
		}
		if thread.RequireIdle && result.Thread.Status.Type != "idle" {
			_ = r.conn.Close()
			if result.Thread.Status.Type == "systemError" {
				return nil, fmt.Errorf("%w: %w", ErrNotSent, ErrThreadUnavailable)
			}
			return nil, fmt.Errorf("%w: maintenance native thread is not idle", ErrNotSent)
		}
	}
	return r, nil
}

func (n *NativeClient) releaseRPC(id string, r *nativeRPC, result error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if errors.Is(result, ErrNotSent) && n.unstarted[id] == r {
		return
	}
	delete(n.unstarted, id)
	delete(n.sandboxes, id)
	_ = r.conn.Close()
}

func (n *NativeClient) ForgetUnstarted(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r := n.unstarted[id]; r != nil {
		delete(n.unstarted, id)
		_ = r.conn.Close()
	}
	delete(n.sandboxes, id)
}

func (n *NativeClient) Start(ctx context.Context, b protocol.MaintenanceBackend, thread NativeThread, inputID, prompt string) (turnID string, err error) {
	id := thread.ID
	r, err := n.threadRPC(ctx, b, thread)
	if err != nil {
		return "", err
	}
	defer func() { n.releaseRPC(id, r, err) }()
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	params := map[string]any{"threadId": id, "input": []any{map[string]any{"type": "text", "text": prompt, "text_elements": []any{}}}, "userMessageId": inputID}
	if kind := r.sandbox["type"]; kind == "workspaceWrite" || kind == "readOnly" {
		policy := maps.Clone(r.sandbox)
		policy["networkAccess"] = b.NetworkAccess
		if kind == "workspaceWrite" && len(b.WritableRoots) > 0 {
			roots, _ := policy["writableRoots"].([]any)
			roots = append([]any(nil), roots...)
			for _, root := range b.WritableRoots {
				if !slices.ContainsFunc(roots, func(v any) bool { return v == root }) {
					roots = append(roots, root)
				}
			}
			policy["writableRoots"] = roots
		}
		params["sandboxPolicy"] = policy
	}
	if b.Agent == "codex" {
		delete(params, "userMessageId")
		params["clientUserMessageId"] = inputID
	}
	if b.Model != "" {
		params["model"] = b.Model
	}
	if b.Effort != "" {
		params["effort"] = b.Effort
	}
	err = r.call(ctx, "turn/start", params, &result)
	return result.Turn.ID, err
}

func (*NativeClient) Interrupt(ctx context.Context, b protocol.MaintenanceBackend, id, turn string) error {
	r, err := connectNative(ctx, b)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	defer r.conn.Close()
	return r.call(ctx, "turn/interrupt", map[string]any{"threadId": id, "turnId": turn}, nil)
}
