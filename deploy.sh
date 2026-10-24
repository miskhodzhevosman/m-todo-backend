#!/usr/bin/env bash
set -Eeuo pipefail

# Run as the repository owner; sudo is used only for system installation.
ROOT_DIR="$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")"
if [[ "${1:-}" == "--help" ]]; then
    printf 'Usage: [HEALTHCHECK_URL=http://127.0.0.1:8000/categories] ./deploy.sh\nBuild and deploy to /opt/m-todo-backend with a systemd service.\nRequires Go 1.23+, sudo, and Debian/Ubuntu with systemd.\n'
    exit 0
fi
[[ $# -eq 0 ]] || { printf 'Unknown argument. Use --help.\n' >&2; exit 1; }
for command in go sudo systemctl apt-get curl; do
    command -v "$command" >/dev/null || { printf 'Required command: %s\n' "$command" >&2; exit 1; }
done
sudo -v

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
go -C "$ROOT_DIR" test ./...
CGO_ENABLED=0 go -C "$ROOT_DIR" build -trimpath -o "$BUILD_DIR/m-todo-backend" .

sudo apt-get update
sudo apt-get install -y sqlite3
if ! id m-todo-backend >/dev/null 2>&1; then
    sudo useradd --system --user-group --home-dir /var/lib/m-todo-backend --shell /usr/sbin/nologin m-todo-backend
fi
sudo install -d -m 0755 /opt/m-todo-backend
sudo install -d -o m-todo-backend -g m-todo-backend -m 0750 /var/lib/m-todo-backend

# Keep the deployed configuration and data on subsequent deployments.
if ! sudo test -e /var/lib/m-todo-backend/.env; then
    sudo install -o root -g m-todo-backend -m 0640 "$ROOT_DIR/.env.example" /var/lib/m-todo-backend/.env
fi
if ! sudo test -e /var/lib/m-todo-backend/todos.db && [[ -f "$ROOT_DIR/todos.db" ]]; then
    # SQLite backup also captures committed data in a possible WAL file.
    sqlite3 "$ROOT_DIR/todos.db" ".backup '$BUILD_DIR/todos.db'"
    sudo install -o m-todo-backend -g m-todo-backend -m 0640 "$BUILD_DIR/todos.db" /var/lib/m-todo-backend/todos.db
fi

# Replace the executable atomically, including while the old process is running.
sudo install -m 0755 "$BUILD_DIR/m-todo-backend" /opt/m-todo-backend/m-todo-backend.new
sudo mv -f /opt/m-todo-backend/m-todo-backend.new /opt/m-todo-backend/m-todo-backend
sudo tee /etc/systemd/system/m-todo-backend.service >/dev/null <<'UNIT'
[Unit]
Description=M-Todo Go API
After=network.target

[Service]
Type=simple
User=m-todo-backend
Group=m-todo-backend
WorkingDirectory=/var/lib/m-todo-backend
ExecStart=/opt/m-todo-backend/m-todo-backend
Restart=on-failure
RestartSec=3
TimeoutStopSec=15
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/m-todo-backend

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable m-todo-backend.service
sudo systemctl restart m-todo-backend.service

curl --fail --silent --show-error --retry 10 --retry-delay 1 --retry-connrefused \
    "${HEALTHCHECK_URL:-http://127.0.0.1:8000/categories}" >/dev/null
sudo systemctl is-active --quiet m-todo-backend.service
printf 'Backend deployed. Config: /var/lib/m-todo-backend/.env\nLogs: sudo journalctl -u m-todo-backend -f\n'
