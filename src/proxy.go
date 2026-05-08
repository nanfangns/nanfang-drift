package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Mixed proxy server: auto-detects SOCKS5 vs HTTP CONNECT

func serveProxy(listenAddr string, nodes []AeroNode) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	fmt.Printf("Proxy listening on %s (%d nodes)\n", listenAddr, len(nodes))

	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Printf("accept error: %v\n", err)
			continue
		}
		go handleClient(conn, nodes)
	}
}

func handleClient(conn net.Conn, nodes []AeroNode) {
	defer conn.Close()

	// Peek first byte to detect protocol
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	one := make([]byte, 1)
	if _, err := io.ReadFull(conn, one); err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})

	if one[0] == 5 {
		// SOCKS5
		handleSOCKS5Client(conn, one[0], nodes)
	} else {
		// HTTP: first byte already read, put it back for HTTP parsing
		handleHTTPClient(conn, one[0], nodes)
	}
}

func handleSOCKS5Client(conn net.Conn, firstByte byte, nodes []AeroNode) {
	// Auth negotiation (first byte already read: 0x05)
	nmethods := make([]byte, 1)
	if _, err := io.ReadFull(conn, nmethods); err != nil {
		return
	}
	methods := make([]byte, nmethods[0])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	// No auth required
	conn.Write([]byte{5, 0})

	// Connect request
	host, port, err := socks5ConnectReq(conn)
	if err != nil {
		return
	}

	// Pick a node
	node := pickNode(nodes)

	// Open aero_v2 tunnel
	remote, err := OpenAeroTunnel(&node, host, port)
	if err != nil {
		fmt.Printf("tunnel error %s:%d via %s: %v\n", host, port, node.Name, err)
		socks5SendReply(conn, 0x01)
		return
	}

	// Send SOCKS5 success reply
	socks5SendReply(conn, 0x00)

	fmt.Printf(">> %s:%d via %s (socks5)\n", host, port, node.Name)

	// Relay bidirectionally
	relay(conn, remote)

	fmt.Printf("<< %s:%d closed\n", host, port)
}

func pickNode(nodes []AeroNode) AeroNode {
	idx := int(time.Now().UnixNano()) % len(nodes)
	return nodes[idx]
}

func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	cp := func(dst net.Conn, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		// Half-close write side to signal EOF to the other end
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}

	go cp(a, b)
	go cp(b, a)

	wg.Wait()
	a.Close()
	b.Close()
}

// --- SOCKS5 protocol helpers ---

func socks5Auth(conn net.Conn) error {
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != 5 {
		return fmt.Errorf("not socks5")
	}
	nmethods := int(buf[1])
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	// No auth required
	conn.Write([]byte{5, 0})
	return nil
}

func socks5ConnectReq(conn net.Conn) (host string, port int, err error) {
	header := make([]byte, 4)
	if _, err = io.ReadFull(conn, header); err != nil {
		return
	}
	if header[0] != 5 || header[1] != 1 {
		socks5SendReply(conn, 0x07)
		err = fmt.Errorf("unsupported command")
		return
	}

	atyp := header[3]
	switch atyp {
	case 1: // IPv4
		addr := make([]byte, 4)
		if _, err = io.ReadFull(conn, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	case 3: // Domain
		alen := make([]byte, 1)
		if _, err = io.ReadFull(conn, alen); err != nil {
			return
		}
		domain := make([]byte, alen[0])
		if _, err = io.ReadFull(conn, domain); err != nil {
			return
		}
		host = string(domain)
	case 4: // IPv6
		addr := make([]byte, 16)
		if _, err = io.ReadFull(conn, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	default:
		socks5SendReply(conn, 0x08)
		err = fmt.Errorf("unsupported atyp %d", atyp)
		return
	}

	pbuf := make([]byte, 2)
	if _, err = io.ReadFull(conn, pbuf); err != nil {
		return
	}
	port = int(binary.BigEndian.Uint16(pbuf))
	return
}

func socks5SendReply(conn net.Conn, rep byte) {
	conn.Write([]byte{5, rep, 0, 1, 0, 0, 0, 0, 0, 0})
}

// --- HTTP CONNECT proxy ---

func handleHTTPClient(conn net.Conn, firstByte byte, nodes []AeroNode) {
	// Read the rest of the first line (we already have the first byte)
	rest := make([]byte, 0, 256)
	rest = append(rest, firstByte)

	// Read until \r\n
	tmp := make([]byte, 1)
	for {
		if _, err := io.ReadFull(conn, tmp); err != nil {
			return
		}
		rest = append(rest, tmp[0])
		if len(rest) >= 2 && rest[len(rest)-2] == '\r' && rest[len(rest)-1] == '\n' {
			break
		}
		if len(rest) > 4096 {
			return // line too long
		}
	}

	line := string(rest[:len(rest)-2]) // strip \r\n
	// Parse: CONNECT host:port HTTP/1.1
	// or: GET http://host:port/... (plain HTTP proxy)
	// or: POST, etc.

	var host string
	var port int

	fmt.Sscanf(line, "CONNECT %s", &host) // host:port format

	if host == "" {
		// Try parsing as regular HTTP proxy: GET http://host:port/path
		var url string
		fmt.Sscanf(line, "%s %s", &url, &url)
		if len(url) > 7 && url[:7] == "http://" {
			url = url[7:]
			if idx := strings.Index(url, "/"); idx > 0 {
				url = url[:idx]
			}
			if idx := strings.Index(url, ":"); idx > 0 {
				host = url[:idx]
				fmt.Sscanf(url[idx+1:], "%d", &port)
			} else {
				host = url
				port = 80
			}
		}
	}

	if host == "" {
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}

	// If no port, check if host:port was parsed together
	if port == 0 {
		if idx := strings.LastIndex(host, ":"); idx > 0 {
			fmt.Sscanf(host[idx+1:], "%d", &port)
			host = host[:idx]
		} else {
			port = 80
		}
	}

	// Drain remaining headers
	for {
		hdr := make([]byte, 0, 256)
		for {
			if _, err := io.ReadFull(conn, tmp); err != nil {
				return
			}
			hdr = append(hdr, tmp[0])
			if len(hdr) >= 2 && hdr[len(hdr)-2] == '\r' && hdr[len(hdr)-1] == '\n' {
				break
			}
		}
		if string(hdr) == "\r\n" {
			break // end of headers
		}
	}

	// Pick a node and open tunnel
	node := pickNode(nodes)
	remote, err := OpenAeroTunnel(&node, host, port)
	if err != nil {
		fmt.Printf("tunnel error %s:%d via %s: %v\n", host, port, node.Name, err)
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	// Send 200 Connection Established
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	fmt.Printf(">> %s:%d via %s (http)\n", host, port, node.Name)

	relay(conn, remote)

	fmt.Printf("<< %s:%d closed\n", host, port)
}
