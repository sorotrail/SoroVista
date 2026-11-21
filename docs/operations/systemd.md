# Deploying with systemd

SoroVista ships as a single self-contained Go binary (embedded SQL migrations and HTML templates included), which makes running it directly under `systemd` on a Linux host straightforward when you do not want a container runtime on the machine.

## 1. Create a dedicated system user and install the binary

Never run SoroVista as `root`. Create a dedicated, unprivileged system account with no login shell and no home directory:

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin --user-group sorovista
```

Copy the compiled `sorovista` binary into `/usr/local/bin` owned by `root:root`:

```bash
sudo install -o root -g root -m 0755 bin/sorovista /usr/local/bin/sorovista
```

Because SoroVista listens on `:8080` by default — an unprivileged port above `1024` — the service needs **no capability grants** (`AmbientCapabilities=CAP_NET_BIND_SERVICE` is unnecessary and should be omitted). Terminate TLS on `:443` in a reverse proxy (Caddy, nginx, or Traefik) and proxy to `127.0.0.1:8080`.

## 2. Configure `/etc/sorovista/sorovista.env`

Keep configuration in `/etc/sorovista/sorovista.env` rather than inline in the unit file. In standalone mode, `DATABASE_URL` contains your Postgres password (and `RPC_URL` may contain an API key), so a world-readable environment file (`0644`) leaks credentials to every local user on the machine. Restrict `/etc/sorovista/sorovista.env` to `0640` owned by `root:sorovista`:

```bash
sudo install -d -o root -g sorovista -m 0750 /etc/sorovista
sudo install -o root -g sorovista -m 0640 /dev/null /etc/sorovista/sorovista.env
```

### Standalone mode (`SOURCE_MODE=rpc`)

Edit the two required connection settings (`DATABASE_URL` and `NETWORK` / `RPC_URL`):

```ini
# /etc/sorovista/sorovista.env (standalone mode)
SOURCE_MODE=rpc
NETWORK=testnet
DATABASE_URL=postgres://sorovista:replace-with-strong-password@127.0.0.1:5432/sorovista?sslmode=disable
HTTP_ADDR=127.0.0.1:8080
LOG_LEVEL=info
```

### Upstream mode (`SOURCE_MODE=sorotrail`)

Upstream mode is stateless and opens no database connection — only `SOURCE_MODE` and `SOROTRAIL_URL` are required:

```ini
# /etc/sorovista/sorovista.env (upstream mode)
SOURCE_MODE=sorotrail
SOROTRAIL_URL=http://127.0.0.1:8081
HTTP_ADDR=127.0.0.1:8080
LOG_LEVEL=info
```

## 3. Install the `systemd` unit file

Write `/etc/systemd/system/sorovista.service`:

```ini
[Unit]
Description=SoroVista Soroban contract event explorer
Documentation=https://github.com/sorotrail/SoroVista
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=sorovista
Group=sorovista
EnvironmentFile=/etc/sorovista/sorovista.env
ExecStart=/usr/local/bin/sorovista

# Graceful shutdown: cmd/sorovista/main.go traps SIGINT and SIGTERM with a
# 10-second drain timeout (shutdownTimeout = 10 * time.Second). Keep the
# default KillSignal=SIGTERM and set TimeoutStopSec above 10s so in-flight
# HTTP requests and the ingestion loop finish cleanly before SIGKILL.
KillSignal=SIGTERM
TimeoutStopSec=15s

# Restart policy: restart on non-zero exits with a 5-second backoff.
Restart=on-failure
RestartSec=5s

# Logging: SoroVista writes structured JSON logs (log/slog) to stdout.
StandardOutput=journal
StandardError=journal
SyslogIdentifier=sorovista

# Sandboxing & hardening
NoNewPrivileges=yes
ProtectSystem=strict
PrivateTmp=yes
ProtectHome=yes
ReadWritePaths=
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes

[Install]
WantedBy=multi-user.target
```

### Ordering for standalone vs upstream mode

- **Standalone mode with local Postgres:** Keep `After=network-online.target postgresql.service` so SoroVista starts after the local database is ready to accept connections and run startup migrations. (If Postgres runs on another host, `After=network-online.target` alone is sufficient.)
- **Upstream mode (`SOURCE_MODE=sorotrail`):** No Postgres instance is used. You can drop `postgresql.service` from `After=` (leaving `After=network-online.target`) or optionally order after `sorotrail.service` if SoroTrail runs on the same machine. Leaving `After=network-online.target postgresql.service` is also harmless on hosts without `postgresql.service` because `After=` is an ordering directive, not a hard `Requires=` dependency.

### Why each hardening directive is there

- **`NoNewPrivileges=yes`** — Prevents the process (or any child) from gaining privileges via `setuid`/`setgid` binaries or file capabilities.
- **`ProtectSystem=strict`** — Mounts the entire filesystem hierarchy read-only for the service, except for `/dev`, `/proc`, and `/sys`.
- **`PrivateTmp=yes`** — Gives the unit its own isolated `/tmp` and `/var/tmp` namespace that disappears when the service stops, preventing temp-file collisions or symlink attacks.
- **`ProtectHome=yes`** — Makes `/home`, `/root`, and `/run/user` inaccessible to the process.
- **`ReadWritePaths=` (empty)** — SoroVista writes nothing to local disk in either mode (standalone mode writes only to Postgres over a socket; upstream mode is completely stateless). Keeping `ReadWritePaths=` empty alongside `ProtectSystem=strict` leaves zero writable directories on the host filesystem.
- **`PrivateDevices=yes`**, **`ProtectKernelTunables=yes`**, **`ProtectControlGroups=yes`**, **`RestrictSUIDSGID=yes`** — Blocks access to physical device nodes, `/proc/sys` kernel tunables, cgroup hierarchies, and creation of setuid/setgid files.

## 4. Enable, start, and verify

Reload `systemd`, enable the unit at boot, and start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now sorovista.service
sudo systemctl status sorovista.service
```

Confirm the HTTP probes respond:

```bash
curl -s http://127.0.0.1:8080/livez
curl -s http://127.0.0.1:8080/readyz
curl -s http://127.0.0.1:8080/health
```

## 5. Reading logs and diagnosing restart loops

SoroVista writes structured JSON records (`time`, `level`, `msg`, `request_id`, etc.) to standard output via Go's `log/slog`, which `systemd-journald` captures automatically.

Tail live logs or filter recent errors with `journalctl`:

```bash
# Follow live logs
sudo journalctl -u sorovista.service -f

# Show only raw JSON log lines from the current boot
sudo journalctl -u sorovista.service -b -o cat
```

> **Startup validation & restart loops:** At startup, `cmd/sorovista/main.go` validates all environment variables for the selected `SOURCE_MODE` and (in standalone mode) pings Postgres, runs migrations, and verifies that the Stellar RPC's network passphrase matches `NETWORK` / `NETWORK_PASSPHRASE`. If any check fails, SoroVista logs a `"startup failed"` JSON entry and exits with status `1`. With `Restart=on-failure`, a persistent restart loop every 5 seconds almost always indicates a configuration error in `/etc/sorovista/sorovista.env` (such as a missing `DATABASE_URL`, a wrong `NETWORK` preset, or an unreachable database) rather than a runtime crash.
