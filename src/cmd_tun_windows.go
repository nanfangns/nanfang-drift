//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"nanfang/core"
	"os"
)

func cmdTUNEntry() {
	nodesFile := ""

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--nodes-file":
			i++
			if i < len(os.Args) {
				nodesFile = os.Args[i]
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
		fmt.Printf("Loaded %d nodes from %s\n", len(nodes), nodesFile)
	} else {
		nodes = loadDefaultNodes()
	}

	if len(nodes) == 0 {
		fmt.Fprintf(os.Stderr, "No aero_v2 nodes found\n")
		os.Exit(1)
	}

	if err := cmdTUN(nodes); err != nil {
		fmt.Fprintf(os.Stderr, "TUN error: %v\n", err)
		os.Exit(1)
	}
}
