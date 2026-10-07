param([string]$OutputRoot)
$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
$taskBinary = Join-Path $taskRoot 'bin/viber.exe'
if (-not (Test-Path -LiteralPath $taskBinary)) { throw 'Build bin/viber.exe with scripts/check.ps1 first.' }
if (-not $OutputRoot) { $OutputRoot = Join-Path $taskRoot ('.cache/demo-' + [guid]::NewGuid().ToString('N')) }
$demoRoot = [IO.Path]::GetFullPath($OutputRoot)
if (Test-Path -LiteralPath $demoRoot) { throw 'Demo output must be a new directory.' }
[IO.Directory]::CreateDirectory($demoRoot) | Out-Null
$demoSource = Join-Path $taskRoot 'examples/offline/source'
$demoFixture = Join-Path $taskRoot 'examples/offline/greeting.json'
$demoStore = Join-Path $demoRoot 'store'
$demoDelivery = Join-Path $demoRoot 'delivery'
$demoBackup = Join-Path $demoRoot 'backup'
$demoRestored = Join-Path $demoRoot 'restored'
& $taskBinary run 'Update hello.txt to the fixture greeting' --offline --fixture $demoFixture --root $demoSource --store $demoStore --task greeting --allow-unverified --json
if ($LASTEXITCODE -ne 2) { throw 'Expected FINISHED/UNVERIFIED limited delivery (exit 2).' }
& $taskBinary export greeting --store $demoStore --output $demoDelivery --json
if ($LASTEXITCODE -ne 0) { throw 'Changeset export failed.' }
& $taskBinary store-backup --store $demoStore --output $demoBackup --json
if ($LASTEXITCODE -ne 0) { throw 'Backup failed.' }
& $taskBinary store-restore --backup $demoBackup --store $demoRestored --json
if ($LASTEXITCODE -ne 0) { throw 'Fresh restore failed.' }
& $taskBinary replay greeting --store $demoRestored --until 1 --json
if ($LASTEXITCODE -ne 0) { throw 'Historical replay failed.' }
if ([IO.File]::ReadAllText((Join-Path $demoSource 'hello.txt')) -ne 'hello') { throw 'Source preservation failed.' }
Write-Output ('Demo artifacts: ' + $demoRoot)
