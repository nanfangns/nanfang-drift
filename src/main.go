package main

import (
	"encoding/json"
	"fmt"
	"nanfang/core"
	"os"
	"strconv"
)

const version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	switch os.Args[1] {
	case "serve":
		cmdServe()
	case "tun":
		cmdTUNEntry()
	case "sub":
		cmdSub()
	case "list-nodes":
		cmdListNodes()
	case "test":
		cmdTest()
	case "version":
		fmt.Printf("nanfang v%s\n", version)
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println(`nanfang v1.0.0 - aero_v2 proxy client

Usage:
  nanfang serve [options]     Start SOCKS5/HTTP proxy server
  nanfang tun [options]       Start TUN mode (captures all traffic)
  nanfang sub <url>           Fetch nodes from subscription
  nanfang list-nodes          List cached nodes
  nanfang test <node_id>      Test connection to a node
  nanfang version             Print version

Serve options:
  --listen <addr>             Listen address (default: 127.0.0.1:7890)
  --node-id <id>              Use single node by ID
  --nodes-file <path>         Load nodes from JSON file

TUN options:
  --nodes-file <path>         Load nodes from JSON file

Examples:
  nanfang serve --nodes-file nodes.json --listen 127.0.0.1:7890
  nanfang tun --nodes-file nodes.json
  nanfang sub "https://example.com/api/v1/client/subscribe?token=xxx&flag=aero"
  nanfang test 7`)
}

func cmdServe() {
	listenAddr := "127.0.0.1:7890"
	nodesFile := ""
	singleNodeID := 0

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--listen":
			i++
			if i < len(os.Args) {
				listenAddr = os.Args[i]
			}
		case "--nodes-file":
			i++
			if i < len(os.Args) {
				nodesFile = os.Args[i]
			}
		case "--node-id":
			i++
			if i < len(os.Args) {
				singleNodeID, _ = strconv.Atoi(os.Args[i])
			}
		}
	}

	var nodes []core.AeroNode

	if nodesFile != "" {
		data, err := os.ReadFile(nodesFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading nodes file: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(data, &nodes); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing nodes file: %v\n", err)
			os.Exit(1)
		}
		if singleNodeID > 0 {
			filtered := make([]core.AeroNode, 0)
			for _, n := range nodes {
				if n.ID == singleNodeID {
					filtered = append(filtered, n)
				}
			}
			if len(filtered) == 0 {
				fmt.Fprintf(os.Stderr, "Node %d not found\n", singleNodeID)
				os.Exit(1)
			}
			nodes = filtered
		}
		fmt.Printf("Loaded %d nodes from %s\n", len(nodes), nodesFile)
	} else if singleNodeID > 0 {
		nodes = loadDefaultNodes()
		filtered := make([]core.AeroNode, 0)
		for _, n := range nodes {
			if n.ID == singleNodeID {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "Node %d not found\n", singleNodeID)
			os.Exit(1)
		}
		nodes = filtered
	} else {
		fmt.Fprintf(os.Stderr, "Error: specify --nodes-file or --node-id\n")
		os.Exit(1)
	}

	if len(nodes) == 0 {
		fmt.Fprintf(os.Stderr, "No aero_v2 nodes found\n")
		os.Exit(1)
	}

	if err := core.ServeProxy(listenAddr, nodes); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdSub() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: nanfang sub <subscription_url>")
		os.Exit(1)
	}

	nodes, err := core.FetchSubscription(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	data, _ := json.MarshalIndent(nodes, "", "  ")
	outFile := "nodes.json"
	if err := os.WriteFile(outFile, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", outFile, err)
		os.Exit(1)
	}
	fmt.Printf("Saved %d nodes to %s\n", len(nodes), outFile)
}

func cmdListNodes() {
	nodes := loadDefaultNodes()
	if len(nodes) == 0 {
		fmt.Println("No nodes found. Use 'nanfang sub <url>' to fetch nodes.")
		return
	}
	fmt.Printf("%-5s %-25s %-30s %s\n", "ID", "Name", "Server", "Port")
	fmt.Println("---------------------------------------------------------------")
	for _, n := range nodes {
		fmt.Printf("%-5d %-25s %-30s %d\n", n.ID, n.Name, n.Server, n.Port)
	}
	fmt.Printf("\nTotal: %d nodes\n", len(nodes))
}

func cmdTest() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: nanfang test <node_id>")
		os.Exit(1)
	}

	nodeID, _ := strconv.Atoi(os.Args[2])
	nodes := loadDefaultNodes()

	var node *core.AeroNode
	for i := range nodes {
		if nodes[i].ID == nodeID {
			node = &nodes[i]
			break
		}
	}
	if node == nil {
		fmt.Fprintf(os.Stderr, "Node %d not found\n", nodeID)
		os.Exit(1)
	}

	fmt.Printf("Testing node %d (%s) -> %s:%d\n", node.ID, node.Name, node.Server, node.Port)

	conn, err := core.OpenAeroTunnel(node, "www.google.com", 443)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Println("SUCCESS: tunnel established")
}

// GetNodesJSON returns nodes as JSON string for FFI
func GetNodesJSON() string {
	nodes := loadDefaultNodes()
	data, _ := json.Marshal(nodes)
	return string(data)
}

func loadDefaultNodes() []core.AeroNode {
	data, err := os.ReadFile("nodes.json")
	if err != nil {
		return nil
	}
	var nodes []core.AeroNode
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil
	}
	return nodes
}
