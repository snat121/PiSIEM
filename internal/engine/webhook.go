package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Webhook is a non-blocking HTTP POST dispatcher with a hard 5s timeout per
// request. Callers hand off a URL + message and the goroutine does the rest.
// The ingestion pipeline never blocks on a slow endpoint.
type Webhook struct {
	logger *slog.Logger
	client *http.Client
}

func NewWebhook(logger *slog.Logger) *Webhook {
	return &Webhook{
		logger: logger,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

func (w *Webhook) Send(url, message string) {
	if url == "" {
		return
	}
	go func() {
		payload, _ := json.Marshal(map[string]string{"content": message})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			w.logger.Warn("webhook build", "err", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := w.client.Do(req)
		if err != nil {
			w.logger.Warn("webhook send", "url", url, "err", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			w.logger.Warn("webhook non-2xx", "url", url, "status", resp.StatusCode)
		}
	}()
}
