package main

import (
	"flag"
	"testing"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/selfupdate"
)

func TestSetFragValid(t *testing.T) {
	for _, s := range []string{"none", "split", "tls-record"} {
		c := config.Default()
		setFrag(&c, s)
		if string(c.Frag) != s {
			t.Errorf("setFrag(%q): Frag = %q", s, c.Frag)
		}
	}
}

func TestBindCommonParsesFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c, fragStr := bindCommon(fs)
	if err := fs.Parse([]string{
		"-doh", "https://9.9.9.9/dns-query",
		"-bootstrap", "9.9.9.9",
		"-frag", "tls-record",
		"-frag-delay", "5ms",
		"-timeout", "3s",
	}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DoHURL != "https://9.9.9.9/dns-query" {
		t.Errorf("DoHURL = %q", c.DoHURL)
	}
	if c.DoHBootstrap != "9.9.9.9" {
		t.Errorf("DoHBootstrap = %q", c.DoHBootstrap)
	}
	if *fragStr != "tls-record" {
		t.Errorf("fragStr = %q", *fragStr)
	}
	if c.FragDelay.String() != "5ms" {
		t.Errorf("FragDelay = %v", c.FragDelay)
	}
	if c.Timeout.String() != "3s" {
		t.Errorf("Timeout = %v", c.Timeout)
	}
}

func TestStageLabel(t *testing.T) {
	cases := map[selfupdate.Stage]string{
		selfupdate.StageDownload: "내려받는 중",
		selfupdate.StageVerify:   "검증 중",
		selfupdate.StageExtract:  "압축 해제",
		selfupdate.StageReplace:  "교체 중",
		selfupdate.StageDone:     "완료",
		selfupdate.StageStart:    "준비 중", // hits the default branch
	}
	for stage, want := range cases {
		if got := stageLabel(stage); got != want {
			t.Errorf("stageLabel(%q) = %q, want %q", stage, got, want)
		}
	}
}
