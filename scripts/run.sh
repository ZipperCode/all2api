#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CONFIG_FILE="${CONFIG_FILE:-config.yaml}"

random_key() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
    return
  fi
  if [ -r /proc/sys/kernel/random/uuid ]; then
    tr -d '-' </proc/sys/kernel/random/uuid
    return
  fi
  date +%s%N
}

ensure_config() {
  if [ -f "$CONFIG_FILE" ]; then
    echo "Using existing $CONFIG_FILE"
    return
  fi

  local admin_key
  admin_key="$(random_key)"
  cp config.example.yaml "$CONFIG_FILE"
  perl -0pi -e "s/admin_key: \"change-this-admin-key\"/admin_key: \"$admin_key\"/" "$CONFIG_FILE"
  chmod 600 "$CONFIG_FILE"
  mkdir -p logs

  echo "Created $CONFIG_FILE"
  echo "Admin key: $admin_key"
  echo "Edit $CONFIG_FILE and replace placeholder upstream keys before real forwarding."
}

ensure_config

config_port="$(
  awk '
    /^server:/ { in_server=1; next }
    in_server && /^[^[:space:]]/ { in_server=0 }
    in_server && /^[[:space:]]*port:/ {
      gsub(/"/, "", $2)
      print $2
      exit
    }
  ' "$CONFIG_FILE"
)"
PORT="${GW_SERVER_PORT:-${config_port:-8080}}"

if grep -q "your_real_\\|your_weather_api_key\\|change-this-admin-key" "$CONFIG_FILE"; then
  echo "Warning: $CONFIG_FILE still contains placeholder keys."
fi

echo "Starting all2api on http://127.0.0.1:${PORT}"
echo "Admin UI: http://127.0.0.1:${PORT}/__admin/"

exec go run ./cmd/gateway -config "$CONFIG_FILE"
