# nanfang

`nanfang` is an `aero_v2` client project with multiple experiments in one repo:

- `src/`: Go core, currently usable for local proxy mode, TUN still incomplete
- `nanfang_gui.py`: Windows Python GUI
- `flutter_app/`: planned Win / Android shared Flutter GUI
- `rust_core/`: experimental cross-platform native core

## Can someone clone this repo and use it directly?

No, not reliably.

Reasons:

1. The Windows GUI needs a runtime core binary named `nanfang-core.exe`
2. The Windows portable package also includes a bundled Python runtime
3. The Android app is still only a Flutter shell and does not yet have a real Android-capable proxy core wired in
4. TUN mode is not finished yet

So:

- Cloning the repo gives source code, not a ready-to-run app
- Downloading a Release asset is the correct distribution path

## Recommended release strategy

### Windows

Ship a zip bundle that contains at least:

- `nanfang.exe` (Windows launcher)
- `nanfang-core.exe` (Go runtime core)
- `runtime/` (bundled Python runtime)
- optional extra files you want to ship

The launcher starts `nanfang_gui.py` with the bundled runtime and keeps the
portable package self-contained.

### Android

Do not publish a "real usable" APK yet unless the Android runtime core is wired in.

Current state:

- Windows release: ready to package
- Android release: source scaffold exists, runtime path still incomplete

## Local build

### Go core

```powershell
cd src
D:\breakVelochron\toolchain\go\go\bin\go.exe build -o ..\nanfang-core.exe .
```

## One-step Windows release build

Use:

- `scripts/build_windows_portable_release.ps1`

It will:

1. build `nanfang-core.exe`
2. build the Windows launcher
3. bundle a Python runtime plus `nanfang_gui.py`
4. assemble a distributable bundle into `dist/windows-portable-release/`
5. create `dist/nanfang-windows.zip`

## GitHub Release recommendation

Publish Windows first:

- `nanfang-windows.zip`

Publish Android later, after the runtime core is actually integrated:

- `nanfang-android.apk`
