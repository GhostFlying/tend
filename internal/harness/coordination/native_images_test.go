package coordination

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeImageEvidenceFitsTransportWithoutRepeatingOriginalBytes(t *testing.T) {
	image := "data:image/png;base64," + strings.Repeat("A", 5<<20)
	original, err := json.Marshal(map[string]any{"id": "native-item", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "Exact original owner text"}, map[string]any{"type": "image", "url": image}}, "native_sequence": json.Number("9007199254740993")})
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), original...)
	compact, err := CompactNativeImages(original)
	if err != nil || len(compact) > 1024 || !bytes.Equal(original, before) {
		t.Fatal("native history was changed or duplicate image survived transport", len(compact), err)
	}
	digest := sha256.Sum256([]byte(image))
	for _, evidence := range []string{"native-item", "Exact original owner text", "9007199254740993", "tend:inline-image-url:sha256:" + hex.EncodeToString(digest[:])} {
		if !bytes.Contains(compact, []byte(evidence)) {
			t.Fatal("lost receipt evidence", evidence)
		}
	}
	// Recovery receipts repeat items in their ordered list and raw transcript.
	receipt, err := json.Marshal(map[string]any{"Items": []json.RawMessage{original}, "Raw": json.RawMessage(original)})
	if err != nil {
		t.Fatal(err)
	}
	compact, err = CompactNativeImages(receipt)
	if err != nil || len(compact) > 2048 {
		t.Fatal("native receipt retained duplicate images", len(compact), err)
	}
}
