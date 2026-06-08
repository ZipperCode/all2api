#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CONFIG_FILE="${CONFIG_FILE:-config.yaml}"
COMPOSE="${COMPOSE:-docker compose}"

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

if [ ! -f "$CONFIG_FILE" ]; then
  admin_key="$(random_key)"
  cp config.example.yaml "$CONFIG_FILE"
  perl -0pi -e "s/admin_key: \"change-this-admin-key\"/admin_key: \"$admin_key\"/" "$CONFIG_FILE"
  chmod 600 "$CONFIG_FILE"
  echo "Created $CONFIG_FILE"
  echo "Admin key: $admin_key"
fi

if grep -q "your_real_\\|your_weather_api_key\\|change-this-admin-key" "$CONFIG_FILE"; then
  echo "Warning: $CONFIG_FILE still contains placeholder keys."
fi

mkdir -p logs

echo "Building and starting all2api with Docker Compose"
$COMPOSE up -d --build

host_port="${ALL2API_PORT:-8080}"
echo "Admin UI: http://127.0.0.1:${host_port}/__admin/"
echo "Logs: docker compose logs -f all2api"
