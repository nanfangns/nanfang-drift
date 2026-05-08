package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ===== wintun.dll pure Go wrapper (no CGo) =====

var (
	modWintun             *syscall.LazyDLL
	procCreateAdapter     *syscall.LazyProc
	procOpenAdapter       *syscall.LazyProc
	procCloseAdapter      *syscall.LazyProc
	procGetReadWaitEvent  *syscall.LazyProc
	procDeleteDriver      *syscall.LazyProc
	procStartSession      *syscall.LazyProc
	procEndSession        *syscall.LazyProc
	procReceivePacket     *syscall.LazyProc
	procSendPacket        *syscall.LazyProc
)

func init() {
	// Try loading wintun.dll from the executable's directory
	exePath, _ := os.Executable()
	dllPath := exePath[:len(exePath)-len(filepath.Base(exePath))] + "wintun.dll"
	if _, err := os.Stat(dllPath); err == nil {
		modWintun = syscall.NewLazyDLL(dllPath)
	} else {
		modWintun = syscall.NewLazyDLL("wintun.dll")
	}
	procCreateAdapter = modWintun.NewProc("WintunCreateAdapter")
	procOpenAdapter = modWintun.NewProc("WintunOpenAdapter")
	procCloseAdapter = modWintun.NewProc("WintunCloseAdapter")
	procGetReadWaitEvent = modWintun.NewProc("WintunGetReadWaitEvent")
	procDeleteDriver = modWintun.NewProc("WintunDeleteDriver")
	procStartSession = modWintun.NewProc("WintunStartSession")
	procEndSession = modWintun.NewProc("WintunEndSession")
	procReceivePacket = modWintun.NewProc("WintunReceivePacket")
	procSendPacket = modWintun.NewProc("WintunSendPacket")
}

type wintunAdapter struct {
	handle uintptr // WINTUN_ADAPTER* (opaque C pointer)
}

type wintunSession struct {
	adapter *wintunAdapter
	handle  uintptr // WINTUN_SESSION_HANDLE (opaque C pointer)
	event   uintptr // read wait event from WintunGetReadWaitEvent
}

func wintunCreateAdapter(name, tunnelType string) (*wintunAdapter, error) {
	name16, _ := syscall.UTF16PtrFromString(name)
	tunnelType16, _ := syscall.UTF16PtrFromString(tunnelType)
	var reboot uintptr

	fmt.Printf("DEBUG: DLL=%v proc=%v\n", modWintun, procCreateAdapter)

	// Try creating a new adapter
	r, _, err := procCreateAdapter.Call(
		uintptr(unsafe.Pointer(name16)),
		uintptr(unsafe.Pointer(tunnelType16)),
		uintptr(unsafe.Pointer(&reboot)),
	)
	fmt.Printf("DEBUG: Create result=0x%x err=%v\n", r, err)
	if r != 0 {
		return &wintunAdapter{handle: r}, nil
	}

	// Create failed — try opening existing adapter
	r, _, err = procOpenAdapter.Call(uintptr(unsafe.Pointer(name16)))
	fmt.Printf("DEBUG: Open result=0x%x err=%v\n", r, err)
	if r != 0 {
		// Opened successfully — close it and try creating fresh
		procCloseAdapter.Call(r)
		r, _, err = procCreateAdapter.Call(
			uintptr(unsafe.Pointer(name16)),
			uintptr(unsafe.Pointer(tunnelType16)),
			uintptr(unsafe.Pointer(&reboot)),
		)
		fmt.Printf("DEBUG: Create2 result=0x%x err=%v\n", r, err)
		if r != 0 {
			return &wintunAdapter{handle: r}, nil
		}
	}

	return nil, fmt.Errorf("cannot create TUN adapter '%s' (try killing leftover nanfang processes)", name)
}

func (a *wintunAdapter) close() {
	procCloseAdapter.Call(a.handle)
}

func wintunDeleteDriver() {
	procDeleteDriver.Call()
}

func (a *wintunAdapter) startSession(capacity uint32) (*wintunSession, error) {
	// Try starting a new session
	r, _, startErr := procStartSession.Call(a.handle, uintptr(capacity))
	if r != 0 {
		// New session started successfully
		evt, _, _ := procGetReadWaitEvent.Call(a.handle)
		if evt == 0 {
			procEndSession.Call(r)
			return nil, fmt.Errorf("WintunGetReadWaitEvent failed")
		}
		return &wintunSession{adapter: a, handle: r, event: evt}, nil
	}

	// WintunStartSession failed — the adapter may already be initialized
	// (from a previous process that didn't clean up). Try to get the event
	// handle anyway and use the adapter without a new session.
	evt, _, evtErr := procGetReadWaitEvent.Call(a.handle)
	if evt == 0 {
		return nil, fmt.Errorf("WintunStartSession: %v; WintunGetReadWaitEvent: %v", startErr, evtErr)
	}
	// Return session with handle=0 (no session to end)
	return &wintunSession{adapter: a, handle: 0, event: evt}, nil
}

func (s *wintunSession) end() {
	if s.handle != 0 {
		procEndSession.Call(s.handle)
	}
}

// ReceivePacket reads one IP packet from the TUN device.
// Call WaitForSingleObject(s.event) before calling this.
func (s *wintunSession) ReceivePacket(buf []byte) (int, error) {
	var size uint32
	r, _, err := procReceivePacket.Call(
		s.handle,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 {
		return 0, fmt.Errorf("WintunReceivePacket: %v", err)
	}
	return int(size), nil
}

// SendPacket writes one IP packet to the TUN device.
func (s *wintunSession) SendPacket(buf []byte) error {
	r, _, err := procSendPacket.Call(
		s.handle,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r == 0 {
		return fmt.Errorf("WintunSendPacket: %v", err)
	}
	return nil
}

// ===== IP / TCP utilities =====

func ipChecksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(header); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func tcpChecksum(src, dst net.IP, tcpData []byte) uint16 {
	tcpLen := len(tcpData)
	// Pseudo-header
	ph := make([]byte, 12)
	copy(ph[0:4], src.To4())
	copy(ph[4:8], dst.To4())
	ph[8] = 0
	ph[9] = 6 // TCP
	binary.BigEndian.PutUint16(ph[10:12], uint16(tcpLen))

	var sum uint32
	for i := 0; i+1 < len(ph); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(ph[i : i+2]))
	}
	for i := 0; i+1 < tcpLen; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(tcpData[i : i+2]))
	}
	if tcpLen&1 == 1 {
		sum += uint32(tcpData[tcpLen-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// buildIPPacket constructs a full IP+TCP packet with correct checksums.
func buildIPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16,
	seq, ack uint32, flags uint8, payload []byte) []byte {

	tcpHeaderLen := 20
	tcpTotalLen := tcpHeaderLen + len(payload)
	ipHeaderLen := 20
	ipTotalLen := ipHeaderLen + tcpTotalLen

	// Build TCP header first (needed for checksum)
	tcp := make([]byte, tcpTotalLen)
	binary.BigEndian.PutUint16(tcp[0:2], srcPort)
	binary.BigEndian.PutUint16(tcp[2:4], dstPort)
	binary.BigEndian.PutUint32(tcp[4:8], seq)
	binary.BigEndian.PutUint32(tcp[8:12], ack)
	tcp[12] = 0x50 // data offset = 5 words (5 << 4)
	tcp[13] = flags
	binary.BigEndian.PutUint16(tcp[14:16], 65535) // window
	// checksum at [16:18] — filled below
	binary.BigEndian.PutUint16(tcp[18:20], 0) // urgent pointer
	copy(tcp[20:], payload)

	// TCP checksum
	csum := tcpChecksum(srcIP, dstIP, tcp)
	binary.BigEndian.PutUint16(tcp[16:18], csum)

	// Build IP header
	ip := make([]byte, ipHeaderLen)
	ip[0] = 0x45 // version=4, IHL=5
	ip[1] = 0x00  // DSCP
	binary.BigEndian.PutUint16(ip[2:4], uint16(ipTotalLen))
	binary.BigEndian.PutUint16(ip[4:6], 0) // ID
	binary.BigEndian.PutUint16(ip[6:8], 0) // flags+fragment
	ip[8] = 64                              // TTL
	ip[9] = 6                               // protocol: TCP
	// checksum at [10:12] — filled below
	copy(ip[12:16], srcIP.To4())
	copy(ip[16:20], dstIP.To4())

	binary.BigEndian.PutUint16(ip[10:12], ipChecksum(ip))

	return append(ip, tcp...)
}

// ===== TUN connection state =====

type tunConn struct {
	srcIP    net.IP
	dstIP    net.IP
	srcPort  uint16
	dstPort  uint16
	clientSeq uint32 // next seq to send to client
	tunnelSeq uint32 // next seq to send to tunnel
	remoteSeq uint32 // client's next expected seq
	state    int
	tunnel   net.Conn
	mu       sync.Mutex
}

const (
	tunStateSYNReceived = iota
	tunStateEstablished
	tunStateFINWait
)

// ===== TUN mode =====

const (
	tunAddrStr = "10.0.0.2"
	tunGWStr   = "10.0.0.1"
	tunMaskStr = "255.255.255.252" // /30
)

var tunConns = struct {
	sync.RWMutex
	m map[string]*tunConn // key: "srcIP:srcPort-dstIP:dstPort"
}{m: make(map[string]*tunConn)}

func connKey(srcIP, dstIP net.IP, srcPort, dstPort uint16) string {
	return fmt.Sprintf("%s:%d-%s:%d", srcIP, srcPort, dstIP, dstPort)
}

func cmdTUN(nodes []AeroNode) error {
	fmt.Println("Starting TUN mode...")

	// Resolve edge server IPs BEFORE setting up TUN routes (so DNS works normally)
	edgeServerIPs := resolveEdgeServers(nodes)

	// Create wintun adapter with random name to avoid stale adapter conflicts
	rnd := make([]byte, 4)
	rand.Read(rnd)
	adapterName := fmt.Sprintf("nanfang-%x", rnd)
	adapter, err := wintunCreateAdapter(adapterName, "nanfang")
	if err != nil {
		return fmt.Errorf("create TUN adapter: %v\n"+
			"Make sure wintun.dll is in the same directory as nanfang.exe", err)
	}
	defer adapter.close()

	// Start session (capacity = 256 packets)
	session, err := adapter.startSession(1 << 20) // 1MB ring buffer
	if err != nil {
		return fmt.Errorf("start session: %v", err)
	}
	defer session.end()

	// Find the adapter's interface name via netsh
	ifaceName := findWintunInterface()
	if ifaceName == "" {
		fmt.Println("Warning: could not find TUN interface name, routing may not work")
		ifaceName = "nanfang"
	} else {
		fmt.Printf("TUN adapter: %s\n", ifaceName)
	}

	// Configure adapter IP via netsh (delay briefly for adapter to settle)
	time.Sleep(500 * time.Millisecond)
	setupTUNAdapter(ifaceName)

	// Setup routes
	setupTUNRoutes(ifaceName, edgeServerIPs)

	// Handle Ctrl+C: cleanup routes and close adapter
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nStopping TUN mode...")
		stopTUNRoutes()
		session.end()
		adapter.close()
		os.Exit(0)
	}()

	fmt.Printf("TUN mode active: %s\n", tunAddrStr)
	fmt.Println("Press Ctrl+C to stop")

	// Packet read loop: wait for event, then read
	buf := make([]byte, 1500)
	for {
		// Wait for packet availability
		syscall.WaitForSingleObject(syscall.Handle(session.event), 1000)

		n, err := session.ReceivePacket(buf)
		if err != nil {
			// Timeout or error — continue loop
			continue
		}
		if n < 20 {
			continue // too short for IP header
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		go handleTUNPacket(pkt, session, nodes)
	}
}

func handleTUNPacket(pkt []byte, session *wintunSession, nodes []AeroNode) {
	// Parse IP header
	if pkt[0]>>4 != 4 {
		return // not IPv4
	}
	ipHeaderLen := int(pkt[0]&0x0f) * 4
	if len(pkt) < ipHeaderLen {
		return
	}

	protocol := pkt[9]
	srcIP := net.IP(pkt[12:16])
	dstIP := net.IP(pkt[16:20])

	// Handle UDP (DNS passthrough)
	if protocol == 17 { // UDP
		handleUDPPkt(pkt, ipHeaderLen, session)
		return
	}

	// Handle TCP
	if protocol != 6 {
		return
	}

	tcpData := pkt[ipHeaderLen:]
	if len(tcpData) < 20 {
		return
	}

	srcPort := binary.BigEndian.Uint16(tcpData[0:2])
	dstPort := binary.BigEndian.Uint16(tcpData[2:4])
	seq := binary.BigEndian.Uint32(tcpData[4:8])
	flags := tcpData[13]

	// RST: close connection
	if flags&0x04 != 0 {
		key := connKey(srcIP, dstIP, srcPort, dstPort)
		tunConns.Lock()
		if c, ok := tunConns.m[key]; ok {
			c.mu.Lock()
			if c.tunnel != nil {
				c.tunnel.Close()
			}
			c.state = tunStateFINWait
			c.mu.Unlock()
			delete(tunConns.m, key)
		}
		tunConns.Unlock()
		return
	}

	// SYN: new connection
	if flags&0x02 != 0 {
		key := connKey(srcIP, dstIP, srcPort, dstPort)
		tunConns.RLock()
		_, exists := tunConns.m[key]
		tunConns.RUnlock()
		if exists {
			return // duplicate SYN
		}

		node := pickNode(nodes)
		tunnel, err := OpenAeroTunnel(&node, dstIP.String(), int(dstPort))
		if err != nil {
			fmt.Printf("TUN: tunnel %s:%d via %s failed: %v\n", dstIP, dstPort, node.Name, err)
			return
		}
		fmt.Printf("TUN >> %s:%d via %s\n", dstIP, dstPort, node.Name)

		conn := &tunConn{
			srcIP:     srcIP,
			dstIP:     dstIP,
			srcPort:   srcPort,
			dstPort:   dstPort,
			clientSeq: seq + 1,    // consume SYN
			remoteSeq: seq + 1,    // what client expects next
			tunnelSeq: 1000,       // our starting seq to client
			state:     tunStateSYNReceived,
			tunnel:    tunnel,
		}

		tunConns.Lock()
		tunConns.m[key] = conn
		tunConns.Unlock()

		// Send SYN-ACK
		synAck := buildIPPacket(dstIP, srcIP, dstPort, srcPort,
			conn.tunnelSeq, conn.remoteSeq, 0x12, nil) // SYN+ACK
		conn.tunnelSeq++
		session.SendPacket(synAck)

		// Forward initial data if SYN carried payload
		payloadLen := len(tcpData) - 20
		if payloadLen > 0 {
			conn.mu.Lock()
			conn.state = tunStateEstablished
			conn.remoteSeq = seq + 1 + uint32(payloadLen)
			conn.mu.Unlock()
			tunnel.Write(tcpData[20:])
		}

		// Start tunnel -> TUN forwarding
		go tunnelToTUN(conn, session)

		return
	}

	// ACK / data packets
	key := connKey(srcIP, dstIP, srcPort, dstPort)
	tunConns.RLock()
	conn, ok := tunConns.m[key]
	tunConns.RUnlock()
	if !ok {
		return
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()

	payloadLen := len(tcpData) - 20

	// ACK with data: forward to tunnel
	if payloadLen > 0 && conn.tunnel != nil {
		conn.remoteSeq = seq + uint32(payloadLen)
		conn.tunnel.Write(tcpData[20:])

		// ACK the data
		ackPkt := buildIPPacket(dstIP, srcIP, dstPort, srcPort,
			conn.tunnelSeq, conn.remoteSeq, 0x10, nil) // ACK
		session.SendPacket(ackPkt)
	}

	// FIN: close connection
	if flags&0x01 != 0 {
		conn.remoteSeq++
		conn.state = tunStateFINWait

		// FIN-ACK to client
		finAck := buildIPPacket(dstIP, srcIP, dstPort, srcPort,
			conn.tunnelSeq, conn.remoteSeq, 0x11, nil) // FIN+ACK
		conn.tunnelSeq++
		session.SendPacket(finAck)

		if conn.tunnel != nil {
			conn.tunnel.Close()
		}
		tunConns.Lock()
		delete(tunConns.m, key)
		tunConns.Unlock()
	}
}

// tunnelToTUN reads from the aero_v2 tunnel and sends TCP packets to the client via TUN.
func tunnelToTUN(conn *tunConn, session *wintunSession) {
	buf := make([]byte, 1500)
	for {
		conn.mu.Lock()
		t := conn.tunnel
		state := conn.state
		conn.mu.Unlock()
		if t == nil || state == tunStateFINWait {
			break
		}

		n, err := t.Read(buf)
		if err != nil {
			break
		}

		conn.mu.Lock()
		if conn.state == tunStateFINWait {
			conn.mu.Unlock()
			break
		}

		// Build TCP data packet: src=tunnel dst=client
		pkt := buildIPPacket(conn.dstIP, conn.srcIP, conn.dstPort, conn.srcPort,
			conn.tunnelSeq, conn.remoteSeq, 0x18, buf[:n]) // PSH+ACK
		conn.tunnelSeq += uint32(n)
		conn.mu.Unlock()

		session.SendPacket(pkt)
	}

	// Send FIN
	conn.mu.Lock()
	if conn.state != tunStateFINWait {
		fin := buildIPPacket(conn.dstIP, conn.srcIP, conn.dstPort, conn.srcPort,
			conn.tunnelSeq, conn.remoteSeq, 0x11, nil) // FIN+ACK
		conn.tunnelSeq++
		session.SendPacket(fin)
		conn.state = tunStateFINWait
	}
	if conn.tunnel != nil {
		conn.tunnel.Close()
		conn.tunnel = nil
	}
	conn.mu.Unlock()

	key := connKey(conn.srcIP, conn.dstIP, conn.srcPort, conn.dstPort)
	tunConns.Lock()
	delete(tunConns.m, key)
	tunConns.Unlock()

	fmt.Printf("TUN << %s:%d closed\n", conn.dstIP, conn.dstPort)
}

// handleUDPPkt handles UDP packets from TUN — primarily DNS queries.
// It forwards DNS (port 53) to 8.8.8.8 via a direct UDP socket.
func handleUDPPkt(pkt []byte, ipHeaderLen int, session *wintunSession) {
	if len(pkt) < ipHeaderLen+8 {
		return
	}
	udpData := pkt[ipHeaderLen:]
	srcPort := binary.BigEndian.Uint16(udpData[0:2])
	dstPort := binary.BigEndian.Uint16(udpData[2:4])
	udpLen := int(binary.BigEndian.Uint16(udpData[4:6]))
	srcIP := net.IP(pkt[12:16])
	dstIP := net.IP(pkt[16:20])

	if dstPort != 53 {
		return // only handle DNS
	}
	if udpLen < 8 || udpLen > len(udpData) {
		return
	}
	payload := udpData[8:udpLen]
	if len(payload) < 12 {
		return
	}

	// Forward DNS query to 8.8.8.8 via UDP
	resp, err := forwardDNS(payload)
	if err != nil {
		return
	}

	// Build IP+UDP response and send back through TUN
	sendDNSResponse(session, srcIP, dstIP, srcPort, resp)
}

// forwardDNS sends a DNS query to 8.8.8.8 and returns the response.
func forwardDNS(query []byte) ([]byte, error) {
	conn, err := net.DialTimeout("udp", "8.8.8.8:53", 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	resp := make([]byte, 1500)
	n, err := conn.Read(resp)
	if err != nil {
		return nil, err
	}
	return resp[:n], nil
}

// sendDNSResponse builds IP+UDP packets and writes them to the TUN device.
func sendDNSResponse(session *wintunSession, srcIP, dstIP net.IP, srcPort uint16, payload []byte) {
	udpLen := 8 + len(payload)
	ipLen := 20 + udpLen

	// Build UDP header
	udp := make([]byte, udpLen)
	binary.BigEndian.PutUint16(udp[0:2], 53)      // src port = 53
	binary.BigEndian.PutUint16(udp[2:4], srcPort) // dst port = original src
	binary.BigEndian.PutUint16(udp[4:6], uint16(udpLen))
	// UDP checksum: 0 means "no checksum" (valid for IPv4 UDP)
	binary.BigEndian.PutUint16(udp[6:8], 0)
	copy(udp[8:], payload)

	// Compute UDP checksum (optional for IPv4, but some stacks require it)
	csum := udpChecksum(srcIP, dstIP, udp)
	if csum == 0 {
		csum = 0xffff
	}
	binary.BigEndian.PutUint16(udp[6:8], csum)

	// Build IP header
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(ipLen))
	ip[8] = 64
	ip[9] = 17 // UDP
	copy(ip[12:16], srcIP.To4())
	copy(ip[16:20], dstIP.To4())
	binary.BigEndian.PutUint16(ip[10:12], ipChecksum(ip))

	session.SendPacket(append(ip, udp...))
}

// udpChecksum computes UDP checksum with pseudo-header.
func udpChecksum(src, dst net.IP, udpData []byte) uint16 {
	udpLen := len(udpData)
	ph := make([]byte, 12)
	copy(ph[0:4], src.To4())
	copy(ph[4:8], dst.To4())
	ph[8] = 0
	ph[9] = 17 // UDP
	binary.BigEndian.PutUint16(ph[10:12], uint16(udpLen))

	var sum uint32
	for i := 0; i+1 < len(ph); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(ph[i : i+2]))
	}
	for i := 0; i+1 < udpLen; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(udpData[i : i+2]))
	}
	if udpLen&1 == 1 {
		sum += uint32(udpData[udpLen-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// ===== Windows route/adapter setup =====

// findWintunInterface finds the network interface name for the wintun adapter
// by querying netsh for interface IPs and matching against known TUN addresses.
func findWintunInterface() string {
	// Use netsh to find interfaces — the wintun adapter will be a "virtual" adapter
	out, err := exec.Command("netsh", "interface", "show", "interface").CombinedOutput()
	if err != nil {
		return ""
	}
	// netsh output columns: admin_status status type interface_name
	// We look for our "nanfang-" prefixed adapter and return the LAST field
	lines := splitLines(string(out))
	for _, line := range lines {
		if contains(line, "nanfang") || contains(line, "Wintun") {
			parts := splitFields(line)
			if len(parts) >= 4 {
				return parts[len(parts)-1] // last column is interface name
			}
		}
	}
	return ""
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitFields(s string) []string {
	var fields []string
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if start >= 0 {
				fields = append(fields, s[start:i])
				start = -1
			}
		} else {
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		fields = append(fields, s[start:])
	}
	return fields
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func setupTUNAdapter(ifaceName string) {
	ip := tunAddrStr

	// Use PowerShell to set IP — netsh has argument escaping issues when called from Go
	// Adding DefaultGateway here so Windows auto-creates the default route for us (metric ~5)
	psCmd := fmt.Sprintf(
		"New-NetIPAddress -InterfaceAlias '%s' -IPAddress %s -PrefixLength 30 -DefaultGateway %s -ErrorAction SilentlyContinue",
		ifaceName, ip, tunGWStr)
	c := exec.Command("powershell", "-NoProfile", "-Command", psCmd)
	out, err := c.CombinedOutput()
	if err != nil {
		fmt.Printf("PowerShell IP config error: %v, output: %s\n", err, string(out))
	}

	// Verify the IP was set
	verify := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-NetIPAddress -InterfaceAlias '%s' -AddressFamily IPv4).IPAddress", ifaceName))
	if vout, err := verify.CombinedOutput(); err == nil {
		fmt.Printf("Adapter IP: %s\n", strings.TrimSpace(string(vout)))
	}
}

// resolveEdgeServers extracts unique edge server IPs from the node list.
// This must be called BEFORE TUN routes are set up so DNS resolution works normally.
func resolveEdgeServers(nodes []AeroNode) []string {
	seen := map[string]bool{}
	var ips []string
	for _, n := range nodes {
		if n.Server == "" || seen[n.Server] {
			continue
		}
		seen[n.Server] = true
		// Try direct IP parse first
		if net.ParseIP(n.Server) != nil {
			ips = append(ips, n.Server)
			continue
		}
		// Resolve hostname
		if resolved, err := net.LookupHost(n.Server); err == nil {
			for _, ip := range resolved {
				if !seen[ip] {
					seen[ip] = true
					ips = append(ips, ip)
				}
			}
			fmt.Printf("Resolved %s -> %v\n", n.Server, resolved)
		} else {
			fmt.Printf("Warning: cannot resolve %s: %v\n", n.Server, err)
		}
	}
	return ips
}

func setupTUNRoutes(ifaceName string, edgeServerIPs []string) {
	gw := detectPhysicalGateway()
	if gw == "" {
		fmt.Println("Warning: could not detect physical gateway, traffic may not route correctly")
	}

	// PowerShell New-NetIPAddress already added the default route via TUN (low metric).
	// We only need to add exclusion routes and edge server routes through physical gateway.

	// Exclude local networks (so we don't capture our own traffic)
	excludes := []struct{ net, mask string }{
		{"10.0.0.0", "255.0.0.0"},
		{"127.0.0.0", "255.0.0.0"},
		{"172.16.0.0", "255.240.0.0"},
		{"192.168.0.0", "255.255.0.0"},
	}
	for _, ex := range excludes {
		exec.Command("route", "add", ex.net, "mask", ex.mask, "0.0.0.0").Run()
	}

	// Route DNS servers AND edge servers through physical gateway (not TUN)
	if gw != "" {
		// DNS servers — our UDP socket needs to reach them directly
		for _, dns := range []string{"8.8.8.8", "8.8.4.4", "1.1.1.1", "1.0.0.1"} {
			exec.Command("route", "add", dns, "mask", "255.255.255.255", gw).Run()
		}
		// Edge server — net.Dial in OpenAeroTunnel must not loop through TUN
		for _, ip := range edgeServerIPs {
			fmt.Printf("Edge server route: %s -> %s (physical)\n", ip, gw)
			exec.Command("route", "add", ip, "mask", "255.255.255.255", gw).Run()
		}
	}
}

// detectPhysicalGateway finds the default gateway of the physical network adapter.
func detectPhysicalGateway() string {
	out, err := exec.Command("route", "print", "0.0.0.0").CombinedOutput()
	if err != nil {
		return ""
	}
	lines := splitLines(string(out))
	for _, line := range lines {
		parts := splitFields(line)
		// Route table format: destination netmask gateway interface metric
		// We need parts[2] (gateway), not parts[1] (netmask)
		if len(parts) >= 3 && parts[0] == "0.0.0.0" && parts[2] != "0.0.0.0" && parts[2] != tunGWStr {
			return parts[2]
		}
	}
	return ""
}

func stopTUNRoutes() {
	// Remove DNS and edge server routes we added
	for _, ip := range []string{"8.8.8.8", "8.8.4.4", "1.1.1.1", "1.0.0.1"} {
		exec.Command("route", "delete", ip, "mask", "255.255.255.255").Run()
	}
}

