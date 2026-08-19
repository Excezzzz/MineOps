param (
    [Parameter(Mandatory=$true)]
    [string]$Target
)

Write-Host "[MineOps] 🔨 Шаг 0/5: Кросс-компиляция бинарника (linux/amd64)..." -ForegroundColor Cyan
$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = "amd64"
New-Item -ItemType Directory -Force -Path bin | Out-Null
go build -trimpath -ldflags="-s -w" -o bin/mineops ./cmd/bot
if ($LASTEXITCODE -ne 0) { throw "go build failed" }
Write-Host "[MineOps] ✅ Бинарник bin/mineops собран" -ForegroundColor Green

Write-Host "[MineOps] 🚀 Шаг 1/5: Подготовка сервера и упаковка файлов..." -ForegroundColor Cyan
ssh $Target "mkdir -p ~/mineops"
# Упаковываем локально во временный файл (.env и data/ остаются на сервере!)
tar -czf deploy.tar.gz --exclude=data --exclude=.git --exclude=venv --exclude=__pycache__ --exclude=deploy.tar.gz .

Write-Host "[MineOps] 🧹 Шаг 2/5: Очистка старого кода на $Target (сохраняются data/ и .env)..." -ForegroundColor Cyan
# ВАЖНО: без переменной $DC и двойных кавычек в remote-команде —
# PS 5.1 + Windows ssh.exe их искажают (проверено тестами).
ssh $Target 'mkdir -p ~/mineops && find ~/mineops -mindepth 1 -maxdepth 1 ! -name data ! -name .env -exec rm -rf {} +'

Write-Host "[MineOps] 🚀 Шаг 3/5: Отправка файлов на $Target..." -ForegroundColor Cyan
scp deploy.tar.gz "$Target`:~/mineops/"

Write-Host "[MineOps] 🚀 Шаг 4/5: Распаковка и запуск Docker Compose..." -ForegroundColor Cyan
# Распаковка и запуск Go-версии (кэш python-aternos больше не создаётся,
# но на всякий случай чистим старый файл — он в data/, владелец root).
ssh $Target 'cd ~/mineops && (rm -f data/aternos_session.json || docker exec mineops_bot rm -f /app/data/aternos_session.json) && tar -xzf deploy.tar.gz && rm deploy.tar.gz && if docker compose version >/dev/null 2>&1; then docker compose up -d --build; else docker-compose up -d --build; fi'

Write-Host "[MineOps] 🧹 Очистка временных файлов..." -ForegroundColor Cyan
Remove-Item deploy.tar.gz
Remove-Item -Recurse -Force bin

Write-Host "[MineOps] ✅ Деплой успешно завершен!" -ForegroundColor Green