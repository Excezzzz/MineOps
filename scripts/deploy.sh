#!/usr/bin/env bash
# ============================================================
#  MineOps — deploy.sh (Linux / macOS / Git Bash)
#
#  Копирует проект (включая .env) на VPS в ~/mineops
#  и пересобирает Docker.
#
#  Использование:  ./deploy.sh user@host
#  Пример:         ./deploy.sh admin@144.31.73.85
#
#  Требования: SSH-доступ по ключу, Docker + Compose на сервере.
# ============================================================
set -euo pipefail

usage() {
    cat <<'EOF'

  MineOps — deploy.sh (Linux / macOS / Git Bash)

  Использование:  ./deploy.sh user@host
  Пример:         ./deploy.sh admin@144.31.73.85

EOF
}

log()  { printf '\033[1;36m[MineOps]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[MineOps]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[MineOps]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[MineOps]\033[0m %s\n' "$*" >&2; exit 1; }

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)

# --- проверка аргументов -------------------------------------
if [ $# -ne 1 ]; then
    usage
    die "Ошибка: не передан SSH-адрес сервера (например, admin@144.31.73.85)"
fi
TARGET="$1"
case "$TARGET" in
    *@*[^@]) ;;
    *) die "Некорректный адрес '$TARGET' — ожидается формат user@host" ;;
esac

# --- переходим в корень проекта ------------------------------
cd "$(dirname "$0")/.."

# --- 1. копирование файлов -----------------------------------
log "🚀 Шаг 1/2: Копирование файлов на ${TARGET}:~/mineops ..."

EXCLUDES=(--exclude=data --exclude=venv --exclude=.venv --exclude=.git \
          --exclude=__pycache__ --exclude='*.pyc')

# Очистка старого кода на сервере (сохраняем только data/ и .env),
# чтобы удалённые локально файлы не оставались в контейнере.
ssh "${SSH_OPTS[@]}" "${TARGET}" 'mkdir -p ~/mineops && cd ~/mineops && find ~/mineops -mindepth 1 -maxdepth 1 ! -name data ! -name .env -exec rm -rf {} +'

if command -v rsync >/dev/null 2>&1; then
    rsync -az --delete "${EXCLUDES[@]}" ./ "${TARGET}:~/mineops/"
    ok "✅ rsync: файлы скопированы (исключены: data/, .git/, и т.д.)"
else
    tar -czf - "${EXCLUDES[@]}" . \
        | ssh "${SSH_OPTS[@]}" "${TARGET}" \
              "tar -xzf - -C ~/mineops"
    ok "✅ tar+ssh: файлы скопированы (исключены: data/, .git/, и т.д.)"
fi

# --- 2. сборка и запуск Docker --------------------------------
log "📦 Шаг 2/2: Сборка и запуск Docker на ${TARGET} ..."
ssh "${SSH_OPTS[@]}" "${TARGET}" 'cd ~/mineops && if docker compose version >/dev/null 2>&1; then DC="docker compose"; elif docker-compose version >/dev/null 2>&1; then DC="docker-compose"; else echo "Error: Docker Compose not found" >&2; exit 1; fi && $DC up -d --build'

ok "✅ Готово! Бот пересобран и запущен на ${TARGET}."
log "📖 Логи: ssh ${TARGET} 'cd ~/mineops && docker compose logs -f'"
