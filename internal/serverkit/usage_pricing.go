package serverkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/app"
)

func fetchUsagePrices(ctx context.Context) (json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch public usage quotes: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("public usage quote status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("public usage quote catalog exceeds limit")
	}
	return data, nil
}

func runUsagePrices(ctx context.Context, a *app.App, fetch func(context.Context) (json.RawMessage, error), log *slog.Logger) {
	for {
		request, cancel := context.WithTimeout(ctx, 20*time.Second)
		raw, err := fetch(request)
		if err == nil {
			err = a.RecordUsagePriceCatalog(request, raw)
		}
		cancel()
		delay := 6 * time.Hour
		if err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "usage price refresh pending", "err", err)
			delay = time.Minute
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
