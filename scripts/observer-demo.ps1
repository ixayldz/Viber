param([string]$Image='golang@sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66')
$ErrorActionPreference='Stop'
$taskRoot=Split-Path -Parent $PSScriptRoot
$taskBinary=Join-Path $taskRoot 'bin/viber.exe'
if(!(Test-Path -LiteralPath $taskBinary)){throw 'Build bin/viber.exe first'}
$taskDemo=Join-Path $taskRoot ('.cache/observer-demo-'+[guid]::NewGuid().ToString('N'))
$taskSource=Join-Path $taskDemo 'source'
New-Item -ItemType Directory -Path $taskSource | Out-Null
$taskEncoding=[Text.UTF8Encoding]::new($false)
function Write-TaskJSON([string]$Name,$Value){
 [IO.File]::WriteAllText((Join-Path $taskDemo $Name),($Value|ConvertTo-Json -Depth 40),$taskEncoding)
}
function Get-TaskHash([byte[]]$Bytes){
 $algorithm=[Security.Cryptography.SHA256]::Create()
 try{return (($algorithm.ComputeHash($Bytes)|ForEach-Object {$_.ToString('x2')}) -join '')}finally{$algorithm.Dispose()}
}
[IO.File]::WriteAllText((Join-Path $taskSource 'app.sh'),'cat'+[char]10,$taskEncoding)
$taskSourceHash=Get-TaskHash ([IO.File]::ReadAllBytes((Join-Path $taskSource 'app.sh')))
$taskInput=[Convert]::ToBase64String($taskEncoding.GetBytes('offline-echo'))
$taskSuite=@{schema_version=1;check_id='echo';level='V4';protocol='KERNEL_STDIO_EXACT_V1';repeats=2;cases=@(@{id='bytes';input=$taskInput;expected_stdout=$taskInput;expected_stderr='';expected_exit_code=0},@{id='empty';input='';expected_stdout='';expected_stderr='';expected_exit_code=0})}
$taskProfile=@{schema_version=1;image=$Image;memory_bytes=536870912;pids=64;cpus=1;scratch_bytes=134217728;timeout_seconds=5;max_output_bytes=16384}
$taskCheck=@{id='echo';kind='TEST';requirement_ids=@('user-goal');argv=@('/bin/sh','/workspace/app.sh');runner_digest='';selection='external exact STDIO cases';expected_tests=@('bytes','empty');closure=@(@{path='checks';recursive=$true})}
Write-TaskJSON 'recipe.json' @{schema_version=1;plan=@{schema_version=1;checks=@($taskCheck)};runtime=@{schema_version=1;profile=$taskProfile;observer_suites=@($taskSuite)}}
& $taskBinary check-prepare --file (Join-Path $taskDemo 'recipe.json') --output (Join-Path $taskDemo 'checks.json')
if($LASTEXITCODE-ne 0){throw 'Check recipe binding failed'}
$taskPrompt='Echo the supplied fixture bytes exactly, with no stderr.'
Write-TaskJSON 'goal-review.json' @{schema_version=1;input_digests=@((Get-TaskHash ($taskEncoding.GetBytes($taskPrompt))));coverage=@(@{requirement_id='user-goal';check_ids=@('echo')});dependency_checks=@('echo');acknowledgement='RAW_GOALS_AND_EXECUTION_DEPENDENCIES_REVIEWED_V1'}
Write-TaskJSON 'fixture.json' @{schema_version=1;turns=@(@{tool_calls=@(@{id='observer-run';name='check_run';arguments=@{check_id='echo'}});usage_known=$true;input_tokens=10;output_tokens=10},@{text='No change is required: the candidate echoes the registered inputs.';usage_known=$true;input_tokens=10;output_tokens=10})}
Write-TaskJSON 'user-config.json' @{schema_version=1;preferences=@{};restrictions=@{}}
$taskStore=Join-Path $taskDemo 'store'
& $taskBinary run $taskPrompt --root $taskSource --store $taskStore --task observer-demo --offline --fixture (Join-Path $taskDemo 'fixture.json') --check-config (Join-Path $taskDemo 'checks.json') --goal-review (Join-Path $taskDemo 'goal-review.json') --user-config (Join-Path $taskDemo 'user-config.json') --json
if($LASTEXITCODE-ne 0){throw 'Independent observer demo did not satisfy guarded candidate-only finalization'}
& $taskBinary verification observer-demo --store $taskStore --json
if($LASTEXITCODE-ne 0){throw 'Current evidence observation failed'}
& $taskBinary check-output observer-demo --store $taskStore --run-id observer-run --case bytes --repeat 2 --scope baseline --offset 0 --limit 16384 --json
if($LASTEXITCODE-ne 0){throw 'Exact retained observer page unavailable'}
if($taskSourceHash-ne (Get-TaskHash ([IO.File]::ReadAllBytes((Join-Path $taskSource 'app.sh'))))){throw 'User source changed'}
Write-Output $taskDemo
