import zipfile, os, shutil

aar = '../nanfang.aar'
outdir = 'build/apk'
official_libs = 'official-libs'

with zipfile.ZipFile(aar) as z:
    for name in z.namelist():
        if name.startswith('jni/'):
            # Android APK expects libs at lib/ not jni/
            arcname = name.replace('jni/', 'lib/', 1)
            data = z.read(name)
            dest = os.path.join(outdir, arcname)
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            with open(dest, 'wb') as f:
                f.write(data)
            print(f'Extracted: {name} -> {arcname}')

for abi in ('arm64-v8a', 'armeabi-v7a'):
    src = os.path.join(official_libs, abi, 'libnanocore.so')
    if os.path.exists(src):
        dest = os.path.join(outdir, 'lib', abi, 'libnanocore.so')
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        shutil.copy2(src, dest)
        print(f'Copied official lib: {src} -> {dest}')

print('Done extracting native libs')
