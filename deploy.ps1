# MineOps deployment (Windows PowerShell 5.1+).
# Usage: .\deploy.ps1 user@host     e.g. .\deploy.ps1 admin@144.31.73.85
#
# Workflow: git init -> gh push (collision-free name) -> ssh mkdir -> scp files
#   -> ssh rebuild (auto-detects docker compose v2 / docker-compose v1)

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Target
)

$ErrorActionPreference = 'Stop'

function Write-Step([string]$Text) { Write-Host "[MineOps] $Text" -ForegroundColor Cyan }
function Write-Warn([string]$Text) { Write-Host "[MineOps] WARNING: $Text" -ForegroundColor Yellow }

if (-not $Target) {
    Write-Host '[MineOps] Error: SSH target missing.' -ForegroundColor Red
    Write-Host ''
    Write-Host 'Usage: .\deploy.ps1 user@host'
    Write-Host 'Example: .\deploy.ps1 admin@144.31.73.85'
    exit 1
}
if ($Target -notmatch '^[^@]+@[^@]+$') {
    Write-Host "[MineOps] Error: invalid target '$Target' - expected 'user@host'." -ForegroundColor Red
    exit 1
}
$RemoteHost = $Target.Split('@')[-1]
$RepoPrefix = if ($env:REPO_PREFIX) { $env:REPO_PREFIX } else { 'MineOps' }
$Visibility = if ($env:GH_VISIBILITY) { $env:GH_VISIBILITY } else { 'private' }
$Files = @('main.py', 'database.py', 'keyboards.py', 'aternos_service.py',
           'requirements.txt', 'Dockerfile', 'docker-compose.yml')

Set-Location $PSScriptRoot

# 1. local git
Write-Step 'Step 1/5: Git (local)'
if (-not (Test-Path '.git')) {
    git init -b main
    if ($LASTEXITCODE -ne 0) { exit 1 }
}
git add -A
git diff --cached --quiet
if ($LASTEXITCODE -eq 1) {
    git commit -m "MineOps: auto-deploy $(Get-Date -Format o)"
    if ($LASTEXITCODE -ne 0) { exit 1 }
}

# 2. GitHub push
Write-Step 'Step 2/5: GitHub'
if (Get-Command gh -ErrorAction SilentlyContinue) {
    gh auth status *> $null
    if ($LASTEXITCODE -eq 0) {
        $Owner = (gh api user --jq .login).Trim()
        function Test-Repo([string]$Name) {
            gh repo view "$Owner/$Name" *> $null
            return ($LASTEXITCODE -eq 0)
        }
        $RepoName = $RepoPrefix
        if (Test-Repo $RepoName) {
            $RepoName = "$RepoPrefix-Bot"
            $n = 2
            while (Test-Repo $RepoName) {
                $RepoName = "$RepoPrefix-Bot-$n"
                $n++
            }
            Write-Warn "'$RepoPrefix' exists on GitHub - using '$RepoName'"
        }
        git remote remove origin 2>$null
        if (Test-Repo $RepoName) {
            git remote add origin "https://github.com/$Owner/$RepoName.git"
            git push -u origin main
        }
        else {
            gh repo create $RepoName --source . --remote origin --push --visibility $Visibility
        }
        if ($LASTEXITCODE -ne 0) { exit 1 }
        Write-Step "pushed to https://github.com/$Owner/$RepoName"
    }
    else {
        Write-Warn 'gh not authenticated - skipping GitHub push'
    }
}
else {
    Write-Warn 'gh not installed - skipping GitHub push'
}

# 3. SSH preparation
Write-Step "Step 3/5: SSH preparation on $Target"
ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 $Target 'mkdir -p ~/MineOps'
if ($LASTEXITCODE -ne 0) {
    Write-Host "[MineOps] SSH connection to '$Target' failed." -ForegroundColor Red
    exit 1
}

# 4. transfer
Write-Step 'Step 4/5: Transferring files'
foreach ($File in $Files) {
    scp -o StrictHostKeyChecking=accept-new "$File" "${Target}:~/MineOps/"
    if ($LASTEXITCODE -ne 0) { exit 1 }
}
if (Test-Path '.env') {
    scp -o StrictHostKeyChecking=accept-new '.env' "${Target}:~/MineOps/"
    if ($LASTEXITCODE -ne 0) { exit 1 }
}
else {
    Write-Warn 'no local .env found - create one in ~/MineOps on the server before the first run'
}

# 5. rebuild
Write-Step 'Step 5/5: Rebuilding containers'
$RemoteCmd = @'
cd ~/MineOps
if docker compose version >/dev/null 2>&1; then
    CMD="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
    CMD="docker-compose"
else
    echo "[MineOps] error: neither docker compose nor docker-compose is installed" >&2
    exit 1
fi
$CMD down && $CMD up -d --build
'@
$RemoteCmd = $RemoteCmd -replace "`r?`n", "`n"
ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 $Target $RemoteCmd
exit $LASTEXITCODE