import zipfile, os

src = 'build/app.unsigned.apk'
out = 'build/app.apk'

with zipfile.ZipFile(src, 'r') as zin:
    with zipfile.ZipFile(out, 'w', zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            zout.writestr(item, zin.read(item))

        # Add classes.dex
        dex = 'build/apk/classes.dex'
        if os.path.exists(dex):
            zout.write(dex, 'classes.dex')

        # Add native libs
        for root, dirs, files in os.walk('build/apk'):
            for f in files:
                if f == 'classes.dex':
                    continue
                full = os.path.join(root, f)
                arc = os.path.relpath(full, 'build/apk').replace(os.sep, '/')
                if arc.startswith('lib/'):
                    zout.write(full, arc)

print('APK assembled')
