package traex

import (
	"context"
	"encoding/json"
	"github.com/BurntSushi/toml"
	"github.com/GhostFlying/tend/internal/protocol"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (s *Session) EnvironmentRequiresOriginalThread() bool {
	return s.environmentReference != nil && s.environmentReference.ResumeOnly
}

// A process handshake alone does not prove the original thread can be loaded.
// Keep its rollback reference until thread attachment has actually succeeded.
func (s *Session) environmentThreadAttached(ctx context.Context, threadID string) error {
	if s.environmentReference == nil {
		return nil
	}
	s.mu.Lock()
	if s.environmentAttached {
		s.mu.Unlock()
		return nil
	}
	// A failed persistence receipt must not be interpreted as a known failed
	// native attachment when the caller later closes the session.
	s.environmentAttached = true
	s.mu.Unlock()
	if err := s.environmentReference.Record(ctx, "ready"); err != nil {
		return err
	}
	s.observeEnvironmentMCP(ctx, threadID)
	return nil
}

// An observation error must not fail a successfully attached native thread.
// MCP status distinguishes configuration, connection and tool catalogs; it is
// not a claim that an arbitrary tool call has the required account permission.
func (s *Session) observeEnvironmentMCP(ctx context.Context, threadID string) {
	ref := s.environmentReference
	if ref == nil || ref.Extensions == (protocol.ExtensionExecutionPin{}) {
		return
	}
	var config struct {
		MCP     map[string]any `toml:"mcp_servers"`
		Plugins map[string]any `toml:"plugins"`
	}
	if _, err := toml.DecodeFile(filepath.Join(ref.ProcessHome, "config.toml"), &config); err != nil {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var response struct {
		Data []struct {
			Name          string                     `json:"name"`
			RuntimeStatus string                     `json:"runtimeStatus"`
			AuthStatus    string                     `json:"authStatus"`
			PluginID      string                     `json:"pluginId"`
			Tools         map[string]json.RawMessage `json:"tools"`
			ToolsError    *string                    `json:"toolsError"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	statuses := []protocol.ExtensionMCPStatus{}
	cursor := ""
	for {
		params := map[string]any{"threadId": threadID, "limit": 100, "detail": "toolsAndAuthOnly"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := s.conn.call(probeCtx, "mcpServerStatus/list", params, &response); err != nil {
			return
		}
		for _, server := range response.Data {
			source := "project"
			if _, ok := config.MCP[server.Name]; ok {
				source = "managed"
			}
			if server.PluginID != "" {
				source = "project_plugin"
				if _, ok := config.Plugins[server.PluginID]; ok {
					source = "managed_plugin"
				}
			}
			state := nativeMCPState(server.RuntimeStatus, server.AuthStatus, server.ToolsError != nil, len(server.Tools))
			statuses = append(statuses, protocol.ExtensionMCPStatus{ID: server.Name, State: state, Source: source, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		}
		if response.NextCursor == nil || *response.NextCursor == "" || *response.NextCursor == cursor {
			break
		}
		cursor = *response.NextCursor
	}
	slices.SortFunc(statuses, func(a, b protocol.ExtensionMCPStatus) int { return strings.Compare(a.ID, b.ID) })
	_ = ref.RecordMCP(probeCtx, statuses)
}

func nativeMCPState(runtime, auth string, toolsError bool, tools int) string {
	if auth == "notLoggedIn" || runtime == "authenticationRequired" {
		return "authentication_missing"
	}
	if runtime == "failed" || runtime == "cancelled" {
		return "connection_failed"
	}
	if runtime == "disabled" {
		return "disabled_by_policy"
	}
	if toolsError {
		return "tool_loading_failed"
	}
	if runtime == "connected" {
		if tools > 0 {
			return "connected_tools_loaded"
		}
		return "connected_no_tools"
	}
	if tools > 0 {
		return "catalog_loaded_connection_unverified"
	}
	if runtime == "starting" {
		return "connecting"
	}
	return "connection_unverified"
}

func (s *Session) EnvironmentPending() bool {
	if s.environmentReference == nil {
		return false
	}
	s.mu.Lock()
	if time.Since(s.lastEnvironmentCheck) < 5*time.Second {
		s.mu.Unlock()
		return false
	}
	s.lastEnvironmentCheck = time.Now()
	s.mu.Unlock()
	return !s.environmentReference.Current()
}
func (s *Session) AdoptEnvironment(ctx context.Context) error {
	return s.environmentReference.Adopt(ctx)
}

// EnvironmentQuiescent reads original native facts before idle replacement.
func (s *Session) EnvironmentQuiescent(ctx context.Context, id string) (bool, error) {
	var response struct {
		Thread struct {
			Preview string `json:"preview"`
			Path    string `json:"path"`
			Status  struct {
				Type string `json:"type"`
			} `json:"status"`
			ID string `json:"id"`
		} `json:"thread"`
	}
	// Read live status without hydrating full history: supported native stores
	// can expose live thread state while list_turns is unavailable. The owner
	// separately fences deliveries, native receipts and maintenance operations.
	if err := s.conn.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &response); err != nil {
		return false, err
	}
	if response.Thread.ID != id || response.Thread.Status.Type != "idle" {
		return false, nil
	}
	if s.environmentReference != nil {
		// Native threads without a first persisted input are not resumable
		// after process exit. Leave their update pending until history exists.
		if response.Thread.Preview == "" || response.Thread.Path == "" {
			return false, nil
		}
		info, err := os.Stat(response.Thread.Path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return false, nil
		}
	}
	s.mu.Lock()
	callbacks := s.toolCallbacks
	s.mu.Unlock()
	s.conn.mu.Lock()
	pending := len(s.conn.pending)
	s.conn.mu.Unlock()
	return callbacks == 0 && pending == 0, nil
}

func (s *Session) EnvironmentPin() protocol.ExtensionExecutionPin {
	if s.environmentReference == nil {
		return protocol.ExtensionExecutionPin{}
	}
	return s.environmentReference.Extensions
}
