package remote

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"
)

func runTUIProcess(ctx context.Context, bin string, args []string, cfg PrivateTUI, path string, stdin io.Reader, stdout, stderr io.Writer) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, bin, args...)
	cmd.Dir = cfg.Cwd
	cmd.Env = append(os.Environ(), "TEND_NATIVE_TUI_TOKEN="+cfg.Token)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	// Give the native frontend a chance to restore its terminal before replacing
	// a disconnected view. This never interrupts the centrally running loop.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			cancel()
			<-done
			return ctx.Err()
		case <-ticker.C:
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var next PrivateTUI
			if json.Unmarshal(raw, &next) != nil {
				continue
			}
			if next.Thread != cfg.Thread || next.Cwd != cfg.Cwd || next.Closed || next.Token != cfg.Token {
				cancel()
				<-done
				return context.Canceled
			}
		}
	}
}
