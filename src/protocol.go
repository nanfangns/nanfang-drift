package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// aero_v2 protocol constants
const (
	aeroMagicAC2 = "AC2"
	aeroMagicAT2 = "AT2"
	aeroVersion  = 2
	tlsTypeHS    = 0x16
	tlsTypeApp   = 0x17
)

type AeroNode struct {
	ID       int    `json:"node_id"`
	Server   string `json:"server"`
	Port     int    `json:"server_port"`
	Password string `json:"password"`
	EdgePSK  string `json:"aero_v2_edge_psk"`
	AEADKey  string `json:"aero_v2_aead_key"`
	Name     string `json:"name"`
}

func sha256Prefix16(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:16]
}

func hexDecode(s string) []byte {
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		fmt.Sscanf(s[i:i+2], "%02x", &b[i/2])
	}
	return b
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func buildAC2Extension(node *AeroNode, ts int64, rand12 []byte) []byte {
	prefix := sha256Prefix16(node.Password)

	// AC2 header: magic(3) + \0 + version(1) + nodeID(2) + prefix16(16) + timestamp(8) + rand12(12) = 43 bytes
	header := make([]byte, 0, 43)
	header = append(header, aeroMagicAC2...)
	header = append(header, 0) // null separator
	header = append(header, aeroVersion)
	header = append(header, byte(node.ID>>8), byte(node.ID))
	header = append(header, prefix...)
	tsBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBytes, uint64(ts))
	header = append(header, tsBytes...)
	header = append(header, rand12...)

	// HMAC-SHA256(edgePSK, header)[:16]
	edgePSK := hexDecode(node.EdgePSK)
	mac := hmac.New(sha256.New, edgePSK)
	mac.Write(header)
	auth := mac.Sum(nil)[:16]

	return append(header, auth...)
}

func buildClientHello(node *AeroNode) ([]byte, int64) {
	tlsRandom := randBytes(32)
	sessionID := randBytes(32)
	rand12 := randBytes(12)
	ts := time.Now().Unix()

	// supported_groups: x25519
	extGroups := []byte{0x00, 0x0a, 0x00, 0x04, 0x00, 0x02, 0x00, 0x1d}

	// AC2 extension: type(2) + len(2) + blob
	ac2Blob := buildAC2Extension(node, ts, rand12)
	ac2Ext := make([]byte, 4, 4+len(ac2Blob))
	binary.BigEndian.PutUint16(ac2Ext[0:2], 0xFFA5) // extension type
	binary.BigEndian.PutUint16(ac2Ext[2:4], uint16(len(ac2Blob)))
	ac2Ext = append(ac2Ext, ac2Blob...)

	extensions := append(extGroups, ac2Ext...)

	// Cipher suites with length prefix: count(2) + TLS_AES_128_GCM_SHA256(0x1301) + TLS_CHACHA20_POLY1305_SHA256(0x1303)
	cipherSuites := []byte{0x00, 0x04, 0x13, 0x01, 0x13, 0x03}

	// ClientHello body
	body := make([]byte, 0, 512)
	body = append(body, 0x03, 0x03) // legacy_version = TLS 1.2
	body = append(body, tlsRandom...)
	body = append(body, byte(len(sessionID)))
	body = append(body, sessionID...)
	body = append(body, cipherSuites...)
	body = append(body, 0x01, 0x00) // compression: null
	bExtLen := make([]byte, 2)
	binary.BigEndian.PutUint16(bExtLen, uint16(len(extensions)))
	body = append(body, bExtLen...)
	body = append(body, extensions...)

	// Handshake header: type(1) + length(3)
	hsBody := make([]byte, 4)
	hsBody[0] = 1 // ClientHello
	copy(hsBody[1:4], []byte{byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))})
	hsBody = append(hsBody, body...)

	// TLS record: type(1) + version(2) + length(2)
	record := make([]byte, 5)
	record[0] = tlsTypeHS
	binary.BigEndian.PutUint16(record[1:3], 0x0303) // TLS 1.2
	binary.BigEndian.PutUint16(record[3:5], uint16(len(hsBody)))
	record = append(record, hsBody...)

	return record, ts
}

func buildAT2Frame(node *AeroNode, host string, port int, payload []byte) []byte {
	prefix := sha256Prefix16(node.Password)
	hostBytes := []byte(host)

	// AT2 body: magic(3) + \0 + version(1) + prefix16(16) + hostLen(2) + host + port(2) + payloadLen(2) + payload
	body := make([]byte, 0, 24+len(hostBytes)+len(payload))
	body = append(body, aeroMagicAT2...)
	body = append(body, 0) // null
	body = append(body, aeroVersion)
	body = append(body, prefix...)
	bHostLen := make([]byte, 2)
	binary.BigEndian.PutUint16(bHostLen, uint16(len(hostBytes)))
	body = append(body, bHostLen...)
	body = append(body, hostBytes...)
	bPort := make([]byte, 2)
	binary.BigEndian.PutUint16(bPort, uint16(port))
	body = append(body, bPort...)
	bPayloadLen := make([]byte, 2)
	binary.BigEndian.PutUint16(bPayloadLen, uint16(len(payload)))
	body = append(body, bPayloadLen...)
	body = append(body, payload...)

	// TLS ApplicationData record
	record := make([]byte, 5)
	record[0] = tlsTypeApp
	binary.BigEndian.PutUint16(record[1:3], 0x0303) // TLS 1.2
	binary.BigEndian.PutUint16(record[3:5], uint16(len(body)))
	record = append(record, body...)

	return record
}

func recvExact(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func recvTLSRecord(r io.Reader) ([]byte, error) {
	hdr, err := recvExact(r, 5)
	if err != nil {
		return nil, fmt.Errorf("tls header: %w", err)
	}
	length := int(binary.BigEndian.Uint16(hdr[3:5]))
	body, err := recvExact(r, length)
	if err != nil {
		return nil, fmt.Errorf("tls body: %w", err)
	}
	return append(hdr, body...), nil
}

// AeroTunnel wraps a TCP connection for aero_v2 relay.
// After the AC2 handshake + AT2 connect, data flows as raw TCP.
type AeroTunnel struct {
	conn net.Conn
}

func (t *AeroTunnel) Read(p []byte) (int, error)            { return t.conn.Read(p) }
func (t *AeroTunnel) Write(p []byte) (int, error)           { return t.conn.Write(p) }
func (t *AeroTunnel) Close() error                          { return t.conn.Close() }
func (t *AeroTunnel) LocalAddr() net.Addr                   { return t.conn.LocalAddr() }
func (t *AeroTunnel) RemoteAddr() net.Addr                  { return t.conn.RemoteAddr() }
func (t *AeroTunnel) SetDeadline(d time.Time) error         { return t.conn.SetDeadline(d) }
func (t *AeroTunnel) SetReadDeadline(d time.Time) error     { return t.conn.SetReadDeadline(d) }
func (t *AeroTunnel) SetWriteDeadline(d time.Time) error    { return t.conn.SetWriteDeadline(d) }

// OpenAeroTunnel establishes an aero_v2 connection and returns an AeroTunnel.
// Protocol: ClientHello (with AC2) -> ServerHello -> AT2 connect frame -> raw TCP relay.
func OpenAeroTunnel(node *AeroNode, targetHost string, targetPort int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", node.Server, node.Port), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	// Send ClientHello with AC2 extension
	hello, _ := buildClientHello(node)
	if _, err := conn.Write(hello); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}

	// Receive server response (ServerHello)
	_, err = recvTLSRecord(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("recv server: %w", err)
	}

	// Send AT2 connect frame (payload=nil tells server to open tunnel to target)
	at2 := buildAT2Frame(node, targetHost, targetPort, nil)
	if _, err := conn.Write(at2); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send at2: %w", err)
	}

	conn.SetDeadline(time.Time{}) // clear any deadline

	return &AeroTunnel{conn: conn}, nil
}
