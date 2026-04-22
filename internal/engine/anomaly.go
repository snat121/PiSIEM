package engine

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	anomalyWindow  = 60 * time.Second
	emaAlpha       = 0.2
	idleEvictAfter = 10 * anomalyWindow
)

type hostState struct {
	windowStart time.Time
	count       int
	baseline    float64 // EMA of per-window count
	lastFire    time.Time
}

// AnomalyDetector maintains a per-host rolling 60s message count and an EMA
// baseline. When the current window exceeds multiplier × baseline, it emits a
// webhook alert. Built-in — no rule configuration required.
type AnomalyDetector struct {
	mu         sync.Mutex
	hosts      map[string]*hostState
	multiplier float64
	webhookURL string
	webhook    *Webhook
	logger     *slog.Logger
}

func NewAnomalyDetector(multiplier float64, webhookURL string, webhook *Webhook, logger *slog.Logger) *AnomalyDetector {
	if multiplier <= 0 {
		multiplier = 10
	}
	return &AnomalyDetector{
		hosts:      make(map[string]*hostState),
		multiplier: multiplier,
		webhookURL: webhookURL,
		webhook:    webhook,
		logger:     logger,
	}
}

func (a *AnomalyDetector) Observe(host string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.hosts[host]
	if !ok {
		st = &hostState{windowStart: now}
		a.hosts[host] = st
	}
	if now.Sub(st.windowStart) >= anomalyWindow {
		if st.baseline == 0 {
			st.baseline = float64(st.count)
		} else {
			st.baseline = emaAlpha*float64(st.count) + (1-emaAlpha)*st.baseline
		}
		st.count = 0
		st.windowStart = now
	}
	st.count++
	if st.baseline > 0 && float64(st.count) > a.multiplier*st.baseline {
		// don't re-fire within the same 60s window
		if now.Sub(st.lastFire) < anomalyWindow {
			return
		}
		st.lastFire = now
		msg := fmt.Sprintf("Anomaly: host %s sent %d messages in 60s (baseline: %.1f)", host, st.count, st.baseline)
		a.logger.Info("anomaly detected", "host", host, "count", st.count, "baseline", st.baseline)
		if a.webhookURL != "" {
			a.webhook.Send(a.webhookURL, msg)
		}
	}
}

// Evict drops host state that has been idle for more than idleEvictAfter, so
// the map cannot grow indefinitely as hosts appear and disappear.
func (a *AnomalyDetector) Evict() {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().Add(-idleEvictAfter)
	for host, st := range a.hosts {
		if st.windowStart.Before(cutoff) {
			delete(a.hosts, host)
		}
	}
}
