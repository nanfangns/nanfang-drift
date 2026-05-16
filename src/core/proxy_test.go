package core

import (
	"bufio"
	"bytes"
	"net/textproto"
	"strings"
	"testing"
)

func TestParseHTTPProxyRequestConnect(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(
		"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
	))

	req, err := parseHTTPProxyRequest(reader)
	if err != nil {
		t.Fatalf("parse CONNECT failed: %v", err)
	}
	if !req.connect {
		t.Fatalf("expected CONNECT request")
	}
	if req.host != "example.com" || req.port != 443 {
		t.Fatalf("unexpected target: %s:%d", req.host, req.port)
	}
}

func TestParseHTTPProxyRequestAbsoluteURL(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(
		"GET http://example.com:8080/hello?q=1 HTTP/1.1\r\nHost: example.com:8080\r\n\r\n",
	))

	req, err := parseHTTPProxyRequest(reader)
	if err != nil {
		t.Fatalf("parse absolute URL failed: %v", err)
	}
	if req.connect {
		t.Fatalf("expected plain HTTP request")
	}
	if req.host != "example.com" || req.port != 8080 {
		t.Fatalf("unexpected target: %s:%d", req.host, req.port)
	}
	if req.requestURI != "/hello?q=1" {
		t.Fatalf("unexpected request URI: %q", req.requestURI)
	}
}

func TestWriteHTTPRequestRewritesProxyHeaders(t *testing.T) {
	req := &httpProxyRequest{
		method:     "GET",
		requestURI: "/hello",
		version:    "HTTP/1.1",
		host:       "example.com",
		port:       80,
		headers: textproto.MIMEHeader{
			"Host":                {"example.com"},
			"User-Agent":          {"nanfang-test"},
			"Connection":          {"keep-alive"},
			"Proxy-Connection":    {"keep-alive"},
			"Proxy-Authorization": {"Basic abc"},
		},
	}

	var buf bytes.Buffer
	if err := writeHTTPRequest(&buf, req); err != nil {
		t.Fatalf("write request failed: %v", err)
	}

	out := buf.String()
	if !strings.HasPrefix(out, "GET /hello HTTP/1.1\r\n") {
		t.Fatalf("request line was not rewritten: %q", out)
	}
	if strings.Contains(out, "Proxy-Connection:") {
		t.Fatalf("proxy-only headers leaked: %q", out)
	}
	if strings.Contains(out, "Proxy-Authorization:") {
		t.Fatalf("proxy auth header leaked: %q", out)
	}
	if !strings.Contains(out, "Connection: close\r\n") {
		t.Fatalf("expected Connection: close, got %q", out)
	}
}
