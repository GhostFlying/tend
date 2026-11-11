package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/protocol/schema"
)

type mainOwnerRequestKey struct{}

const ownerToolResponseLimit = 1 << 20

type ownerToolResponse struct {
	headers   http.Header
	code      int
	body      bytes.Buffer
	truncated bool
}

func (w *ownerToolResponse) Header() http.Header { return w.headers }
func (w *ownerToolResponse) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}
func (w *ownerToolResponse) Flush() {
	if w.code == 0 {
		w.code = http.StatusOK
	}
}
func (w *ownerToolResponse) Write(data []byte) (int, error) {
	w.Flush()
	// JSON includes immutable operation receipts and may exceed the binary
	// preview limit. Keep complete business results so callers can read actual state.
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		return w.body.Write(data)
	}
	remaining := ownerToolResponseLimit - w.body.Len()
	if len(data) > remaining {
		w.truncated = true
		_, _ = w.body.Write(data[:remaining])
		return remaining, io.ErrShortWrite
	}
	return w.body.Write(data)
}

// OwnerTools uses the registered owner handlers, including their normal
// validation and external-boundary preparation. It never gives the model tokens.
type OwnerTools struct{ server *Server }

func (s *Server) MainOwnerTools() *OwnerTools { return &OwnerTools{server: s} }

func ownerToolName(capability string) string {
	r := strings.NewReplacer(" ", "_", "/", "_", "{", "", "}", "", ".", "_", "-", "_")
	name := "owner_" + strings.ToLower(strings.Trim(r.Replace(capability), "_"))
	if len(name) > 64 {
		sum := sha256.Sum256([]byte(capability))
		name = fmt.Sprintf("%s_%x", name[:53], sum[:5])
	}
	return name
}

func (g *OwnerTools) Specs() []protocol.OwnerToolSpec {
	gen := schema.New()
	gen.Add(protocol.OwnerToolArguments{})
	b, err := gen.Document("OwnerToolArguments")
	if err != nil {
		panic(err)
	}
	var document map[string]any
	if err := json.Unmarshal(b, &document); err != nil {
		panic(err)
	}
	definition := document["definitions"].(map[string]any)["OwnerToolArguments"].(map[string]any)
	root := map[string]any{}
	for key, value := range definition {
		root[key] = value
	}
	root["definitions"] = document["definitions"]
	// Legacy replay input is rejected by Call and is not advertised to Agents.
	delete(root["properties"].(map[string]any), "original_envelope")
	input, _ := json.Marshal(root)
	var out []protocol.OwnerToolSpec
	for _, capability := range OwnerCapabilities() {
		if capability == "POST /api/v1/ops" {
			continue
		}
		if capability == "PUT /api/v1/webhook-keys/{id}" {
			restricted := map[string]any{}
			_ = json.Unmarshal(input, &restricted)
			props := restricted["properties"].(map[string]any)
			delete(props, "body")
			delete(props, "content_base64")
			restricted["required"] = []string{"path", "effect_seq", "webhook_key_ref"}
			privateInput, _ := json.Marshal(restricted)
			out = append(out, protocol.OwnerToolSpec{Name: ownerToolName(capability), Capability: capability, Description: "Stage a private webhook credential by opaque webhook_key_ref already held in private server storage. Use path.id for the new reference. Never pass credential bytes in body, content_base64 or model arguments; initial private bytes are staged through the owner's secure CLI key-file path.", InputSchema: privateInput})
			continue
		}
		out = append(out, protocol.OwnerToolSpec{Name: ownerToolName(capability), Capability: capability, Description: "Owner capability: " + capability + ". Operations use target, expected_control_revision, payload and positive effect_seq. HTTP reads use exact path parameters and query (including pagination); HTTP writes use body or content_base64/content_type and effect_seq. The normal owner validation applies; read the business object on unknown outcomes; retain actual action identity and never blindly repeat an external side effect.", InputSchema: input})
	}
	return out
}

func (g *OwnerTools) Call(ctx context.Context, call protocol.OwnerToolCall) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var args protocol.OwnerToolArguments
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid owner tool arguments")
	}
	if args.OriginalEnvelope != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "request replay is retired; read the business object and submit its explicit identity and current revision")
	}
	owner, err := g.server.app.MainOwner(ctx, call.Actor, call.ExecutionID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(g.server.cfg.Owners, owner.Subject) {
		return nil, protocol.Errorf(protocol.CodeForbidden, "owner is no longer configured")
	}
	execution, found, err := g.server.app.HostedExecution(ctx, call.ExecutionID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, protocol.Errorf(protocol.CodeForbidden, "main execution is unavailable")
	}
	var selected *protocol.OwnerToolSpec
	for _, spec := range g.Specs() {
		if spec.Name == call.Name {
			copy := spec
			selected = &copy
			break
		}
	}
	if selected == nil {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "unknown owner capability")
	}
	method, path, isRoute := strings.Cut(selected.Capability, " ")
	body := args.Body
	authority := &app.OwnerExecution{Actor: call.Actor, Agent: execution.Selection.Identity, ExecutionID: call.ExecutionID, CallID: call.RequestID, EffectSeq: args.EffectSeq}
	requestID := call.RequestID
	if !isRoute {
		method, path = http.MethodPost, "/api/v1/ops"
		if len(args.Payload) == 0 {
			args.Payload = json.RawMessage(`{}`)
		}
		if args.Target.ID == "" {
			if prefix := protocol.CreationTargetPrefix(selected.Capability); prefix != "" {
				args.Target.ID = protocol.NativeObjectID(prefix, "owner:"+call.ExecutionID, call.RequestID)
			}
		}
		if identities := protocol.NativePayloadIdentities(selected.Capability); len(identities) > 0 {
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(args.Payload, &payload); err != nil || payload == nil {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "operation payload must be an object")
			}
			for field, prefix := range identities {
				if len(payload[field]) == 0 || string(payload[field]) == `""` {
					payload[field], _ = json.Marshal(protocol.NativeObjectID(prefix, "owner:"+call.ExecutionID, call.RequestID+"/"+field))
				}
			}
			args.Payload, err = json.Marshal(payload)
			if err != nil {
				return nil, err
			}
		}
		envelope := protocol.Envelope{RequestID: call.RequestID, Operation: selected.Capability, Target: args.Target, ExpectedControlRevision: args.ExpectedControlRevision, Reason: args.Reason, Payload: args.Payload}
		body, err = json.Marshal(envelope)
		requestID = envelope.RequestID
		if err != nil {
			return nil, err
		}
	}
	if selected.Capability == "PUT /api/v1/webhook-keys/{id}" {
		if len(args.Body) != 0 || args.ContentBase64 != "" || args.WebhookKeyRef == "" {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "select webhook_key_ref from private storage; raw credentials are not tool arguments")
		}
		if g.server.cfg.WebhookKeys == nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "webhook storage is unavailable")
		}
		body, err = g.server.cfg.WebhookKeys.Get(ctx, args.WebhookKeyRef)
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeNotFound, "private webhook key reference is unavailable")
		}
	}
	for name, value := range args.Path {
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	if strings.ContainsAny(path, "{}") {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "all route path parameters are required")
	}
	query := url.Values{}
	for name, value := range args.Query {
		query.Set(name, value)
	}
	if args.ContentBase64 != "" {
		if !isRoute {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "operation tools accept payload, not raw HTTP content")
		}
		body, err = base64.StdEncoding.DecodeString(args.ContentBase64)
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid content_base64")
		}
	}
	if isRoute && method != http.MethodGet {
		if selected.Capability == "POST /api/v1/extensions/stage" {
			var input map[string]json.RawMessage
			if json.Unmarshal(body, &input) == nil && input != nil && len(input["request_id"]) == 0 {
				input["request_id"], _ = json.Marshal(call.RequestID)
				body, _ = json.Marshal(input)
			}
		}
		if selected.Capability == "POST /api/v1/conversation/attachments" && query.Get("request_id") == "" {
			query.Set("request_id", call.RequestID)
		}
		var identity struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(body, &identity) == nil && identity.RequestID != "" {
			requestID = identity.RequestID
		}
		if value := query.Get("request_id"); value != "" {
			requestID = value
		}
	}
	if method != http.MethodGet && method != http.MethodHead {
		if args.EffectSeq < 1 {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "positive effect_seq is required")
		}
	}
	ctx = context.WithValue(ctx, mainOwnerRequestKey{}, owner)
	ctx = app.WithOwnerExecution(ctx, authority)
	req, err := http.NewRequestWithContext(ctx, method, "http://owner.internal"+path+"?"+query.Encode(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	contentType := args.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	if args.Range != "" {
		req.Header.Set("Range", args.Range)
	}
	deployment, err := g.server.app.DeploymentID(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set(deploymentHeader, deployment)
	rec := &ownerToolResponse{headers: make(http.Header)}
	// Only the registered route set is reachable; no arbitrary network request
	// or alternate authentication middleware is selected by tool arguments.
	g.server.Handler().ServeHTTP(rec, req)
	var result any
	original := rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("X-Content-SHA256") != ""
	if original || json.Unmarshal(rec.body.Bytes(), &result) != nil {
		result = map[string]any{"content_type": rec.Header().Get("Content-Type"), "content_base64": base64.StdEncoding.EncodeToString(rec.body.Bytes()), "truncated": rec.truncated, "status_code": rec.code, "content_range": rec.Header().Get("Content-Range"), "content_sha256": rec.Header().Get("X-Content-SHA256")}
	}
	if method != http.MethodGet {
		if value, ok := result.(map[string]any); ok {
			value["request_id"] = requestID
		}
	}
	// Authentication is established internally. A credential issuance response
	// must not become model context or a durable native tool log.
	if value, ok := result.(map[string]any); ok && selected.Capability == "POST /api/v1/session" {
		delete(value, "token")
	}
	if rec.code >= 400 {
		var response protocol.APIResponse
		if json.Unmarshal(rec.body.Bytes(), &response) == nil && response.Error != nil {
			return result, response.Error
		}
		return nil, protocol.Errorf(protocol.CodeInternal, "owner interface returned HTTP %d", rec.code)
	}
	var response protocol.APIResponse
	if method != http.MethodGet && json.Unmarshal(rec.body.Bytes(), &response) == nil && response.Operation != nil && response.Operation.Error != nil {
		return result, response.Operation.Error
	}
	return result, nil
}
