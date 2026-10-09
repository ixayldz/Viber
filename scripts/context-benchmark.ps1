param(
    [ValidatePattern('^[0-9]+(ms|s)$')][string]$BenchTime = '1s',
    [string]$Go = ''
)
$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path $PSScriptRoot -Parent
if (!$Go) {
    $taskBundled = Join-Path $taskRoot '.tools/go1.27.2/go/bin/go.exe'
    if (Test-Path -LiteralPath $taskBundled) { $Go = $taskBundled }
    else { $Go = (Get-Command go -ErrorAction Stop).Source }
}
$taskEvidence = Join-Path $taskRoot ('.cache/context-benchmark-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($taskEvidence)
$taskOldToolchain = $env:GOTOOLCHAIN
$env:GOTOOLCHAIN = 'local'
Push-Location $taskRoot
try {
    $taskVersion = & $Go version
    if ($LASTEXITCODE -ne 0 -or $taskVersion -notmatch 'go1\.27\.2 ') { throw 'Go 1.27.2 required' }
    $taskVersion | Set-Content -LiteralPath (Join-Path $taskEvidence 'environment.txt') -Encoding utf8
    & $Go env GOOS GOARCH | Add-Content -LiteralPath (Join-Path $taskEvidence 'environment.txt') -Encoding utf8
    & $Go test -json -count=1 ./internal/retrieval ./internal/context ./internal/agent -run 'ContextEffectiveness|IntentLog|RealRepositoryOwner|OwnerSearch|Cache|BodyEvidence|PerFileSpan|Partition' |
        Tee-Object -FilePath (Join-Path $taskEvidence 'effectiveness.jsonl')
    if ($LASTEXITCODE -ne 0) { throw 'Effectiveness acceptance failed' }
    & $Go test -json -count=1 ./internal/retrieval ./internal/context ./internal/agent -run '^$' -bench 'BenchmarkSourceRetrieval|BenchmarkContextLogPacking|BenchmarkOwnerRankedSearch|BenchmarkOwnerLazyPartition' -benchmem "-benchtime=$BenchTime" |
        Tee-Object -FilePath (Join-Path $taskEvidence 'benchmarks.jsonl')
    if ($LASTEXITCODE -ne 0) { throw 'Context benchmark failed' }
    Write-Output "Evidence: $taskEvidence"
}
finally { Pop-Location; $env:GOTOOLCHAIN = $taskOldToolchain }
