# Deploying with systemd

SoroVista ships as one static binary (built with `CGO_ENABLED=0`) and, in upstream mode, needs no database at all. That makes a single systemd service on a small VPS a sensible deployment: one process, one environment file, no container runtime.

The unit below is complete. Copy it to `/etc/systemd/system/sorovista.service`, change the two paths if your binary or environment file live elsewhere, and start it.

## Create an unprivileged user

Never run SoroVista as root. It only needs to read its binary and environment file and open a listening socket above 1024, so a dedicated system account with no shell and no home directory is enough:

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin sorovista
```

## Install the binary

```bash
CGO_ENABLED=0 go build -trimpath -o sorovista ./cmd/sorovista
sudo install -m 0755 sorovista /usr/local/bin/sorovista
```

## Environment file

All configuration is environment variables. Put them in one file rather than scattering `Environment=` lines across the unit:

```bash
sudo install -d -m 0750 /etc/sorovista
sudo install -m 0640 -o root -g sorovista .env /etc/sorovista/sorovista.env
```

The file must not be world-readable: in standalone mode `DATABASE_URL` can carry a Postgres password. Mode `0640`, owned by `root:sorovista`, lets the service read it while keeping it private from every other local account.

A minimal standalone file:

```bash
SOURCE_MODE=rpc
NETWORK=testnet
DATABASE_URL=postgres://sorovista:secret@localhost:5432/sorovista?sslmode=disable
HTTP_ADDR=:8080
LOG_LEVEL=info
# Optional: bound storage. Unset keeps events forever.
# EVENT_RETENTION=2160h
```

A minimal upstream file needs no database:

```bash
SOURCE_MODE=sorotrail
SOROTRAIL_URL=https://your-sorotrail-instance.example.com
HTTP_ADDR=:8080
LOG_LEVEL=info
```

## The unit file

```ini
[Unit]
Description=SoroVista contract event explorer
Documentation=https://github.com/sorotrail/SoroVista
Wants=network-online.target
After=network-online.target
# Standalone mode with a local Postgres also needs the database started first.
# Add this line only when Postgres runs on the same host; a remote database
# needs nothing beyond the network dependency above. Upstream mode never
# needs Postgres at all.
# After=postgresql.service

[Service]
Type=simple
User=sorovista
Group=sorovista
EnvironmentFile=/etc/sorovista/sorovista.env
ExecStart=/usr/local/bin/sorovista

# SoroVista exits non-zero on a configuration error, so a restart loop means
# the environment file is wrong, not that a transient failure should be
# waited out. The backoff still avoids hammering a genuinely transient fault.
Restart=on-failure
RestartSec=5s

# Must exceed the 15s graceful-shutdown timeout in cmd/sorovista/main.go,
# otherwise systemd SIGKILLs in-flight requests after the HTTP server has
# already begun draining them.
TimeoutStopSec=30s

# No ambient capabilities are needed: the default listen address is :8080,
# which is above 1024, so the process can bind it as an ordinary user.
# Resist the urge to add AmbientCapabilities=CAP_NET_BIND_SERVICE.

# --- Hardening -------------------------------------------------------------
# NoNewPrivileges: a process can never gain privileges through setuid/setgid
# binaries or file capabilities, so a compromise cannot escalate inside the
# service.
NoNewPrivileges=true
# ProtectSystem: mounts /usr, /boot and /etc read-only for this service. The
# binary and its environment file stay readable but cannot be modified.
ProtectSystem=strict
# ProtectHome: hides /home, /root and /run/user. SoroVista stores nothing
# there, so there is no reason to expose it.
ProtectHome=true
# PrivateTmp: gives the service its own /tmp and /var/tmp namespaces, so it
# cannot read or race another process's temporary files (and vice versa).
PrivateTmp=true
# ReadWritePaths: deliberately empty. SoroVista keeps no local state — events
# live in Postgres, logs go to stdout/journald — so ProtectSystem=strict can
# make the rest of the filesystem read-only. If you later add a local write
# path, list only that exact directory here rather than weakening the whole
# filesystem.
ReadWritePaths=

# Additional low-cost restrictions: no device nodes, no kernel module or
# sysctl changes, no control-group access, and only the socket families a
# client/server process actually needs.
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=true

[Install]
WantedBy=multi-user.target
```

## Enable and start

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now sorovista
sudo systemctl status sorovista
```

Then check the service answers:

```bash
curl -s http://localhost:8080/health
curl -s http://localhost:8080/api/version
```

`/health` returns 200 when the backend is reachable and 503 when it is not, so it is a useful first check after a restart as well as a container/load-balancer probe.

## Logs

The binary writes structured JSON to stdout, which journald captures without any extra configuration:

```bash
journalctl -u sorovista -f          # follow live
journalctl -u sorovista -n 200      # last 200 lines
journalctl -u sorovista -o cat      # raw JSON, for piping into jq
journalctl -u sorovista --since "1 hour ago" -o cat | jq 'select(.level=="ERROR")'
```

Because each line carries lower_snake_case fields such as `request_id`, `path` and `error`, errors quoted from the UI or an API response can be matched to a log line with `jq`.

## Upgrading

```bash
sudo install -m 0755 sorovista /usr/local/bin/sorovista
sudo systemctl restart sorovista
```

Standalone mode applies pending migrations at startup; back up the database first. Upstream mode has nothing local to migrate.

## When it keeps restarting

A `Restart=on-failure` loop is almost always a configuration problem: SoroVista validates its environment at startup and exits non-zero with a message naming the missing or invalid variable. Read it with `journalctl -u sorovista -n 50 -o cat` before changing the unit.
