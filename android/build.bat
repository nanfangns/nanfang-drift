@echo off
setlocal enabledelayedexpansion

set JAVA=D:\jdk17\bin
set SDK=D:\android-sdk
set BT=%SDK%\build-tools\34.0.0
set AAPT2=%BT%\aapt2.exe
set D8JAR=%BT%\lib\d8.jar
set ZIPALIGN=%BT%\zipalign.exe
set PLAT=%SDK%\platforms\android-34\android.jar
set PROJ=app\src\main
set BUILD=build

echo === NanFang VPN APK Build ===

if exist %BUILD% rmdir /s /q %BUILD%
mkdir %BUILD%\gen
mkdir %BUILD%\obj
mkdir %BUILD%\dex
mkdir %BUILD%\aar
mkdir %BUILD%\apk\lib

echo [1/7] Compile resources...
for /r %PROJ%\res %%f in (*) do (
    %AAPT2% compile -o %BUILD%\obj\ "%%f"
    if errorlevel 1 (
        echo FAIL: aapt2 compile %%f
        exit /b 1
    )
)

echo [2/7] Link resources...
set FLATS=
for %%f in (%BUILD%\obj\*.flat) do set FLATS=!FLATS! %%f
%AAPT2% link -o %BUILD%\app.unsigned.apk -I %PLAT% --manifest %PROJ%\AndroidManifest.xml --java %BUILD%\gen --auto-add-overlay -A %PROJ%\assets !FLATS!
if errorlevel 1 (echo FAIL: aapt2 link & exit /b 1)

echo [3/7] Extract classes.jar from AAR...
python -c "import zipfile; z=zipfile.ZipFile('../nanfang.aar'); z.extract('classes.jar','build/aar'); z.close()"
if errorlevel 1 (echo FAIL: extract classes.jar & exit /b 1)

echo [4/7] Compile Java...
set SRC_FILES=
for /r %PROJ%\java %%f in (*.java) do set SRC_FILES=!SRC_FILES! %%f
%JAVA%\javac.exe -encoding UTF-8 -classpath "%PLAT%;%BUILD%\aar\classes.jar" -d %BUILD%\dex -sourcepath "%PROJ%\java;%BUILD%\gen" !SRC_FILES! %BUILD%\gen\com\nanfang\vpn\R.java
if errorlevel 1 (echo FAIL: javac & exit /b 1)

echo [5/7] DEX...
python build_dex.py
if errorlevel 1 (echo FAIL: d8 & exit /b 1)

echo [6/7] Extract native libs...
python extract_aar.py
copy %BUILD%\dex_out\classes.dex %BUILD%\apk\ >nul

echo [7/7] Assemble APK...
python assemble.py
if errorlevel 1 (echo FAIL: assemble & exit /b 1)

echo Zipalign...
%ZIPALIGN% -f 4 %BUILD%\app.apk %BUILD%\app-aligned.apk
if errorlevel 1 (echo FAIL: zipalign & exit /b 1)

copy %BUILD%\app-aligned.apk nanfang-vpn.apk >nul

echo [8/8] Sign APK...
if not exist debug.keystore (
    %JAVA%\keytool.exe -genkey -v -keystore debug.keystore -storepass android -alias androiddebugkey -keypass android -keyalg RSA -keysize 2048 -validity 10000 -dname "CN=Android Debug,O=Android,C=US"
)
%JAVA%\java.exe -jar %BT%\lib\apksigner.jar sign --ks debug.keystore --ks-pass pass:android --key-pass pass:android --ks-key-alias androiddebugkey nanfang-vpn.apk
if errorlevel 1 (echo FAIL: sign & exit /b 1)

echo === BUILD SUCCESS ===
echo APK: android\nanfang-vpn.apk
dir nanfang-vpn.apk
