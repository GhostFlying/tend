package api

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"strings"
	"testing"
)

func TestMainOwnerNotificationWithUploadedAttachment(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "main"}[delegated], func(t *testing.T) {
			e, call := mainOwnerFixture(t)
			owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}
			uploaded, err := e.app.UploadConversationAttachment(t.Context(), owner, e.ids.New(protocol.PrefixRequest), "report.txt", "text/plain", strings.NewReader("findings"))
			if err != nil || uploaded.Error != nil {
				t.Fatal(uploaded, err)
			}
			var file protocol.ConversationAttachment
			if err := json.Unmarshal(uploaded.Result, &file); err != nil {
				t.Fatal(err)
			}
			p := protocol.MessagingPublication{Body: "Here is the report", Purpose: "progress", Reason: "Requested findings", Attachments: []string{file.FileID}}
			if delegated {
				payload, _ := json.Marshal(p)
				response, err := ownerToolInvoke(t, e, call, "messaging.notify", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "message"}, Payload: payload})
				if err != nil {
					t.Errorf("main notification rejected: %v response=%+v", err, response)
				}
			} else {
				env := e.envelope("messaging.notify", protocol.Target{Type: "message"}, nil, p)
				response, err := e.app.Execute(t.Context(), owner, &env, app.MessagingCommand{Action: "notify"})
				if err != nil || response.Error != nil {
					t.Errorf("owner notification rejected: %v response=%+v", err, response)
				}
			}
		})
	}
}
