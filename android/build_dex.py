"""Pack all .class files into classes.jar, run d8, produce classes.dex"""
import subprocess, os, sys, zipfile

BT = r"D:\android-sdk\build-tools\34.0.0"
JAVA = r"D:\jdk17\bin\java.exe"
D8JAR = os.path.join(BT, "lib", "d8.jar")
PLAT = r"D:\android-sdk\platforms\android-34\android.jar"

CLASS_DIR = "build/dex"
DEX_JAR = "build/classes.jar"
DEX_OUT = "build/dex_out"

# Step 1: pack .class files into a jar
print("Packing classes into jar...")
with zipfile.ZipFile(DEX_JAR, 'w', zipfile.ZIP_DEFLATED) as zf:
    for root, dirs, files in os.walk(CLASS_DIR):
        for f in files:
            if f.endswith('.class'):
                full = os.path.join(root, f)
                arc = os.path.relpath(full, CLASS_DIR).replace(os.sep, '/')
                zf.write(full, arc)
                print(f"  {arc}")

# Step 2: create clean output dir
if os.path.exists(DEX_OUT):
    import shutil
    shutil.rmtree(DEX_OUT)
os.makedirs(DEX_OUT)

# Step 3: run d8
print("Running d8...")
cmd = [
    JAVA, "-Xmx1024M", "-cp", D8JAR,
    "com.android.tools.r8.D8",
    "--output", DEX_OUT,
    "--lib", PLAT,
    "--min-api", "21",
    DEX_JAR,
    r"..\nanfang.aar"
]
print(" ".join(cmd))
result = subprocess.run(cmd, capture_output=True, text=True)
print(result.stdout)
if result.returncode != 0:
    print("STDERR:", result.stderr)
    sys.exit(1)

# Check output
dex_path = os.path.join(DEX_OUT, "classes.dex")
if os.path.exists(dex_path):
    size = os.path.getsize(dex_path)
    with open(dex_path, 'rb') as f:
        data = f.read()
    has_main = b'MainActivity' in data
    has_tun = b'Tun2Socks' in data
    has_vpn = b'VpnService' in data
    has_go = b'go/Seq' in data
    print(f"classes.dex: {size} bytes")
    print(f"  MainActivity: {has_main}")
    print(f"  Tun2Socks: {has_tun}")
    print(f"  VpnService: {has_vpn}")
    print(f"  go/Seq: {has_go}")
else:
    print("ERROR: classes.dex not found!")
    sys.exit(1)

print("Done!")
