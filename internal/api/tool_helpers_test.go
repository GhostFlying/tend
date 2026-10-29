package api

import (
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// toolAnswer is a tool's answer as the model reads it.
type toolAnswer struct {
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   *protocol.Error `json:"error"`
	Current json.RawMessage `json:"current"`
}
