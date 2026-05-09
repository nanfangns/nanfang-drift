#!/bin/bash
set -e

export PATH="D:/jdk17/bin:$PATH"

BT="D:\\android-sdk\\build-tools\\34.0.0"
PLAT="D:\\android-sdk\\platforms\\android-34\\android.jar"
PROJ=app/src/main
BUILD=build

echo "=== NanFang VPN APK Build ==="

rm -rf $BUILD
mkdir -p $BUILD/gen $BUILD/obj $BUILD/dex $BUILD/apk/lib

echo "[1/7] Compile resources..."
cmd.exe //c "$BT\\aapt2.exe compile -o $BUILD\\obj $PROJ\\res\\layout\\activity_main.xml"
cmd.exe //c "$BT\\aapt2.exe compile -o $BUILD\\obj $PROJ\\res\\layout\\activity_main.xml" 2>/dev/null || true

echo "[2/7] Link resources..."
cmd.exe //c "$BT\\aapt2.exe link -o $BUILD\\app.unsigned.apk -I $PLAT --manifest $PROJ\\AndroidManifest.xml --java $BUILD\\gen --auto-add-overlay $BUILD\\obj\\*.flat"

echo "[3/7] Compile Java..."
find $PROJ/java -name '*.java' > /tmp/src_files.txt
find $BUILD/gen -name '*.java' >> /tmp/src_files.txt
D:/jdk17/bin/javac.exe -source 11 -target 11 \
    -classpath "$PLAT;../nanfang.aar" \
    -d $BUILD/dex \
    @/tmp/src_files.txt

echo "[4/7] DEX..."
D:/jdk17/bin/java.exe -Xmx1024M -cp "D:\\android-sdk\\build-tools\\34.0.0\\lib\\d8.jar" com.android.tools.r8.D8 \
    --output $BUILD/dex \
    --lib "$PLAT" \
    --min-api 21 \
    $BUILD/dex/*.class \
    ../nanfang.aar

echo "[5/7] Extract AAR native libs..."
cd $BUILD/apk
python3 -c "
import zipfile, os
with zipfile.ZipFile('../../nanfang.aar') as z:
    for name in z.namelist():
        if name.startswith('jni/'):
            z.extract(name, '.')
" || true
cp $BUILD/dex/classes.dex .
cd ../..

echo "[6/7] Assemble APK..."
python3 -c "
import zipfile, os
src = 'build/app.unsigned.apk'
out = 'build/app.apk'
with zipfile.ZipFile(src, 'r') as zin:
    with zipfile.ZipFile(out, 'w', zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            zout.writestr(item, zin.read(item))
        dex = 'build/apk/classes.dex'
        if os.path.exists(dex):
            zout.write(dex, 'classes.dex')
        for root, dirs, files in os.walk('build/apk'):
            for f in files:
                if f == 'classes.dex':
                    continue
                full = os.path.join(root, f)
                arc = os.path.relpath(full, 'build/apk').replace(os.sep, '/')
                if arc.startswith('lib/'):
                    zout.write(full, arc)
print('APK assembled')
"

echo "[7/7] Zipalign..."
cmd.exe //c "$BT\\zipalign.exe -f 4 $BUILD\\app.apk $BUILD\\app-aligned.apk"

cp $BUILD/app-aligned.apk nanfang-vpn.apk

echo "=== BUILD SUCCESS ==="
ls -lh nanfang-vpn.apk
