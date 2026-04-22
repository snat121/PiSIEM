package engine

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/snat121/PiSIEM/internal/config"
	"github.com/snat121/PiSIEM/internal/storage"
)

type compiledRule struct {
	rule     config.Rule
	template *template.Template

	mu   sync.Mutex
	hits map[string][]time.Time // host -> ring of recent hit timestamps
}

type Engine struct {
	rules   []*compiledRule
	anomaly *AnomalyDetector
	webhook *Webhook
	logger  *slog.Logger
}

func New(rules []config.Rule, anomalyEnabled bool, anomalyMultiplier float64, anomalyWebhookURL string, webhook *Webhook, logger *slog.Logger) *Engine {
	compiled := make([]*compiledRule, 0, len(rules))
	for _, r := range rules {
		tpl, err := template.New(r.ID).Parse(r.Action.MessageTemplate)
		if err != nil {
			logger.Warn("rule template parse failed; skipping", "rule", r.ID, "err", err)
			continue
		}
		compiled = append(compiled, &compiledRule{
			rule:     r,
			template: tpl,
			hits:     make(map[string][]time.Time),
		})
	}
	e := &Engine{
		rules:   compiled,
		webhook: webhook,
		logger:  logger,
	}
	if anomalyEnabled {
		e.anomaly = NewAnomalyDetector(anomalyMultiplier, anomalyWebhookURL, webhook, logger)
	}
	return e
}

// Process runs a LogEvent through every rule and the anomaly detector. It is
// safe for concurrent callers — each rule guards its own state.
func (e *Engine) Process(evt storage.LogEvent) {
	now := evt.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	for _, r := range e.rules {
		cond := r.rule.Condition
		if cond.Source != "" && evt.Source != cond.Source {
			continue
		}
		if cond.SourceFile != "" && !strings.Contains(evt.SourceFile, cond.SourceFile) {
			continue
		}
		if cond.Host != "" && !strings.Contains(evt.Host, cond.Host) {
			continue
		}
		if !strings.Contains(evt.Message, cond.Match) {
			continue
		}
		tf := time.Duration(cond.Timeframe) * time.Second
		cutoff := now.Add(-tf)

		r.mu.Lock()
		times := r.hits[evt.Host]
		kept := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		kept = append(kept, now)
		fire := len(kept) >= r.rule.Condition.Threshold
		count := len(kept)
		if fire {
			r.hits[evt.Host] = nil // reset so we don't spam
		} else {
			r.hits[evt.Host] = kept
		}
		r.mu.Unlock()

		if fire {
			e.fireRule(r, evt, count)
		}
	}
	if e.anomaly != nil {
		e.anomaly.Observe(evt.Host, now)
	}
}

func (e *Engine) fireRule(r *compiledRule, evt storage.LogEvent, count int) {
	var buf bytes.Buffer
	data := map[string]any{
		"host":     evt.Host,
		"count":    count,
		"message":  evt.Message,
		"severity": evt.Severity,
		"source":   evt.Source,
	}
	if err := r.template.Execute(&buf, data); err != nil {
		e.logger.Warn("rule template exec", "rule", r.rule.ID, "err", err)
		return
	}
	e.logger.Info("rule fired", "rule", r.rule.ID, "host", evt.Host, "count", count)
	e.webhook.Send(r.rule.Action.WebhookURL, buf.String())
}

// RunEvictionLoop periodically sweeps per-rule / per-host hit records older
// than each rule's timeframe so the maps do not grow unboundedly.
func (e *Engine) RunEvictionLoop(stop <-chan struct{}) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			e.evictExpired()
		}
	}
}

func (e *Engine) evictExpired() {
	now := time.Now()
	for _, r := range e.rules {
		tf := time.Duration(r.rule.Condition.Timeframe) * time.Second
		cutoff := now.Add(-tf)
		r.mu.Lock()
		for host, times := range r.hits {
			kept := times[:0]
			for _, t := range times {
				if t.After(cutoff) {
					kept = append(kept, t)
				}
			}
			if len(kept) == 0 {
				delete(r.hits, host)
			} else if len(kept) != len(times) {
				r.hits[host] = kept
			}
		}
		r.mu.Unlock()
	}
	if e.anomaly != nil {
		e.anomaly.Evict()
	}
}
