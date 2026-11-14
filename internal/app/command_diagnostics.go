package app

import (
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// commandDiagnostic only reports facts already loaded within the command's
// authorized scope. It does not read another object or perform recovery.
func commandDiagnostic(code protocol.Code, message string, facts map[string]any) *protocol.Error {
	details, _ := json.Marshal(facts)
	return &protocol.Error{Code: code, Message: message, Details: details}
}
