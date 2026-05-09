[CmdletBinding()]
param(
    [string]$Root = ""
)

$ErrorActionPreference = "Stop"

if (-not $Root) {
    $scriptDir = Split-Path -Parent $PSCommandPath
    $Root = (Resolve-Path (Join-Path $scriptDir "..")).Path
}

$go = "D:\breakVelochron\toolchain\go\go\bin\go.exe"
$pythonHome = "D:\python"
$releaseDir = Join-Path $Root "dist\windows-portable-release"
$runtimeDir = Join-Path $releaseDir "runtime"
$coreExe = Join-Path $Root "nanfang-core.exe"
$launcherExe = Join-Path $releaseDir "nanfang.exe"
$guiScript = Join-Path $Root "nanfang_gui.py"
$zipPath = Join-Path $Root "dist\nanfang-windows.zip"

New-Item -ItemType Directory -Force -Path (Join-Path $Root "dist") | Out-Null

Write-Host "==> Build Go core"
Push-Location (Join-Path $Root "src")
& $go build -o $coreExe .
if ($LASTEXITCODE -ne 0) { throw "Go core build failed" }
Pop-Location

Write-Host "==> Prepare portable release directory"
if (Test-Path $releaseDir) {
    Remove-Item -LiteralPath $releaseDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $runtimeDir | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $runtimeDir "Lib") | Out-Null

Write-Host "==> Copy Python runtime subset"
Copy-Item -LiteralPath (Join-Path $pythonHome "python.exe") -Destination (Join-Path $runtimeDir "python.exe") -Force
Copy-Item -LiteralPath (Join-Path $pythonHome "pythonw.exe") -Destination (Join-Path $runtimeDir "pythonw.exe") -Force
Copy-Item -LiteralPath (Join-Path $pythonHome "python3.dll") -Destination (Join-Path $runtimeDir "python3.dll") -Force
Copy-Item -LiteralPath (Join-Path $pythonHome "python312.dll") -Destination (Join-Path $runtimeDir "python312.dll") -Force
Copy-Item -LiteralPath (Join-Path $pythonHome "vcruntime140.dll") -Destination (Join-Path $runtimeDir "vcruntime140.dll") -Force
Copy-Item -LiteralPath (Join-Path $pythonHome "vcruntime140_1.dll") -Destination (Join-Path $runtimeDir "vcruntime140_1.dll") -Force
Copy-Item -LiteralPath (Join-Path $pythonHome "DLLs") -Destination (Join-Path $runtimeDir "DLLs") -Recurse -Force
Copy-Item -LiteralPath (Join-Path $pythonHome "tcl") -Destination (Join-Path $runtimeDir "tcl") -Recurse -Force
Copy-Item -Path (Join-Path $pythonHome "Lib\*") -Destination (Join-Path $runtimeDir "Lib") -Recurse -Force
if (Test-Path (Join-Path $runtimeDir "Lib\site-packages")) {
    Remove-Item -LiteralPath (Join-Path $runtimeDir "Lib\site-packages") -Recurse -Force
}
if (Test-Path (Join-Path $runtimeDir "Lib\test")) {
    Remove-Item -LiteralPath (Join-Path $runtimeDir "Lib\test") -Recurse -Force
}
if (Test-Path (Join-Path $runtimeDir "Lib\idlelib")) {
    Remove-Item -LiteralPath (Join-Path $runtimeDir "Lib\idlelib") -Recurse -Force
}
if (Test-Path (Join-Path $runtimeDir "Lib\turtledemo")) {
    Remove-Item -LiteralPath (Join-Path $runtimeDir "Lib\turtledemo") -Recurse -Force
}

Write-Host "==> Build Windows launcher"
Push-Location (Join-Path $Root "src")
& $go build -ldflags "-H windowsgui" -o $launcherExe ./cmd/launcher
if ($LASTEXITCODE -ne 0) { throw "Go launcher build failed" }
Pop-Location

Write-Host "==> Copy application files"
Copy-Item -LiteralPath $guiScript -Destination (Join-Path $releaseDir "nanfang_gui.py") -Force
Copy-Item -LiteralPath $coreExe -Destination (Join-Path $releaseDir "nanfang-core.exe") -Force
if (Test-Path (Join-Path $Root "wintun.dll")) {
    Copy-Item -LiteralPath (Join-Path $Root "wintun.dll") -Destination (Join-Path $releaseDir "wintun.dll") -Force
}

@"
Nanfang Windows 使用说明

1. 打开 nanfang.exe
2. 在“订阅链接”输入你的订阅 URL
3. 点击“拉取节点”
4. 选择节点后点击“系统代理”

说明：
- nanfang-core.exe 是代理核心，不要删除
- runtime 目录是内置 Python 运行时，不要删除
- wintun.dll 供 TUN 模式使用
- 第一次运行会在当前目录生成 nodes.json / settings.json / debug.log
"@ | Set-Content -Path (Join-Path $releaseDir "README-Windows.txt") -Encoding UTF8

Write-Host "==> Verify portable runtime fetch path"
$testScript = @'
import json, os
import nanfang_gui
prefs = os.path.expandvars(r"%APPDATA%\Velochron\Velochron\shared_preferences.json")
if not os.path.exists(prefs):
    print("portable_fetch_skip no_prefs")
    raise SystemExit(0)
url = json.load(open(prefs, "r", encoding="utf-8")).get("flutter.last_subscribe_url")
data = nanfang_gui.fetch_subscription_with_fallback(url)
aero = [n for n in data if n.get("type") == "aero_v2"]
print(f"portable_fetch_ok total={len(data)} aero={len(aero)}")
'@

$tmpTest = Join-Path $releaseDir "_portable_test.py"
$testScript | Set-Content -Path $tmpTest -Encoding UTF8
Push-Location $releaseDir
$env:PYTHONHOME = $runtimeDir
$env:TCL_LIBRARY = Join-Path $runtimeDir "tcl\tcl8.6"
$env:TK_LIBRARY = Join-Path $runtimeDir "tcl\tk8.6"
& (Join-Path $runtimeDir "python.exe") $tmpTest
$env:PYTHONHOME = $null
$env:TCL_LIBRARY = $null
$env:TK_LIBRARY = $null
Pop-Location
Remove-Item -LiteralPath $tmpTest -Force
if (Test-Path (Join-Path $releaseDir "__pycache__")) {
    Remove-Item -LiteralPath (Join-Path $releaseDir "__pycache__") -Recurse -Force
}
if (Test-Path (Join-Path $releaseDir "debug.log")) {
    Remove-Item -LiteralPath (Join-Path $releaseDir "debug.log") -Force
}

if (Test-Path $zipPath) {
    Remove-Item -LiteralPath $zipPath -Force
}
Compress-Archive -Path (Join-Path $releaseDir "*") -DestinationPath $zipPath -Force

Write-Host ""
Write-Host "Done:"
Write-Host "  Bundle: $releaseDir"
Write-Host "  Zip:    $zipPath"
