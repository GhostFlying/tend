//go:build integration

package traex

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// TestIntegration_ReadOriginalAttachments requires an explicitly supplied
// harness binary. It provides paths only: neither text bytes nor animation
// frames enter the model's turn input. The agent chooses how to read them.
func TestIntegration_ReadOriginalAttachments(t *testing.T) {
	bin := os.Getenv("TEND_TRAEX_BIN")
	if bin == "" {
		t.Skip("set TEND_TRAEX_BIN explicitly to authorize a real attachment-reading turn")
	}
	dir := t.TempDir()
	markerBytes := make([]byte, 24)
	if _, err := rand.Read(markerBytes); err != nil {
		t.Fatal(err)
	}
	marker := hex.EncodeToString(markerBytes)
	textPath := filepath.Join(dir, "evidence.txt")
	if err := os.WriteFile(textPath, []byte(marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	palette := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}}
	animation := &gif.GIF{Delay: []int{10, 10}, LoopCount: 0}
	for index := range 2 {
		frame := image.NewPaletted(image.Rect(0, 0, 16, 16), palette)
		for pixel := range frame.Pix {
			frame.Pix[pixel] = uint8(index)
		}
		animation.Image = append(animation.Image, frame)
	}
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, animation); err != nil {
		t.Fatal(err)
	}
	gifPath := filepath.Join(dir, "original.gif")
	if err := os.WriteFile(gifPath, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded.Bytes())
	gifSHA := hex.EncodeToString(digest[:])
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := Start(ctx, Options{Bin: bin, Dir: dir, Version: "attachment-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close own attachment session: %v", err)
		}
	}()
	th, err := s.StartThread(ctx, harness.ThreadConfig{
		Cwd: dir, Model: model(), Sandbox: "read-only", ApprovalPolicy: "never",
		DeveloperInstructions: "This is a read-only attachment inspection. Use native shell tools to read only the two supplied files; do not access the network or edit files. Determine the answers from original file bytes. Return only the requested JSON in your final answer.",
		Config:                map[string]any{"features.apps": false, "features.plugins": false, "features.memories": false, "features.browser_use": false, "features.image_generation": false, "skills.include_instructions": false, "skills.bundled.enabled": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("own attachment test thread: %s", th.ID())
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = th.Interrupt(cleanup, "")
		if err := s.conn.call(cleanup, "thread/archive", map[string]string{"threadId": th.ID()}, nil); err != nil {
			t.Errorf("archive own attachment thread: %v", err)
		} else {
			t.Log("own attachment test thread archived")
		}
	}()
	prompt := fmt.Sprintf("Inspect these original local attachments using tools:\n- text/plain: %s\n- image/gif: %s\nRead the complete text (trim its trailing newline), count all GIF frames, determine the center-pixel RGB values of each decoded frame in display order, and compute the SHA256 of the original GIF bytes. Print the read text and computed original GIF SHA256 in your tool output as evidence. Return exactly one JSON object with keys text (string), gif_frame_count (integer), gif_center_rgb (array of RGB integer arrays), gif_sha256 (string). Do not guess values from filenames or give a preview-only answer.", textPath, gifPath)
	turnID, err := th.StartTurn(ctx, harness.TurnInput{Text: prompt})
	if err != nil {
		t.Fatal(err)
	}
	var answer string
	var readText, readGIF bool
	for {
		select {
		case <-ctx.Done():
			t.Fatal("original-attachment turn timed out")
		case ev, ok := <-th.Events():
			if !ok {
				t.Fatal("attachment session closed")
			}
			if ev.TurnID != turnID || ev.Kind != harness.EventItemCompleted && ev.Kind != harness.EventTurnCompleted {
				continue
			}
			if ev.ItemType == harness.ItemAgentMessage {
				answer = ev.Text
			}
			if ev.Tool != nil {
				t.Logf("attachment inspection tool %s status=%s input=%s output=%s", ev.Tool.Name, ev.Tool.Status, ev.Tool.Input, ev.Tool.Output)
				// A batch can read a file successfully before a later optional
				// utility fails. Validate observed bytes rather than the final
				// exit code; od-style hex output is also a legitimate read.
				if ev.Tool.Name == "shell" {
					var hexOutput strings.Builder
					for _, word := range strings.Fields(ev.Tool.Output) {
						if len(word) == 2 {
							if _, err := hex.DecodeString(word); err == nil {
								hexOutput.WriteString(word)
							}
						}
					}
					textObserved := strings.Contains(ev.Tool.Output, marker) || strings.Contains(hexOutput.String(), hex.EncodeToString([]byte(marker)))
					readText = readText || strings.Contains(ev.Tool.Input, "evidence.txt") && textObserved
					readGIF = readGIF || strings.Contains(ev.Tool.Input, "original.gif") && strings.Contains(ev.Tool.Output, gifSHA)
				}
			}
			if ev.Kind != harness.EventTurnCompleted {
				continue
			}
			var result struct {
				Text       string  `json:"text"`
				FrameCount int     `json:"gif_frame_count"`
				RGB        [][]int `json:"gif_center_rgb"`
				SHA256     string  `json:"gif_sha256"`
			}
			if err := json.Unmarshal([]byte(answer), &result); err != nil {
				t.Fatalf("attachment answer was not JSON: %q: %v", answer, err)
			}
			if ev.TurnStatus != "completed" || !readText || !readGIF || result.Text != marker || result.FrameCount != 2 || result.SHA256 != gifSHA || !reflect.DeepEqual(result.RGB, [][]int{{255, 0, 0}, {0, 0, 255}}) {
				t.Fatalf("attachment result=%s status=%s observed_text_read=%v observed_original_gif_read=%v", answer, ev.TurnStatus, readText, readGIF)
			}
			t.Logf("original text and animated GIF independently read: %s", answer)
			return
		}
	}
}
