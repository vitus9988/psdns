//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// runGUI strips the macOS quarantine flag from the sibling psdns.app and launches
// it. Downloading the release in a browser flags the archive with
// com.apple.quarantine, which tar/unzip propagate into the extracted .app; since
// the app is ad-hoc signed (not Apple-notarized), Gatekeeper then blocks a
// double-click ("can't be verified"). Running THIS CLI from a terminal is not
// subject to that Gatekeeper check, so it can clear the flag and open the app —
// the one reliably-automatable path until Developer ID notarization lands.
//
// macOS only (build tag): Windows/Linux have no such friction (see gui_other.go).
func runGUI(args []string) {
	app := guiAppPath(args)
	if _, err := os.Stat(app); err != nil {
		fmt.Fprintf(os.Stderr, "psdns: '%s' 를 찾지 못했어요. 압축을 푼 폴더에서 실행하거나 경로를 넘겨 주세요:\n  psdns gui [psdns.app 경로]\n", filepath.Base(app))
		os.Exit(1)
	}
	// Clear the quarantine flag recursively. Best-effort: a benign "no such xattr"
	// is fine (already clean), so we only warn and still try to open.
	if out, err := exec.Command("xattr", "-dr", "com.apple.quarantine", app).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "psdns: 격리 속성 제거 경고: %v %s", err, out)
	}
	if err := exec.Command("open", app).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "psdns: GUI 실행에 실패했어요: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("psdns GUI를 열었어요 (%s). 이후에는 앱을 더블클릭해도 바로 열립니다.\n", filepath.Base(app))
}

// guiAppPath returns the .app to operate on: an explicit path argument if given,
// otherwise psdns.app next to this executable.
func guiAppPath(args []string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	exe, err := os.Executable()
	if err != nil {
		return "psdns.app"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "psdns.app")
}
