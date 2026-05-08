[CmdletBinding()]
param(
    [string]$Root = ""
)

$ErrorActionPreference = "Stop"

if (-not $Root) {
    $scriptDir = Split-Path -Parent $PSCommandPath
    $Root = (Resolve-Path (Join-Path $scriptDir "..")).Path
}

$flutter = "D:\breakVelochron\flutter_sdk\bin\flutter.bat"
$go = "D:\breakVelochron\toolchain\go\go\bin\go.exe"

$srcDir = Join-Path $Root "src"
$flutterDir = Join-Path $Root "flutter_app"
$distDir = Join-Path $Root "dist\windows-release"
$bundleDir = Join-Path $flutterDir "build\windows\x64\runner\Release"
$coreExe = Join-Path $Root "nanfang-core.exe"
$zipPath = Join-Path $Root "dist\nanfang-windows.zip"

if (-not (Test-Path $flutter)) {
    throw "Flutter not found: $flutter"
}
if (-not (Test-Path $go)) {
    throw "Go not found: $go"
}

New-Item -ItemType Directory -Force -Path (Join-Path $Root "dist") | Out-Null

Write-Host "==> Build Go core"
Push-Location $srcDir
& $go build -o $coreExe .
Pop-Location

Write-Host "==> Flutter pub get"
Push-Location $flutterDir
& $flutter pub get

Write-Host "==> Build Flutter Windows"
& $flutter build windows --release
Pop-Location

if (-not (Test-Path $bundleDir)) {
    throw "Flutter Windows bundle not found: $bundleDir"
}

Write-Host "==> Prepare release bundle"
if (Test-Path $distDir) {
    Remove-Item -LiteralPath $distDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $distDir | Out-Null
Copy-Item -Path (Join-Path $bundleDir "*") -Destination $distDir -Recurse -Force
Copy-Item -LiteralPath $coreExe -Destination (Join-Path $distDir "nanfang-core.exe") -Force

if (Test-Path (Join-Path $Root "wintun.dll")) {
    Copy-Item -LiteralPath (Join-Path $Root "wintun.dll") -Destination (Join-Path $distDir "wintun.dll") -Force
}

if (Test-Path $zipPath) {
    Remove-Item -LiteralPath $zipPath -Force
}
Compress-Archive -Path (Join-Path $distDir "*") -DestinationPath $zipPath -Force

Write-Host ""
Write-Host "Done:"
Write-Host "  Bundle: $distDir"
Write-Host "  Zip:    $zipPath"
