package com.aero.nanocore;

public abstract class NanoCore {
    static {
        System.loadLibrary("nanocore");
    }

    public static native void clearLogs();

    public static native String getLogs(int maxLines);

    public static native String getStats();

    public static native String getTraffic();

    public static native boolean isRunning();

    public static native int nodeLatency(String server, int port);

    public static native void setSocketProtector(SocketProtector protector);

    public static native boolean startService(String configJson, int logLines);

    public static native String status();

    public static native void stopService();

    public static native boolean switchNode(String configJson);

    public static native String urlTest(String nodesJson);

    public static native String urlTestFast(String nodesJson);
}
