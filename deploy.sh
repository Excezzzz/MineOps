#!/usr/bin/env bash
# MineOps deployment (Linux/macOS).
# Usage: ./deploy.sh user@host     e.g. ./deploy.sh admin@144.31.73.85
#
# Workflow: git init -> gh push (collision-free name) -> ssh mkdir -> scp files
#   -> ssh rebuild (auto-detects docker compose v2 / docker-compose v1)
set -euo pipefail

REPO_PREFIX="${REPO_PREFIX:-MineOps}"
GH_VISIBILITY="${GH_VISIBILITY:-private}"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)

FILES=(main.py database.py keyboards.py aternos_service.py requirements.txt \
       Dockerfile docker-compose.yml)

cd "$(dirname "$0")"

log()  { printf '\033[1;34m[MineOps]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[MineOps]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[MineOps]\033[0m %s\n' "$*" >&2; exit 1; }

[ $# -ge 1 ] || {
    cat <<'EOF'
Usage: ./deploy.sh user@host
Example: ./deploy.sh admin@144.31.73.85
EOF
    exit 1
}

TARGET="$1"
REMOTE_HOST="${TARGET##*@}"
case "$TARGET" in
    *@*) [ -n "$REMOTE_HOST" ] || die "missing host in '$1'";;
    *)   die "invalid target '$1' - expected 'user@host'";;
esac

# 1. local git
if [ ! -d .git ]; then
    log "initializing git repository"
    git init -b main >/dev/null
fi
git add -A
if ! git diff --cached --quiet; then
    git commit -m "MineOps: auto-deploy $(date -u +'%Y-%m-%dT%H:%M:%SZ')" >/dev/null
    log "committed changes"
fi

# 2. GitHub push
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    OWNER="$(gh api user --jq .login)"
    repo_exists() { gh repo view "$OWNER/$1" >/dev/null 2>&1; }

    repo_name="$REPO_PREFIX"
    if repo_exists "$repo_name"; then
        repo_name="${REPO_PREFIX}-Bot"
        n=2
        while repo_exists "$repo_name"; do
            repo_name="${REPO_PREFIX}-Bot-${n}"
            n=$((n + 1))
        done
        warn "'$REPO_PREFIX' exists on GitHub - using '$repo_name'"
    fi

    git remote remove origin 2>/dev/null || true
    if repo_exists "$repo_name"; then
        git remote add origin "https://github.com/$OWNER/$repo_name.git"
        git push -u origin main
    else
        gh repo create "$repo_name" --source . --remote origin --push --visibility "$GH_VISIBILITY"
    fi
    log "pushed to https://github.com/$OWNER/$repo_name"
else
    warn "gh unavailable - skipping GitHub push"
fi

# 3. SSH preparation
log "preparing $TARGET:~/MineOps"
ssh "${SSH_OPTS[@]}" "$TARGET" "mkdir -p ~/MineOps" || die "SSH to '$TARGET' failed"

# 4. transfer
log "transferring files"
scp "${SSH_OPTS[@]}" "${FILES[@]}" "$TARGET":~/MineOps/
if [ -f .env ]; then
    # Preserve a working remote .env: never truncate live credentials with
    # local placeholders. Force-copy with DEPLOY_FORCE_ENV=1 when intended.
    if [ "${DEPLOY_FORCE_ENV:-0}" = "1" ]; then
        scp "${SSH_OPTS[@]}" .env "$TARGET":~/MineOps/
        log "copied local .env (DEPLOY_FORCE_ENV=1)"
    elif ssh "${SSH_OPTS[@]}" "$TARGET" "test -f ~/MineOps/.env"; then
        warn "remote ~/MineOps/.env already exists - keeping it (DEPLOY_FORCE_ENV=1 to overwrite)"
    else
        scp "${SSH_OPTS[@]}" .env "$TARGET":~/MineOps/
    fi
else
    warn "no local .env - create one in ~/MineOps on the server before the first run"
fi

# 5. rebuild
log "rebuilding containers"
ssh "${SSH_OPTS[@]}" "$TARGET" 'cd ~/MineOps && if docker compose version >/dev/null 2>&1; then docker compose down && docker compose up -d --build; elif command -v docker-compose >/dev/null 2>&1; then docker-compose down && docker-compose up -d --build; else echo "Error: Docker Compose not found" >&2; exit 1; fi'

log "done. Logs: ssh $TARGET 'cd ~/MineOps && docker compose logs -f'"