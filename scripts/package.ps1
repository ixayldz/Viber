param([string]$Revision='HEAD',[string]$Output='',[string[]]$Targets=@('windows/amd64','linux/amd64','darwin/arm64'))
$ErrorActionPreference='Stop'
$taskRoot=Split-Path -Parent $PSScriptRoot
$taskGo=Join-Path $taskRoot '.tools/go1.27.2/go/bin/go.exe'
if(!(Test-Path -LiteralPath $taskGo)){$taskGo=(Get-Command go -ErrorAction Stop).Source}
$env:GOTOOLCHAIN='local'
$taskGoVersion=(& $taskGo version).Trim()
if($LASTEXITCODE-ne 0 -or $taskGoVersion-notmatch '^go version go1\.27\.2 [a-zA-Z0-9]+/[a-zA-Z0-9]+$'){throw 'Exact Go 1.27.2 toolchain required'}
$taskRevision=(& git -C $taskRoot rev-parse --verify ($Revision+'^{commit}')).Trim()
if($LASTEXITCODE-ne 0 -or $taskRevision-notmatch '^[0-9a-f]{40}$'){throw 'Exact Git commit required'}
$taskLicense=& git -C $taskRoot show ($taskRevision+':LICENSE') 2>$null
if($LASTEXITCODE-ne 0 -or !$taskLicense){throw 'Committed root LICENSE required. The code owner must choose a license before distribution.'}
foreach($target in $Targets){if($target-notin @('windows/amd64','linux/amd64','darwin/arm64')){throw 'Unsupported release target'}}
if($Targets.Count-eq 0 -or ($Targets | Select-Object -Unique).Count-ne $Targets.Count){throw 'Unique supported target list required'}
if($Output-eq ''){$Output=Join-Path $taskRoot ('.cache/package-'+$taskRevision.Substring(0,12))}
$taskOutput=[IO.Path]::GetFullPath($Output)
$taskComparison=[StringComparison]::Ordinal
if([IO.Path]::DirectorySeparatorChar-eq '\'){$taskComparison=[StringComparison]::OrdinalIgnoreCase}
$taskWorkspace=[IO.Path]::GetFullPath($taskRoot)+[IO.Path]::DirectorySeparatorChar
if(!$taskOutput.StartsWith($taskWorkspace,$taskComparison)){throw 'Build output must stay inside this workspace'}
if(Test-Path -LiteralPath $taskOutput){throw 'Fresh output required'}
$taskParent=[IO.Path]::GetDirectoryName($taskOutput)
while($taskParent.Length-ge $taskWorkspace.TrimEnd([IO.Path]::DirectorySeparatorChar).Length){
 if(Test-Path -LiteralPath $taskParent){
  if((Get-Item -LiteralPath $taskParent -Force).Attributes-band [IO.FileAttributes]::ReparsePoint){throw 'Reparse output ancestor denied'}
 }
 if($taskParent-eq $taskRoot){break}
 $taskParent=[IO.Path]::GetDirectoryName($taskParent)
}
$taskCache=Join-Path $taskRoot '.cache'
if((Test-Path -LiteralPath $taskCache) -and ((Get-Item -LiteralPath $taskCache -Force).Attributes-band [IO.FileAttributes]::ReparsePoint)){throw 'Reparse staging ancestor denied'}
$taskStage=Join-Path $taskRoot ('.cache/package-stage-'+[guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $taskStage -Force | Out-Null
$taskArchive=Join-Path $taskStage 'source.zip'
& git -C $taskRoot archive --format=zip --output $taskArchive $taskRevision
if($LASTEXITCODE-ne 0){throw 'Git source archive failed'}
$taskSource=Join-Path $taskStage 'source'
Add-Type -AssemblyName System.IO.Compression.FileSystem
[IO.Compression.ZipFile]::ExtractToDirectory($taskArchive,$taskSource)
$env:GOTOOLCHAIN='local'
$env:GOPROXY='off'
$env:GOCACHE=Join-Path $taskRoot '.cache/go-build'
$env:GOMODCACHE=Join-Path $taskRoot '.cache/go-mod'
$env:CGO_ENABLED='0'
$taskOldGOOS=$env:GOOS
$taskOldGOARCH=$env:GOARCH
New-Item -ItemType Directory -Path $taskOutput | Out-Null
$taskProof=@{schema_version=1;source_revision=$taskRevision;source_archive_sha256=(Get-FileHash $taskArchive -Algorithm SHA256).Hash.ToLower();go_version=$taskGoVersion;release_ready=$false;signature_status='UNSIGNED_ENGINEERING_BUNDLE';files=@();targets=@()}
Push-Location -LiteralPath $taskSource
try{
 & $taskGo mod verify
 if($LASTEXITCODE-ne 0){throw 'Pinned dependency verification failed'}
 # Inventory the union of actual binary dependency closures, including
 # platform-only imports. `list -m all` also resolves unused test/tool modules
 # and incorrectly requires their downloads for an offline release build.
 $taskModules=@()
 foreach($target in $Targets){
  $parts=$target -split '/';$env:GOOS=$parts[0];$env:GOARCH=$parts[1]
  $taskRows=& $taskGo list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/viber
  if($LASTEXITCODE-ne 0){throw ('Binary module inventory failed: '+$target)}
  $taskModules+=@($taskRows | Where-Object {$_-ne ''})
 }
 $taskModules=@($taskModules | Sort-Object -Unique -CaseSensitive)
 $taskComponents=@()
 $taskAttribution=Join-Path $taskOutput 'licenses'
 New-Item -ItemType Directory -Path $taskAttribution | Out-Null
 [IO.File]::WriteAllText((Join-Path $taskOutput 'LICENSE'),($taskLicense -join [char]10)+[char]10,[Text.UTF8Encoding]::new($false))
 foreach($row in $taskModules){
  # JSON preserves native paths (including Unix pipe characters); no delimiter
  # parsing can change the module directory or checksum attribution.
  $taskModuleJSON=& $taskGo list -m -json $row
  if($LASTEXITCODE-ne 0){throw ('Selected module metadata failed: '+$row)}
  $module=($taskModuleJSON -join [char]10) | ConvertFrom-Json
  if($module.Path-cne $row){throw 'Selected module identity mismatch'}
  if(!$module.Version){continue}
  if(!$module.Dir -or $module.Sum-notmatch '^h1:[A-Za-z0-9+/]{43}=$' -or $module.Replace){throw ('Pinned release dependency required: '+$row)}
  $taskLicensePaths=@(Get-ChildItem -LiteralPath $module.Dir -File | Where-Object {$_.Name -match '^(LICENSE|COPYING|NOTICE)(\..*)?$'} | Sort-Object Name)
  if($taskLicensePaths.Count-eq 0){throw ('Dependency license material missing: '+$module.Path)}
  $taskName=($module.Path -replace '[^a-zA-Z0-9_.-]','_')+'-'+$module.Version
  $taskLicenseRefs=@()
  foreach($licenseFile in $taskLicensePaths){
   $taskRelative='licenses/'+$taskName+'-'+$licenseFile.Name
   [IO.File]::Copy($licenseFile.FullName,(Join-Path $taskOutput $taskRelative),$false)
   $taskLicenseRefs+=$taskRelative
  }
  $taskComponents+=@{type='library';name=$module.Path;version=$module.Version;purl=('pkg:golang/'+$module.Path+'@'+$module.Version);properties=@(@{name='go:module:sum';value=$module.Sum},@{name='viber:license_files';value=($taskLicenseRefs -join ',')})}
 }
 $taskGOROOT=(& $taskGo env GOROOT).Trim()
 [IO.File]::Copy((Join-Path $taskGOROOT 'LICENSE'),(Join-Path $taskAttribution 'go-toolchain-LICENSE'),$false)
 $taskSBOM=@{bomFormat='CycloneDX';specVersion='1.6';version=1;metadata=@{component=@{type='application';name='Viber';version=$taskRevision}};components=$taskComponents}
 [IO.File]::WriteAllText((Join-Path $taskOutput 'sbom.cdx.json'),($taskSBOM|ConvertTo-Json -Depth 30),[Text.UTF8Encoding]::new($false))
 foreach($target in $Targets){
  $parts=$target -split '/';$env:GOOS=$parts[0];$env:GOARCH=$parts[1]
  $name='viber-'+$parts[0]+'-'+$parts[1];if($parts[0]-eq 'windows'){$name+='.exe'}
  $binary=Join-Path $taskOutput $name
  & $taskGo build -trimpath -buildvcs=false -ldflags '-s -w' -o $binary ./cmd/viber
  if($LASTEXITCODE-ne 0){throw ('Build failed: '+$target)}
  $second=Join-Path $taskStage ('repeat-'+$name)
  & $taskGo build -trimpath -buildvcs=false -ldflags '-s -w' -o $second ./cmd/viber
  if($LASTEXITCODE-ne 0){throw ('Reproducibility build failed: '+$target)}
  $firstHash=(Get-FileHash $binary -Algorithm SHA256).Hash.ToLower()
  if($firstHash-ne (Get-FileHash $second -Algorithm SHA256).Hash.ToLower()){throw ('Byte reproducibility failed: '+$target)}
  $taskProof.targets+=@{target=$target;binary=$name;sha256=$firstHash;reproducible=$true;native_acceptance='NOT_RUN'}
 }
 $taskFiles=Get-ChildItem -LiteralPath $taskOutput -File -Recurse | Sort-Object FullName
 foreach($file in $taskFiles){
  $relative=[IO.Path]::GetRelativePath($taskOutput,$file.FullName).Replace('\','/')
  $taskProof.files+=@{path=$relative;bytes=$file.Length;sha256=(Get-FileHash $file.FullName -Algorithm SHA256).Hash.ToLower()}
 }
 # Publication marker is last. A partial directory is never a complete bundle.
 [IO.File]::WriteAllText((Join-Path $taskOutput 'bundle.json'),($taskProof|ConvertTo-Json -Depth 30),[Text.UTF8Encoding]::new($false))
}finally{Pop-Location;$env:GOOS=$taskOldGOOS;$env:GOARCH=$taskOldGOARCH}
Write-Output $taskOutput
