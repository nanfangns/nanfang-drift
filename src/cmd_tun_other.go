//go:build !windows

package main

import (
	"fmt"
	"os"
)

func cmdTUNEntry() {
	fmt.Fprintln(os.Stderr, "TUN mode is only supported on Windows")
	os.Exit(1)
}
