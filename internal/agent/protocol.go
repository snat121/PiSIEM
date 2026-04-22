// Package agent defines the shared wire format between the PiSIEM server and
// the PiSIEM agent, plus server-side (listener) and agent-side (tailer) logic.
//
// protocol.go is the single source of truth for AgentEvent — both binaries
// import it so there is no duplication.
package agent

import "time"

// AgentEvent is one newline-delimited JSON message shipped from an agent to
// the server over TCP.
type AgentEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Host       string    `json:"host"`
	SourceFile string    `json:"source_file"`
	Message    string    `json:"message"`
	Severity   int       `json:"severity"`
}
