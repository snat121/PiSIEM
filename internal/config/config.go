package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	SyslogUDPPort              int     `yaml:"syslog_udp_port"`
	SyslogTCPPort              int     `yaml:"syslog_tcp_port"`
	EnableTCPSyslog            bool    `yaml:"enable_tcp_syslog"`
	WebPort                    int     `yaml:"web_port"`
	DBPath                     string  `yaml:"db_path"`
	RulesPath                  string  `yaml:"rules_path"`
	BufferFlushIntervalSeconds int     `yaml:"buffer_flush_interval_seconds"`
	BufferFlushSize            int     `yaml:"buffer_flush_size"`
	EnableAnomalyDetection     bool    `yaml:"enable_anomaly_detection"`
	AnomalyMultiplier          float64 `yaml:"anomaly_multiplier"`
	AnomalyWebhookURL          string  `yaml:"anomaly_webhook_url"`
	EnableAgentListener        bool    `yaml:"enable_agent_listener"`
	AgentPort                  int     `yaml:"agent_port"`
	LogLevel                   string  `yaml:"log_level"`
}

func defaults() *Config {
	return &Config{
		SyslogUDPPort:              514,
		SyslogTCPPort:              514,
		EnableTCPSyslog:            false,
		WebPort:                    8080,
		DBPath:                     "./pisiem.db",
		RulesPath:                  "./rules.yaml",
		BufferFlushIntervalSeconds: 60,
		BufferFlushSize:            1000,
		EnableAnomalyDetection:     true,
		AnomalyMultiplier:          10,
		EnableAgentListener:        true,
		AgentPort:                  5514,
		LogLevel:                   "info",
	}
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

type RuleCondition struct {
	Match     string `yaml:"match"`
	Threshold int    `yaml:"threshold"`
	Timeframe int    `yaml:"timeframe"`

	// Optional scoping filters — empty string means "match any".
	// All scoping filters are substring matches and are ANDed together.
	Source     string `yaml:"source,omitempty"`      // "syslog" | "agent"
	SourceFile string `yaml:"source_file,omitempty"` // substring of source_file (agent events)
	Host       string `yaml:"host,omitempty"`        // substring of hostname / IP
}

type RuleAction struct {
	WebhookURL      string `yaml:"webhook_url"`
	MessageTemplate string `yaml:"message_template"`
}

type Rule struct {
	ID        string        `yaml:"id"`
	Name      string        `yaml:"name"`
	Condition RuleCondition `yaml:"condition"`
	Action    RuleAction    `yaml:"action"`
}

type ruleFile struct {
	Rules []Rule `yaml:"rules"`
}

func LoadRules(path string) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules %s: %w", path, err)
	}
	var rf ruleFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	return rf.Rules, nil
}

type AgentWatchFile struct {
	Path     string `yaml:"path"`
	Severity int    `yaml:"severity"`
}

type AgentConfig struct {
	ServerHost                  string           `yaml:"server_host"`
	ServerPort                  int              `yaml:"server_port"`
	WatchFiles                  []AgentWatchFile `yaml:"watch_files"`
	BatchSize                   int              `yaml:"batch_size"`
	BatchIntervalSeconds        int              `yaml:"batch_interval_seconds"`
	ReconnectMaxIntervalSeconds int              `yaml:"reconnect_max_interval_seconds"`
	LocalBufferMaxEvents        int              `yaml:"local_buffer_max_events"`
}

func LoadAgent(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent config %s: %w", path, err)
	}
	a := &AgentConfig{
		ServerPort:                  5514,
		BatchSize:                   100,
		BatchIntervalSeconds:        5,
		ReconnectMaxIntervalSeconds: 60,
		LocalBufferMaxEvents:        10000,
	}
	if err := yaml.Unmarshal(data, a); err != nil {
		return nil, fmt.Errorf("parse agent config: %w", err)
	}
	if a.ServerHost == "" {
		return nil, fmt.Errorf("agent config: server_host required")
	}
	return a, nil
}
