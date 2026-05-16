package core

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var openTunnel = OpenAeroTunnel

func ServeProxy(listenAddr string, nodes []AeroNode) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Printf("Proxy listening on %s (%d nodes)\n", listenAddr, len(nodes))

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept error: %v\n", err)
			continue
		}
		go handleClient(conn, nodes)
	}
}

func handleClient(conn net.Conn, nodes []AeroNode) {
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	one := make([]byte, 1)
	if _, err := io.ReadFull(conn, one); err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})

	if one[0] == 5 {
		handleSOCKS5Client(conn, one[0], nodes)
	} else {
		handleHTTPClient(conn, one[0], nodes)
	}
}

func handleSOCKS5Client(conn net.Conn, firstByte byte, nodes []AeroNode) {
	nmethods := make([]byte, 1)
	if _, err := io.ReadFull(conn, nmethods); err != nil {
		return
	}
	methods := make([]byte, nmethods[0])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	conn.Write([]byte{5, 0})

	host, port, err := socks5ConnectReq(conn)
	if err != nil {
		return
	}

	node := PickNode(nodes)
	remote, err := openTunnel(&node, host, port)
	if err != nil {
		log.Printf("tunnel error %s:%d via %s: %v\n", host, port, node.Name, err)
		socks5SendReply(conn, 0x01)
		return
	}

	socks5SendReply(conn, 0x00)
	log.Printf(">> %s:%d via %s (socks5)\n", host, port, node.Name)
	relay(conn, remote)
	log.Printf("<< %s:%d closed\n", host, port)
}

func PickNode(nodes []AeroNode) AeroNode {
	idx := int(time.Now().UnixNano()) % len(nodes)
	return nodes[idx]
}

func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	cp := func(dst net.Conn, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
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
	case 1:
		addr := make([]byte, 4)
		if _, err = io.ReadFull(conn, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	case 3:
		alen := make([]byte, 1)
		if _, err = io.ReadFull(conn, alen); err != nil {
			return
		}
		domain := make([]byte, alen[0])
		if _, err = io.ReadFull(conn, domain); err != nil {
			return
		}
		host = string(domain)
	case 4:
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

type httpProxyRequest struct {
	method     string
	target     string
	version    string
	connect    bool
	host       string
	port       int
	requestURI string
	headers    textproto.MIMEHeader
}

func handleHTTPClient(conn net.Conn, firstByte byte, nodes []AeroNode) {
	reader := bufio.NewReader(io.MultiReader(bytes.NewReader([]byte{firstByte}), conn))
	req, err := parseHTTPProxyRequest(reader)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n"))
		return
	}

	node := PickNode(nodes)
	remote, err := openTunnel(&node, req.host, req.port)
	if err != nil {
		log.Printf("tunnel error %s:%d via %s: %v\n", req.host, req.port, node.Name, err)
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"))
		return
	}

	if req.connect {
		conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		log.Printf(">> %s:%d via %s (connect)\n", req.host, req.port, node.Name)
		relay(conn, remote)
		log.Printf("<< %s:%d closed\n", req.host, req.port)
		return
	}

	if err := writeHTTPRequest(remote, req); err != nil {
		remote.Close()
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"))
		return
	}

	log.Printf(">> %s:%d via %s (http)\n", req.host, req.port, node.Name)
	relayHTTP(conn, remote, reader)
	log.Printf("<< %s:%d closed\n", req.host, req.port)
}

func parseHTTPProxyRequest(reader *bufio.Reader) (*httpProxyRequest, error) {
	tp := textproto.NewReader(reader)
	line, err := tp.ReadLine()
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid request line")
	}

	req := &httpProxyRequest{
		method:  parts[0],
		target:  parts[1],
		version: parts[2],
	}

	headers, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, err
	}
	req.headers = headers

	if strings.EqualFold(req.method, "CONNECT") {
		host, port, err := splitHostPortDefault(req.target, 443)
		if err != nil {
			return nil, err
		}
		req.connect = true
		req.host = host
		req.port = port
		return req, nil
	}

	if parsedURL, err := url.Parse(req.target); err == nil && parsedURL.Host != "" {
		defaultPort := 80
		if strings.EqualFold(parsedURL.Scheme, "https") {
			defaultPort = 443
		}
		host, port, err := splitHostPortDefault(parsedURL.Host, defaultPort)
		if err != nil {
			return nil, err
		}
		req.host = host
		req.port = port
		req.requestURI = parsedURL.RequestURI()
		if req.requestURI == "" {
			req.requestURI = "/"
		}
		return req, nil
	}

	hostHeader := strings.TrimSpace(req.headers.Get("Host"))
	if hostHeader == "" {
		return nil, fmt.Errorf("missing host header")
	}
	host, port, err := splitHostPortDefault(hostHeader, 80)
	if err != nil {
		return nil, err
	}
	req.host = host
	req.port = port
	req.requestURI = req.target
	if req.requestURI == "" {
		req.requestURI = "/"
	}
	return req, nil
}

func splitHostPortDefault(addr string, defaultPort int) (string, int, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, fmt.Errorf("empty host")
	}

	if strings.HasPrefix(addr, "[") {
		host, portStr, err := net.SplitHostPort(addr)
		if err == nil {
			port, convErr := strconv.Atoi(portStr)
			if convErr != nil {
				return "", 0, convErr
			}
			return host, port, nil
		}
		if strings.Contains(err.Error(), "missing port in address") {
			return strings.Trim(addr, "[]"), defaultPort, nil
		}
		return "", 0, err
	}

	if strings.Count(addr, ":") == 0 {
		return addr, defaultPort, nil
	}

	if strings.Count(addr, ":") == 1 {
		host, portStr, found := strings.Cut(addr, ":")
		if !found || host == "" || portStr == "" {
			return "", 0, fmt.Errorf("invalid host:port %q", addr)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return "", 0, err
		}
		return host, port, nil
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err == nil {
		port, convErr := strconv.Atoi(portStr)
		if convErr != nil {
			return "", 0, convErr
		}
		return host, port, nil
	}

	if strings.Contains(err.Error(), "missing port in address") {
		return addr, defaultPort, nil
	}
	return "", 0, err
}

func writeHTTPRequest(dst io.Writer, req *httpProxyRequest) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s %s %s\r\n", req.method, req.requestURI, req.version)

	hostWritten := false
	for key, values := range req.headers {
		switch {
		case strings.EqualFold(key, "Host"):
			hostWritten = true
		case strings.EqualFold(key, "Connection"):
			continue
		case strings.EqualFold(key, "Proxy-Connection"):
			continue
		case strings.EqualFold(key, "Proxy-Authorization"):
			continue
		}

		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", key, value)
		}
	}

	if !hostWritten {
		fmt.Fprintf(&buf, "Host: %s\r\n", formatAuthority(req.host, req.port))
	}
	buf.WriteString("Connection: close\r\n\r\n")

	_, err := dst.Write(buf.Bytes())
	return err
}

func formatAuthority(host string, port int) string {
	switch {
	case strings.Contains(host, ":"):
		return net.JoinHostPort(host, strconv.Itoa(port))
	case port == 80:
		return host
	default:
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
}

func relayHTTP(client net.Conn, remote net.Conn, clientReader io.Reader) {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(remote, clientReader)
		if tc, ok := remote.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		close(done)
	}()

	_, _ = io.Copy(client, remote)
	_ = client.Close()
	_ = remote.Close()
	<-done
}
