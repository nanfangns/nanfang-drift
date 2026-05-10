package com.nanfang.vpn;

import android.app.Activity;
import android.app.ActivityManager;
import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.content.SharedPreferences;
import android.net.VpnService;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.widget.AdapterView;
import android.widget.ArrayAdapter;
import android.widget.Button;
import android.widget.EditText;
import android.widget.Spinner;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.PrintWriter;
import java.io.StringWriter;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.TimeUnit;

import javax.net.ssl.HostnameVerifier;
import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManager;
import javax.net.ssl.X509TrustManager;

public class MainActivity extends Activity {
    private EditText etUrl;
    private Button btnFetch, btnConnect;
    private Spinner spinnerNodes;
    private TextView tvStatus;

    private String nodesJson = "[]";
    private List<String> nodeNames = new ArrayList<>();
    private boolean vpnRunning = false;
    private SharedPreferences prefs;
    private boolean suppressSelectionCallback = false;
    private boolean switchInProgress = false;
    private int pendingSwitchNodeId = -1;
    private String serviceState = com.nanfang.vpn.VpnService.STATE_DISCONNECTED;
    private boolean statusReceiverRegistered = false;
    private long statusTimeoutGeneration = 0;
    private long statusQueryGeneration = 0;
    private final Handler uiHandler = new Handler(Looper.getMainLooper());

    private final BroadcastReceiver statusReceiver = new BroadcastReceiver() {
        @Override
        public void onReceive(Context context, Intent intent) {
            if (intent == null || !com.nanfang.vpn.VpnService.ACTION_STATUS.equals(intent.getAction())) {
                return;
            }
            String state = intent.getStringExtra(com.nanfang.vpn.VpnService.EXTRA_STATE);
            int nodeId = intent.getIntExtra(com.nanfang.vpn.VpnService.EXTRA_NODE_ID, -1);
            String error = intent.getStringExtra(com.nanfang.vpn.VpnService.EXTRA_ERROR);
            handleServiceStatus(state, nodeId, error);
        }
    };

    private static final int VPN_REQUEST = 100;
    private static final String PREFS = "nanfang_prefs";
    private static final String NODES_FILE = "nodes.json";
    private static final String PREF_VPN_RUNNING = "vpn_running";
    private static final String PREF_ACTIVE_NODE_ID = "active_node_id";
    private static final String PREF_SERVICE_STATE = "vpn_service_state";
    private static final String PREF_STATUS_AT = "vpn_status_at";
    private static final long STATUS_STALE_MS = 30_000L;

    private void dbg(String msg) {
        try {
            File dir = getExternalFilesDir(null);
            if (dir == null) {
                dir = getFilesDir();
            }
            File f = new File(dir, "ui-debug.log");
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
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_main);

        etUrl = findViewById(R.id.etUrl);
        btnFetch = findViewById(R.id.btnFetch);
        btnConnect = findViewById(R.id.btnConnect);
        spinnerNodes = findViewById(R.id.spinnerNodes);
        tvStatus = findViewById(R.id.tvStatus);

        prefs = getSharedPreferences(PREFS, MODE_PRIVATE);
        etUrl.setText(prefs.getString("sub_url", ""));

        btnFetch.setOnClickListener(v -> fetchNodes());
        btnConnect.setOnClickListener(v -> toggleVpn());
        spinnerNodes.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener() {
            @Override
            public void onItemSelected(AdapterView<?> parent, android.view.View view, int position, long id) {
                if (suppressSelectionCallback) {
                    return;
                }
                syncVpnState();
                if (vpnRunning && !isTransientState()) {
                    int activeNodeId = prefs.getInt(PREF_ACTIVE_NODE_ID, -1);
                    int selectedNodeId = getSelectedNodeId();
                    if (selectedNodeId > 0 && activeNodeId > 0 && selectedNodeId != activeNodeId) {
                        requestSwitchNode();
                    }
                }
            }

            @Override
            public void onNothingSelected(AdapterView<?> parent) {
            }
        });

        // Restore saved nodes
        String saved = prefs.getString("nodes", "");
        if (!saved.isEmpty()) {
            loadNodesUI(saved);
        }
        syncVpnState();
    }

    @Override
    protected void onStart() {
        super.onStart();
        registerStatusReceiver();
        syncVpnState();
        requestServiceStatus();
    }

    @Override
    protected void onStop() {
        unregisterStatusReceiver();
        super.onStop();
    }

    private void fetchNodes() {
        String url = etUrl.getText().toString().trim();
        dbg("fetchNodes url=" + url);
        if (url.isEmpty()) {
            Toast.makeText(this, "Please enter subscription URL", Toast.LENGTH_SHORT).show();
            return;
        }

        btnFetch.setEnabled(false);
        btnFetch.setText("Fetching...");

        new Thread(() -> {
            try {
                String requestUrl = normalizeSubscriptionUrl(url);
                String result = normalizeNodesJson(fetchNodesDirect(requestUrl));
                runOnUiThread(() -> {
                    btnFetch.setEnabled(true);
                    btnFetch.setText("Fetch Nodes");
                    loadNodesUI(result);
                    etUrl.setText(requestUrl);
                    prefs.edit().putString("sub_url", requestUrl).putString("nodes", result).apply();
                    dbg("fetchNodes success nodeCount=" + nodeNames.size());
                    Toast.makeText(this, "Loaded " + nodeNames.size() + " nodes", Toast.LENGTH_SHORT).show();
                });
            } catch (Exception e) {
                dbgErr("fetchNodes fail", e);
                runOnUiThread(() -> {
                    btnFetch.setEnabled(true);
                    btnFetch.setText("Fetch Nodes");
                    Toast.makeText(this, "Error: " + e.getMessage(), Toast.LENGTH_LONG).show();
                });
            }
        }).start();
    }

    private String fetchNodesDirect(String urlString) throws Exception {
        TrustManager[] trustAll = new TrustManager[]{new X509TrustManager() {
            @Override
            public void checkClientTrusted(java.security.cert.X509Certificate[] chain, String authType) {
            }

            @Override
            public void checkServerTrusted(java.security.cert.X509Certificate[] chain, String authType) {
            }

            @Override
            public java.security.cert.X509Certificate[] getAcceptedIssuers() {
                return new java.security.cert.X509Certificate[0];
            }
        }};

        SSLContext sslContext = SSLContext.getInstance("TLS");
        sslContext.init(null, trustAll, new java.security.SecureRandom());
        HostnameVerifier allHostsValid = (hostname, session) -> true;

        URL url = new URL(urlString);
        HttpURLConnection conn = (HttpURLConnection) url.openConnection(java.net.Proxy.NO_PROXY);
        if (conn instanceof HttpsURLConnection) {
            ((HttpsURLConnection) conn).setSSLSocketFactory(sslContext.getSocketFactory());
            ((HttpsURLConnection) conn).setHostnameVerifier(allHostsValid);
        }
        conn.setConnectTimeout((int) TimeUnit.SECONDS.toMillis(15));
        conn.setReadTimeout((int) TimeUnit.SECONDS.toMillis(15));
        conn.setRequestProperty("User-Agent", "nanfang-android/1.0");
        conn.setRequestMethod("GET");

        int code = conn.getResponseCode();
        InputStream in = code >= 200 && code < 300 ? conn.getInputStream() : conn.getErrorStream();
        if (in == null) {
            throw new IllegalStateException("HTTP " + code + " with empty body");
        }
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        int n;
        while ((n = in.read(buf)) > 0) {
            out.write(buf, 0, n);
        }
        in.close();
        conn.disconnect();

        String body = out.toString("UTF-8");
        if (code < 200 || code >= 300) {
            throw new IllegalStateException("HTTP " + code + ": " + body);
        }
        return body;
    }

    private String normalizeSubscriptionUrl(String raw) {
        String url = raw.trim();
        if (!url.contains("/api/v1/client/subscribe")) {
            return url;
        }
        StringBuilder out = new StringBuilder(url);
        appendQueryIfMissing(out, url, "flag", "aero");
        appendQueryIfMissing(out, out.toString(), "tz", "8");
        appendQueryIfMissing(out, out.toString(), "lang", "zh_CN");
        appendQueryIfMissing(out, out.toString(), "skip_srs", "1");
        return out.toString();
    }

    private void appendQueryIfMissing(StringBuilder out, String url, String key, String value) {
        if (hasQueryParam(url, key)) {
            return;
        }
        out.append(out.indexOf("?") >= 0 ? "&" : "?");
        out.append(key).append("=").append(value);
    }

    private boolean hasQueryParam(String url, String key) {
        String lower = url.toLowerCase(java.util.Locale.ROOT);
        String target = key.toLowerCase(java.util.Locale.ROOT) + "=";
        int query = lower.indexOf('?');
        if (query < 0) {
            return false;
        }
        String q = lower.substring(query + 1);
        return q.startsWith(target) || q.contains("&" + target);
    }

    private String normalizeNodesJson(String json) throws Exception {
        String trimmed = json.trim();
        if (trimmed.startsWith("[")) {
            JSONArray arr = new JSONArray(trimmed);
            if (arr.length() == 0) {
                throw new IllegalStateException("empty node list");
            }
            return arr.toString();
        }
        JSONObject obj = new JSONObject(trimmed);
        JSONArray outbounds = obj.optJSONArray("outbounds");
        if (outbounds != null && outbounds.length() > 0) {
            return outbounds.toString();
        }
        if (obj.has("server") || obj.has("type")) {
            JSONArray arr = new JSONArray();
            arr.put(obj);
            return arr.toString();
        }
        throw new IllegalStateException("subscription response has no nodes");
    }

    private void loadNodesUI(String json) {
        try {
            nodesJson = normalizeNodesJson(json);
            nodeNames.clear();
            JSONArray arr = new JSONArray(nodesJson);
            for (int i = 0; i < arr.length(); i++) {
                JSONObject n = arr.getJSONObject(i);
                nodeNames.add(n.optString("name", "Node " + n.optInt("node_id", i)));
            }
            ArrayAdapter<String> adapter = new ArrayAdapter<>(this,
                    android.R.layout.simple_spinner_item, nodeNames);
            adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);
            suppressSelectionCallback = true;
            spinnerNodes.setAdapter(adapter);

            // Restore last selected node
            int lastIdx = prefs.getInt("last_node", -1);
            if (lastIdx >= 0 && lastIdx < nodeNames.size()) {
                spinnerNodes.setSelection(lastIdx);
            }
            suppressSelectionCallback = false;
        } catch (Exception e) {
            suppressSelectionCallback = false;
            Toast.makeText(this, "Parse error: " + e.getMessage(), Toast.LENGTH_SHORT).show();
        }
    }

    private void toggleVpn() {
        if (isTransientState()) {
            Toast.makeText(this, "VPN is busy, please wait", Toast.LENGTH_SHORT).show();
            return;
        }
        if (vpnRunning) {
            stopVpn();
        } else {
            startVpn();
        }
    }

    private void startVpn() {
        dbg("startVpn selectedIndex=" + spinnerNodes.getSelectedItemPosition());
        // Write nodes file
        try {
            File f = writeSelectedNodeFile();
            dbg("startVpn wrote nodes file bytes=" + f.length());
        } catch (Exception e) {
            dbgErr("startVpn save nodes fail", e);
            Toast.makeText(this, "Error saving nodes", Toast.LENGTH_SHORT).show();
            return;
        }

        // Check VPN permission
        Intent vpnIntent = VpnService.prepare(this);
        if (vpnIntent != null) {
            startActivityForResult(vpnIntent, VPN_REQUEST);
        } else {
            launchVpnService();
        }
    }

    private String validateNode(JSONObject node) {
        String type = node.optString("type", "").trim().toLowerCase(java.util.Locale.ROOT);
        if (type.isEmpty() && (node.has("aero_v2_edge_psk") || node.has("aero_v2_aead_key") || node.has("edge_psk") || node.has("aead_key"))) {
            type = "aero_v2";
        }
        if (node.optString("server", "").trim().isEmpty()) {
            return "Node missing server, tap Fetch Nodes again";
        }
        if (node.optString("password", "").trim().isEmpty()) {
            return "Node missing password, tap Fetch Nodes again";
        }
        if (node.optInt("node_id", 0) <= 0) {
            return "Node missing node_id, tap Fetch Nodes again";
        }
        if ("aero_v2".equals(type) || "aero".equals(type)) {
            boolean hasEdge = node.has("aero_v2_edge_psk") || node.has("edge_psk") || node.has("psk");
            boolean hasAead = node.has("aero_v2_aead_key") || node.has("aead_key");
            if (!hasEdge || !hasAead || node.optInt("auth_id", 0) <= 0) {
                return "Node fields incomplete, tap Fetch Nodes again";
            }
        }
        return null;
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        if (requestCode == VPN_REQUEST) {
            if (resultCode == RESULT_OK) {
                launchVpnService();
            } else {
                Toast.makeText(this, "VPN permission denied", Toast.LENGTH_SHORT).show();
                serviceState = com.nanfang.vpn.VpnService.STATE_DISCONNECTED;
                applyUiState(prefs.getInt(PREF_ACTIVE_NODE_ID, -1));
            }
        }
    }

    private void launchVpnService() {
        int selectedIndex = spinnerNodes.getSelectedItemPosition();
        prefs.edit().putInt("last_node", selectedIndex).apply();
        dbg("launchVpnService startForegroundService");

        try {
            syncVpnState();
            int selectedNodeId = getSelectedNodeId();
            int activeNodeId = prefs.getInt(PREF_ACTIVE_NODE_ID, -1);
            if (vpnRunning) {
                if (selectedNodeId == activeNodeId) {
                    serviceState = com.nanfang.vpn.VpnService.STATE_CONNECTED;
                    applyUiState(activeNodeId);
                    Toast.makeText(this, "Already connected to selected node", Toast.LENGTH_SHORT).show();
                    return;
                }
                requestSwitchNode();
                dbg("launchVpnService switchNode active=" + activeNodeId + " -> " + selectedNodeId);
                return;
            }
        } catch (Exception e) {
            dbgErr("launchVpnService switch precheck fail", e);
        }

        Intent intent = new Intent(this, com.nanfang.vpn.VpnService.class);
        try {
            startForegroundService(intent);
            serviceState = com.nanfang.vpn.VpnService.STATE_STARTING;
            applyUiState(prefs.getInt(PREF_ACTIVE_NODE_ID, -1));
            scheduleStatusTimeout(18_000L, "VPN start timed out");
        } catch (Exception e) {
            dbgErr("launchVpnService fail", e);
            Toast.makeText(this, "VPN start failed: " + e.getMessage(), Toast.LENGTH_LONG).show();
            serviceState = com.nanfang.vpn.VpnService.STATE_ERROR;
            applyErrorState("VPN start failed: " + e.getMessage());
        }
    }

    private void stopVpn() {
        switchInProgress = false;
        pendingSwitchNodeId = -1;
        statusTimeoutGeneration++;
        Intent intent = new Intent(this, com.nanfang.vpn.VpnService.class);
        intent.setAction(com.nanfang.vpn.VpnService.ACTION_STOP);
        startService(intent);

        prefs.edit()
                .putBoolean(PREF_VPN_RUNNING, false)
                .putInt(PREF_ACTIVE_NODE_ID, -1)
                .putString(PREF_SERVICE_STATE, com.nanfang.vpn.VpnService.STATE_DISCONNECTED)
                .putLong(PREF_STATUS_AT, System.currentTimeMillis())
                .apply();
        serviceState = com.nanfang.vpn.VpnService.STATE_DISCONNECTED;
        applyUiState(-1);
    }

    @Override
    protected void onResume() {
        super.onResume();
        syncVpnState();
        requestServiceStatus();
    }

    private void syncVpnState() {
        prefs = getSharedPreferences(PREFS, MODE_PRIVATE);
        if (isTransientState()) {
            applyUiState(prefs.getInt(PREF_ACTIVE_NODE_ID, -1));
            return;
        }
        boolean runningFlag = prefs.getBoolean(PREF_VPN_RUNNING, false);
        boolean serviceAlive = isVpnServiceRunning();
        int activeNodeId = prefs.getInt(PREF_ACTIVE_NODE_ID, -1);
        long statusAt = prefs.getLong(PREF_STATUS_AT, 0L);
        String persistedState = prefs.getString(PREF_SERVICE_STATE, com.nanfang.vpn.VpnService.STATE_DISCONNECTED);
        boolean hasRecentStatus = statusAt > 0L && System.currentTimeMillis() - statusAt <= STATUS_STALE_MS;
        boolean connectedState = com.nanfang.vpn.VpnService.STATE_CONNECTED.equals(persistedState)
                || com.nanfang.vpn.VpnService.STATE_SWITCHING.equals(persistedState)
                || com.nanfang.vpn.VpnService.STATE_RECONNECTING.equals(persistedState);
        if (runningFlag && serviceAlive && hasRecentStatus && connectedState) {
            serviceState = persistedState;
        } else {
            serviceState = com.nanfang.vpn.VpnService.STATE_DISCONNECTED;
            activeNodeId = -1;
            prefs.edit()
                    .putBoolean(PREF_VPN_RUNNING, false)
                    .putInt(PREF_ACTIVE_NODE_ID, -1)
                    .putString(PREF_SERVICE_STATE, com.nanfang.vpn.VpnService.STATE_DISCONNECTED)
                    .putLong(PREF_STATUS_AT, System.currentTimeMillis())
                    .apply();
        }
        vpnRunning = com.nanfang.vpn.VpnService.STATE_CONNECTED.equals(serviceState)
                || com.nanfang.vpn.VpnService.STATE_SWITCHING.equals(serviceState)
                || com.nanfang.vpn.VpnService.STATE_RECONNECTING.equals(serviceState);
        int selectedNodeId = getSelectedNodeId();
        if (switchInProgress && activeNodeId > 0 && activeNodeId == pendingSwitchNodeId) {
            switchInProgress = false;
            pendingSwitchNodeId = -1;
        }
        if (vpnRunning && selectedNodeId > 0 && activeNodeId > 0 && selectedNodeId != activeNodeId) {
            tvStatus.setText(switchInProgress ? "Status: Switching node" : "Status: Connected - select node to switch");
            tvStatus.setTextColor(0xFF43A047);
            applyControlState(false);
            return;
        }
        applyUiState(activeNodeId);
    }

    private boolean isVpnServiceRunning() {
        try {
            ActivityManager am = (ActivityManager) getSystemService(ACTIVITY_SERVICE);
            if (am == null) {
                return false;
            }
            for (ActivityManager.RunningServiceInfo info : am.getRunningServices(Integer.MAX_VALUE)) {
                if (com.nanfang.vpn.VpnService.class.getName().equals(info.service.getClassName())) {
                    return true;
                }
            }
        } catch (Exception e) {
            dbgErr("isVpnServiceRunning fail", e);
        }
        return false;
    }

    private void registerStatusReceiver() {
        if (statusReceiverRegistered) {
            return;
        }
        IntentFilter filter = new IntentFilter(com.nanfang.vpn.VpnService.ACTION_STATUS);
        if (Build.VERSION.SDK_INT >= 33) {
            registerReceiver(statusReceiver, filter, Context.RECEIVER_NOT_EXPORTED);
        } else {
            registerReceiver(statusReceiver, filter);
        }
        statusReceiverRegistered = true;
    }

    private void unregisterStatusReceiver() {
        if (!statusReceiverRegistered) {
            return;
        }
        try {
            unregisterReceiver(statusReceiver);
        } catch (Exception ignored) {
        }
        statusReceiverRegistered = false;
    }

    private void handleServiceStatus(String state, int nodeId, String error) {
        if (state == null || state.isEmpty()) {
            return;
        }
        statusQueryGeneration++;
        dbg("service status state=" + state + " node=" + nodeId + " error=" + error);
        serviceState = state;
        if (com.nanfang.vpn.VpnService.STATE_CONNECTED.equals(state)) {
            vpnRunning = true;
            switchInProgress = false;
            pendingSwitchNodeId = -1;
            statusTimeoutGeneration++;
        } else if (com.nanfang.vpn.VpnService.STATE_SWITCHING.equals(state)) {
            vpnRunning = true;
            switchInProgress = true;
        } else if (com.nanfang.vpn.VpnService.STATE_RECONNECTING.equals(state)) {
            vpnRunning = true;
        } else if (com.nanfang.vpn.VpnService.STATE_ERROR.equals(state)) {
            vpnRunning = false;
            switchInProgress = false;
            pendingSwitchNodeId = -1;
            statusTimeoutGeneration++;
            applyErrorState(error != null && !error.isEmpty() ? error : "VPN error");
            Toast.makeText(this, error != null && !error.isEmpty() ? error : "VPN error", Toast.LENGTH_LONG).show();
            return;
        } else if (com.nanfang.vpn.VpnService.STATE_DISCONNECTED.equals(state)) {
            vpnRunning = false;
            switchInProgress = false;
            pendingSwitchNodeId = -1;
            statusTimeoutGeneration++;
        }
        applyUiState(nodeId);
    }

    private void requestServiceStatus() {
        long generation = ++statusQueryGeneration;
        try {
            Intent intent = new Intent(this, com.nanfang.vpn.VpnService.class);
            intent.setAction(com.nanfang.vpn.VpnService.ACTION_QUERY);
            startService(intent);
        } catch (Exception e) {
            dbgErr("requestServiceStatus fail", e);
        }
        uiHandler.postDelayed(() -> {
            if (generation != statusQueryGeneration) {
                return;
            }
            if (isTransientState()) {
                return;
            }
            if (!isVpnServiceRunning()) {
                dbg("requestServiceStatus timeout -> mark disconnected");
                prefs.edit()
                        .putBoolean(PREF_VPN_RUNNING, false)
                        .putInt(PREF_ACTIVE_NODE_ID, -1)
                        .putString(PREF_SERVICE_STATE, com.nanfang.vpn.VpnService.STATE_DISCONNECTED)
                        .putLong(PREF_STATUS_AT, System.currentTimeMillis())
                        .commit();
                serviceState = com.nanfang.vpn.VpnService.STATE_DISCONNECTED;
                applyUiState(-1);
            }
        }, 2500);
    }

    private void applyUiState(int activeNodeId) {
        if (com.nanfang.vpn.VpnService.STATE_STARTING.equals(serviceState)) {
            vpnRunning = false;
            btnConnect.setText("Starting...");
            btnConnect.setTextColor(0xFF666666);
            tvStatus.setText("Status: Starting VPN");
            tvStatus.setTextColor(0xFFF9A825);
            applyControlState(false);
            return;
        }
        if (com.nanfang.vpn.VpnService.STATE_SWITCHING.equals(serviceState)) {
            vpnRunning = true;
            btnConnect.setText("Switching...");
            btnConnect.setTextColor(0xFF666666);
            tvStatus.setText("Status: Switching node");
            tvStatus.setTextColor(0xFFF9A825);
            applyControlState(false);
            return;
        }
        if (com.nanfang.vpn.VpnService.STATE_RECONNECTING.equals(serviceState)) {
            vpnRunning = true;
            btnConnect.setText("Reconnecting...");
            btnConnect.setTextColor(0xFF666666);
            tvStatus.setText("Status: Network changed, reconnecting");
            tvStatus.setTextColor(0xFFF9A825);
            applyControlState(false);
            return;
        }
        if (com.nanfang.vpn.VpnService.STATE_CONNECTED.equals(serviceState)) {
            vpnRunning = true;
            btnConnect.setText("Disconnect");
            btnConnect.setTextColor(0xFFE53935);
            tvStatus.setText(activeNodeId > 0 ? "Status: Connected (node " + activeNodeId + ")" : "Status: Connected");
            tvStatus.setTextColor(0xFF43A047);
            applyControlState(true);
            return;
        }
        vpnRunning = false;
        btnConnect.setText("Connect");
        btnConnect.setTextColor(0xFF43A047);
        tvStatus.setText("Status: Disconnected");
        tvStatus.setTextColor(0xFF666666);
        switchInProgress = false;
        pendingSwitchNodeId = -1;
        applyControlState(true);
    }

    private void applyErrorState(String message) {
        serviceState = com.nanfang.vpn.VpnService.STATE_ERROR;
        vpnRunning = false;
        switchInProgress = false;
        pendingSwitchNodeId = -1;
        btnConnect.setText("Connect");
        btnConnect.setTextColor(0xFF43A047);
        tvStatus.setText("Status: Error - " + message);
        tvStatus.setTextColor(0xFFE53935);
        applyControlState(true);
    }

    private void applyControlState(boolean enabled) {
        btnConnect.setEnabled(enabled);
        spinnerNodes.setEnabled(enabled);
        btnFetch.setEnabled(enabled);
    }

    private boolean isTransientState() {
        return com.nanfang.vpn.VpnService.STATE_STARTING.equals(serviceState)
                || com.nanfang.vpn.VpnService.STATE_SWITCHING.equals(serviceState)
                || com.nanfang.vpn.VpnService.STATE_RECONNECTING.equals(serviceState);
    }

    private int getSelectedNodeId() {
        try {
            JSONArray arr = new JSONArray(nodesJson);
            int idx = spinnerNodes.getSelectedItemPosition();
            if (idx >= 0 && idx < arr.length()) {
                return arr.getJSONObject(idx).optInt("node_id", -1);
            }
        } catch (Exception ignored) {
        }
        return -1;
    }

    private File writeSelectedNodeFile() throws Exception {
        File f = new File(getFilesDir(), NODES_FILE);
        JSONArray allNodes = new JSONArray(nodesJson);
        JSONArray selected = new JSONArray();
        int idx = spinnerNodes.getSelectedItemPosition();
        if (idx >= 0 && idx < allNodes.length()) {
            JSONObject node = allNodes.getJSONObject(idx);
            String error = validateNode(node);
            if (error != null) {
                Toast.makeText(this, error + ", refetching...", Toast.LENGTH_LONG).show();
                dbg("writeSelectedNodeFile invalid node: " + error + " node=" + node.toString());
                fetchNodes();
                throw new IllegalStateException(error);
            }
            selected.put(node);
        } else {
            throw new IllegalStateException("Please fetch and select a node first");
        }
        try (FileOutputStream fos = new FileOutputStream(f)) {
            fos.write(selected.toString().getBytes("UTF-8"));
        }
        return f;
    }

    private void requestSwitchNode() {
        try {
            File f = writeSelectedNodeFile();
            int selectedNodeId = getSelectedNodeId();
            if (selectedNodeId <= 0) {
                return;
            }
            if (switchInProgress && pendingSwitchNodeId == selectedNodeId) {
                return;
            }
            dbg("requestSwitchNode nodeId=" + selectedNodeId + " bytes=" + f.length());
            Intent switchIntent = new Intent(this, com.nanfang.vpn.VpnService.class);
            switchIntent.setAction(com.nanfang.vpn.VpnService.ACTION_SWITCH);
            startService(switchIntent);
            serviceState = com.nanfang.vpn.VpnService.STATE_SWITCHING;
            switchInProgress = true;
            pendingSwitchNodeId = selectedNodeId;
            applyUiState(prefs.getInt(PREF_ACTIVE_NODE_ID, -1));
            scheduleStatusTimeout(18_000L, "Node switch timed out");
        } catch (Exception e) {
            dbgErr("requestSwitchNode fail", e);
            applyErrorState("Node switch failed: " + e.getMessage());
        }
    }

    private void scheduleStatusTimeout(long timeoutMs, String message) {
        long generation = ++statusTimeoutGeneration;
        uiHandler.postDelayed(() -> {
            if (generation != statusTimeoutGeneration || !isTransientState()) {
                return;
            }
            dbg("status timeout: " + message + " state=" + serviceState);
            applyErrorState(message);
        }, timeoutMs);
    }
}
