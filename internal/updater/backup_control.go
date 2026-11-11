package updater

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Supervisor) backupHandler(w http.ResponseWriter, r *http.Request) {
	var output any
	var err error
	if r.URL.Path == "/backup/status" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		output, err = s.BackupStatus(r.Context())
	} else {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var input ControlRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&input); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid backup command"})
			return
		}
		if err = decoder.Decode(new(any)); err != io.EOF {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err = nil
		switch r.URL.Path {
		case "/backup/protect-log":
			if input.DiagnosticLogProtection == nil {
				err = errors.New("updater: complete diagnostic protection required")
			} else {
				output, err = s.ProtectDiagnosticLog(r.Context(), *input.DiagnosticLogProtection)
			}
		case "/backup/preview":
			p := protocol.BackupPolicy{}
			if input.BackupPolicy != nil {
				p = *input.BackupPolicy
			}
			output, err = s.BackupPreview(r.Context(), p)
		case "/backup/verify":
			if input.BackupVerification == nil {
				err = errors.New("updater: complete backup verification required")
			} else {
				output, err = s.VerifyBackup(r.Context(), *input.BackupVerification)
			}
		case "/backup/prune":
			if input.BackupPrune == nil {
				err = errors.New("updater: exact original preview required")
			} else {
				output, err = s.PruneBackups(r.Context(), *input.BackupPrune)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}
	if err != nil {
		w.WriteHeader(http.StatusConflict)
	}
	response := map[string]any{"result": output}
	if err != nil {
		response["error"] = err.Error()
	}
	_ = json.NewEncoder(w).Encode(response)
}
