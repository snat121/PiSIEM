# PiSIEM

Monolithic, ultra-lightweight SIEM for ARM edge devices (Raspberry Pi 3/4/5) and small servers. Single Go binary, embedded SQLite (`modernc.org/sqlite`, pure Go, no CGO), embedded HTMX dashboard. Runs on Linux (amd64, arm64) and Windows.

Heads up: this project was scaffolded with AI assistance. I am not a strong Go developer, so if you look at the code and think *"this is shite"*, you are probably right.

PRs and forks are welcome. If something is wrong, clunky, or just plain bad:

- **Open an issue** with what you saw and how to reproduce.
- **Send a pull request** — even small improvements (better parsing, cleaner concurrency, tighter queries, more rules, tests) are appreciated.
- **Fork freely** — Apache 2.0 lets you do whatever you want, just keep the license notice.

Any help is appreciated. If you spot a bug, feature gap, or just want to refactor a questionable piece of Go, go for it.


## Features

- **Syslog ingestion** — UDP :514 (RFC 3164/5424 best-effort), optional TCP :514.
- **Native agent** — TCP :5514, newline-delimited JSON, tails files on remote machines and ships events.
- **Rule engine** — YAML-defined substring-match rules with per-host threshold + sliding timeframe, TTL-evicted counters, optional scoping by source / source_file / host.
- **Anomaly detection** — built-in EMA baseline per host, 60s sliding window, configurable spike multiplier.
- **SD card protection** — events buffered in a channel, flushed to SQLite only every 60s OR every 1000 events (single bulk transaction).
- **Webhook alerts** — fire-and-forget POST (Discord-compatible `{"content":"..."}`), 5s timeout.
- **Dashboard** — port 8080, HTMX + Tailwind (CDN), filters on host/severity/source/source_file/text, pagination.
- **Graceful shutdown** — SIGTERM/SIGINT drains the buffer before exit, no data loss.

## Requirements

- Go 1.21 or newer (to build — end users can get pre-built binaries).
- No C toolchain. No external runtime dependencies. Single binary drops onto the target.
- Supported: `linux/amd64`, `linux/arm64`, `windows/amd64`. Agent additionally runs anywhere Go runs.

## Layout

```
pisiem/
├── cmd/
│   ├── server/main.go      # server entry point
│   └── agent/main.go       # agent entry point
├── internal/
│   ├── config/             # YAML loaders (config.yaml, rules.yaml, agent.yaml)
│   ├── syslog/             # UDP/TCP listener + RFC 3164/5424 parser
│   ├── agent/              # shared protocol + server listener + client tailer
│   ├── engine/             # rules, anomaly detector, webhook dispatcher
│   ├── storage/            # SQLite schema, channel buffer, batch writer, query API
│   └── web/                # HTTP server + dashboard handlers
├── ui/                     # go:embed dashboard templates
├── config.yaml             # sample server config
├── agent.yaml              # sample agent config
├── rules.yaml              # sample rules
└── go.mod
```

---

## Build

### Resolve deps (one-time)

```
cd C:\prog\mystuff\pisiem
go mod tidy
```

### Build for the current machine

Linux / macOS:
```
go build -o pisiem       ./cmd/server
go build -o pisiem-agent ./cmd/agent
```

Windows (PowerShell or cmd):
```
go build -o pisiem.exe       .\cmd\server
go build -o pisiem-agent.exe .\cmd\agent
```

### Cross-compile matrix (from any dev box, no CGO needed)

Bash / zsh:
```
GOOS=linux   GOARCH=amd64 go build -o pisiem-linux-amd64        ./cmd/server
GOOS=linux   GOARCH=arm64 go build -o pisiem-linux-arm64        ./cmd/server
GOOS=windows GOARCH=amd64 go build -o pisiem-windows-amd64.exe  ./cmd/server

GOOS=linux   GOARCH=amd64 go build -o pisiem-agent-linux-amd64       ./cmd/agent
GOOS=linux   GOARCH=arm64 go build -o pisiem-agent-linux-arm64       ./cmd/agent
GOOS=windows GOARCH=amd64 go build -o pisiem-agent-windows-amd64.exe ./cmd/agent
```

PowerShell (set env per command):
```
$env:GOOS="linux";   $env:GOARCH="arm64"; go build -o pisiem-linux-arm64 .\cmd\server
$env:GOOS="windows"; $env:GOARCH="amd64"; go build -o pisiem.exe         .\cmd\server
Remove-Item Env:GOOS; Remove-Item Env:GOARCH
```

---

## Install — Linux (Raspberry Pi / server)

### 1. Copy binary + configs

```
scp pisiem-linux-arm64 pi@<pi-ip>:/home/pi/pisiem
scp config.yaml rules.yaml pi@<pi-ip>:/home/pi/
ssh pi@<pi-ip>
chmod +x /home/pi/pisiem
```

### 2. Allow bind to port 514 without root

```
sudo setcap 'cap_net_bind_service=+ep' /home/pi/pisiem
```

Or change `syslog_udp_port` / `syslog_tcp_port` to a non-privileged port (>1024).

### 3. Run manually

```
/home/pi/pisiem -config /home/pi/config.yaml
```

### 4. Install as a systemd service

Create `/etc/systemd/system/pisiem.service`:

```ini
[Unit]
Description=PiSIEM
After=network.target

[Service]
ExecStart=/home/pi/pisiem -config /home/pi/config.yaml
WorkingDirectory=/home/pi
Restart=always
RestartSec=5
User=pi
AmbientCapabilities=CAP_NET_BIND_SERVICE
StandardOutput=append:/var/log/pisiem.log
StandardError=append:/var/log/pisiem.log

[Install]
WantedBy=multi-user.target
```

Enable:
```
sudo systemctl daemon-reload
sudo systemctl enable --now pisiem
sudo systemctl status pisiem
```

---

## Install — Windows (server)

PiSIEM runs as a native Windows service. Ports 514 and 5514 both require Administrator to bind (514 is privileged, 5514 is just firewall).

### 1. Copy files

Pick an install directory, e.g. `C:\pisiem\`:

```
C:\pisiem\
├── pisiem.exe
├── config.yaml
└── rules.yaml
```

### 2. Open firewall ports

Open an **elevated** PowerShell and run:

```powershell
New-NetFirewallRule -DisplayName "PiSIEM Syslog UDP"  -Direction Inbound -Action Allow -Protocol UDP -LocalPort 514
New-NetFirewallRule -DisplayName "PiSIEM Syslog TCP"  -Direction Inbound -Action Allow -Protocol TCP -LocalPort 514
New-NetFirewallRule -DisplayName "PiSIEM Agent TCP"   -Direction Inbound -Action Allow -Protocol TCP -LocalPort 5514
New-NetFirewallRule -DisplayName "PiSIEM Dashboard"   -Direction Inbound -Action Allow -Protocol TCP -LocalPort 8080
```

(If you're running on a LAN only, limit with `-RemoteAddress LocalSubnet`.)

### 3. Run manually for a first smoke test

In the same elevated PowerShell:

```powershell
cd C:\pisiem
.\pisiem.exe -config config.yaml
```

Expect:
```
level=INFO msg="pisiem starting" config=config.yaml
level=INFO msg="syslog UDP listening" port=514
level=INFO msg="agent listener listening" port=5514
level=INFO msg="web server listening" addr=:8080
```

Open `http://localhost:8080/`. Ctrl+C to stop.

### 4. Install as a Windows service

PiSIEM has no built-in service wrapper. Use **NSSM** (the Non-Sucking Service Manager — open source, free): <https://nssm.cc>.

Download `nssm.exe` and put it on `PATH` (or just in `C:\pisiem\`). From an elevated PowerShell:

```powershell
cd C:\pisiem
.\nssm.exe install PiSIEM "C:\pisiem\pisiem.exe" "-config C:\pisiem\config.yaml"
.\nssm.exe set PiSIEM AppDirectory "C:\pisiem"
.\nssm.exe set PiSIEM AppStdout    "C:\pisiem\pisiem.log"
.\nssm.exe set PiSIEM AppStderr    "C:\pisiem\pisiem.log"
.\nssm.exe set PiSIEM Start        SERVICE_AUTO_START
.\nssm.exe set PiSIEM ObjectName   LocalSystem
Start-Service PiSIEM
Get-Service   PiSIEM
```

Uninstall:
```powershell
Stop-Service PiSIEM
.\nssm.exe remove PiSIEM confirm
```

### 5. (Optional) Alternative without NSSM — `sc.exe`

`sc.exe create` works but PiSIEM isn't a SCM-aware service, so Windows will complain that "the service did not respond" after ~30s. NSSM wraps any EXE as a proper service — recommended.

### 6. Path conventions on Windows

Use **forward slashes** or double-backslash in YAML paths:

```yaml
db_path: "C:/pisiem/pisiem.db"
rules_path: "C:/pisiem/rules.yaml"
```

---

## Configuration reference

### `config.yaml` — server

All fields with their types, defaults, and effects:

| Field | Type | Default | Description |
|---|---|---|---|
| `syslog_udp_port` | int | `514` | UDP port for syslog ingestion. Privileged on Linux (<1024); needs `setcap` or root. |
| `syslog_tcp_port` | int | `514` | TCP port used only if `enable_tcp_syslog: true`. Same privilege rule. |
| `enable_tcp_syslog` | bool | `false` | Enable TCP syslog listener (for pfSense, OPNsense, and other devices that prefer reliable delivery). |
| `web_port` | int | `8080` | HTTP dashboard + JSON API port. |
| `db_path` | string | `./pisiem.db` | SQLite file path. Created if absent. WAL mode is enabled automatically. |
| `rules_path` | string | `./rules.yaml` | Path to rule definitions. Missing file is warned but not fatal. |
| `buffer_flush_interval_seconds` | int | `60` | Max seconds between DB writes. Lower = fresher dashboard, higher SD wear. |
| `buffer_flush_size` | int | `1000` | Events pending in the channel that force an early flush. Lower = fresher, higher = fewer DB writes. |
| `enable_anomaly_detection` | bool | `true` | Turn on the EMA-based per-host volume anomaly detector. |
| `anomaly_multiplier` | float | `10` | Fire when a host's current 60s event count exceeds `multiplier × baseline_EMA`. Higher = less sensitive. |
| `anomaly_webhook_url` | string | `""` | If non-empty, anomaly alerts POST here (Discord-compatible). Empty = log-only. |
| `enable_agent_listener` | bool | `true` | Start the TCP :5514 agent listener. |
| `agent_port` | int | `5514` | Port for the agent listener. |
| `log_level` | string | `info` | One of `debug`, `info`, `warn`, `error`. Uses `log/slog`. |

**Choosing flush tuning:**
- Tight-budget Pi on SD card → keep defaults (60s / 1000 events) or raise to 120s / 2000.
- NVMe-backed server → lower (e.g. 10s / 200) for near-real-time dashboard freshness without wear concerns.

**`buffer_depth`** can be watched at `/api/health`. If it grows steadily, the DB writer can't keep up with ingestion — increase `buffer_flush_size` and interval, or move to faster storage.

### `rules.yaml` — detection signatures

One `rules:` list, each item is a Rule.

```yaml
rules:
  - id: ssh_brute_force              # unique string, used in logs
    name: "SSH Failed Login Spike"   # human-friendly name
    condition:
      match: "Failed password"       # substring matched against event.message (case-sensitive)
      threshold: 5                   # trigger if matched N times...
      timeframe: 60                  # ...within this many seconds, per host
      source: "syslog"               # optional: "syslog" | "agent" | "" (any)
      source_file: ""                # optional: substring of source_file (agent only)
      host: ""                       # optional: substring of host / IP
    action:
      webhook_url: "https://discord.com/api/webhooks/..."
      message_template: "Alert: {{.count}} failed SSH logins from {{.host}}"
```

| Field | Type | Default | Required | Description |
|---|---|---|---|---|
| `id` | string | — | yes | Identifier used in log output. |
| `name` | string | — | yes | Human name shown in logs. |
| `condition.match` | string | — | yes | Substring to look for in `event.message`. |
| `condition.threshold` | int | — | yes | Number of matches that fires the rule. |
| `condition.timeframe` | int | — | yes | Sliding window in seconds; per host. |
| `condition.source` | string | `""` | no | Restrict to `"syslog"` or `"agent"`. Empty = any. |
| `condition.source_file` | string | `""` | no | Substring of `source_file`. Useful for agent rules (`"access.log"`, `"Security"`). |
| `condition.host` | string | `""` | no | Substring of hostname/IP — scope a rule to specific boxes. |
| `action.webhook_url` | string | `""` | no | Fire-and-forget POST on trigger. Empty = log-only (still appears in the server log). |
| `action.message_template` | string | — | yes | Go `text/template` body. Variables: `{{.host}}`, `{{.count}}`, `{{.message}}`, `{{.severity}}`, `{{.source}}`. |

**Scoping filters are ANDed.** Example: only alert on web SQL injection attempts seen from nginx hosts:

```yaml
condition:
  match: "UNION SELECT"
  threshold: 1
  timeframe: 60
  source: "agent"
  source_file: "nginx/access.log"
```

**Rules reload:** rules are read at startup. Restart PiSIEM (`systemctl restart pisiem` / `Restart-Service PiSIEM`) after editing.

**Shipped catalogue** (see `rules.yaml`): SSH (brute force, invalid user, login, root, too-many-fail), sudo / PAM / su, user/group changes, kernel (OOM, segfault, AppArmor), systemd unit failure, cron, APT/dpkg install, UFW/pfSense, web 5xx/401 burst + SQLi + path traversal (agent on `access.log`), Linux `auth.log` agent tails, Windows Security events (4625, 4720, 4724, 4732, 1102 via agent reading a text export), generic `TEST_ALERT`.

### `agent.yaml` — per-machine agent config

```yaml
server_host: "192.168.1.100"        # PiSIEM server address (required)
server_port: 5514                   # server agent-listener port

watch_files:
  - path: "/var/log/auth.log"
    severity: 4                     # 0..7 (syslog severity levels)
  - path: "C:/pisiem/SecurityExport.log"
    severity: 3

batch_size: 100                     # max events per network write
batch_interval_seconds: 5           # flush partial batch after this many seconds
reconnect_max_interval_seconds: 60  # cap for exponential backoff on disconnect
local_buffer_max_events: 10000      # ring buffer while the server is down
```

| Field | Type | Default | Description |
|---|---|---|---|
| `server_host` | string | — | **Required.** Hostname / IP of the PiSIEM server. |
| `server_port` | int | `5514` | Server's `agent_port`. |
| `watch_files[].path` | string | — | Path to tail. Forward slashes work on Windows. Files are tracked by seek offset; truncation/rotation resets to 0. |
| `watch_files[].severity` | int | — | Stamped onto every event from this file (0=emerg, 7=debug). |
| `batch_size` | int | `100` | Flush when N events accumulated. |
| `batch_interval_seconds` | int | `5` | Flush any buffered events after this interval. |
| `reconnect_max_interval_seconds` | int | `60` | Upper bound for exponential backoff when the server is unreachable. |
| `local_buffer_max_events` | int | `10000` | Ring buffer size while disconnected. Oldest events drop when full. |

**Agent does NOT persist offsets across restarts** — it starts at current EOF for each file, so events written while the agent was down are skipped. Acceptable for most security use cases where live tailing is sufficient.

**Windows Event Log note:** `.evtx` is binary. Point the agent at a plain-text export produced by a scheduled task:

```powershell
# one-time setup — create C:\pisiem first
schtasks /Create /SC MINUTE /MO 5 /TN "PiSIEM-SecurityExport" /RU SYSTEM /TR `
 "cmd /c wevtutil qe Security /f:text /c:200 /rd:true >> C:\pisiem\SecurityExport.log"
```

Then in `agent.yaml`:

```yaml
  - path: "C:/pisiem/SecurityExport.log"
    severity: 3
```

Rules scoped with `source_file: "Security"` will match.

---

## Run

### Linux

```
./pisiem -config config.yaml
```

(see systemd unit above for persistent install).

### Windows (manual, elevated PowerShell)

```powershell
cd C:\pisiem
.\pisiem.exe -config config.yaml
```

### Windows (as a service)

See **Install — Windows** section above for NSSM setup.

### Dashboard

Open `http://<server-ip>:8080/` — auto-refreshes every 5s.

---

## Smoke tests

### Linux — send a test syslog UDP packet

```
logger -n 127.0.0.1 -P 514 "TEST_ALERT from test host"
```

### Windows — send a test syslog UDP packet from PowerShell

```powershell
$udp   = New-Object System.Net.Sockets.UdpClient
$bytes = [System.Text.Encoding]::ASCII.GetBytes('<34>Oct 11 22:14:15 winhost pisiem-test: TEST_ALERT from ps')
$udp.Send($bytes, $bytes.Length, "127.0.0.1", 514) | Out-Null
$udp.Close()
```

### Trigger a rule

With the `ssh_brute_force` rule (threshold 5/60s):

```
for i in 1 2 3 4 5; do logger -n 127.0.0.1 -P 514 "sshd: Failed password for root"; done
```

PowerShell equivalent:

```powershell
1..5 | ForEach-Object {
  $u = New-Object System.Net.Sockets.UdpClient
  $b = [System.Text.Encoding]::ASCII.GetBytes('<38>Oct 11 22:14:15 winhost sshd: Failed password for root')
  $u.Send($b, $b.Length, "127.0.0.1", 514) | Out-Null
  $u.Close()
}
```

### Agent-source event via raw netcat on :5514

```
echo '{"timestamp":"2026-04-22T12:00:00Z","host":"mybox","source_file":"/var/log/auth.log","message":"agent test","severity":6}' | nc 127.0.0.1 5514
```

PowerShell:

```powershell
$t = New-Object System.Net.Sockets.TcpClient("127.0.0.1", 5514)
$s = $t.GetStream()
$w = New-Object System.IO.StreamWriter($s); $w.AutoFlush = $true
$w.WriteLine('{"timestamp":"2026-04-22T12:00:00Z","host":"mybox","source_file":"C:/Windows/Security","message":"4625 failed logon","severity":3}')
$t.Close()
```

### Inspect SQLite directly

```
sqlite3 pisiem.db "SELECT id, timestamp, host, source, message FROM log_events ORDER BY id DESC LIMIT 10;"
```

### Health

```
curl -s http://localhost:8080/api/health
```

Returns `uptime_seconds`, `buffer_depth`, `dropped_events`, `total_written`, `db_size_bytes`, `goroutines`.

---

## API

| Route | Method | Description |
|---|---|---|
| `/` | GET | Dashboard HTML |
| `/api/logs` | GET | Paginated, filtered log query (`{total, limit, offset, logs[]}`) |
| `/api/stats` | GET | Top hosts, events/minute, total events |
| `/api/health` | GET | Uptime, buffer depth, dropped events, DB size, goroutines |
| `/api/flush` | POST | Force the storage writer to drain the in-memory buffer to SQLite immediately. Returns `{"ok":true}`. |

`/api/logs` query params: `host`, `severity` (int 0–7), `source` (`syslog` | `agent`), `source_file`, `q` (substring over `message`), `limit` (max 1000, default 100), `offset`, `from` (RFC3339), `to` (RFC3339).

All `/api/*` GET endpoints auto-switch to HTML fragments when HTMX sends `HX-Request: true`, and return JSON otherwise.

### Dashboard refresh behaviour

Events are normally written to SQLite only every 60s (or every 1000 events). Until a flush happens, they are held in RAM and the dashboard cannot see them.

- Click **↻ Refresh** — forces `POST /api/flush` then reloads logs, stats, and health. Use this when you want to immediately verify that an event you just sent was received.
- The **auto** dropdown (Off / 5 min / 10 min / 15 min) schedules a repeating flush-and-refresh. The choice is persisted in `localStorage` so it survives page reloads.
- Submitting the filters form also flushes + reloads.

If you want shorter end-to-end latency without touching the UI, lower `buffer_flush_interval_seconds` and `buffer_flush_size` in `config.yaml` — at the cost of more disk writes.

---

## Operational notes

- **Buffer depth rising** — check `/api/health`. If `dropped_events` grows, SQLite writes are the bottleneck; tune `buffer_flush_size` down, interval up, or move to faster storage.
- **Rule counters** — eviction runs every 30s; no manual maintenance needed.
- **DB retention** — no built-in retention. Rotate / prune:
  ```
  sqlite3 pisiem.db "DELETE FROM log_events WHERE timestamp < datetime('now','-30 days'); VACUUM;"
  ```
  Schedule with cron (Linux) or Task Scheduler (Windows).
- **SD card lifespan** — every event is buffered in RAM; one flush = one transaction ≈ one batched SD write. Keep defaults.
- **Log levels** — `debug` logs every flush; `info` covers starts/stops + rule fires + connects; `warn` and `error` only for real problems.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `listen udp :514: bind: permission denied` (Linux) | Not root, no capability | `sudo setcap 'cap_net_bind_service=+ep' ./pisiem` or use a non-privileged port |
| `listen udp :514: An attempt was made to access a socket in a way forbidden by its access permissions` (Windows) | Not elevated / another service holds port | Run PowerShell as Admin; check `netstat -ano | findstr :514` for conflicts |
| `parse config: yaml: ...` | YAML indent / quoting | Validate YAML syntax (use a linter) |
| Agent logs `dial failed ... retry_in 1s` repeatedly | Server offline, firewall blocks 5514, wrong `server_host` | Verify with `nc <server> 5514` / `Test-NetConnection <server> -Port 5514` |
| Dashboard loads blank or unstyled | Tailwind/HTMX CDN blocked by network policy | Self-host the two scripts (they're small) and patch `ui/templates/index.html` |
| `/api/health` `dropped_events` > 0 | Ingestion outpacing the DB writer | Raise `buffer_flush_size`, move DB to SSD, or reduce ingestion |
| Windows service "service did not respond in a timely fashion" | Used raw `sc.exe create` | Use NSSM (see install section) |

## License

Licensed under the **Apache License, Version 2.0**. See [`LICENSE`](./LICENSE) for the full text.

```
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
```
