param(
 [Parameter(Mandatory=$true)][string]$LegacyStore,
 [switch]$AllowInPlaceMigration,
 [string]$Task = 'greeting',
 [string]$OutputRoot
)
$ErrorActionPreference='Stop'
if (!$AllowInPlaceMigration) {throw 'Migration updates the named existing store. Pass -AllowInPlaceMigration explicitly.'}
$taskRoot=Split-Path -Parent $PSScriptRoot
$taskBinary=Join-Path $taskRoot 'bin/viber.exe'
if (!(Test-Path -LiteralPath $taskBinary)) {throw 'Build bin/viber.exe first.'}
if (!$OutputRoot) {$OutputRoot=Join-Path $taskRoot ('.cache/migration-demo-'+[guid]::NewGuid().ToString('N'))}
$demoRoot=[IO.Path]::GetFullPath($OutputRoot)
if (Test-Path -LiteralPath $demoRoot) {throw 'Demo output must be fresh.'}
[IO.Directory]::CreateDirectory($demoRoot) | Out-Null
$demoStore=[IO.Path]::GetFullPath($LegacyStore)
$demoBefore=Join-Path $demoRoot 'before-migration'
$demoAfter=Join-Path $demoRoot 'after-migration'
$demoRestored=Join-Path $demoRoot 'restored'
function Invoke-DemoCommand {
 param([string[]]$Arguments)
 $raw=(& $taskBinary @Arguments) -join [Environment]::NewLine
 if ($LASTEXITCODE -ne 0) {throw ('CLI failed with exit '+$LASTEXITCODE+': '+$raw)}
 return ($raw | ConvertFrom-Json)
}
$before=Invoke-DemoCommand @('status',$Task,'--store',$demoStore,'--json')
$oldStatus=Invoke-DemoCommand @('store-migration-status','--store',$demoStore,'--json')
if ($oldStatus.snapshot.schema_version -ne 1) {throw 'Restore performed an automatic migration.'}
$argsMigration=@('store-migrate','--store',$demoStore,'--backup-output',$demoBefore,'--command-id','native-migration-demo','--json')
$receipt=Invoke-DemoCommand $argsMigration
if ($receipt.status -ne 'COMMITTED' -or $receipt.target_snapshot.schema_version -ne 2) {throw 'Migration not committed.'}
$again=Invoke-DemoCommand $argsMigration
if (($receipt|ConvertTo-Json -Depth 64 -Compress) -ne ($again|ConvertTo-Json -Depth 64 -Compress)) {throw 'Idempotent migration receipt changed.'}
$after=Invoke-DemoCommand @('status',$Task,'--store',$demoStore,'--json')
if (($before.state|ConvertTo-Json -Depth 64 -Compress) -ne ($after.state|ConvertTo-Json -Depth 64 -Compress)) {throw 'Migration changed task state.'}
$backup=Invoke-DemoCommand @('store-backup','--store',$demoStore,'--output',$demoAfter,'--json')
if ($backup.store.format_digest -ne $receipt.target_snapshot.format_digest) {throw 'Backup lost format identity.'}
$restored=Invoke-DemoCommand @('store-restore','--backup',$demoAfter,'--store',$demoRestored,'--json')
if (($backup.store|ConvertTo-Json -Depth 64 -Compress) -ne ($restored.store|ConvertTo-Json -Depth 64 -Compress)) {throw 'Fresh restore changed snapshot identity.'}
$restoredState=Invoke-DemoCommand @('status',$Task,'--store',$demoRestored,'--json')
if (($after.state|ConvertTo-Json -Depth 64 -Compress) -ne ($restoredState.state|ConvertTo-Json -Depth 64 -Compress)) {throw 'Fresh restore changed task state.'}
$null=Invoke-DemoCommand @('replay',$Task,'--store',$demoRestored,'--until','1','--json')
[ordered]@{
 schema_version=1
 status='PASS'
 source_store=[IO.Path]::GetFullPath($LegacyStore)
 artifacts=$demoRoot
 from_schema=1
 to_schema=2
 original_receipt=$receipt
 task_state_preserved=$true
 historical_replay=$true
} | ConvertTo-Json -Depth 64 -Compress