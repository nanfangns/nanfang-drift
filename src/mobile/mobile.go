package mobile

import (
	"encoding/json"
	"fmt"
	"log"
	"nanfang/core"
	"sync"
	"time"
)

var (
	mu       sync.Mutex
	running  bool
	stopFunc func()

	tunMu      sync.Mutex
	tunRunning bool
	tun2socks  *core.Tun2Socks
)

// StartProxy starts the mixed SOCKS5/HTTP proxy on the given address.
// nodesJSON is the nodes array as a JSON string.
// nodeID selects which node to use (0 = random).
func StartProxy(nodesJSON string, nodeID int, listenAddr string) error {
	mu.Lock()
	if running {
		mu.Unlock()
		return fmt.Errorf("proxy already running")
	}
	mu.Unlock()

	var nodes []core.AeroNode
	if err := json.Unmarshal([]byte(nodesJSON), &nodes); err != nil {
		return fmt.Errorf("parse nodes: %v", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes provided")
	}

	// Filter to a specific node if requested
	if nodeID > 0 {
		filtered := make([]core.AeroNode, 0)
		for _, n := range nodes {
			if n.ID == nodeID {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) > 0 {
			nodes = filtered
		}
	}

	// Start proxy in a goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- core.ServeProxy(listenAddr, nodes)
	}()

	// Check if it started successfully (give it a moment)
	select {
	case err := <-errCh:
		return fmt.Errorf("proxy failed: %v", err)
	default:
	}

	mu.Lock()
	running = true
	mu.Unlock()

	return nil
}

// StopProxy stops the running proxy.
func StopProxy() {
	mu.Lock()
	defer mu.Unlock()
	if stopFunc != nil {
		stopFunc()
		stopFunc = nil
	}
	running = false
}

// FetchNodes fetches nodes from a subscription URL and returns them as JSON.
func FetchNodes(url string) (string, error) {
	nodes, err := core.FetchSubscription(url)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(nodes)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// IsRunning returns whether the proxy is currently running.
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return running
}

// StartTun2Socks starts the tun2socks engine. fd is the TUN file descriptor int,
// nodesJSON is the nodes array as a JSON string.
func StartTun2Socks(fd int, nodesJSON string) error {
	tunMu.Lock()
	if tunRunning {
		tunMu.Unlock()
		return fmt.Errorf("tun2socks already running")
	}
	tunMu.Unlock()

	var nodes []core.AeroNode
	if err := json.Unmarshal([]byte(nodesJSON), &nodes); err != nil {
		return fmt.Errorf("parse nodes: %v", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes provided")
	}

	t := core.NewTun2Socks(uintptr(fd), nodes)
	tunMu.Lock()
	tun2socks = t
	tunRunning = true
	tunMu.Unlock()

	go func() {
		log.Printf("tun2socks starting, fd=%d, nodes=%d", fd, len(nodes))
		t.Run()
		tunMu.Lock()
		tunRunning = false
		tun2socks = nil
		tunMu.Unlock()
		log.Printf("tun2socks stopped")
	}()

	// Brief wait to check if it started successfully
	time.Sleep(100 * time.Millisecond)
	tunMu.Lock()
	running := tunRunning
	tunMu.Unlock()
	if !running {
		return fmt.Errorf("tun2socks failed to start")
	}

	return nil
}

// StopTun2Socks stops the running tun2socks.
func StopTun2Socks() {
	tunMu.Lock()
	defer tunMu.Unlock()
	if tun2socks != nil {
		tun2socks.Stop()
		tun2socks = nil
	}
	tunRunning = false
}
