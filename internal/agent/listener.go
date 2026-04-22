package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strconv"
	"time"
)

// EventHandler is invoked for each valid AgentEvent received from a connected
// agent. The handler is expected to be non-blocking (e.g. it enqueues into a
// channel) — a slow handler will backpressure the agent TCP stream.
type EventHandler func(AgentEvent, net.Addr)

type Listener struct {
	port    int
	handler EventHandler
	logger  *slog.Logger
	ln      net.Listener
}

func NewListener(port int, handler EventHandler, logger *slog.Logger) *Listener {
	return &Listener{port: port, handler: handler, logger: logger}
}

func (l *Listener) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(l.port))
	if err != nil {
		return err
	}
	l.ln = ln
	l.logger.Info("agent listener listening", "port", l.port)
	go l.acceptLoop(ctx)
	return nil
}

func (l *Listener) acceptLoop(ctx context.Context) {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			l.logger.Warn("agent accept ended", "err", err)
			return
		}
		go l.handleConn(conn)
	}
}

func (l *Listener) handleConn(conn net.Conn) {
	remote := conn.RemoteAddr()
	l.logger.Info("agent connected", "remote", remote.String())
	defer func() {
		conn.Close()
		l.logger.Info("agent disconnected", "remote", remote.String())
	}()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e AgentEvent
		if err := json.Unmarshal(line, &e); err != nil {
			l.logger.Warn("agent json parse failed", "remote", remote.String(), "err", err)
			continue
		}
		if e.Timestamp.IsZero() {
			e.Timestamp = time.Now()
		}
		l.handler(e, remote)
	}
	if err := sc.Err(); err != nil {
		l.logger.Warn("agent conn read ended", "remote", remote.String(), "err", err)
	}
}

func (l *Listener) Stop() {
	if l.ln != nil {
		l.ln.Close()
	}
}
