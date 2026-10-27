#!/usr/bin/env bash
# Сквозной сценарий через Docker Compose: отдельный стек (своё имя проекта, порты и секреты),
# сборка образа, миграции, тест tests/e2e, затем удаление стека вместе с данными.
# E2E_KEEP=1 оставляет стек после прогона для ручной проверки.
set -euo pipefail

cd "$(dirname "$0")/.."
PROJECT="${E2E_PROJECT:-team-tasks-e2e}"
API_PORT="${E2E_API_PORT:-18090}"
ENV_FILE="$(mktemp)"

secret() { head -c 48 /dev/urandom | base64 | tr -d '\n/+='; }

# Лимиты заданы явно: сценарий не должен зависеть от значений по умолчанию.
cat >"$ENV_FILE" <<EOF
MYSQL_ROOT_PASSWORD=$(secret)
MYSQL_DATABASE=tasks
MYSQL_USER=tasks
MYSQL_PASSWORD=$(secret)
JWT_SECRET=$(secret)
CURSOR_SECRET=$(secret)
MYSQL_HOST=127.0.0.1
MYSQL_PORT=${E2E_MYSQL_PORT:-13307}
REDIS_HOST=127.0.0.1
REDIS_PORT=${E2E_REDIS_PORT:-16381}
API_PORT=${API_PORT}
RATE_USER_PER_MINUTE=6000
RATE_USER_BURST=1000
RATE_AUTH_PER_MINUTE=6000
RATE_AUTH_BURST=1000
EOF

compose() { docker compose -p "$PROJECT" --env-file "$ENV_FILE" "$@"; }
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    compose logs --no-color --tail=50 api migrate || true
  fi
  if [ "${E2E_KEEP:-0}" != "1" ]; then
    compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -f "$ENV_FILE"
  exit "$status"
}
trap cleanup EXIT

compose up -d --build --wait
E2E_BASE_URL="http://127.0.0.1:${API_PORT}" go test -tags e2e -count=1 ./tests/e2e/...
