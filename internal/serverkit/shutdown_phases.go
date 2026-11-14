package serverkit

import (
	"context"
	"log/slog"
	"time"
)

// Observe an existing component lifetime without cancelling its final writes
// or creating another shutdown gate. The callback is joined before returning.
func runWithShutdownLog(ctx context.Context, now func() time.Time, log *slog.Logger, component string, run func()) {
	observed := make(chan struct{})
	var started time.Time
	stop := context.AfterFunc(ctx, func() {
		started = now()
		log.Info("server shutdown phase", "phase", component, "state", "started")
		close(observed)
	})
	run()
	if !stop() {
		<-observed
		log.Info("server shutdown phase", "phase", component, "state", "finished", "elapsed_ms", now().Sub(started).Milliseconds())
	}
}
