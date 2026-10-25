package orca

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"time"
)

type failedTurn struct {
	Code   string
	TurnID string
}

var recoveryErrorCode = regexp.MustCompile(`(?:^|[\s:;"{])code:\s*(invalid_encrypted_content|rate_limit_exceeded)(?:[;\s"}]|$)`)

// readFailedTurn reads only the provider's explicitly bound root transcript.
// Assistant prose, child notifications and old turn errors never classify a
// model failure. Raw provider error text can contain credentials and is not
// persisted or returned to callers.
func readFailedTurn(ctx context.Context, session ProviderSession, since int64) (failedTurn, error) {
	f, err := os.Open(session.TranscriptPath)
	if err != nil {
		return failedTurn{}, errors.New("root transcript cannot be read")
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	identity := ""
	latestTurn := ""
	var result failedTurn
	pending := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return failedTurn{}, err
		}
		var row struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Payload   struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				TurnID string `json:"turn_id"`
				CallID string `json:"call_id"`
				Error  *struct {
					Code           string          `json:"code"`
					Message        string          `json:"message"`
					CodexErrorInfo json.RawMessage `json:"codex_error_info"`
				} `json:"error"`
			} `json:"payload"`
		}
		if err := decoder.Decode(&row); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return failedTurn{}, errors.New("root transcript contains an incomplete record")
		}
		if row.Type == "session_meta" {
			identity = row.Payload.ID
			continue
		}
		if row.Timestamp.UnixMilli() < since {
			continue
		}
		p := row.Payload
		if row.Type == "response_item" && p.CallID != "" {
			switch p.Type {
			case "function_call", "custom_tool_call":
				pending[p.CallID] = true
			case "function_call_output", "custom_tool_call_output":
				delete(pending, p.CallID)
			}
		}
		if row.Type != "event_msg" {
			continue
		}
		switch p.Type {
		case "task_started", "turn_started":
			latestTurn = p.TurnID
			result = failedTurn{}
		case "task_complete", "turn_complete":
			if p.TurnID == "" || latestTurn != p.TurnID {
				continue
			}
			result = failedTurn{}
			if p.Error != nil {
				result = failedTurn{Code: p.Error.Code, TurnID: p.TurnID}
				if result.Code == "" {
					result.Code = temporaryModelError(p.Error.CodexErrorInfo)
				}
				if match := recoveryErrorCode.FindStringSubmatch(p.Error.Message); match != nil && result.Code != "unauthorized" && result.Code != "insufficient_quota" {
					result.Code = match[1]
				}
			}
		case "turn_aborted":
			result = failedTurn{}
		}
	}
	if identity != session.ID || result.TurnID == "" {
		return failedTurn{}, errors.New("root transcript does not confirm this session's latest failed turn")
	}
	if len(pending) != 0 {
		return failedTurn{}, errors.New("root transcript has tool calls with unknown outcomes; reconciliation is required")
	}
	return result, nil
}

// temporaryModelError uses Codex's tagged error enum and excludes HTTP client
// errors, which need authentication, specification or policy changes.
func temporaryModelError(raw json.RawMessage) string {
	var code string
	if json.Unmarshal(raw, &code) == nil && code == "rate_limit_exceeded" {
		return code
	}
	var tagged map[string]struct {
		Status *int `json:"http_status_code"`
	}
	if json.Unmarshal(raw, &tagged) != nil {
		return ""
	}
	for _, code := range []string{"http_connection_failed", "response_stream_connection_failed", "response_stream_disconnected"} {
		if info, ok := tagged[code]; ok && (info.Status == nil || *info.Status >= 500) {
			return code
		}
	}
	return ""
}
