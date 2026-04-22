package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type LogEvent struct {
	ID         int64     `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Host       string    `json:"host"`
	Severity   int       `json:"severity"`
	Facility   int       `json:"facility"`
	Message    string    `json:"message"`
	RawLog     string    `json:"raw_log"`
	Source     string    `json:"source"`
	SourceFile string    `json:"source_file"`
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS log_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp   DATETIME NOT NULL,
    host        TEXT NOT NULL,
    severity    INTEGER,
    facility    INTEGER,
    message     TEXT,
    raw_log     TEXT,
    source      TEXT NOT NULL DEFAULT 'syslog',
    source_file TEXT
);
CREATE INDEX IF NOT EXISTS idx_timestamp   ON log_events(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_host        ON log_events(host);
CREATE INDEX IF NOT EXISTS idx_severity    ON log_events(severity);
CREATE INDEX IF NOT EXISTS idx_source      ON log_events(source);
`

type Store struct {
	db            *sql.DB
	buf           chan LogEvent
	flushSignal   chan struct{}
	flushInterval time.Duration
	flushSize     int
	logger        *slog.Logger
	wg            sync.WaitGroup
	stop          chan struct{}
	stopOnce      sync.Once

	droppedEvents atomic.Int64
	bufferDepth   atomic.Int64
	totalWritten  atomic.Int64
}

func Open(path string, bufSize, flushSize, flushIntervalSec int, logger *slog.Logger) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			logger.Warn("pragma failed", "pragma", pragma, "err", err)
		}
	}
	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema init: %w", err)
	}
	if bufSize <= 0 {
		bufSize = 5000
	}
	if flushSize <= 0 {
		flushSize = 1000
	}
	if flushIntervalSec <= 0 {
		flushIntervalSec = 60
	}
	return &Store{
		db:            db,
		buf:           make(chan LogEvent, bufSize),
		flushSignal:   make(chan struct{}, 1),
		flushInterval: time.Duration(flushIntervalSec) * time.Second,
		flushSize:     flushSize,
		logger:        logger,
		stop:          make(chan struct{}),
	}, nil
}

// FlushNow asks the writer goroutine to flush any buffered events to SQLite
// at its next opportunity. Non-blocking: if a flush is already queued it is a
// no-op. Useful for dashboards that want to see the latest data immediately.
func (s *Store) FlushNow() {
	select {
	case s.flushSignal <- struct{}{}:
	default:
	}
}

func (s *Store) Run() {
	s.wg.Add(1)
	go s.writer()
}

func (s *Store) Ingest(e LogEvent) bool {
	select {
	case s.buf <- e:
		s.bufferDepth.Add(1)
		return true
	default:
		s.droppedEvents.Add(1)
		s.logger.Warn("buffer full, dropping event", "host", e.Host, "source", e.Source)
		return false
	}
}

func (s *Store) writer() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	batch := make([]LogEvent, 0, s.flushSize)
	for {
		select {
		case <-s.stop:
			batch = s.drainInto(batch)
			if len(batch) > 0 {
				s.flush(batch)
			}
			return
		case <-ticker.C:
			batch = s.drainInto(batch)
			if len(batch) > 0 {
				s.flush(batch)
				batch = batch[:0]
			}
		case <-s.flushSignal:
			batch = s.drainInto(batch)
			if len(batch) > 0 {
				s.flush(batch)
				batch = batch[:0]
			}
		case e := <-s.buf:
			s.bufferDepth.Add(-1)
			batch = append(batch, e)
			if len(batch) >= s.flushSize {
				s.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (s *Store) drainInto(batch []LogEvent) []LogEvent {
	for {
		select {
		case e := <-s.buf:
			s.bufferDepth.Add(-1)
			batch = append(batch, e)
		default:
			return batch
		}
	}
}

func (s *Store) flush(batch []LogEvent) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.Error("tx begin", "err", err)
		return
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO log_events (timestamp, host, severity, facility, message, raw_log, source, source_file) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		s.logger.Error("prepare insert", "err", err)
		return
	}
	var written int64
	for _, e := range batch {
		if _, err := stmt.ExecContext(ctx, e.Timestamp, e.Host, e.Severity, e.Facility, e.Message, e.RawLog, e.Source, e.SourceFile); err != nil {
			s.logger.Error("row insert", "err", err)
			continue
		}
		written++
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		s.logger.Error("commit", "err", err)
		return
	}
	s.totalWritten.Add(written)
	s.logger.Debug("flushed batch", "count", written)
}

func (s *Store) Stop() {
	s.stopOnce.Do(func() {
		close(s.stop)
	})
	s.wg.Wait()
	s.db.Close()
}

func (s *Store) DroppedEvents() int64 { return s.droppedEvents.Load() }
func (s *Store) BufferDepth() int64   { return s.bufferDepth.Load() }
func (s *Store) TotalWritten() int64  { return s.totalWritten.Load() }

type LogFilter struct {
	Host       string
	Severity   *int
	Source     string
	SourceFile string
	Query      string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

func (s *Store) QueryLogs(ctx context.Context, f LogFilter) ([]LogEvent, int, error) {
	var where []string
	var args []any
	if f.Host != "" {
		where = append(where, "host = ?")
		args = append(args, f.Host)
	}
	if f.Severity != nil {
		where = append(where, "severity = ?")
		args = append(args, *f.Severity)
	}
	if f.Source != "" {
		where = append(where, "source = ?")
		args = append(args, f.Source)
	}
	if f.SourceFile != "" {
		where = append(where, "source_file = ?")
		args = append(args, f.SourceFile)
	}
	if f.Query != "" {
		where = append(where, "message LIKE ?")
		args = append(args, "%"+f.Query+"%")
	}
	if f.From != nil {
		where = append(where, "timestamp >= ?")
		args = append(args, *f.From)
	}
	if f.To != nil {
		where = append(where, "timestamp <= ?")
		args = append(args, *f.To)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM log_events"+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	q := "SELECT id, timestamp, host, severity, facility, message, raw_log, source, COALESCE(source_file, '') FROM log_events" + whereSQL + " ORDER BY timestamp DESC LIMIT ? OFFSET ?"
	qArgs := append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, qArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var logs []LogEvent
	for rows.Next() {
		var e LogEvent
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Host, &e.Severity, &e.Facility, &e.Message, &e.RawLog, &e.Source, &e.SourceFile); err != nil {
			return nil, 0, fmt.Errorf("scan: %w", err)
		}
		logs = append(logs, e)
	}
	return logs, total, rows.Err()
}

type HostCount struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

type Stats struct {
	TopHosts         []HostCount `json:"top_hosts"`
	EventsLastMinute int         `json:"events_last_minute"`
	TotalEvents      int         `json:"total_events"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var out Stats
	rows, err := s.db.QueryContext(ctx, `SELECT host, COUNT(*) FROM log_events GROUP BY host ORDER BY COUNT(*) DESC LIMIT 10`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var h HostCount
		if err := rows.Scan(&h.Host, &h.Count); err != nil {
			return out, err
		}
		out.TopHosts = append(out.TopHosts, h)
	}
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_events WHERE timestamp >= ?`, time.Now().Add(-1*time.Minute)).Scan(&out.EventsLastMinute)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_events`).Scan(&out.TotalEvents)
	return out, nil
}

func (s *Store) DBSize() int64 {
	var pageCount, pageSize int64
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0
	}
	if err := s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0
	}
	return pageCount * pageSize
}
