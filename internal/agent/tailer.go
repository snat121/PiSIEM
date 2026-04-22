package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

type TailFile struct {
	Path     string
	Severity int
}

type TailerConfig struct {
	ServerHost           string
	ServerPort           int
	Files                []TailFile
	BatchSize            int
	BatchInterval        time.Duration
	ReconnectMaxInterval time.Duration
	LocalBufferMaxEvents int
}

// Tailer watches a list of files, batches new lines, and ships them as
// AgentEvent JSON lines to the PiSIEM server over a persistent TCP connection.
// On disconnection it buffers events locally and reconnects with exponential
// backoff, dropping oldest events when the buffer overflows.
type Tailer struct {
	cfg    TailerConfig
	logger *slog.Logger
	host   string

	mu     sync.Mutex
	buf    []AgentEvent
	notify chan struct{}

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewTailer(cfg TailerConfig, logger *slog.Logger) *Tailer {
	host, _ := os.Hostname()
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.BatchInterval <= 0 {
		cfg.BatchInterval = 5 * time.Second
	}
	if cfg.ReconnectMaxInterval <= 0 {
		cfg.ReconnectMaxInterval = 60 * time.Second
	}
	if cfg.LocalBufferMaxEvents <= 0 {
		cfg.LocalBufferMaxEvents = 10000
	}
	return &Tailer{
		cfg:    cfg,
		logger: logger,
		host:   host,
		notify: make(chan struct{}, 1),
		stop:   make(chan struct{}),
	}
}

func (t *Tailer) Start(ctx context.Context) {
	for _, f := range t.cfg.Files {
		f := f
		t.wg.Add(1)
		go t.tailFile(ctx, f)
	}
	t.wg.Add(1)
	go t.shipper(ctx)
}

func (t *Tailer) tailFile(ctx context.Context, f TailFile) {
	defer t.wg.Done()
	var offset int64
	if fi, err := os.Stat(f.Path); err == nil {
		offset = fi.Size() // start at tail
	}
	for {
		if !t.alive(ctx) {
			return
		}
		fp, err := os.Open(f.Path)
		if err != nil {
			t.logger.Warn("open log file", "path", f.Path, "err", err)
			if !sleepOrDone(ctx, t.stop, 5*time.Second) {
				return
			}
			continue
		}
		if fi, err := fp.Stat(); err == nil && fi.Size() < offset {
			offset = 0 // rotated / truncated
		}
		if _, err := fp.Seek(offset, io.SeekStart); err != nil {
			fp.Close()
			if !sleepOrDone(ctx, t.stop, 5*time.Second) {
				return
			}
			continue
		}
		reader := bufio.NewReader(fp)
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				clean := line
				if clean[len(clean)-1] == '\n' {
					clean = clean[:len(clean)-1]
				}
				if l := len(clean); l > 0 && clean[l-1] == '\r' {
					clean = clean[:l-1]
				}
				if clean != "" {
					t.enqueue(AgentEvent{
						Timestamp:  time.Now().UTC(),
						Host:       t.host,
						SourceFile: f.Path,
						Message:    clean,
						Severity:   f.Severity,
					})
				}
				offset += int64(len(line))
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				t.logger.Warn("read log file", "path", f.Path, "err", err)
				break
			}
		}
		fp.Close()
		if !sleepOrDone(ctx, t.stop, 1*time.Second) {
			return
		}
	}
}

func (t *Tailer) enqueue(e AgentEvent) {
	t.mu.Lock()
	if len(t.buf) >= t.cfg.LocalBufferMaxEvents {
		drop := len(t.buf) - t.cfg.LocalBufferMaxEvents + 1
		t.buf = append(t.buf[:0], t.buf[drop:]...)
		t.logger.Warn("local buffer overflow; dropped oldest", "dropped", drop, "cap", t.cfg.LocalBufferMaxEvents)
	}
	t.buf = append(t.buf, e)
	t.mu.Unlock()
	select {
	case t.notify <- struct{}{}:
	default:
	}
}

func (t *Tailer) shipper(ctx context.Context) {
	defer t.wg.Done()
	backoff := 1 * time.Second
	addr := t.cfg.ServerHost + ":" + strconv.Itoa(t.cfg.ServerPort)
	for {
		if !t.alive(ctx) {
			return
		}
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			t.logger.Warn("agent dial failed", "addr", addr, "err", err, "retry_in", backoff)
			if !sleepOrDone(ctx, t.stop, backoff) {
				return
			}
			backoff *= 2
			if backoff > t.cfg.ReconnectMaxInterval {
				backoff = t.cfg.ReconnectMaxInterval
			}
			continue
		}
		t.logger.Info("agent connected to server", "addr", addr)
		backoff = 1 * time.Second
		if err := t.pump(ctx, conn); err != nil {
			t.logger.Warn("agent session ended", "err", err)
		}
		conn.Close()
	}
}

func (t *Tailer) pump(ctx context.Context, conn net.Conn) error {
	tick := time.NewTicker(t.cfg.BatchInterval)
	defer tick.Stop()
	enc := json.NewEncoder(conn)
	for {
		select {
		case <-ctx.Done():
			t.flushAll(enc)
			return nil
		case <-t.stop:
			t.flushAll(enc)
			return nil
		case <-t.notify:
			if err := t.flushIfFull(enc); err != nil {
				return err
			}
		case <-tick.C:
			if err := t.flushAll(enc); err != nil {
				return err
			}
		}
	}
}

func (t *Tailer) flushIfFull(enc *json.Encoder) error {
	t.mu.Lock()
	if len(t.buf) < t.cfg.BatchSize {
		t.mu.Unlock()
		return nil
	}
	batch := t.buf
	t.buf = nil
	t.mu.Unlock()
	return t.sendBatch(enc, batch)
}

func (t *Tailer) flushAll(enc *json.Encoder) error {
	t.mu.Lock()
	if len(t.buf) == 0 {
		t.mu.Unlock()
		return nil
	}
	batch := t.buf
	t.buf = nil
	t.mu.Unlock()
	return t.sendBatch(enc, batch)
}

func (t *Tailer) sendBatch(enc *json.Encoder, batch []AgentEvent) error {
	for i, e := range batch {
		if err := enc.Encode(&e); err != nil {
			// requeue unsent remainder at the front
			remaining := batch[i:]
			t.mu.Lock()
			t.buf = append(append([]AgentEvent(nil), remaining...), t.buf...)
			t.mu.Unlock()
			return err
		}
	}
	return nil
}

func (t *Tailer) Stop() {
	t.stopOnce.Do(func() { close(t.stop) })
	t.wg.Wait()
}

func (t *Tailer) alive(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-t.stop:
		return false
	default:
		return true
	}
}

func sleepOrDone(ctx context.Context, stop <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}
