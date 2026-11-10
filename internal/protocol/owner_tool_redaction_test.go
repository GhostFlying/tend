package protocol

import (
	"strings"
	"testing"
)

func TestOwnerCredentialRedactionMatchesJSONCasingAndAliases(t *testing.T) {
	const privateTool = "owner_put__api_v1_webhook_keys_id"
	const secret = "private-signing-material"
	for _, test := range []struct{ name, tool, input string }{
		{"named_aliases", privateTool, `{"Body":"private-signing-material","CONTENT_BASE64":"private-signing-material","Payload":{"raw":"private-signing-material"},"webhook_key_ref":"staged-key"}`},
		{"wrapped_aliases", "owner_call", `{"Name":"owner_put__api_v1_webhook_keys_id","Arguments":{"Body":"private-signing-material","Content_Base64":"private-signing-material","PAYLOAD":"private-signing-material","webhook_key_ref":"staged-key"}}`},
		{"duplicate_fields", "owner_call", `{"name":"other","Name":"owner_put__api_v1_webhook_keys_id","arguments":{"body":"private-signing-material"},"Arguments":{"content_base64":"private-signing-material","webhook_key_ref":"staged-key"}}`},
		{"duplicate_target", "owner_call", `{"name":"owner_put__api_v1_webhook_keys_id","name":"other","arguments":{"Body":"private-signing-material","webhook_key_ref":"staged-key"}}`},
		{"shadowed_target", "owner_call", `{"Name":"owner_put__api_v1_webhook_keys_id","name":"other","Arguments":{"Body":"private-signing-material","webhook_key_ref":"staged-key"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := RedactOwnerToolInput(test.tool, test.input)
			if strings.Contains(result, secret) {
				t.Fatal("credential survived tool observation redaction", result)
			}
			if !strings.Contains(result, "staged-key") {
				t.Fatal("opaque staging reference was lost", result)
			}
		})
	}
}
