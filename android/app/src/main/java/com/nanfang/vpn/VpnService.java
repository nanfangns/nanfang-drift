package com.nanfang.vpn;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.content.Intent;
import android.net.ConnectivityManager;
import android.net.IpPrefix;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.os.Build;
import android.os.ParcelFileDescriptor;
import android.util.Log;

import com.aero.nanocore.NanoCore;
import com.aero.nanocore.SocketProtector;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.io.PrintWriter;
import java.io.StringWriter;
import java.net.Inet4Address;
import java.net.InetAddress;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;

public class VpnService extends android.net.VpnService implements SocketProtector {
    private static final String TAG = "NanFangVPN";
    private static final String CHANNEL_ID = "nanfang_vpn";
    public static final String ACTION_STOP = "STOP";
    public static final String ACTION_SWITCH = "SWITCH_NODE";
    public static final String ACTION_QUERY = "QUERY_STATUS";
    public static final String ACTION_STATUS = "com.nanfang.vpn.STATUS";
    public static final String EXTRA_STATE = "state";
    public static final String EXTRA_NODE_ID = "node_id";
    public static final String EXTRA_ERROR = "error";
    public static final String EXTRA_TRAFFIC = "traffic";
    public static final String STATE_STARTING = "starting";
    public static final String STATE_CONNECTED = "connected";
    public static final String STATE_SWITCHING = "switching";
    public static final String STATE_RECONNECTING = "reconnecting";
    public static final String STATE_DISCONNECTED = "disconnected";
    public static final String STATE_ERROR = "error";
    private static final String NODES_FILE = "nodes.json";
    private static final String NANO_DIR = "nanocore";
    private static final String PREFS = "nanfang_prefs";
    private static final String PREF_VPN_RUNNING = "vpn_running";
    private static final String PREF_ACTIVE_NODE_ID = "active_node_id";
    private static final String PREF_SERVICE_STATE = "vpn_service_state";
    private static final String PREF_STATUS_AT = "vpn_status_at";
    private static final String GEOIP_FILE = "geoip-cn.mrs";
    private static final String GEOSITE_FILE = "geosite-cn.mrs";
    private static final String CNCIDR_ROUTES_FILE = "cncidr-v4-14.txt";
    private static final String TUN_ADDR = "172.19.0.1";
    private static final int TUN_PREFIX = 30;
    private static final int TUN_MTU = 1350;
    private static final String FAKEIP_ADDR = "198.18.0.1";
    private static final String FAKEIP_DNS = "198.18.0.2";
    private static final int FAKEIP_PREFIX = 16;
    private static final String FAKEIP_V6_ADDR = "fd66:1234:5678:9abc::1";
    private static final int FAKEIP_V6_PREFIX = 64;
    private static final long NETWORK_RECOVERY_DEBOUNCE_MS = 1200L;
    private static final long PROTECT_RETRY_WAIT_MS = 3000L;
    private static final long PROTECT_RETRY_POLL_MS = 150L;

    private volatile boolean running;
    private ParcelFileDescriptor tunFd;
    private int detachedTunFd = -1;
    private Thread statsThread;
    private volatile boolean statsLoopRunning;
    private volatile String currentState = STATE_DISCONNECTED;
    private volatile int activeNodeId = -1;
    private volatile long networkChangeGeneration = 0L;
    private ConnectivityManager.NetworkCallback networkCallback;
    private final Object lifecycleLock = new Object();
    private volatile long ignoreNetworkEventsUntil = 0L;

    private void dbg(String msg) {
        Log.i(TAG, msg);
        try {
            File dir = getExternalFilesDir(null);
            if (dir == null) {
                dir = getFilesDir();
            }
            File f = new File(dir, "vpn-debug.log");
            try (FileOutputStream out = new FileOutputStream(f, true)) {
                out.write(("[" + System.currentTimeMillis() + "] " + msg + "\n").getBytes("UTF-8"));
            }
        } catch (Exception ignored) {
        }
    }

    private void dbgErr(String prefix, Throwable t) {
        StringWriter sw = new StringWriter();
        t.printStackTrace(new PrintWriter(sw));
        dbg(prefix + " :: " + sw.toString());
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        dbg("onStartCommand action=" + (intent != null ? intent.getAction() : "null"));
        if (intent != null && ACTION_STOP.equals(intent.getAction())) {
            dbg("STOP received");
            stopVpn();
            stopForeground(true);
            stopSelf();
            return START_NOT_STICKY;
        }

        if (intent != null && ACTION_QUERY.equals(intent.getAction())) {
            dbg("QUERY received");
            if (running) {
                publishStatus(currentState, activeNodeId, null, null);
                return START_STICKY;
            }
            publishStatus(STATE_DISCONNECTED, -1, null, null);
            stopSelf(startId);
            return START_NOT_STICKY;
        }

        if (intent != null && ACTION_SWITCH.equals(intent.getAction())) {
            if (running) {
                handleSwitchRequest();
                return START_STICKY;
            }
            dbg("SWITCH received while not running; falling through to normal start");
        }

        createNotificationChannel();
        startForeground(1, buildNotification("VPN starting..."));

        if (running) {
            dbg("Already running");
            publishStatus(STATE_CONNECTED, activeNodeId, null, null);
            return START_STICKY;
        }

        return startVpnFromSelectedNode(STATE_STARTING, "VPN starting...", true) ? START_STICKY : START_NOT_STICKY;
    }

    @Override
    public void onDestroy() {
        stopVpn();
        super.onDestroy();
    }

    @Override
    public void onRevoke() {
        stopVpn();
        super.onRevoke();
    }

    @Override
    public boolean protect(int fd) {
        boolean ok = false;
        try {
            ok = super.protect(fd);
        } catch (Exception e) {
            dbgErr("protect(" + fd + ") failed", e);
        }
        if (!ok && waitForUsablePhysicalNetwork("protect_retry")) {
            try {
                ok = super.protect(fd);
                dbg("protect retry fd=" + fd + " ok=" + ok);
            } catch (Exception e) {
                dbgErr("protect retry fd=" + fd + " failed", e);
            }
        }
        if (!ok) {
            dbg("protect(" + fd + ")=false");
        }
        return ok;
    }

    private void stopVpn() {
        synchronized (lifecycleLock) {
            cleanupVpnResources();
            activeNodeId = -1;
            publishStatus(STATE_DISCONNECTED, -1, null, null);
        }
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel ch = new NotificationChannel(CHANNEL_ID, "VPN", NotificationManager.IMPORTANCE_LOW);
            ch.setDescription("NanFang VPN service");
            ((NotificationManager) getSystemService(NOTIFICATION_SERVICE)).createNotificationChannel(ch);
        }
    }

    private Notification buildNotification(String text) {
        return new Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("NanFang VPN")
                .setContentText(text)
                .setSmallIcon(android.R.drawable.ic_lock_lock)
                .setOngoing(true)
                .build();
    }

    private void updateNotification(String text) {
        NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
        if (nm != null) {
            nm.notify(1, buildNotification(text));
        }
    }

    private boolean startVpnFromSelectedNode(String state, String notification, boolean stopSelfOnFailure) {
        synchronized (lifecycleLock) {
            publishStatus(state, activeNodeId, null, null);
            try {
                File nodesFile = new File(getFilesDir(), NODES_FILE);
                if (!nodesFile.exists()) {
                    throw new IllegalStateException("nodes.json not found");
                }
                dbg("nodes file=" + nodesFile.getAbsolutePath() + " bytes=" + nodesFile.length());

                JSONObject selectedNode = readSelectedNode(nodesFile);
                dbg("selected node type=" + selectedNode.optString("type") + " node_id=" + selectedNode.optInt("node_id") + " name=" + selectedNode.optString("name"));
                File nanoDir = prepareNanoDir();
                Builder builder = buildVpnBuilder();
                tunFd = builder.establish();
                if (tunFd == null) {
                    throw new IllegalStateException("builder.establish returned null");
                }

                detachedTunFd = tunFd.detachFd();
                dbg("TUN established fd=" + detachedTunFd);

                NanoCore.setSocketProtector(this);
                dbg("NanoCore.setSocketProtector done");
                JSONObject config = buildNanoCoreConfig(selectedNode, nanoDir, detachedTunFd);
                dbg("Starting NanoCore, type=" + config.optString("type") + " node=" + config.optInt("node_id") + " config=" + config.toString());
                if (!NanoCore.startService(config.toString(), 50)) {
                    throw new IllegalStateException("NanoCore.startService returned false");
                }
                dbg("NanoCore.startService returned true");
                try {
                    dbg("NanoCore.status=" + NanoCore.status());
                    dbg("NanoCore.logs=" + NanoCore.getLogs(20));
                } catch (Throwable t) {
                    dbgErr("post-start native calls failed", t);
                }

                running = true;
                activeNodeId = config.optInt("node_id", -1);
                persistVpnState(true, activeNodeId, STATE_CONNECTED);
                updateNotification("VPN connected");
                startStatsLoop();
                startNetworkCallback();
                ignoreNetworkEventsUntil = System.currentTimeMillis() + 1500L;
                publishStatus(STATE_CONNECTED, activeNodeId, null, null);
                return true;
            } catch (Exception e) {
                dbgErr("Failed to start VPN", e);
                cleanupVpnResources();
                updateNotification("VPN error: " + e.getMessage());
                publishStatus(STATE_ERROR, -1, e.getMessage(), null);
                if (stopSelfOnFailure) {
                    stopForeground(true);
                    stopSelf();
                }
                return false;
            }
        }
    }

    private void handleSwitchRequest() {
        synchronized (lifecycleLock) {
            publishStatus(STATE_SWITCHING, activeNodeId, null, null);
            try {
                File nodesFile = new File(getFilesDir(), NODES_FILE);
                JSONObject selectedNode = readSelectedNode(nodesFile);
                File nanoDir = prepareNanoDir();
                JSONObject config = buildNanoCoreConfig(selectedNode, nanoDir, detachedTunFd);
                int newNodeId = config.optInt("node_id", -1);
                dbg("SWITCH request node_id=" + newNodeId + " type=" + config.optString("type"));
                boolean switched = false;
                try {
                    switched = NanoCore.switchNode(config.toString());
                } catch (Exception e) {
                    dbgErr("NanoCore.switchNode threw", e);
                }
                if (!switched) {
                    dbg("SWITCH direct failed; restarting core with current TUN");
                    try {
                        NanoCore.stopService();
                    } catch (Exception e) {
                        dbgErr("NanoCore.stopService before switch restart failed", e);
                    }
                    Thread.sleep(250);
                    if (!NanoCore.startService(config.toString(), 50)) {
                        dbg("SWITCH current TUN restart failed; rebuilding VPN");
                        cleanupVpnResources();
                        if (!startVpnFromSelectedNode(STATE_SWITCHING, "VPN switching...", false)) {
                            throw new IllegalStateException("switch rebuild returned false");
                        }
                        return;
                    }
                }
                running = true;
                activeNodeId = newNodeId;
                persistVpnState(true, activeNodeId, STATE_CONNECTED);
                updateNotification(switched ? "VPN switched" : "VPN restarted on new node");
                dbg("SWITCH success node_id=" + activeNodeId + " direct=" + switched);
                publishStatus(STATE_CONNECTED, activeNodeId, null, null);
            } catch (Exception e) {
                dbgErr("SWITCH failed", e);
                publishStatus(STATE_ERROR, activeNodeId, "Node switch failed: " + e.getMessage(), null);
                if (running) {
                    publishStatus(STATE_CONNECTED, activeNodeId, null, null);
                }
            }
        }
    }

    private File prepareNanoDir() throws Exception {
        File nanoDir = new File(getFilesDir(), NANO_DIR);
        if (!nanoDir.exists() && !nanoDir.mkdirs()) {
            throw new IllegalStateException("failed to create nanocore dir");
        }
        extractAssetIfMissing(GEOIP_FILE, new File(nanoDir, GEOIP_FILE));
        extractAssetIfMissing(GEOSITE_FILE, new File(nanoDir, GEOSITE_FILE));
        return nanoDir;
    }

    private Builder buildVpnBuilder() throws Exception {
        Builder builder = new Builder()
                .setSession("NanFang")
                .setMtu(TUN_MTU)
                .addRoute("0.0.0.0", 0)
                .addAddress(TUN_ADDR, TUN_PREFIX)
                .addAddress(FAKEIP_ADDR, FAKEIP_PREFIX)
                .addDnsServer(FAKEIP_DNS);

        try {
            builder.addRoute("::", 0);
            builder.addAddress(FAKEIP_V6_ADDR, FAKEIP_V6_PREFIX);
        } catch (Exception ignored) {
        }

        if (Build.VERSION.SDK_INT >= 29) {
            try {
                builder.setMetered(false);
            } catch (Exception ignored) {
            }
        }

        excludeSystemRoutes(builder);

        try {
            builder.addDisallowedApplication(getPackageName());
        } catch (Exception e) {
            Log.w(TAG, "addDisallowedApplication failed", e);
        }
        addDomesticBypassApplications(builder);
        return builder;
    }

    private void addDomesticBypassApplications(Builder builder) {
        String[] packages = new String[]{
                "com.ss.android.ugc.aweme",
                "com.ss.android.ugc.aweme.lite",
                "com.ss.android.ugc.live",
                "com.ss.android.article.news",
                "com.ss.android.article.lite",
                "com.ss.android.ugc.live.lite",
                "com.ss.android.auto",
                "com.ss.android.ugc.aweme.mobile",
                "com.tencent.mm",
                "com.tencent.mobileqq",
                "com.tencent.tim",
                "com.tencent.qqlive",
                "com.tencent.qqmusic",
                "com.tencent.weishi",
                "com.eg.android.AlipayGphone",
                "com.taobao.taobao",
                "com.tmall.wireless",
                "com.taobao.idlefish",
                "com.jingdong.app.mall",
                "com.sankuai.meituan",
                "com.dianping.v1",
                "com.sina.weibo",
                "com.netease.cloudmusic",
                "com.netease.mobimail",
                "tv.danmaku.bili",
                "com.bilibili.app.in",
                "com.bilibili.studio",
                "com.smile.gifmaker",
                "com.kuaishou.nebula",
                "com.xingin.xhs",
                "com.zhihu.android",
                "com.baidu.BaiduMap",
                "com.baidu.netdisk",
                "ctrip.android.view",
                "com.Qunar",
                "com.autonavi.minimap"
        };
        for (String packageName : packages) {
            try {
                builder.addDisallowedApplication(packageName);
                dbg("domestic app bypass added " + packageName);
            } catch (Exception ignored) {
            }
        }
    }

    private void cleanupVpnResources() {
        running = false;
        stopNetworkCallback();
        stopStatsLoop();
        try {
            NanoCore.stopService();
        } catch (Exception e) {
            dbgErr("NanoCore.stopService failed", e);
        }
        if (detachedTunFd >= 0) {
            try {
                ParcelFileDescriptor.adoptFd(detachedTunFd).close();
            } catch (Exception ignored) {
            }
            detachedTunFd = -1;
        }
        if (tunFd != null) {
            try {
                tunFd.close();
            } catch (Exception ignored) {
            }
            tunFd = null;
        }
        persistVpnState(false, -1, STATE_DISCONNECTED);
    }

    private void startStatsLoop() {
        stopStatsLoop();
        statsLoopRunning = true;
        statsThread = new Thread(() -> {
            while (statsLoopRunning) {
                try {
                    String traffic = NanoCore.getTraffic();
                    String status = NanoCore.status();
                    dbg("stats traffic=" + traffic + " status=" + status);
                    updateNotification("VPN connected");
                    if (running && STATE_CONNECTED.equals(currentState)) {
                        publishStatus(STATE_CONNECTED, activeNodeId, null, traffic);
                    }
                } catch (Exception e) {
                    dbgErr("stats loop failed", e);
                }
                try {
                    Thread.sleep(5000);
                } catch (InterruptedException ignored) {
                    return;
                }
            }
        }, "nanfang-stats");
        statsThread.start();
    }

    private void stopStatsLoop() {
        statsLoopRunning = false;
        if (statsThread != null) {
            statsThread.interrupt();
            statsThread = null;
        }
    }

    private void startNetworkCallback() {
        if (networkCallback != null) {
            return;
        }
        try {
            Object svc = getSystemService(CONNECTIVITY_SERVICE);
            if (!(svc instanceof ConnectivityManager)) {
                return;
            }
            ConnectivityManager cm = (ConnectivityManager) svc;
            networkCallback = new ConnectivityManager.NetworkCallback() {
                @Override
                public void onAvailable(Network network) {
                    dbg("network available " + network);
                    scheduleNetworkRecovery("available");
                }

                @Override
                public void onCapabilitiesChanged(Network network, NetworkCapabilities networkCapabilities) {
                    if (networkCapabilities != null && !networkCapabilities.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) {
                        dbg("network capabilities changed " + network);
                        scheduleNetworkRecovery("capabilities");
                    }
                }

                @Override
                public void onLost(Network network) {
                    dbg("network lost " + network);
                    scheduleNetworkRecovery("lost");
                }
            };
            cm.registerDefaultNetworkCallback(networkCallback);
            dbg("network callback registered");
        } catch (Exception e) {
            dbgErr("register network callback failed", e);
            networkCallback = null;
        }
    }

    private void stopNetworkCallback() {
        ConnectivityManager.NetworkCallback callback = networkCallback;
        if (callback == null) {
            return;
        }
        networkCallback = null;
        try {
            Object svc = getSystemService(CONNECTIVITY_SERVICE);
            if (svc instanceof ConnectivityManager) {
                ((ConnectivityManager) svc).unregisterNetworkCallback(callback);
            }
        } catch (Exception ignored) {
        }
    }

    private void scheduleNetworkRecovery(String reason) {
        if (!running || System.currentTimeMillis() < ignoreNetworkEventsUntil) {
            return;
        }
        long generation = ++networkChangeGeneration;
        new Thread(() -> {
            try {
                Thread.sleep(NETWORK_RECOVERY_DEBOUNCE_MS);
            } catch (InterruptedException ignored) {
                return;
            }
            if (generation != networkChangeGeneration || !running) {
                return;
            }
            recoverAfterNetworkChange(reason);
        }, "nanfang-network-recover").start();
    }

    private void recoverAfterNetworkChange(String reason) {
        synchronized (lifecycleLock) {
            if (!running) {
                return;
            }
            publishStatus(STATE_RECONNECTING, activeNodeId, null, null);
            updateNotification("VPN reconnecting...");
            if (!waitForUsablePhysicalNetwork("network_" + reason)) {
                publishStatus(STATE_CONNECTED, activeNodeId, null, null);
                return;
            }
            try {
                File nodesFile = new File(getFilesDir(), NODES_FILE);
                JSONObject selectedNode = readSelectedNode(nodesFile);
                File nanoDir = prepareNanoDir();
                JSONObject config = buildNanoCoreConfig(selectedNode, nanoDir, detachedTunFd);
                dbg("network recovery reason=" + reason + " node_id=" + config.optInt("node_id"));
                boolean switched = false;
                try {
                    switched = NanoCore.switchNode(config.toString());
                } catch (Exception e) {
                    dbgErr("network recovery switch threw", e);
                }
                if (!switched) {
                    try {
                        NanoCore.stopService();
                    } catch (Exception e) {
                        dbgErr("network recovery stop failed", e);
                    }
                    Thread.sleep(250);
                    if (!NanoCore.startService(config.toString(), 50)) {
                        dbg("network recovery current TUN restart failed; rebuilding VPN");
                        cleanupVpnResources();
                        if (!startVpnFromSelectedNode(STATE_RECONNECTING, "VPN reconnecting...", false)) {
                            throw new IllegalStateException("network recovery rebuild returned false");
                        }
                        return;
                    }
                }
                activeNodeId = config.optInt("node_id", activeNodeId);
                persistVpnState(true, activeNodeId, STATE_CONNECTED);
                updateNotification("VPN connected");
                publishStatus(STATE_CONNECTED, activeNodeId, null, null);
                dbg("network recovery success direct=" + switched);
            } catch (Exception e) {
                dbgErr("network recovery failed", e);
                publishStatus(STATE_ERROR, activeNodeId, "Network recovery failed: " + e.getMessage(), null);
                publishStatus(STATE_CONNECTED, activeNodeId, null, null);
            }
        }
    }

    private boolean waitForUsablePhysicalNetwork(String reason) {
        long started = System.currentTimeMillis();
        while (!hasUsablePhysicalNetwork()) {
            long waited = System.currentTimeMillis() - started;
            if (waited >= PROTECT_RETRY_WAIT_MS) {
                dbg("physical network wait timeout reason=" + reason + " waited=" + waited);
                return false;
            }
            try {
                Thread.sleep(PROTECT_RETRY_POLL_MS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return false;
            }
        }
        return true;
    }

    private boolean hasUsablePhysicalNetwork() {
        try {
            Object svc = getSystemService(CONNECTIVITY_SERVICE);
            if (!(svc instanceof ConnectivityManager)) {
                return true;
            }
            ConnectivityManager cm = (ConnectivityManager) svc;
            for (Network network : cm.getAllNetworks()) {
                NetworkCapabilities caps = cm.getNetworkCapabilities(network);
                if (caps == null || caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) {
                    continue;
                }
                boolean hasPhysicalTransport = caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)
                        || caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR)
                        || caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET);
                boolean usable = caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                        && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED);
                if (hasPhysicalTransport && usable) {
                    return true;
                }
            }
        } catch (Exception e) {
            dbgErr("hasUsablePhysicalNetwork failed", e);
            return true;
        }
        return false;
    }

    private JSONObject readSelectedNode(File file) throws Exception {
        String text = readFile(file);
        if (text.trim().startsWith("[")) {
            JSONArray arr = new JSONArray(text);
            return selectUsableNode(arr);
        }
        JSONObject obj = new JSONObject(text);
        JSONArray outbounds = obj.optJSONArray("outbounds");
        if (outbounds != null) {
            return selectUsableNode(outbounds);
        }
        return obj;
    }

    private JSONObject buildNanoCoreConfig(JSONObject node, File nanoDir, int tunFd) throws Exception {
        JSONObject cfg = new JSONObject();
        String type = normalizeNodeType(node);
        String server = node.optString("server", "");
        int port = node.optInt("server_port", node.optInt("port", defaultPort(type)));
        if (server.isEmpty()) {
            throw new IllegalStateException("node server is empty");
        }
        if (port <= 0) {
            throw new IllegalStateException("node server_port is invalid");
        }
        cfg.put("mode", "tun");
        cfg.put("type", type);
        copyIfPresent(node, cfg, "name");
        cfg.put("server", server);
        cfg.put("server_port", port);
        copyIfPresent(node, cfg, "server_ip");
        copyIfPresent(node, cfg, "password");
        copyIfPresent(node, cfg, "auth_id");
        copyIfPresent(node, cfg, "node_id");
        copyIfPresent(node, cfg, "sni");
        copyIfPresent(node, cfg, "pool_size");
        copyIfPresent(node, cfg, "udp_port");
        copyIfPresent(node, cfg, "mtu");
        copyIfPresent(node, cfg, "iplc_mode");
        copyIfPresent(node, cfg, "udp_lane_mode");
        copyIfPresent(node, cfg, "resume_ticket");
        copyIfPresent(node, cfg, "resume_ttl_sec");
        copyStringAlias(node, cfg, "aero_v2_edge_psk", "edge_psk", "psk");
        copyStringAlias(node, cfg, "aero_v2_aead_key", "aead_key");
        copyStringAlias(node, cfg, "morph_api_path", "api_path");
        copyIfPresent(node, cfg, "morph_sni_root");
        copyStringAlias(node, cfg, "morph_static_pub", "static_pub");
        if ("anytls".equals(type)) {
            cfg.put("anytls_pool_size", node.optInt("pool_size", 4));
        }

        cfg.put("tun_fd", tunFd);
        cfg.put("tun_mtu", TUN_MTU);
        cfg.put("routing_mode", "rule");

        JSONObject routing = node.optJSONObject("routing");
        String domesticDns = routing != null ? routing.optString("domestic_dns", "") : "";
        if (domesticDns.isEmpty()) {
            domesticDns = "223.5.5.5:53";
        }
        cfg.put("domestic_dns", domesticDns);
        cfg.put("geosite_srs_path", new File(nanoDir, GEOSITE_FILE).getAbsolutePath());
        cfg.put("geoip_srs_path", new File(nanoDir, GEOIP_FILE).getAbsolutePath());
        cfg.put("system_dns_fallback", collectSystemDns());
        cfg.put("extra_rules", buildDefaultExtraRules());
        JSONArray rules = node.optJSONArray("subscription_rules");
        if (rules == null && routing != null) {
            rules = routing.optJSONArray("rules");
        }
        cfg.put("subscription_rules", rules != null ? rules : new JSONArray());
        return cfg;
    }

    private JSONObject selectUsableNode(JSONArray nodes) throws Exception {
        if (nodes.length() == 0) {
            throw new IllegalStateException("empty node list");
        }
        JSONObject fallback = null;
        for (int i = 0; i < nodes.length(); i++) {
            JSONObject node = nodes.optJSONObject(i);
            if (node == null) {
                continue;
            }
            if (fallback == null) {
                fallback = node;
            }
            String type = normalizeNodeType(node);
            if ("aero_v2".equals(type) || "aero".equals(type) || "anytls".equals(type) || "morph".equals(type)) {
                return node;
            }
        }
        if (fallback == null) {
            throw new IllegalStateException("no object node found");
        }
        return fallback;
    }

    private String normalizeNodeType(JSONObject node) {
        String type = node.optString("type", "").trim().toLowerCase(Locale.ROOT);
        if (type.isEmpty()) {
            if (node.has("aero_v2_edge_psk") || node.has("aero_v2_aead_key") || node.has("edge_psk") || node.has("aead_key")) {
                type = "aero_v2";
            } else if (node.has("morph_static_pub") || node.has("static_pub") || node.has("morph_api_path") || node.has("api_path")) {
                type = "morph";
            } else {
                type = "aero";
            }
        }
        return type;
    }

    private int defaultPort(String type) {
        if ("anytls".equals(type)) {
            return 9443;
        }
        if ("morph".equals(type)) {
            return 443;
        }
        return 20443;
    }

    private void copyStringAlias(JSONObject src, JSONObject dst, String canonical, String... aliases) throws Exception {
        if (src.has(canonical)) {
            dst.put(canonical, src.get(canonical));
            return;
        }
        for (String alias : aliases) {
            String value = src.optString(alias, "");
            if (!value.isEmpty()) {
                dst.put(canonical, value);
                return;
            }
        }
    }

    private void copyIfPresent(JSONObject src, JSONObject dst, String key) throws Exception {
        if (src.has(key)) {
            dst.put(key, src.get(key));
        }
    }

    private JSONArray buildDefaultExtraRules() throws Exception {
        JSONArray rules = new JSONArray();
        addPrivateCidrRules(rules);
        addProxyDomainRules(rules);
        addDirectDomainRules(rules);
        String publicDomesticAction = publicDomesticAction();
        addRule(rules, "geosite_cn", "", publicDomesticAction);
        addRule(rules, "geoip_cn", "", publicDomesticAction);
        addRule(rules, "match", "", "Proxy");
        return rules;
    }

    private String publicDomesticAction() {
        return Build.VERSION.SDK_INT >= 33 ? "Direct" : "Proxy";
    }

    private void addDirectDomainRules(JSONArray rules) throws Exception {
        String action = publicDomesticAction();
        String[] suffixes = new String[]{
                "cn", "中国", "公司", "网络",
                "douyin.com", "douyinpic.com", "douyinstatic.com", "douyinvod.com", "douyinliving.com",
                "amemv.com", "amemv.net", "ixigua.com", "snssdk.com", "pstatp.com", "toutiao.com",
                "byteimg.com", "bytedance.com", "bytedance.net", "bytecdn.cn", "bytecdn.com", "zijieapi.com",
                "volces.com", "volccdn.com", "volcengine.com", "feelgood.cn", "ibytedtos.com", "ibytedapm.com",
                "bdurl.net", "bdstatic.com", "baidu.com", "baidubce.com", "bcebos.com",
                "qq.com", "gtimg.com", "qpic.cn", "weixin.qq.com", "wechat.com", "tenpay.com",
                "alicdn.com", "aliyun.com", "alipay.com", "taobao.com", "tmall.com", "mmstat.com", "uc.cn",
                "jd.com", "jd.hk", "jdpay.com", "360buyimg.com",
                "bilibili.com", "biliapi.com", "bilivideo.com", "hdslb.com",
                "kuaishou.com", "ksapisrv.com", "gifshow.com", "yximgs.com",
                "xiaomi.com", "mi.com", "miui.com", "oppo.com", "coloros.com", "heytapmobi.com",
                "vivo.com", "huawei.com", "hicloud.com", "hihonor.com", "honor.cn",
                "meituan.com", "dianping.com", "ctrip.com", "qunar.com", "12306.cn",
                "netease.com", "163.com", "126.net", "music.163.com", "sina.com.cn", "weibo.com",
                "zhihu.com", "xiaohongshu.com", "xhscdn.com"
        };
        for (String suffix : suffixes) {
            addRule(rules, "domain_suffix", suffix, action);
        }
        String[] keywords = new String[]{
                "douyin", "aweme", "bytedance", "bytecdn", "toutiao", "ixigua", "pstatp",
                "alicdn", "aliyun", "alipay", "taobao", "tmall", "qq", "wechat",
                "weixin", "baidu", "bilibili", "kuaishou", "xiaohongshu", "netease"
        };
        for (String keyword : keywords) {
            addRule(rules, "domain_keyword", keyword, action);
        }
        String[] exactDomains = new String[]{
                "api.amemv.com",
                "aweme.snssdk.com",
                "www.douyin.com"
        };
        for (String domain : exactDomains) {
            addRule(rules, "domain_exact", domain, action);
        }
    }

    private void addPrivateCidrRules(JSONArray rules) throws Exception {
        String[] cidrs = new String[]{
                "10.0.0.0/8",
                "172.16.0.0/12",
                "192.168.0.0/16",
                "100.64.0.0/10",
                "169.254.0.0/16",
                "224.0.0.0/4"
        };
        for (String cidr : cidrs) {
            addRule(rules, "ip_cidr", cidr, "Direct");
        }
    }

    private void addProxyDomainRules(JSONArray rules) throws Exception {
        String[] exactDomains = new String[]{
                "ldcstore.com",
                "www.ldcstore.com",
                "ldstore.cc.cd",
                "www.ldstore.cc.cd",
                "api2.ldspro.qzz.io",
                "api1.ldspro.qzz.io",
                "api.ldspro.qzz.io",
                "img.ldspro.qzz.io",
                "credit.linux.do",
                "linux.do",
                "mxana.tacool.com",
                "static.cloudflareinsights.com",
                "challenges.cloudflare.com",
                "a.nel.cloudflare.com"
        };
        for (String domain : exactDomains) {
            addRule(rules, "domain_exact", domain, "Proxy");
        }

        String[] suffixes = new String[]{
                "ldcstore.com",
                "ldstore.cc.cd",
                "qzz.io",
                "linux.do",
                "workers.dev",
                "tacool.com",
                "cloudflareinsights.com",
                "cloudflare.com",
                "googleapis.com",
                "googleusercontent.com",
                "gvt1.com",
                "gvt2.com",
                "ggpht.com",
                "googlevideo.com",
                "android.com",
                "google.com",
                "google.cn",
                "gstatic.com",
                "youtube.com",
                "ytimg.com"
        };
        for (String suffix : suffixes) {
            addRule(rules, "domain_suffix", suffix, "Proxy");
        }

        addRule(rules, "domain_keyword", "play-fe", "Proxy");
        addRule(rules, "domain_keyword", "android.clients.google", "Proxy");
    }

    private void addRule(JSONArray rules, String type, String value, String action) throws Exception {
        JSONObject rule = new JSONObject();
        rule.put("type", type);
        rule.put("value", value);
        rule.put("action", action.toLowerCase(Locale.ROOT));
        rules.put(rule);
    }

    private void excludeSystemRoutes(Builder builder) {
        if (Build.VERSION.SDK_INT < 33) {
            return;
        }
        String[] routes = new String[]{
                "10.0.0.0/8",
                "172.16.0.0/12",
                "192.168.0.0/16",
                "100.64.0.0/10",
                "169.254.0.0/16",
                "224.0.0.0/4",
                "255.255.255.255/32"
        };
        int count = 0;
        for (String route : routes) {
            if (excludeRoute(builder, route)) {
                count++;
            }
        }
        count += excludeRoutesFromAsset(builder, CNCIDR_ROUTES_FILE);
        dbg("excludeRoute installed count=" + count);
    }

    private int excludeRoutesFromAsset(Builder builder, String assetName) {
        int count = 0;
        try (BufferedReader reader = new BufferedReader(new InputStreamReader(getAssets().open(assetName), "UTF-8"))) {
            String line;
            while ((line = reader.readLine()) != null) {
                String route = line.trim();
                if (route.isEmpty() || route.startsWith("#")) {
                    continue;
                }
                if (excludeRoute(builder, route)) {
                    count++;
                }
            }
        } catch (Exception e) {
            dbgErr("excludeRoutesFromAsset " + assetName + " failed", e);
        }
        return count;
    }

    private boolean excludeRoute(Builder builder, String route) {
        try {
            String[] parts = route.split("/");
            builder.excludeRoute(new IpPrefix(InetAddress.getByName(parts[0]), Integer.parseInt(parts[1])));
            return true;
        } catch (Exception e) {
            Log.w(TAG, "excludeRoute " + route + " failed", e);
            return false;
        }
    }

    private JSONArray collectSystemDns() {
        LinkedHashSet<String> dns = new LinkedHashSet<>();
        try {
            Object svc = getSystemService(CONNECTIVITY_SERVICE);
            if (!(svc instanceof ConnectivityManager)) {
                return new JSONArray();
            }
            ConnectivityManager cm = (ConnectivityManager) svc;
            for (Network network : cm.getAllNetworks()) {
                NetworkCapabilities caps = cm.getNetworkCapabilities(network);
                if (caps != null && caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) {
                    continue;
                }
                LinkProperties lp = cm.getLinkProperties(network);
                if (lp == null) {
                    continue;
                }
                List<InetAddress> servers = lp.getDnsServers();
                if (servers == null) {
                    continue;
                }
                for (InetAddress addr : servers) {
                    if (addr instanceof Inet4Address) {
                        String host = addr.getHostAddress();
                        if (host != null && !host.startsWith("198.18.") && !host.startsWith("0.")) {
                            dns.add(host + ":53");
                        }
                    }
                }
            }
        } catch (Exception e) {
            Log.w(TAG, "collectSystemDns failed", e);
        }
        return new JSONArray(dns);
    }

    private void extractAssetIfMissing(String assetName, File dest) throws Exception {
        if (dest.exists() && dest.length() > 0) {
            return;
        }
        try (InputStream in = getAssets().open(assetName);
             FileOutputStream out = new FileOutputStream(dest)) {
            byte[] buf = new byte[8192];
            int n;
            while ((n = in.read(buf)) > 0) {
                out.write(buf, 0, n);
            }
        }
    }

    private static String readFile(File file) throws Exception {
        try (InputStream in = new java.io.FileInputStream(file)) {
            java.io.ByteArrayOutputStream out = new java.io.ByteArrayOutputStream();
            byte[] buf = new byte[8192];
            int n;
            while ((n = in.read(buf)) > 0) {
                out.write(buf, 0, n);
            }
            return out.toString("UTF-8");
        }
    }

    private void publishStatus(String state, int nodeId, String error, String traffic) {
        currentState = state;
        if (STATE_CONNECTED.equals(state)) {
            activeNodeId = nodeId;
            persistVpnState(true, nodeId, state);
        } else if (STATE_DISCONNECTED.equals(state) || STATE_ERROR.equals(state)) {
            persistVpnState(false, STATE_ERROR.equals(state) ? nodeId : -1, state);
        } else {
            persistVpnState(running, nodeId, state);
        }
        Intent intent = new Intent(ACTION_STATUS);
        intent.setPackage(getPackageName());
        intent.putExtra(EXTRA_STATE, state);
        intent.putExtra(EXTRA_NODE_ID, nodeId);
        if (error != null) {
            intent.putExtra(EXTRA_ERROR, error);
        }
        if (traffic != null) {
            intent.putExtra(EXTRA_TRAFFIC, traffic);
        }
        sendBroadcast(intent);
    }

    private void persistVpnState(boolean isRunning, int nodeId, String state) {
        try {
            getSharedPreferences(PREFS, MODE_PRIVATE)
                    .edit()
                    .putBoolean(PREF_VPN_RUNNING, isRunning)
                    .putInt(PREF_ACTIVE_NODE_ID, nodeId)
                    .putString(PREF_SERVICE_STATE, state)
                    .putLong(PREF_STATUS_AT, System.currentTimeMillis())
                    .commit();
        } catch (Exception ignored) {
        }
    }
}
