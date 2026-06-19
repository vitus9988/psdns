//go:build !darwin

package main

import (
	"fmt"
	"os"
)

// runGUI is macOS-only: the quarantine/Gatekeeper friction it works around does
// not exist on Windows/Linux, where psdns-gui runs directly.
func runGUI(_ []string) {
	fmt.Fprintln(os.Stderr, "psdns gui 는 macOS 전용이에요. Windows/Linux 에서는 psdns-gui 를 직접 실행하세요.")
	os.Exit(2)
}
