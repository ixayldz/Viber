$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $taskRoot
$taskGo = Join-Path $taskRoot '.tools\go1.27.2\go\bin\go.exe'
if (-not (Test-Path -LiteralPath $taskGo)) { $taskGo = (Get-Command go -ErrorAction Stop).Source }
$env:GOCACHE = Join-Path $taskRoot '.cache\go-build'
$env:GOMODCACHE = Join-Path $taskRoot '.cache\go-mod'
$env:GOTOOLCHAIN = 'local'
$taskSources = Get-ChildItem -LiteralPath cmd,internal -Filter *.go -Recurse | ForEach-Object { $_.FullName }
$taskGofmt = Join-Path (Split-Path $taskGo) 'gofmt.exe'
$taskUnformatted = & $taskGofmt -l $taskSources
if ($LASTEXITCODE -ne 0 -or $taskUnformatted) { throw ('Unformatted Go files: ' + ($taskUnformatted -join ', ')) }
& $taskGo mod verify
if ($LASTEXITCODE -ne 0) { throw 'Dependency verification failed' }
& $taskGo vet ./...
if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
& $taskGo test -count=1 ./...
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
New-Item -ItemType Directory -Path bin -Force | Out-Null
& $taskGo build -trimpath -o bin/viber.exe ./cmd/viber
if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
& .\bin\viber.exe doctor --json
if ($LASTEXITCODE -ne 0) { throw 'Doctor failed' }