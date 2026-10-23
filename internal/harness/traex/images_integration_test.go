//go:build integration

package traex

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// TestIntegration_NativeImageRecognition requires explicit TEND_TRAEX_BIN.
// Bin may name an owner-provided wrapper that starts app-server via a gateway.
func TestIntegration_NativeImageRecognition(t *testing.T) {
	bin := os.Getenv("TEND_TRAEX_BIN")
	if bin == "" {
		t.Skip("set TEND_TRAEX_BIN explicitly to authorize a real image-recognition turn")
	}
	picture := image.NewRGBA(image.Rect(0, 0, 128, 128))
	for y := range 128 {
		for x := range 128 {
			c := color.RGBA{0, 0, 255, 255}
			if (x-64)*(x-64)+(y-64)*(y-64) < 32*32 {
				c = color.RGBA{255, 0, 0, 255}
			}
			picture.SetRGBA(x, y, c)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := Start(ctx, Options{Bin: bin, Dir: t.TempDir(), Version: "image-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	th, err := s.StartThread(ctx, harness.ThreadConfig{Cwd: t.TempDir(), Model: model(), Sandbox: "read-only", ApprovalPolicy: "never", DeveloperInstructions: "This is a visual recognition check. Describe only what is visibly present. Do not call any tools or inspect files.", Config: map[string]any{"features.apps": false, "features.plugins": false, "features.memories": false, "features.browser_use": false, "features.image_generation": false, "skills.include_instructions": false, "skills.bundled.enabled": false}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = th.Interrupt(cleanup, "")
		if err := s.conn.call(cleanup, "thread/archive", map[string]string{"threadId": th.ID()}, nil); err != nil {
			t.Errorf("archive own test thread: %v", err)
		}
	}()
	turnID, err := th.StartTurn(ctx, harness.TurnInput{Text: "Name the foreground color, foreground geometric shape, and background color. Reply in English with one short sentence. Do not use tools.", ImageURLs: []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())}})
	if err != nil {
		t.Fatal(err)
	}
	var answer strings.Builder
	for {
		select {
		case <-ctx.Done():
			t.Fatal("image turn timed out")
		case ev, ok := <-th.Events():
			if !ok {
				t.Fatal("session closed")
			}
			if ev.TurnID != turnID {
				continue
			}
			if ev.Kind == harness.EventItemCompleted && ev.ItemType == harness.ItemAgentMessage {
				answer.WriteString(ev.Text)
			}
			if ev.Tool != nil {
				t.Fatalf("unexpected non-message item: %s", ev.ItemType)
			}
			if ev.Kind == harness.EventTurnCompleted {
				text := strings.ToLower(answer.String())
				if ev.TurnStatus != "completed" || !strings.Contains(text, "red") || !strings.Contains(text, "circle") || !strings.Contains(text, "blue") {
					t.Fatalf("visual answer=%q status=%s", answer.String(), ev.TurnStatus)
				}
				t.Logf("native visual recognition passed: %s", answer.String())
				return
			}
		}
	}
}
