# Explicit opt-in: two disposable privileged daemons. No host socket, bind mount
# or published port. Every created container is reclaimed in finally.
param([switch]$AllowPrivilegedDisposableEngines)
$ErrorActionPreference='Stop'
if (!$AllowPrivilegedDisposableEngines) {throw 'Explicit -AllowPrivilegedDisposableEngines opt-in required.'}
$taskRoot=Split-Path -Parent $PSScriptRoot
$taskGo=Join-Path $taskRoot '.tools/go1.27.2/go/bin/go.exe'
if (!(Test-Path -LiteralPath $taskGo)) {$taskGo=(Get-Command go -ErrorAction Stop).Source}
$taskEvidence=Join-Path $taskRoot ('.cache/engine-matrix-'+[guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($taskEvidence)|Out-Null
$taskBinary=Join-Path $taskEvidence 'runner.test'
$taskOriginalGOOS=$env:GOOS;$taskOriginalGOARCH=$env:GOARCH;$taskOriginalCGO=$env:CGO_ENABLED
$taskOriginalToolchain=$env:GOTOOLCHAIN
function Invoke-MatrixDocker {
 param([string[]]$Arguments)
 $text=(& docker @Arguments 2>&1 | Out-String).Trim()
 if ($LASTEXITCODE -ne 0) {throw ('Disposable engine operation failed: '+$text)}
 return $text
}
$taskImage='golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61'
try {
 $env:GOOS='linux';$env:GOARCH='amd64';$env:CGO_ENABLED='0';$env:GOTOOLCHAIN='local'
 Push-Location -LiteralPath $taskRoot
 try {& $taskGo test -c -o $taskBinary ./internal/runner;if($LASTEXITCODE){throw 'Acceptance binary compile failed'}} finally {Pop-Location}
 foreach($mode in @('rootful','rootless')) {
  $taskContainer=''
  try {
   $daemon='docker:29.8.2-dind@sha256:1e08cdb63405ca788aea94ef35b792d1e299607c667d33d7bcbcae1fd2611ced'
   $endpoint='unix:///var/run/docker.sock'
   if($mode -eq 'rootless') {$daemon='docker:29.8.2-dind-rootless@sha256:c652fa9cebdaa3d655c33c6389c3c409f9a0fea6449c7d7c56c486fe71af0591';$endpoint='unix:///run/user/1000/docker.sock'}
   $null=Invoke-MatrixDocker @('pull',$daemon)
   $name='viber-disposable-'+[guid]::NewGuid().ToString('N')
   $taskContainer=Invoke-MatrixDocker @('run','-d','--name',$name,'--label','io.viber.acceptance=disposable-engine','--privileged','--cpus','2','--memory','3g','--pids-limit','512','--env','DOCKER_TLS_CERTDIR=',$daemon,('--host='+$endpoint),'--iptables=false','--bridge=none')
   if($taskContainer -notmatch '^[a-f0-9]{64}$') {throw 'Exact created container identity required.'}
   $ready=$false
   for($i=0;$i -lt 30;$i++) {
    & docker exec $taskContainer timeout 5 docker --host $endpoint info *> $null
    if(!$LASTEXITCODE){$ready=$true;break}
    Start-Sleep -Seconds 1
   }
   if(!$ready){throw 'Disposable engine did not become ready within 30 seconds.'}
   $info=Invoke-MatrixDocker @('exec',$taskContainer,'docker','--host',$endpoint,'info','--format','{{json .}}')
   [IO.File]::WriteAllText((Join-Path $taskEvidence ($mode+'-info.json')),$info)
   $null=Invoke-MatrixDocker @('exec','--user','0',$taskContainer,'mkdir','/matrix')
   $null=Invoke-MatrixDocker @('cp',$taskBinary,($taskContainer+':/runner.test'))
   $null=Invoke-MatrixDocker @('exec','--user','0',$taskContainer,'chmod','0755','/runner.test','/matrix')
   $null=Invoke-MatrixDocker @('exec','--user','0',$taskContainer,'chown','1000:1000','/matrix')
   $base=@('exec','--env',('VIBER_DOCKER_HOST='+$endpoint),'--env','VIBER_ENGINE_MATRIX_DIR=/matrix','--env',('VIBER_DOCKER_TEST_IMAGE='+$taskImage))
   if($mode -eq 'rootless') {
    $probe=Invoke-MatrixDocker ($base+@('--env','VIBER_ENGINE_MATRIX_PHASE=rootless-probe',$taskContainer,'/runner.test','-test.v','-test.run=^TestDisposableRootlessCapability$'))
    [IO.File]::WriteAllText((Join-Path $taskEvidence 'rootless-probe.txt'),$probe)
    if($probe -notmatch 'ROOTLESS_SUPPORTED|ROOTLESS_DENIED_UNSUPPORTED_RESOURCE_CONTROLLERS'){throw 'No measured rootless classification.'}
    if($probe -match 'ROOTLESS_DENIED_UNSUPPORTED_RESOURCE_CONTROLLERS'){continue}
   }
   $null=Invoke-MatrixDocker @('exec',$taskContainer,'docker','--host',$endpoint,'pull',$taskImage)
   $before=Invoke-MatrixDocker ($base+@('--env','VIBER_ENGINE_MATRIX_PHASE=before',$taskContainer,'/runner.test','-test.v','-test.run=^TestDisposableEnginePhase$'))
   [IO.File]::WriteAllText((Join-Path $taskEvidence ($mode+'-before.txt')),$before)
   if($before -notmatch 'BEFORE_RESTART_OWNED_SUBJECT_RUNNING'){throw 'Subject was not observed running.'}
   $null=Invoke-MatrixDocker @('restart','--time','5',$taskContainer)
   $ready=$false
   for($i=0;$i -lt 30;$i++) {& docker exec $taskContainer timeout 5 docker --host $endpoint info *> $null;if(!$LASTEXITCODE){$ready=$true;break};Start-Sleep -Seconds 1}
   if(!$ready){throw 'Restarted disposable engine unavailable.'}
   $after=Invoke-MatrixDocker ($base+@('--env','VIBER_ENGINE_MATRIX_PHASE=after',$taskContainer,'/runner.test','-test.v','-test.run=^TestDisposableEnginePhase$'))
   [IO.File]::WriteAllText((Join-Path $taskEvidence ($mode+'-after.txt')),$after)
   if($after -notmatch 'AFTER_RESTART_FENCED'){throw 'Restart fencing proof missing.'}
  } finally {
   if($taskContainer -match '^[a-f0-9]{64}$') {
    & docker logs $taskContainer *> (Join-Path $taskEvidence ($mode+'-daemon.log'))
    & docker rm --force --volumes $taskContainer | Out-Null
    if($LASTEXITCODE){throw ('Cleanup failed for exact disposable container '+$taskContainer)}
   }
  }
 }
 Write-Output ('Acceptance evidence: '+$taskEvidence)
} finally {
 $env:GOOS=$taskOriginalGOOS;$env:GOARCH=$taskOriginalGOARCH;$env:CGO_ENABLED=$taskOriginalCGO;$env:GOTOOLCHAIN=$taskOriginalToolchain
}
