#!/usr/bin/env bash
# Совокупное покрытие (unit + integration + API) и проверка порога для бизнес-пакетов.
#
# Бизнес-логика: internal/auth, internal/team, internal/task, internal/history, internal/statistics — по ним считается
# порог (по умолчанию 90% statement coverage). Остальные internal-пакеты (httpapi, storage, cache,
# platform, config) попадают в отчёт, но метрику бизнес-логики не заменяют и не подменяют.
# Код возврата 1, если покрытие бизнес-пакетов ниже порога или тесты упали.
set -euo pipefail

cd "$(dirname "$0")/.."
THRESHOLD="${COVERAGE_THRESHOLD:-90}"
PROFILE="${COVERAGE_PROFILE:-coverage.out}"
BUSINESS_RE='/internal/(auth|team|task|history|statistics)(/|$)'

# Все пакеты internal, кроме тестовых помощников, измеряются одной командой.
COVERPKG="$(go list ./internal/... | grep -v '/internal/testutil' | paste -sd, -)"

go test -tags "integration api" -count=1 -covermode=atomic -coverpkg="$COVERPKG" -coverprofile="$PROFILE" ./...

# В профиле один и тот же блок встречается по разу на каждый тестовый бинарник: блоки объединяются
# по диапазону (покрыт, если покрыт хотя бы одним набором), иначе инструкции считались бы дважды.
REPORT="$(awk -v re="$BUSINESS_RE" '
  NR == 1 { next }                                   # строка "mode: ..."
  { if (!($1 in st)) st[$1] = $2; cnt[$1] += $3 }
  END {
    for (key in st) {
      split(key, a, ":"); file = a[1]
      pkg = file; sub(/\/[^\/]*$/, "", pkg)
      stmts[pkg] += st[key]
      if (cnt[key] > 0) covered[pkg] += st[key]
    }
    bt = 0; bc = 0; at = 0; ac = 0
    for (p in stmts) {
      pct = stmts[p] ? 100 * covered[p] / stmts[p] : 0
      isb = (("/" p) ~ re)
      printf "%-8s %-62s %6.1f%%  (%d/%d)\n", isb ? "business" : "infra", p, pct, covered[p], stmts[p]
      at += stmts[p]; ac += covered[p]
      if (isb) { bt += stmts[p]; bc += covered[p] }
    }
    printf "BUSINESS_TOTAL %d %d\n", bc, bt
    printf "ALL_TOTAL %d %d\n", ac, at
  }' "$PROFILE")"

echo
echo "=== Покрытие по пакетам (statement coverage, unit + integration + API) ==="
grep -v 'TOTAL' <<<"$REPORT" | sort

read -r _ BC BT < <(grep '^BUSINESS_TOTAL' <<<"$REPORT")
read -r _ AC AT < <(grep '^ALL_TOTAL' <<<"$REPORT")

echo
echo "=== Функции бизнес-пакетов с покрытием ниже 100% (непокрытые участки) ==="
go tool cover -func="$PROFILE" | awk -v re="$BUSINESS_RE" '
  $1 ~ /^total:/ { next }
  { path = $1; sub(/:[0-9]+:$/, "", path); sub(/:[0-9]+:.*/, "", path)
    pct = $NF; gsub("%", "", pct)
    if (("/" path) ~ re && pct + 0 < 100) print }' || true

if [[ "${BT:-0}" -eq 0 ]]; then
  echo
  echo "В бизнес-пакетах нет измеряемых инструкций: порог не может быть подтверждён." >&2
  exit 1
fi

BUSINESS_PCT="$(awk -v c="$BC" -v t="$BT" 'BEGIN { printf "%.1f", 100 * c / t }')"
ALL_PCT="$(awk -v c="$AC" -v t="$AT" 'BEGIN { printf "%.1f", 100 * c / t }')"
echo
echo "Все internal-пакеты:       ${ALL_PCT}% (${AC}/${AT})"
echo "Бизнес-логика (метрика):   ${BUSINESS_PCT}% (${BC}/${BT}), порог ${THRESHOLD}%"

if awk -v p="$BUSINESS_PCT" -v th="$THRESHOLD" 'BEGIN { exit !(p + 0 < th + 0) }'; then
  echo "ОШИБКА: покрытие бизнес-логики ${BUSINESS_PCT}% ниже порога ${THRESHOLD}%" >&2
  exit 1
fi
echo "OK: порог покрытия выполнен"
