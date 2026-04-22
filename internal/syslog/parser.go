package syslog

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/snat121/PiSIEM/internal/storage"
)

// Parse converts a raw syslog datagram into a LogEvent (best-effort RFC 3164/5424).
// The raw string is always preserved in RawLog.
func Parse(raw, remoteIP string) storage.LogEvent {
	e := storage.LogEvent{
		RawLog:    raw,
		Source:    "syslog",
		Timestamp: time.Now(),
		Host:      remoteIP,
		Severity:  6,
		Facility:  1,
		Message:   raw,
	}

	s := strings.TrimRight(raw, "\r\n")
	if !strings.HasPrefix(s, "<") {
		e.Message = s
		return e
	}
	end := strings.IndexByte(s, '>')
	if end < 2 || end > 5 {
		e.Message = s
		return e
	}
	pri, err := strconv.Atoi(s[1:end])
	if err != nil || pri < 0 || pri > 191 {
		e.Message = s
		return e
	}
	e.Facility = pri / 8
	e.Severity = pri % 8
	rest := s[end+1:]

	// RFC 5424: "VERSION TIMESTAMP HOST APP-NAME PROCID MSGID [SD] MSG"
	if len(rest) >= 2 && rest[0] >= '1' && rest[0] <= '9' && rest[1] == ' ' {
		parts := strings.SplitN(rest[2:], " ", 6)
		if len(parts) >= 5 {
			if parts[0] != "-" {
				if t, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
					e.Timestamp = t
				}
			}
			if parts[1] != "-" {
				e.Host = parts[1]
			}
			if len(parts) == 6 {
				e.Message = strings.TrimSpace(parts[5])
			} else {
				e.Message = ""
			}
			return e
		}
	}

	// RFC 3164: "Mmm dd hh:mm:ss HOST TAG: MSG"
	if len(rest) >= 16 {
		ts := rest[:15]
		if t, err := time.Parse(time.Stamp, ts); err == nil {
			now := time.Now()
			e.Timestamp = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local)
			rest = strings.TrimSpace(rest[15:])
		}
	}
	if sp := strings.IndexByte(rest, ' '); sp > 0 {
		candidate := rest[:sp]
		if isHostnameLike(candidate) {
			e.Host = candidate
			rest = rest[sp+1:]
		}
	}
	e.Message = strings.TrimSpace(rest)
	return e
}

func isHostnameLike(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' || r == ':' || r == '_') {
			return false
		}
	}
	return true
}
