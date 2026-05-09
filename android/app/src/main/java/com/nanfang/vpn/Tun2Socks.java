package com.nanfang.vpn;

import android.os.ParcelFileDescriptor;
import android.system.Os;
import android.system.StructPollfd;
import android.util.Log;

import java.io.FileDescriptor;
import java.io.RandomAccessFile;
import java.net.InetSocketAddress;
import java.nio.ByteBuffer;
import java.nio.channels.SocketChannel;
import java.util.Arrays;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * tun2socks: reads IP packets from TUN, handles DNS (UDP-to-TCP via proxy),
 * and forwards TCP connections through HTTP CONNECT proxy.
 * Pure Java implementation - no native code needed.
 */
public class Tun2Socks {
    private static final String TAG = "Tun2Socks";

    private static final String PROXY_HOST = "127.0.0.1";
    private static final int PROXY_PORT = 7890;
    private static final byte[] DNS_SERVER = {8, 8, 8, 8};
    private static final int BUFSIZE = 65535;

    private static volatile AtomicBoolean stopped = new AtomicBoolean(false);
    private static volatile ParcelFileDescriptor tunPfd;

    // TCP connections: key = "srcIp:srcPort-dstIp:dstPort"
    private static final Map<String, TcpEntry> tcpConns = new ConcurrentHashMap<>();

    // DNS: transaction ID maps to source addr:port
    private static final Map<Integer, byte[]> dnsMap = new ConcurrentHashMap<>();

    private static final ExecutorService pool = Executors.newCachedThreadPool();

    public static void start(ParcelFileDescriptor pfd, String nodesFile) throws Exception {
        stopped.set(false);
        tunPfd = pfd;
        FileDescriptor fd = pfd.getFileDescriptor();
        Log.i(TAG, "tun2socks starting, fd=" + fd);

        // Start local proxy
        startLocalProxy(nodesFile);

        byte[] buf = new byte[BUFSIZE];
        Log.i(TAG, "Entering read loop, fd valid=" + fd.valid());

        int readCount = 0;
        while (!stopped.get()) {
            int n;
            try {
                n = Os.read(fd, buf, 0, buf.length);
            } catch (android.system.ErrnoException e) {
                if (e.errno == 11) { // EAGAIN
                    try { Thread.sleep(10); } catch (InterruptedException ignored) {}
                    continue;
                }
                Log.w(TAG, "TUN read error: " + e.getMessage());
                break;
            } catch (Exception e) {
                Log.w(TAG, "TUN read error: " + e.getClass().getSimpleName() + ": " + e.getMessage());
                break;
            }
            if (n <= 0) {
                Log.i(TAG, "TUN read returned " + n + ", exiting");
                break;
            }
            readCount++;

            byte[] pkt = Arrays.copyOf(buf, n);
            if (pkt[0] >> 4 != 4) continue; // only IPv4

            byte proto = pkt[9];
            if (proto == 17) { // UDP
                processUdp(pkt);
            } else if (proto == 6) { // TCP
                processTcp(pkt);
            }
        }

        Log.i(TAG, "tun2socks exiting");
    }

    public static void stop() {
        stopped.set(true);
        for (TcpEntry e : tcpConns.values()) {
            try { e.channel.close(); } catch (Exception ignored) {}
        }
        tcpConns.clear();
        dnsMap.clear();
        try {
            mobile.Mobile.stopProxy();
        } catch (Exception ignored) {}
        Log.i(TAG, "tun2socks stopped");
    }

    // ==================== PROXY ====================

    private static void startLocalProxy(String nodesFile) {
        pool.execute(() -> {
            try {
                mobile.Mobile.stopProxy();
                mobile.Mobile.startProxy(readFile(nodesFile), 0, PROXY_HOST + ":" + PROXY_PORT);
            } catch (Exception e) {
                Log.e(TAG, "Local proxy failed", e);
            }
        });
        long deadline = System.currentTimeMillis() + 5000;
        while (System.currentTimeMillis() < deadline && !stopped.get()) {
            try (SocketChannel ch = SocketChannel.open()) {
                ch.socket().connect(new InetSocketAddress(PROXY_HOST, PROXY_PORT), 250);
                Log.i(TAG, "Local proxy ready on " + PROXY_HOST + ":" + PROXY_PORT);
                return;
            } catch (Exception ignored) {
                try { Thread.sleep(100); } catch (InterruptedException ignored2) {}
            }
        }
        Log.w(TAG, "Local proxy not ready after timeout");
    }

    // ==================== DNS ====================

    private static void processUdp(byte[] pkt) {
        if (pkt.length < 28) return;

        int ipHdrLen = (pkt[0] & 0x0f) * 4;
        byte[] srcIp = Arrays.copyOfRange(pkt, 12, 16);
        byte[] dstIp = Arrays.copyOfRange(pkt, 16, 20);

        int srcPort = ((pkt[ipHdrLen] & 0xff) << 8) | (pkt[ipHdrLen + 1] & 0xff);
        int dstPort = ((pkt[ipHdrLen + 2] & 0xff) << 8) | (pkt[ipHdrLen + 3] & 0xff);

        if (dstPort != 53) return; // only DNS

        byte[] udpPayload = Arrays.copyOfRange(pkt, ipHdrLen + 8, pkt.length);
        if (udpPayload.length < 12) return;

        int txId = ((udpPayload[0] & 0xff) << 8) | (udpPayload[1] & 0xff);
        int flags = ((udpPayload[2] & 0xff) << 8) | (udpPayload[3] & 0xff);
        if ((flags & 0x8000) != 0) return; // response, skip

        // Save for response routing
        byte[] key = new byte[6];
        System.arraycopy(srcIp, 0, key, 0, 4);
        key[4] = (byte) (srcPort >> 8);
        key[5] = (byte) srcPort;
        dnsMap.put(txId, key);

        pool.execute(() -> forwardDns(txId, udpPayload, srcIp, srcPort));
    }

    private static void forwardDns(int txId, byte[] query, byte[] srcIp, int srcPort) {
        SocketChannel ch = null;
        try {
            ch = SocketChannel.open();
            ch.socket().connect(new InetSocketAddress(PROXY_HOST, PROXY_PORT), 5000);

            // HTTP CONNECT to 8.8.8.8:53
            String req = "CONNECT 8.8.8.8:53 HTTP/1.1\r\nHost: 8.8.8.8:53\r\n\r\n";
            ch.socket().getOutputStream().write(req.getBytes());
            ch.socket().getOutputStream().flush();

            // Read CONNECT response
            byte[] respBuf = new byte[256];
            int total = 0;
            boolean headersDone = false;
            while (total < respBuf.length && !headersDone) {
                int r = ch.socket().getInputStream().read(respBuf, total, respBuf.length - total);
                if (r <= 0) break;
                total += r;
                String s = new String(respBuf, 0, total);
                if (s.contains("\r\n\r\n")) headersDone = true;
            }
            String hdr = new String(respBuf, 0, total);
            if (!hdr.contains("200")) {
                Log.w(TAG, "CONNECT failed for DNS: " + hdr.substring(0, Math.min(80, hdr.length())));
                return;
            }

            // DNS-over-TCP: 2-byte length prefix
            byte[] lenBytes = new byte[]{(byte) (query.length >> 8), (byte) query.length};
            ch.socket().getOutputStream().write(lenBytes);
            ch.socket().getOutputStream().write(query);
            ch.socket().getOutputStream().flush();

            // Read 2-byte length
            byte[] lenBuf = new byte[2];
            readExact(ch, lenBuf, 0, 2);
            int respLen = ((lenBuf[0] & 0xff) << 8) | (lenBuf[1] & 0xff);
            if (respLen > 4096 || respLen < 0) return;

            byte[] respDns = new byte[respLen];
            readExact(ch, respDns, 0, respLen);

            // Build UDP response: IP + UDP + DNS response
            byte[] resp = buildUdpPacket(dstIp(srcIp), srcIp, 53, srcPort, respDns);
            sendToTun(resp);

            Log.i(TAG, "DNS txId=" + txId + " respLen=" + respLen);
        } catch (Exception e) {
            Log.w(TAG, "DNS forward failed txId=" + txId + ": " + e.getMessage());
        } finally {
            closeQuietly(ch);
        }
    }

    // ==================== TCP ====================

    private static void processTcp(byte[] pkt) {
        if (pkt.length < 40) return;

        int ipHdrLen = (pkt[0] & 0x0f) * 4;
        byte[] srcIp = Arrays.copyOfRange(pkt, 12, 16);
        byte[] dstIp = Arrays.copyOfRange(pkt, 16, 20);
        int srcPort = ((pkt[ipHdrLen] & 0xff) << 8) | (pkt[ipHdrLen + 1] & 0xff);
        int dstPort = ((pkt[ipHdrLen + 2] & 0xff) << 8) | (pkt[ipHdrLen + 3] & 0xff);

        long seqNum = readUint32(pkt, ipHdrLen + 4);
        long ackNum = readUint32(pkt, ipHdrLen + 8);

        int tcpHdrLen = ((pkt[ipHdrLen + 12] >> 4) & 0x0f) * 4;
        int flags = pkt[ipHdrLen + 13] & 0xff;
        byte[] data = Arrays.copyOfRange(pkt, ipHdrLen + tcpHdrLen, pkt.length);

        String key = ipStr(srcIp) + ":" + srcPort + "-" + ipStr(dstIp) + ":" + dstPort;

        boolean syn = (flags & 0x02) != 0;
        boolean ack = (flags & 0x10) != 0;
        boolean fin = (flags & 0x01) != 0;
        boolean rst = (flags & 0x04) != 0;

        if (rst) {
            TcpEntry e = tcpConns.remove(key);
            if (e != null) closeQuietly(e.channel);
            return;
        }

        if (syn && !ack) {
            // SYN means open connection through proxy
            String host = ipStr(dstIp);
            pool.execute(() -> openTcpConn(key, srcIp, srcPort, dstIp, dstPort, seqNum, host));
            return;
        }

        TcpEntry e = tcpConns.get(key);
        if (e == null) return;

        if (fin) {
            // ACK the peer FIN, then close our side.
            e.remoteSeq = seqNum + 1;
            sendAck(dstIp, srcIp, dstPort, srcPort, e.localSeq, e.remoteSeq);
            tcpConns.remove(key);
            closeQuietly(e.channel);
            return;
        }

        // DATA
        if (data.length > 0) {
            e.remoteSeq = seqNum + data.length;
            sendAck(dstIp, srcIp, dstPort, srcPort, e.localSeq, e.remoteSeq);
            pool.execute(() -> sendToProxy(e, data));
        }
    }

    private static void openTcpConn(String key, byte[] srcIp, int srcPort,
                                     byte[] dstIp, int dstPort, long seq, String host) {
        SocketChannel ch = null;
        try {
            ch = SocketChannel.open();
            ch.socket().connect(new InetSocketAddress(PROXY_HOST, PROXY_PORT), 5000);

            // HTTP CONNECT
            String req = "CONNECT " + host + ":" + dstPort + " HTTP/1.1\r\nHost: " + host + ":" + dstPort + "\r\n\r\n";
            ch.socket().getOutputStream().write(req.getBytes());
            ch.socket().getOutputStream().flush();

            // Read response
            byte[] buf = new byte[256];
            int total = 0;
            boolean done = false;
            while (total < buf.length && !done) {
                int r = ch.socket().getInputStream().read(buf, total, buf.length - total);
                if (r <= 0) break;
                total += r;
                if (new String(buf, 0, total).contains("\r\n\r\n")) done = true;
            }
            String hdr = new String(buf, 0, total);
            if (!hdr.contains("200")) {
                Log.w(TAG, "CONNECT failed for " + host + ":" + dstPort);
                sendRst(dstIp, srcIp, dstPort, srcPort);
                return;
            }

            long localIsn = 1000;
            TcpEntry entry = new TcpEntry(ch, srcIp, srcPort, dstIp, dstPort, localIsn, seq + 1);
            tcpConns.put(key, entry);

            // SYN-ACK
            sendTcpPacket(dstIp, srcIp, dstPort, srcPort, localIsn, seq + 1, (byte) 0x12);
            entry.localSeq = localIsn + 1;

            Log.i(TAG, "TCP connected " + host + ":" + dstPort);

            // Read loop: proxy to TUN
            byte[] readBuf = new byte[BUFSIZE];
            while (!stopped.get()) {
                int n;
                try {
                    n = ch.socket().getInputStream().read(readBuf);
                } catch (Exception ex) {
                    break;
                }
                if (n <= 0) break;

                byte[] payload = Arrays.copyOf(readBuf, n);

                byte[] respPkt = buildTcpPacket(
                        entry.dstIp, entry.srcIp, entry.dstPort, entry.srcPort,
                        entry.localSeq, entry.remoteSeq, (byte) 0x18, payload); // PSH+ACK
                sendToTun(respPkt);
                entry.localSeq += n;
            }

            // FIN
            sendTcpPacket(dstIp, srcIp, dstPort, srcPort, entry.localSeq, entry.remoteSeq, (byte) 0x11); // FIN+ACK
            entry.localSeq++;
            Log.i(TAG, "TCP closed " + host + ":" + dstPort);
        } catch (Exception e) {
            Log.w(TAG, "TCP connect failed " + host + ":" + dstPort + ": " + e.getMessage());
            sendRst(dstIp, srcIp, dstPort, srcPort);
        } finally {
            tcpConns.remove(key);
            closeQuietly(ch);
        }
    }

    private static void sendToProxy(TcpEntry e, byte[] data) {
        try {
            e.channel.socket().getOutputStream().write(data);
            e.channel.socket().getOutputStream().flush();
        } catch (Exception ex) {
            Log.w(TAG, "sendToProxy error", ex);
            tcpConns.remove(key(e));
            closeQuietly(e.channel);
        }
    }

    private static String key(TcpEntry e) {
        return ipStr(e.srcIp) + ":" + e.srcPort + "-" + ipStr(e.dstIp) + ":" + e.dstPort;
    }

    // ==================== PACKET BUILDING ====================

    private static byte[] buildTcpPacket(byte[] srcIp, byte[] dstIp, int srcPort, int dstPort,
                                          long seqNum, long ackNum, int flags, byte[] payload) {
        int tcpLen = 20 + payload.length;
        int totalLen = 20 + tcpLen;
        byte[] pkt = new byte[totalLen];

        // IP header
        pkt[0] = 0x45;
        pkt[1] = 0;
        pkt[2] = (byte) (totalLen >> 8);
        pkt[3] = (byte) totalLen;
        pkt[4] = 0; pkt[5] = 0;
        pkt[6] = 0; pkt[7] = 0;
        pkt[8] = 64; // TTL
        pkt[9] = 6;  // TCP
        System.arraycopy(srcIp, 0, pkt, 12, 4);
        System.arraycopy(dstIp, 0, pkt, 16, 4);

        // TCP header
        int off = 20;
        pkt[off] = (byte) (srcPort >> 8);
        pkt[off + 1] = (byte) srcPort;
        pkt[off + 2] = (byte) (dstPort >> 8);
        pkt[off + 3] = (byte) dstPort;
        writeUint32(pkt, off + 4, seqNum);
        writeUint32(pkt, off + 8, ackNum);
        pkt[off + 12] = (byte) 0x50; // data offset = 5 words
        pkt[off + 13] = (byte) flags;
        // Window size: 65535 bytes (big enough for data transfer)
        pkt[off + 14] = (byte) 0xFF; pkt[off + 15] = (byte) 0xFF;

        System.arraycopy(payload, 0, pkt, 20, payload.length);

        // IP checksum
        setIpChecksum(pkt);
        // TCP checksum
        setTcpChecksum(pkt, 20, tcpLen, srcIp, dstIp);

        return pkt;
    }

    private static byte[] buildAck(byte[] srcIp, byte[] dstIp, int srcPort, int dstPort,
                                    long seqNum, long ackNum) {
        return buildTcpPacket(srcIp, dstIp, srcPort, dstPort, seqNum, ackNum, 0x10, new byte[0]);
    }

    private static byte[] buildRst(byte[] srcIp, byte[] dstIp, int srcPort, int dstPort) {
        return buildTcpPacket(srcIp, dstIp, srcPort, dstPort, 0, 0, 0x04, new byte[0]);
    }

    private static byte[] buildUdpPacket(byte[] srcIp, byte[] dstIp, int srcPort, int dstPort, byte[] payload) {
        int udpLen = 8 + payload.length;
        int totalLen = 20 + udpLen;
        byte[] pkt = new byte[totalLen];

        // IP
        pkt[0] = 0x45;
        pkt[2] = (byte) (totalLen >> 8);
        pkt[3] = (byte) totalLen;
        pkt[8] = 64;
        pkt[9] = 17; // UDP
        System.arraycopy(srcIp, 0, pkt, 12, 4);
        System.arraycopy(dstIp, 0, pkt, 16, 4);

        // UDP
        int off = 20;
        pkt[off] = (byte) (srcPort >> 8);
        pkt[off + 1] = (byte) srcPort;
        pkt[off + 2] = (byte) (dstPort >> 8);
        pkt[off + 3] = (byte) dstPort;
        pkt[off + 4] = (byte) (udpLen >> 8);
        pkt[off + 5] = (byte) udpLen;

        System.arraycopy(payload, 0, pkt, 28, payload.length);

        setIpChecksum(pkt);
        // UDP checksum optional for IPv4
        return pkt;
    }

    // ==================== CHECKSUM ====================

    private static void setIpChecksum(byte[] pkt) {
        pkt[10] = 0;
        pkt[11] = 0;
        int sum = 0;
        for (int i = 0; i < 20; i += 2) {
            sum += ((pkt[i] & 0xff) << 8) | (pkt[i + 1] & 0xff);
        }
        while ((sum >> 16) != 0) sum = (sum & 0xffff) + (sum >> 16);
        int cksum = ~sum & 0xffff;
        pkt[10] = (byte) (cksum >> 8);
        pkt[11] = (byte) cksum;
    }

    private static void setTcpChecksum(byte[] pkt, int tcpOff, int tcpLen, byte[] srcIp, byte[] dstIp) {
        // Pseudo header + TCP
        int sum = 0;
        // Pseudo header
        for (int i = 0; i < 4; i += 2) {
            sum += ((srcIp[i] & 0xff) << 8) | (srcIp[i + 1] & 0xff);
            sum += ((dstIp[i] & 0xff) << 8) | (dstIp[i + 1] & 0xff);
        }
        sum += 6; // protocol TCP
        sum += tcpLen;
        // TCP header + data
        int end = tcpOff + tcpLen;
        for (int i = tcpOff; i < end - 1; i += 2) {
            sum += ((pkt[i] & 0xff) << 8) | (pkt[i + 1] & 0xff);
        }
        if ((tcpLen & 1) != 0) sum += (pkt[end - 1] & 0xff) << 8;
        while ((sum >> 16) != 0) sum = (sum & 0xffff) + (sum >> 16);
        int cksum = ~sum & 0xffff;
        pkt[tcpOff + 16] = (byte) (cksum >> 8);
        pkt[tcpOff + 17] = (byte) cksum;
    }

    // ==================== HELPERS ====================

    private static void sendTcpPacket(byte[] srcIp, byte[] dstIp, int srcPort, int dstPort,
                                       long seq, long ack, int flags) {
        sendToTun(buildTcpPacket(srcIp, dstIp, srcPort, dstPort, seq, ack, flags, new byte[0]));
    }

    private static void sendAck(byte[] srcIp, byte[] dstIp, int srcPort, int dstPort,
                                 long seq, long ack) {
        sendToTun(buildAck(srcIp, dstIp, srcPort, dstPort, seq, ack));
    }

    private static void sendRst(byte[] srcIp, byte[] dstIp, int srcPort, int dstPort) {
        sendToTun(buildRst(srcIp, dstIp, srcPort, dstPort));
    }

    private static void sendToTun(byte[] pkt) {
        try {
            ParcelFileDescriptor pfd = tunPfd;
            if (pfd != null) {
                Os.write(pfd.getFileDescriptor(), pkt, 0, pkt.length);
            }
        } catch (Exception e) {
            Log.w(TAG, "TUN write error: " + e.getMessage());
        }
    }

    private static byte[] dstIp(byte[] srcIp) {
        return Arrays.copyOf(srcIp, 4);
    }

    private static String ipStr(byte[] ip) {
        return (ip[0] & 0xff) + "." + (ip[1] & 0xff) + "." + (ip[2] & 0xff) + "." + (ip[3] & 0xff);
    }

    private static long readUint32(byte[] b, int off) {
        return ((long) (b[off] & 0xff) << 24) | ((long) (b[off+1] & 0xff) << 16) |
               ((long) (b[off+2] & 0xff) << 8) | (b[off+3] & 0xff);
    }

    private static void writeUint32(byte[] b, int off, long v) {
        b[off] = (byte) (v >> 24);
        b[off+1] = (byte) (v >> 16);
        b[off+2] = (byte) (v >> 8);
        b[off+3] = (byte) v;
    }

    private static void readExact(SocketChannel ch, byte[] buf, int off, int len) throws Exception {
        int read = 0;
        while (read < len) {
            int n = ch.socket().getInputStream().read(buf, off + read, len - read);
            if (n <= 0) throw new Exception("EOF");
            read += n;
        }
    }

    private static void closeQuietly(java.io.Closeable c) {
        if (c != null) try { c.close(); } catch (Exception ignored) {}
    }

    private static String readFile(String path) throws Exception {
        RandomAccessFile f = new RandomAccessFile(path, "r");
        byte[] data = new byte[(int) f.length()];
        f.readFully(data);
        f.close();
        return new String(data, "UTF-8");
    }

    static class TcpEntry {
        final SocketChannel channel;
        final byte[] srcIp;
        final int srcPort;
        final byte[] dstIp;
        final int dstPort;
        long localSeq;
        long remoteSeq;

        TcpEntry(SocketChannel ch, byte[] srcIp, int srcPort, byte[] dstIp, int dstPort,
                 long localSeq, long remoteSeq) {
            this.channel = ch;
            this.srcIp = srcIp;
            this.srcPort = srcPort;
            this.dstIp = dstIp;
            this.dstPort = dstPort;
            this.localSeq = localSeq;
            this.remoteSeq = remoteSeq;
        }
    }
}
