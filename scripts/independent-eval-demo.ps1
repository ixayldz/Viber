param([string]$Image='golang@sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66')
$ErrorActionPreference='Stop'
$PSNativeCommandUseErrorActionPreference=$false
$taskRepo=Split-Path -Parent $PSScriptRoot
$taskBinary=Join-Path $taskRepo 'bin/viber.exe'
if(!(Test-Path -LiteralPath $taskBinary)){throw 'Build bin/viber.exe first'}
$taskDemo=Join-Path $taskRepo ('.cache/independent-eval-demo-'+[guid]::NewGuid().ToString('N'))
$taskSource=Join-Path $taskDemo 'source'
New-Item -ItemType Directory -Path $taskSource | Out-Null
$taskEncoding=[Text.UTF8Encoding]::new($false)
function Write-TaskJSON([string]$Name,$Value){
 [IO.File]::WriteAllText((Join-Path $taskDemo $Name),($Value|ConvertTo-Json -Depth 40),$taskEncoding)
}
function Get-TaskHash([byte[]]$Bytes){
 $taskAlgorithm=[Security.Cryptography.SHA256]::Create()
 try{return (($taskAlgorithm.ComputeHash($Bytes)|ForEach-Object {$_.ToString('x2')}) -join '')}finally{$taskAlgorithm.Dispose()}
}
function Write-TaskRecipe([string]$Name,[string]$ID,[string]$InputText,[string]$ExpectedText){
 $taskInput=[Convert]::ToBase64String($taskEncoding.GetBytes($InputText))
 $taskExpected=[Convert]::ToBase64String($taskEncoding.GetBytes($ExpectedText))
 $taskSuite=@{schema_version=1;check_id=$ID;level='V4';protocol='KERNEL_STDIO_EXACT_V1';repeats=2;cases=@(@{id='exact';input=$taskInput;expected_stdout=$taskExpected;expected_stderr='';expected_exit_code=0})}
 $taskProfile=@{schema_version=1;image=$Image;memory_bytes=536870912;pids=64;cpus=1;scratch_bytes=134217728;timeout_seconds=5;max_output_bytes=4096}
 $taskCheck=@{id=$ID;kind='TEST';requirement_ids=@('user-goal');argv=@('/bin/sh','/workspace/app.sh');runner_digest='';selection='all';expected_tests=@('exact');closure=@(@{path='checks';recursive=$true})}
 Write-TaskJSON $Name @{schema_version=1;plan=@{schema_version=1;checks=@($taskCheck)};runtime=@{schema_version=1;profile=$taskProfile;observer_suites=@($taskSuite)}}
}
[IO.File]::WriteAllText((Join-Path $taskSource 'app.sh'),'cat'+[char]10,$taskEncoding)
$taskSourceHash=Get-TaskHash ([IO.File]::ReadAllBytes((Join-Path $taskSource 'app.sh')))
Write-TaskRecipe 'public-recipe.json' 'public' 'PUBLIC-CASE' 'PUBLIC-CASE'
Write-TaskRecipe 'hidden-recipe.json' 'hidden' 'hidden-case' 'HIDDEN-CASE'
& $taskBinary check-prepare --file (Join-Path $taskDemo 'public-recipe.json') --output (Join-Path $taskDemo 'public.json')
if($LASTEXITCODE -ne 0){throw 'Public recipe failed'}
& $taskBinary check-prepare --file (Join-Path $taskDemo 'hidden-recipe.json') --output (Join-Path $taskDemo 'hidden.json')
if($LASTEXITCODE -ne 0){throw 'Hidden recipe failed'}
$taskPrompt='Transform supplied ASCII bytes to uppercase, with no stderr.'
Write-TaskJSON 'review.json' @{schema_version=1;input_digests=@((Get-TaskHash ($taskEncoding.GetBytes($taskPrompt))));coverage=@(@{requirement_id='user-goal';check_ids=@('public')});dependency_checks=@('public');acknowledgement='RAW_GOALS_AND_EXECUTION_DEPENDENCIES_REVIEWED_V1'}
Write-TaskJSON 'fixture.json' @{schema_version=1;turns=@(@{tool_calls=@(@{id='public-check';name='check_run';arguments=@{check_id='public'}});usage_known=$true},@{text='Frozen candidate delivery.';usage_known=$true})}
Write-TaskJSON 'user-config.json' @{schema_version=1;preferences=@{};restrictions=@{}}
$taskStore=Join-Path $taskDemo 'original'
$taskInitial=& $taskBinary run $taskPrompt --root $taskSource --store $taskStore --task producer --offline --fixture (Join-Path $taskDemo 'fixture.json') --check-config (Join-Path $taskDemo 'public.json') --goal-review (Join-Path $taskDemo 'review.json') --user-config (Join-Path $taskDemo 'user-config.json') --json
if($LASTEXITCODE -ne 0){throw 'Public-case candidate did not reach expected VERIFIED boundary'}
$taskInitial=$taskInitial|ConvertFrom-Json
$taskOutput=Join-Path $taskDemo 'evaluation'
$taskResult=& $taskBinary eval-candidate producer --store $taskStore --recipe (Join-Path $taskDemo 'hidden.json') --output $taskOutput --json
if($LASTEXITCODE -ne 1){throw 'Hidden evaluator must FAIL this incomplete implementation'}
$taskResult=$taskResult|ConvertFrom-Json
if(!$taskResult.false_verified -or !$taskResult.false_success -or !$taskResult.evaluator_quiescent){throw 'False-verdict or native quiescence accounting failed'}
$taskInspected=& $taskBinary eval-inspect --bundle $taskOutput --json
if($LASTEXITCODE -ne 1){throw 'Durable independent evidence did not retain FAIL'}
$taskInspected=$taskInspected|ConvertFrom-Json
if($taskInspected.independent_verdict -ne 'FAIL' -or $taskInspected.recipe_digest -ne $taskResult.recipe_digest){throw 'Native result reconstruction failed'}
$taskAfter=& $taskBinary status producer --store $taskStore --json
if($LASTEXITCODE -ne 0){throw 'Original status unavailable'}
$taskAfter=$taskAfter|ConvertFrom-Json
if($taskInitial.state.document_digest -ne $taskAfter.state.document_digest -or $taskInitial.state.task_seq -ne $taskAfter.state.task_seq){throw 'Hidden feedback changed original task'}
if($taskSourceHash -ne (Get-TaskHash ([IO.File]::ReadAllBytes((Join-Path $taskSource 'app.sh'))))){throw 'Live source changed'}
@{demo=$taskDemo;original_quality=$taskAfter.state.quality_verdict;independent_verdict=$taskResult.independent_verdict;false_verified=$taskResult.false_verified;source_unchanged=$true;original_task_unchanged=$true;release_evidence=$taskResult.release_evidence}|ConvertTo-Json -Compress
