package main

import (
	"flag"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/selfupdate"
)

func TestSetFragValid(t *testing.T) {
	for _, s := range []string{"none", "split", "tls-record"} {
		c := config.Default()
		if err := setFrag(&c, s); err != nil {
			t.Errorf("setFrag(%q): unexpected error %v", s, err)
		}
		if string(c.Frag) != s {
			t.Errorf("setFrag(%q): Frag = %q", s, c.Frag)
		}
	}
}

func TestSetFragInvalid(t *testing.T) {
	c := config.Default()
	before := c.Frag
	if err := setFrag(&c, "bogus"); err == nil {
		t.Fatal(`setFrag("bogus"): expected error, got nil`)
	}
	if c.Frag != before {
		t.Errorf("setFrag rejected the value but mutated Frag to %q", c.Frag)
	}
}

func TestFinalize(t *testing.T) {
	// Valid strategy + delay applies the strategy and returns nil.
	c := config.Default()
	if err := finalize(&c, "tls-record", ""); err != nil {
		t.Errorf("valid finalize: %v", err)
	}
	if c.Frag != config.FragTLSRecord {
		t.Errorf("finalize did not apply frag: %q", c.Frag)
	}
	// A bad strategy is rejected.
	c = config.Default()
	if err := finalize(&c, "nope", ""); err == nil {
		t.Error("finalize should reject an invalid -frag")
	}
	// An over-cap delay is rejected even with a valid strategy.
	c = config.Default()
	c.FragDelay = config.MaxFragDelay + time.Second
	if err := finalize(&c, "split", ""); err == nil {
		t.Error("finalize should reject an over-cap -frag-delay")
	}
	c = config.Default()
	c.DoHBootstrap = "example.com:853"
	if err := finalize(&c, "split", ""); err == nil {
		t.Error("finalize should reject a hostname -bootstrap")
	}
	c = config.Default()
	c.DoHBootstrap = "2606:4700:4700::1111"
	if err := finalize(&c, "split", ""); err != nil {
		t.Errorf("finalize should accept a bare IPv6 bootstrap: %v", err)
	}
	c = config.Default()
	c.DoHHedgeDelay = config.MaxDoHHedgeDelay + time.Second
	if err := finalize(&c, "split", ""); err == nil {
		t.Error("finalize should reject an over-cap -doh-hedge-delay")
	}
	c = config.Default()
	if err := finalize(&c, "split", "ftp://nope"); err == nil {
		t.Error("finalize should reject a bad fallback DoH URL")
	}
	c = config.Default()
	if err := finalize(&c, "split", "https://8.8.8.8/dns-query, https://9.9.9.9/dns-query"); err != nil {
		t.Errorf("finalize should accept fallback DoH URLs: %v", err)
	}
	if got := config.FormatDoHList(c.DoHFallbacks); got != "https://8.8.8.8/dns-query,https://9.9.9.9/dns-query" {
		t.Errorf("DoHFallbacks = %q", got)
	}
}

func TestCheckFragDelay(t *testing.T) {
	if err := checkFragDelay(10 * time.Millisecond); err != nil {
		t.Errorf("10ms should be allowed: %v", err)
	}
	if err := checkFragDelay(config.MaxFragDelay); err != nil {
		t.Errorf("the cap itself should be allowed: %v", err)
	}
	if err := checkFragDelay(config.MaxFragDelay + time.Second); err == nil {
		t.Error("delay over the cap should be rejected")
	}
	if err := checkFragDelay(-time.Millisecond); err == nil {
		t.Error("negative delay should be rejected")
	}
}

func TestCheckDoHHedgeDelay(t *testing.T) {
	if err := checkDoHHedgeDelay(250 * time.Millisecond); err != nil {
		t.Errorf("250ms should be allowed: %v", err)
	}
	if err := checkDoHHedgeDelay(config.MaxDoHHedgeDelay); err != nil {
		t.Errorf("the cap itself should be allowed: %v", err)
	}
	if err := checkDoHHedgeDelay(config.MaxDoHHedgeDelay + time.Second); err == nil {
		t.Error("delay over the cap should be rejected")
	}
	if err := checkDoHHedgeDelay(-time.Millisecond); err == nil {
		t.Error("negative delay should be rejected")
	}
}

func TestBindCommonParsesFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c, fragStr, fallbacksStr := bindCommon(fs)
	if err := fs.Parse([]string{
		"-doh", "https://9.9.9.9/dns-query",
		"-bootstrap", "9.9.9.9",
		"-doh-fallbacks", "https://8.8.8.8/dns-query,https://1.0.0.1/dns-query",
		"-doh-hedge-delay", "100ms",
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
	if *fallbacksStr != "https://8.8.8.8/dns-query,https://1.0.0.1/dns-query" {
		t.Errorf("fallbacksStr = %q", *fallbacksStr)
	}
	if c.DoHHedgeDelay != 100*time.Millisecond {
		t.Errorf("DoHHedgeDelay = %v", c.DoHHedgeDelay)
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
