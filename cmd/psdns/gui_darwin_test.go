//go:build darwin

package main

import (
	"path/filepath"
	"testing"
)

func TestGUIAppPathExplicit(t *testing.T) {
	if got := guiAppPath([]string{"/tmp/Custom.app"}); got != "/tmp/Custom.app" {
		t.Errorf("guiAppPath(explicit) = %q", got)
	}
}

func TestGUIAppPathDefault(t *testing.T) {
	got := guiAppPath(nil)
	if filepath.Base(got) != "psdns.app" {
		t.Errorf("guiAppPath(nil) = %q, want a psdns.app sibling", got)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("guiAppPath(nil) = %q, want an absolute path next to the executable", got)
	}
}
