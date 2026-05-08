[CmdletBinding()]
param(
    [string]$Root = ""
)

$ErrorActionPreference = "Stop"

if (-not $Root) {
    $scriptDir = Split-Path -Parent $PSCommandPath
    $Root = (Resolve-Path (Join-Path $scriptDir "..")).Path
}

$python = "python"
$go = "D:\breakVelochron\toolchain\go\go\bin\go.exe"
$pyInstaller = "python -m PyInstaller"

$srcDir = Join-Path $Root "src"
$distRoot = Join-Path $Root "dist"
$workDir = Join-Path $Root "build\pyinstaller"
$guiScript = Join-Path $Root "nanfang_gui.py"
$pyDistDir = Join-Path $Root "dist-py"
$releaseDir = Join-Path $distRoot "windows-classic-release"
$coreExe = Join-Path $Root "nanfang-core.exe"
$zipPath = Join-Path $distRoot "nanfang-windows.zip"
$readmePath = Join-Path $releaseDir "README-Windows.txt"

New-Item -ItemType Directory -Force -Path $distRoot | Out-Null

Write-Host "==> Build Go core"
Push-Location $srcDir
& $go build -o $coreExe .
Pop-Location

Write-Host "==> Build Python GUI"
if (Test-Path $pyDistDir) {
    Remove-Item -LiteralPath $pyDistDir -Recurse -Force
}
if (Test-Path $workDir) {
    Remove-Item -LiteralPath $workDir -Recurse -Force
}

& $python -m PyInstaller `
    --noconfirm `
    --clean `
    --windowed `
    --name nanfang `
    --distpath $pyDistDir `
    --workpath $workDir `
    $guiScript

Write-Host "==> Assemble classic release"
if (Test-Path $releaseDir) {
    Remove-Item -LiteralPath $releaseDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $releaseDir | Out-Null

Copy-Item -Path (Join-Path $pyDistDir "nanfang\\*") -Destination $releaseDir -Recurse -Force
Copy-Item -LiteralPath $coreExe -Destination (Join-Path $releaseDir "nanfang-core.exe") -Force

if (Test-Path (Join-Path $Root "wintun.dll")) {
    Copy-Item -LiteralPath (Join-Path $Root "wintun.dll") -Destination (Join-Path $releaseDir "wintun.dll") -Force
}

@"
Nanfang Windows 使用说明

1. 打开 nanfang.exe
2. 在“订阅链接”输入你的订阅 URL
3. 点击“拉取节点”
4. 选中节点后，点击“系统代理”开始使用

说明：
- 核心文件为 nanfang-core.exe，请不要删除
- wintun.dll 供 TUN 模式使用
- 第一次运行会在当前目录生成 settings.json / nodes.json
"@ | Set-Content -Encoding UTF8 $readmePath

if (Test-Path $zipPath) {
    Remove-Item -LiteralPath $zipPath -Force
}
Compress-Archive -Path (Join-Path $releaseDir "*") -DestinationPath $zipPath -Force

Write-Host ""
Write-Host "Done:"
Write-Host "  Bundle: $releaseDir"
Write-Host "  Zip:    $zipPath"
