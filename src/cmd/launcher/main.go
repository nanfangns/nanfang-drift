package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

func messageBox(title, text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	proc.Call(0, uintptr(unsafe.Pointer(textPtr)), uintptr(unsafe.Pointer(titlePtr)), 0x10)
}

func fail(appDir, msg string) {
	_ = os.WriteFile(filepath.Join(appDir, "launcher-error.txt"), []byte(msg), 0644)
	messageBox("Nanfang", msg)
	os.Exit(1)
}

func main() {
	exePath, err := os.Executable()
	if err != nil {
		messageBox("Nanfang", err.Error())
		os.Exit(1)
	}

	appDir := filepath.Dir(exePath)
	runtimeDir := filepath.Join(appDir, "runtime")
	pythonExe := filepath.Join(runtimeDir, "pythonw.exe")
	if _, err := os.Stat(pythonExe); err != nil {
		fail(appDir, fmt.Sprintf("Missing runtime: %s", pythonExe))
	}

	scriptPath := filepath.Join(appDir, "nanfang_gui.py")
	if _, err := os.Stat(scriptPath); err != nil {
		fail(appDir, fmt.Sprintf("Missing script: %s", scriptPath))
	}

	cmd := exec.Command(pythonExe, scriptPath)
	cmd.Dir = appDir
	cmd.Env = append(os.Environ(),
		"PYTHONHOME="+runtimeDir,
		"PYTHONUTF8=1",
		"PYTHONNOUSERSITE=1",
		"TCL_LIBRARY="+filepath.Join(runtimeDir, "tcl", "tcl8.6"),
		"TK_LIBRARY="+filepath.Join(runtimeDir, "tcl", "tk8.6"),
	)

	if err := cmd.Start(); err != nil {
		fail(appDir, err.Error())
	}
}
