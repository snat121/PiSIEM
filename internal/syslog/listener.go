package syslog

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"strconv"

	"github.com/snat121/PiSIEM/internal/storage"
)

type Handler func(storage.LogEvent)

type Listener struct {
	udpPort   int
	tcpPort   int
	enableTCP bool
	handler   Handler
	logger    *slog.Logger

	udpConn *net.UDPConn
	tcpLn   net.Listener
}

func NewListener(udpPort, tcpPort int, enableTCP bool, handler Handler, logger *slog.Logger) *Listener {
	return &Listener{
		udpPort:   udpPort,
		tcpPort:   tcpPort,
		enableTCP: enableTCP,
		handler:   handler,
		logger:    logger,
	}
}

func (l *Listener) Start(ctx context.Context) error {
	addr := net.UDPAddr{Port: l.udpPort, IP: net.IPv4zero}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		return err
	}
	l.udpConn = conn
	l.logger.Info("syslog UDP listening", "port", l.udpPort)
	go l.readUDP(ctx)

	if l.enableTCP {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(l.tcpPort))
		if err != nil {
			l.logger.Error("syslog TCP listen failed", "port", l.tcpPort, "err", err)
		} else {
			l.tcpLn = ln
			l.logger.Info("syslog TCP listening", "port", l.tcpPort)
			go l.acceptTCP(ctx)
		}
	}
	return nil
}

func (l *Listener) readUDP(ctx context.Context) {
	buf := make([]byte, 64*1024)
	for {
		n, src, err := l.udpConn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && !ne.Temporary() {
				l.logger.Warn("udp read ended", "err", err)
				return
			}
			continue
		}
		ip := ""
		if src != nil {
			ip = src.IP.String()
		}
		evt := Parse(string(buf[:n]), ip)
		l.handler(evt)
	}
}

func (l *Listener) acceptTCP(ctx context.Context) {
	for {
		conn, err := l.tcpLn.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			l.logger.Warn("syslog tcp accept ended", "err", err)
			return
		}
		go l.handleTCPConn(conn)
	}
}

func (l *Listener) handleTCPConn(conn net.Conn) {
	defer conn.Close()
	host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		evt := Parse(line, host)
		l.handler(evt)
	}
}

func (l *Listener) Stop() {
	if l.udpConn != nil {
		l.udpConn.Close()
	}
	if l.tcpLn != nil {
		l.tcpLn.Close()
	}
}
