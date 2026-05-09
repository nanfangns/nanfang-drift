package core

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Tun2Socks manually handles TCP/IP from TUN, forwarding connections through aero_v2 proxy.
type Tun2Socks struct {
	tunFile *os.File
	nodes   []AeroNode
	stopCh  chan struct{}
	mu      sync.Mutex

	// Connection tracking: 4-tuple -> connection state
	conns   map[tcpKey]*tcpConn
	connsMu sync.RWMutex
}

type tcpKey struct {
	srcIP    [4]byte
	srcPort  uint16
	dstIP    [4]byte
	dstPort  uint16
}

type tcpConn struct {
	localSeq   uint32 // our sequence number (kernel expects this as ACK)
	remoteSeq  uint32 // remote's sequence number (we ACK this)
	established bool
	dataCh     chan []byte
	closeCh    chan struct{}
}

var connID uint64

func NewTun2Socks(fd uintptr, nodes []AeroNode) *Tun2Socks {
	return &Tun2Socks{
		tunFile: os.NewFile(fd, "tun"),
		nodes:   nodes,
		stopCh:  make(chan struct{}),
		conns:   make(map[tcpKey]*tcpConn),
	}
}

func (t *Tun2Socks) Stop() {
	select {
	case <-t.stopCh:
	default:
		close(t.stopCh)
	}
	t.connsMu.Lock()
	for k, c := range t.conns {
		close(c.closeCh)
		delete(t.conns, k)
	}
	t.connsMu.Unlock()
	if t.tunFile != nil {
		t.tunFile.Close()
	}
}

func (t *Tun2Socks) Run() {
	defer t.Stop()
	log.Printf("tun2socks: starting raw TCP handler")

	go t.readLoop()

	// Keep running until stopped
	<-t.stopCh
}

func (t *Tun2Socks) readLoop() {
	buf := make([]byte, 65535)
	for {
		select {
		case <-t.stopCh:
			return
		default:
		}

		n, err := t.tunFile.Read(buf)
		if err != nil {
			if t.isStopped() {
				return
			}
			log.Printf("tun2socks: TUN read error: %v", err)
			return
		}
		if n < 20 {
			continue
		}

		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		go t.handlePacket(pkt)
	}
}

func (t *Tun2Socks) handlePacket(pkt []byte) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("tun2socks: panic: %v", r)
		}
	}()

	// Parse IP header
	if len(pkt) < 20 {
		return
	}
	version := pkt[0] >> 4
	if version != 4 {
		return // Only handle IPv4
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl > len(pkt) {
		return
	}
	proto := pkt[9]

	var srcIP, dstIP [4]byte
	copy(srcIP[:], pkt[12:16])
	copy(dstIP[:], pkt[16:20])

	// Handle UDP (DNS on port 53)
	if proto == 17 {
		udpPkt := pkt[ihl:]
		if len(udpPkt) < 8 {
			return
		}
		srcPort := binary.BigEndian.Uint16(udpPkt[0:2])
		dstPort := binary.BigEndian.Uint16(udpPkt[2:4])
		// UDP checksum field at [6:8] — we'll recompute it
		dnsPayload := udpPkt[8:]
		if len(dnsPayload) == 0 {
			return
		}
		// Only handle DNS (port 53)
		if dstPort == 53 {
			t.handleDNS(srcIP, dstIP, srcPort, dnsPayload)
		}
		return
	}

	if proto != 6 { // TCP only
		return
	}

	tcpPkt := pkt[ihl:]
	if len(tcpPkt) < 20 {
		return
	}

	srcPort := binary.BigEndian.Uint16(tcpPkt[0:2])
	dstPort := binary.BigEndian.Uint16(tcpPkt[2:4])
	seqNum := binary.BigEndian.Uint32(tcpPkt[4:8])
	flags := tcpPkt[13]

	syn := flags&0x02 != 0
	ack := flags&0x10 != 0
	fin := flags&0x01 != 0
	rst := flags&0x04 != 0

	key := tcpKey{srcIP, srcPort, dstIP, dstPort}

	if rst {
		t.connsMu.Lock()
		if c, ok := t.conns[key]; ok {
			close(c.closeCh)
			delete(t.conns, key)
		}
		t.connsMu.Unlock()
		return
	}

	if syn && !ack {
		// SYN -> SYN-ACK
		t.handleSYN(key, srcIP, dstIP, srcPort, dstPort, seqNum)
		return
	}

	t.connsMu.RLock()
	conn, ok := t.conns[key]
	t.connsMu.RUnlock()

	if !ok {
		return
	}

	select {
	case <-conn.closeCh:
		return
	default:
	}

	if fin {
		t.handleFIN(conn, key, srcIP, dstIP, srcPort, dstPort, seqNum)
		return
	}

	if ack {
		// Update remote seq based on data length
		dataOffset := int(tcpPkt[12]>>4) * 4
		dataLen := len(tcpPkt) - dataOffset
		if dataLen > 0 {
			conn.remoteSeq = seqNum + uint32(dataLen)
			// Extract TCP payload and send to connection handler
			tcpData := tcpPkt[dataOffset:]
			if len(tcpData) > 0 {
				log.Printf("tun2socks: RX data %d bytes from %d.%d.%d.%d:%d",
					len(tcpData), srcIP[0], srcIP[1], srcIP[2], srcIP[3], srcPort)
				select {
				case conn.dataCh <- tcpData:
				default:
					log.Printf("tun2socks: dataCh full, dropping %d bytes", len(tcpData))
				}
			}
			// ACK any data packet (with or without PSH)
			t.sendAck(conn, srcIP, dstIP, srcPort, dstPort)
		}
	}
}

func (t *Tun2Socks) handleDNS(clientIP [4]byte, dnsServerIP [4]byte, clientPort uint16, query []byte) {
	// Resolve DNS by forwarding the raw query to a real DNS server
	log.Printf("tun2socks: DNS query from %d.%d.%d.%d:%d, %d bytes",
		clientIP[0], clientIP[1], clientIP[2], clientIP[3], clientPort, len(query))

	// Send the raw DNS query to 8.8.8.8:53
	dnsServer := "8.8.8.8:53"
	udpAddr, err := net.ResolveUDPAddr("udp", dnsServer)
	if err != nil {
		log.Printf("tun2socks: DNS resolve addr error: %v", err)
		t.sendDNSServFail(clientIP, dnsServerIP, clientPort, query)
		return
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		log.Printf("tun2socks: DNS dial error: %v", err)
		t.sendDNSServFail(clientIP, dnsServerIP, clientPort, query)
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	_, err = conn.Write(query)
	if err != nil {
		log.Printf("tun2socks: DNS write error: %v", err)
		t.sendDNSServFail(clientIP, dnsServerIP, clientPort, query)
		return
	}

	resp := make([]byte, 4096)
	n, err := conn.Read(resp)
	if err != nil {
		log.Printf("tun2socks: DNS read error: %v", err)
		t.sendDNSServFail(clientIP, dnsServerIP, clientPort, query)
		return
	}
	resp = resp[:n]

	// Build UDP response: srcPort=53, dstPort=clientPort
	udpHdr := make([]byte, 8)
	binary.BigEndian.PutUint16(udpHdr[0:2], 53)       // source port = DNS
	binary.BigEndian.PutUint16(udpHdr[2:4], clientPort) // destination port = client
	udpPayloadLen := len(resp)
	binary.BigEndian.PutUint16(udpHdr[4:6], uint16(8+udpPayloadLen))
	binary.BigEndian.PutUint16(udpHdr[6:8], 0) // checksum placeholder

	// UDP checksum with pseudo-header
	udpChecksum := calcUDPChecksum(dnsServerIP, clientIP, udpHdr, resp)
	binary.BigEndian.PutUint16(udpHdr[6:8], udpChecksum)

	// Build IP packet (protocol = 17 for UDP)
	totalLen := 20 + 8 + udpPayloadLen
	ipPkt := t.buildIPHeaderProto(dnsServerIP, clientIP, totalLen, 17)
	copy(ipPkt[20:], udpHdr)
	copy(ipPkt[28:], resp)

	t.tunWrite(ipPkt)
	log.Printf("tun2socks: DNS response sent %d bytes", len(resp))
}

func (t *Tun2Socks) sendDNSServFail(clientIP [4]byte, dnsServerIP [4]byte, clientPort uint16, query []byte) {
	resp := make([]byte, len(query))
	copy(resp, query)
	if len(resp) >= 3 {
		resp[2] |= 0x80 // QR = response
		resp[3] |= 0x02 // RCODE = ServFail
	}

	udpHdr := make([]byte, 8)
	binary.BigEndian.PutUint16(udpHdr[0:2], 53)
	binary.BigEndian.PutUint16(udpHdr[2:4], clientPort)
	udpPayloadLen := len(resp)
	binary.BigEndian.PutUint16(udpHdr[4:6], uint16(8+udpPayloadLen))
	udpChecksum := calcUDPChecksum(dnsServerIP, clientIP, udpHdr, resp)
	binary.BigEndian.PutUint16(udpHdr[6:8], udpChecksum)

	totalLen := 20 + 8 + udpPayloadLen
	ipPkt := t.buildIPHeaderProto(dnsServerIP, clientIP, totalLen, 17)
	copy(ipPkt[20:], udpHdr)
	copy(ipPkt[28:], resp)

	t.tunWrite(ipPkt)
}

func calcUDPChecksum(srcIP, dstIP [4]byte, udpHdr, payload []byte) uint16 {
	// Pseudo-header: src(4) + dst(4) + zero(1) + proto(1) + udpLen(2)
	udpLen := len(udpHdr) + len(payload)
	pseudo := make([]byte, 12+udpLen)
	copy(pseudo[0:4], srcIP[:])
	copy(pseudo[4:8], dstIP[:])
	pseudo[8] = 0
	pseudo[9] = 17 // UDP
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(udpLen))
	copy(pseudo[12:], udpHdr)
	copy(pseudo[12+len(udpHdr):], payload)

	// Zero out checksum field in the pseudo buffer
	// udpHdr is at offset 12, checksum is at bytes 6-7 of udpHdr = pseudo[18:20]
	pseudo[18] = 0
	pseudo[19] = 0

	sum := uint32(0)
	for i := 0; i < len(pseudo)-1; i += 2 {
		sum += uint32(pseudo[i])<<8 | uint32(pseudo[i+1])
	}
	if len(pseudo)%2 == 1 {
		sum += uint32(pseudo[len(pseudo)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	csum := ^uint16(sum)
	return csum
}

func (t *Tun2Socks) handleSYN(key tcpKey, srcIP, dstIP [4]byte, srcPort, dstPort uint16, seqNum uint32) {
	log.Printf("tun2socks: SYN from %d.%d.%d.%d:%d -> %d.%d.%d.%d:%d",
		srcIP[0], srcIP[1], srcIP[2], srcIP[3], srcPort,
		dstIP[0], dstIP[1], dstIP[2], dstIP[3], dstPort)

	// Check if connection already exists
	t.connsMu.Lock()
	if _, ok := t.conns[key]; ok {
		t.connsMu.Unlock()
		return
	}

	// Create connection state
	localSeq := uint32(atomic.AddUint64(&connID, 1)) * 65537 // random-looking ISN
	conn := &tcpConn{
		localSeq:  localSeq,
		remoteSeq: seqNum + 1, // SYN consumes 1 seq
		dataCh:    make(chan []byte, 256),
		closeCh:   make(chan struct{}),
	}
	t.conns[key] = conn
	t.connsMu.Unlock()

	// Send SYN-ACK
	t.sendSYNACK(conn, srcIP, dstIP, srcPort, dstPort, seqNum)

	// Start connection handler
	go t.handleConnection(key, conn, dstIP, dstPort)
}

func (t *Tun2Socks) handleConnection(key tcpKey, conn *tcpConn, dstIP [4]byte, dstPort uint16) {
	host := fmt.Sprintf("%d.%d.%d.%d", dstIP[0], dstIP[1], dstIP[2], dstIP[3])
	log.Printf("tun2socks: connection to %s:%d", host, dstPort)

	// Open aero_v2 tunnel
	node := PickNode(t.nodes)
	remote, err := OpenAeroTunnel(&node, host, int(dstPort))
	if err != nil {
		log.Printf("tun2socks: tunnel error %s:%d via %s: %v", host, dstPort, node.Name, err)
		t.closeConnection(key, conn)
		return
	}
	defer remote.Close()

	log.Printf("tun2socks: tunnel established %s:%d via %s", host, dstPort, node.Name)

	// Relay data bidirectionally
	var wg sync.WaitGroup
	wg.Add(2)

	// remote -> TUN (read from aero_v2, write to TUN as TCP data)
	go func() {
		defer wg.Done()
		buf := make([]byte, 16384)
		for {
			select {
			case <-conn.closeCh:
				return
			default:
			}
			n, err := remote.Read(buf)
			if err != nil {
				log.Printf("tun2socks: proxy read error: %v", err)
				return
			}
			log.Printf("tun2socks: proxy RX %d bytes for %s:%d", n, host, dstPort)
			t.sendData(conn, key, buf[:n])
		}
	}()

	// TUN -> remote (read from dataCh, write to aero_v2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-conn.closeCh:
				return
			case data := <-conn.dataCh:
				log.Printf("tun2socks: proxy TX %d bytes to %s:%d", len(data), host, dstPort)
				if _, err := remote.Write(data); err != nil {
					log.Printf("tun2socks: proxy write error: %v", err)
					return
				}
			}
		}
	}()

	wg.Wait()
	t.closeConnection(key, conn)
}

func (t *Tun2Socks) closeConnection(key tcpKey, conn *tcpConn) {
	t.connsMu.Lock()
	if c, ok := t.conns[key]; ok {
		select {
		case <-c.closeCh:
		default:
			close(c.closeCh)
		}
		delete(t.conns, key)
	}
	t.connsMu.Unlock()
}

func (t *Tun2Socks) handleFIN(conn *tcpConn, key tcpKey, srcIP, dstIP [4]byte, srcPort, dstPort uint16, seqNum uint32) {
	// Step 1: ACK the remote's FIN (FIN consumes 1 seq byte)
	t.sendAckForFIN(conn, srcIP, dstIP, srcPort, dstPort, seqNum+1)
	// Step 2: Send our own FIN
	t.sendFIN(conn, srcIP, dstIP, srcPort, dstPort)
	t.closeConnection(key, conn)
}

func (t *Tun2Socks) sendSYNACK(conn *tcpConn, srcIP, dstIP [4]byte, srcPort, dstPort uint16, remoteSeq uint32) {
	// Response goes FROM server (dstIP) TO app (srcIP)
	rspSrcIP, rspDstIP := dstIP, srcIP
	rspSrcPort, rspDstPort := dstPort, srcPort

	tcpHdr := make([]byte, 20)
	binary.BigEndian.PutUint16(tcpHdr[0:2], rspSrcPort)
	binary.BigEndian.PutUint16(tcpHdr[2:4], rspDstPort)
	binary.BigEndian.PutUint32(tcpHdr[4:8], conn.localSeq)
	binary.BigEndian.PutUint32(tcpHdr[8:12], remoteSeq+1) // ACK remote's SYN
	tcpHdr[12] = 0x50 // data offset: 5 words
	tcpHdr[13] = 0x12 // SYN+ACK flags
	binary.BigEndian.PutUint16(tcpHdr[14:16], 65535) // window
	tcpHdr[16], tcpHdr[17] = tcpChecksum(rspSrcIP, rspDstIP, tcpHdr)

	ipPkt := t.buildIPHeader(rspSrcIP, rspDstIP, len(tcpHdr))
	copy(ipPkt[20:], tcpHdr)

	t.tunWrite(ipPkt)
	conn.localSeq++ // SYN consumes 1 seq
}

func (t *Tun2Socks) sendAck(conn *tcpConn, srcIP, dstIP [4]byte, srcPort, dstPort uint16) {
	// Response goes FROM server (dstIP) TO app (srcIP)
	rspSrcIP, rspDstIP := dstIP, srcIP
	rspSrcPort, rspDstPort := dstPort, srcPort

	tcpHdr := make([]byte, 20)
	binary.BigEndian.PutUint16(tcpHdr[0:2], rspSrcPort)
	binary.BigEndian.PutUint16(tcpHdr[2:4], rspDstPort)
	binary.BigEndian.PutUint32(tcpHdr[4:8], conn.localSeq)
	binary.BigEndian.PutUint32(tcpHdr[8:12], conn.remoteSeq)
	tcpHdr[12] = 0x50
	tcpHdr[13] = 0x10 // ACK
	binary.BigEndian.PutUint16(tcpHdr[14:16], 65535)
	tcpHdr[16], tcpHdr[17] = tcpChecksum(rspSrcIP, rspDstIP, tcpHdr)

	ipPkt := t.buildIPHeader(rspSrcIP, rspDstIP, len(tcpHdr))
	copy(ipPkt[20:], tcpHdr)
	t.tunWrite(ipPkt)
}

func (t *Tun2Socks) sendData(conn *tcpConn, key tcpKey, data []byte) {
	log.Printf("tun2socks: TX data %d bytes to %d.%d.%d.%d:%d (seq=%d ack=%d)",
		len(data), key.srcIP[0], key.srcIP[1], key.srcIP[2], key.srcIP[3], key.srcPort,
		conn.localSeq, conn.remoteSeq)
	srcIP, dstIP := key.dstIP, key.srcIP
	srcPort, dstPort := key.dstPort, key.srcPort

	tcpHdr := make([]byte, 20)
	binary.BigEndian.PutUint16(tcpHdr[0:2], srcPort)
	binary.BigEndian.PutUint16(tcpHdr[2:4], dstPort)
	binary.BigEndian.PutUint32(tcpHdr[4:8], conn.localSeq)
	binary.BigEndian.PutUint32(tcpHdr[8:12], conn.remoteSeq)
	tcpHdr[12] = 0x50
	tcpHdr[13] = 0x18 // PSH+ACK
	binary.BigEndian.PutUint16(tcpHdr[14:16], 65535)
	// Compute checksum over full TCP header (20 bytes) + data
	tcpHdr[16] = 0
	tcpHdr[17] = 0
	tcpHdr[16], tcpHdr[17] = tcpChecksum(srcIP, dstIP, append(tcpHdr, data...))

	// Build IP+TCP packet
	totalLen := 20 + 20 + len(data)
	ipPkt := t.buildIPHeader(srcIP, dstIP, totalLen)
	copy(ipPkt[20:], tcpHdr)
	copy(ipPkt[40:], data)

	t.tunWrite(ipPkt)
	conn.localSeq += uint32(len(data))
}

func (t *Tun2Socks) sendAckForFIN(conn *tcpConn, srcIP, dstIP [4]byte, srcPort, dstPort uint16, ackSeq uint32) {
	rspSrcIP, rspDstIP := dstIP, srcIP
	rspSrcPort, rspDstPort := dstPort, srcPort

	tcpHdr := make([]byte, 20)
	binary.BigEndian.PutUint16(tcpHdr[0:2], rspSrcPort)
	binary.BigEndian.PutUint16(tcpHdr[2:4], rspDstPort)
	binary.BigEndian.PutUint32(tcpHdr[4:8], conn.localSeq)
	binary.BigEndian.PutUint32(tcpHdr[8:12], ackSeq)
	tcpHdr[12] = 0x50
	tcpHdr[13] = 0x10 // ACK
	binary.BigEndian.PutUint16(tcpHdr[14:16], 65535)
	tcpHdr[16], tcpHdr[17] = tcpChecksum(rspSrcIP, rspDstIP, tcpHdr)

	ipPkt := t.buildIPHeader(rspSrcIP, rspDstIP, len(tcpHdr))
	copy(ipPkt[20:], tcpHdr)
	t.tunWrite(ipPkt)
}

func (t *Tun2Socks) sendFIN(conn *tcpConn, srcIP, dstIP [4]byte, srcPort, dstPort uint16) {
	rspSrcIP, rspDstIP := dstIP, srcIP
	rspSrcPort, rspDstPort := dstPort, srcPort

	tcpHdr := make([]byte, 20)
	binary.BigEndian.PutUint16(tcpHdr[0:2], rspSrcPort)
	binary.BigEndian.PutUint16(tcpHdr[2:4], rspDstPort)
	binary.BigEndian.PutUint32(tcpHdr[4:8], conn.localSeq)
	binary.BigEndian.PutUint32(tcpHdr[8:12], conn.remoteSeq)
	tcpHdr[12] = 0x50
	tcpHdr[13] = 0x11 // FIN+ACK
	binary.BigEndian.PutUint16(tcpHdr[14:16], 65535)
	tcpHdr[16], tcpHdr[17] = tcpChecksum(rspSrcIP, rspDstIP, tcpHdr)

	ipPkt := t.buildIPHeader(rspSrcIP, rspDstIP, len(tcpHdr))
	copy(ipPkt[20:], tcpHdr)
	t.tunWrite(ipPkt)
	conn.localSeq++ // FIN consumes 1 seq
}

func (t *Tun2Socks) buildIPHeader(srcIP, dstIP [4]byte, payloadLen int) []byte {
	return t.buildIPHeaderProto(srcIP, dstIP, payloadLen, 6) // default TCP
}

func (t *Tun2Socks) buildIPHeaderProto(srcIP, dstIP [4]byte, payloadLen int, proto byte) []byte {
	totalLen := 20 + payloadLen
	hdr := make([]byte, totalLen)
	hdr[0] = 0x45 // version=4, ihl=5
	hdr[1] = 0x00  // DSCP
	binary.BigEndian.PutUint16(hdr[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(hdr[4:6], 0) // ID
	binary.BigEndian.PutUint16(hdr[6:8], 0x4000) // DF flag, fragment offset
	hdr[8] = 64   // TTL
	hdr[9] = proto
	// Checksum placeholder
	binary.BigEndian.PutUint16(hdr[10:12], 0)
	copy(hdr[12:16], srcIP[:])
	copy(hdr[16:20], dstIP[:])
	// IP checksum
	binary.BigEndian.PutUint16(hdr[10:12], ipChecksum(hdr))
	return hdr
}

func (t *Tun2Socks) tunWrite(data []byte) {
	if t.tunFile != nil {
		t.tunFile.Write(data)
	}
}

func (t *Tun2Socks) isStopped() bool {
	select {
	case <-t.stopCh:
		return true
	default:
		return false
	}
}

// TCP checksum with pseudo-header
func tcpChecksum(srcIP, dstIP [4]byte, tcpData []byte) (byte, byte) {
	// Pseudo-header: src(4) + dst(4) + zero(1) + proto(1) + tcpLen(2)
	tcpLen := len(tcpData)
	pseudo := make([]byte, 12+tcpLen)
	copy(pseudo[0:4], srcIP[:])
	copy(pseudo[4:8], dstIP[:])
	pseudo[8] = 0
	pseudo[9] = 6 // TCP
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(tcpLen))
	copy(pseudo[12:], tcpData)

	// Zero out existing checksum
	pseudo[12+16] = 0
	pseudo[12+17] = 0

	sum := uint32(0)
	for i := 0; i < len(pseudo)-1; i += 2 {
		sum += uint32(pseudo[i])<<8 | uint32(pseudo[i+1])
	}
	if len(pseudo)%2 == 1 {
		sum += uint32(pseudo[len(pseudo)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	csum := ^uint16(sum)
	return byte(csum >> 8), byte(csum)
}

// IP checksum
func ipChecksum(hdr []byte) uint16 {
	sum := uint32(0)
	// Zero out checksum field
	hdr[10] = 0
	hdr[11] = 0
	for i := 0; i < len(hdr)-1; i += 2 {
		sum += uint32(hdr[i])<<8 | uint32(hdr[i+1])
	}
	if len(hdr)%2 == 1 {
		sum += uint32(hdr[len(hdr)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// Ensure PickNode is used
var _ = PickNode
var _ = io.Copy
