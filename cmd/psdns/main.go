// Command psdns is a personal, cross-platform tool that bypasses ISP DNS
// tampering (via DNS-over-HTTPS) and SNI-based HTTPS blocking (via ClientHello
// fragmentation) without any paid VPN subscription or remote server.
//
// Subcommands:
//
//	psdns resolve   run a local DoH resolver (point the OS DNS at it)
//	psdns proxy     run local HTTP CONNECT + SOCKS5 proxies (point the browser at them)
//	psdns run       run the resolver and proxies together
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/dnssrv"
	"github.com/vitus9988/psdns/internal/doh"
	"github.com/vitus9988/psdns/internal/proxy"
	"github.com/vitus9988/psdns/internal/resolver"
)

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "resolve":
		runResolve(args)
	case "proxy":
		runProxy(args)
	case "run":
		runAll(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// bindCommon registers the flags shared by every subcommand and returns the
// config plus a pointer to the raw --frag value (validated after Parse).
func bindCommon(fs *flag.FlagSet) (*config.Config, *string) {
	c := config.Default()
	fragStr := string(c.Frag)
	fs.StringVar(&c.DoHURL, "doh", c.DoHURL, "upstream DoH endpoint URL")
	fs.StringVar(&c.DoHBootstrap, "bootstrap", c.DoHBootstrap, "IP[:port] to dial for the DoH host (bypass system DNS)")
	fs.StringVar(&fragStr, "frag", fragStr, "ClientHello fragmentation: none|split|tls-record")
	fs.DurationVar(&c.FragDelay, "frag-delay", c.FragDelay, "delay inserted between fragments (e.g. 10ms)")
	fs.DurationVar(&c.Timeout, "timeout", c.Timeout, "dial/query timeout")
	return &c, &fragStr
}

func setFrag(c *config.Config, s string) {
	switch config.FragStrategy(s) {
	case config.FragNone, config.FragSplit, config.FragTLSRecord:
		c.Frag = config.FragStrategy(s)
	default:
		log.Fatalf("invalid -frag %q (want none|split|tls-record)", s)
	}
}

func mustDoH(c *config.Config) *doh.Client {
	client, err := doh.New(c.DoHURL, c.DoHBootstrap, c.Timeout)
	if err != nil {
		log.Fatalf("doh client: %v", err)
	}
	return client
}

func runResolve(args []string) {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	c, fragStr := bindCommon(fs)
	fs.StringVar(&c.DNSListen, "listen", c.DNSListen, "local DNS listen address")
	_ = fs.Parse(args)
	setFrag(c, *fragStr)

	srv := dnssrv.New(mustDoH(c), c.DNSListen, c.Timeout)
	go onSignal(srv.Shutdown)
	log.Printf("psdns resolve: DNS on %s -> DoH %s", c.DNSListen, c.DoHURL)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("dns server: %v", err)
	}
}

func runProxy(args []string) {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	c, fragStr := bindCommon(fs)
	fs.StringVar(&c.ProxyListen, "http", c.ProxyListen, "HTTP CONNECT proxy listen address")
	fs.StringVar(&c.SocksListen, "socks", c.SocksListen, "SOCKS5 proxy listen address")
	_ = fs.Parse(args)
	setFrag(c, *fragStr)

	res := resolver.New(mustDoH(c))
	hp := proxy.NewHTTP(res, *c)
	sp := proxy.NewSOCKS(res, *c)

	errCh := make(chan error, 2)
	go func() { errCh <- hp.ListenAndServe() }()
	go func() { errCh <- sp.ListenAndServe() }()
	go onSignal(func() { _ = hp.Close(); _ = sp.Close() })
	log.Printf("psdns proxy: HTTP %s | SOCKS5 %s | frag=%s -> DoH %s", c.ProxyListen, c.SocksListen, c.Frag, c.DoHURL)
	log.Fatal(<-errCh)
}

func runAll(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	c, fragStr := bindCommon(fs)
	fs.StringVar(&c.DNSListen, "dns", c.DNSListen, "local DNS listen address")
	fs.StringVar(&c.ProxyListen, "http", c.ProxyListen, "HTTP CONNECT proxy listen address")
	fs.StringVar(&c.SocksListen, "socks", c.SocksListen, "SOCKS5 proxy listen address")
	_ = fs.Parse(args)
	setFrag(c, *fragStr)

	client := mustDoH(c)
	res := resolver.New(client)
	dsrv := dnssrv.New(client, c.DNSListen, c.Timeout)
	hp := proxy.NewHTTP(res, *c)
	sp := proxy.NewSOCKS(res, *c)

	errCh := make(chan error, 3)
	go func() { errCh <- dsrv.ListenAndServe() }()
	go func() { errCh <- hp.ListenAndServe() }()
	go func() { errCh <- sp.ListenAndServe() }()
	go onSignal(func() { dsrv.Shutdown(); _ = hp.Close(); _ = sp.Close() })
	log.Printf("psdns run: DNS %s | HTTP %s | SOCKS5 %s | frag=%s -> DoH %s", c.DNSListen, c.ProxyListen, c.SocksListen, c.Frag, c.DoHURL)
	log.Fatal(<-errCh)
}

// onSignal blocks until SIGINT/SIGTERM, runs stop, then exits.
func onSignal(stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	log.Println("shutting down...")
	stop()
	os.Exit(0)
}

func usage() {
	fmt.Fprint(os.Stderr, `psdns - bypass DNS tampering (DoH) and SNI-based HTTPS blocking (ClientHello fragmentation)

usage:
  psdns resolve [flags]   run a local DoH resolver; set the OS DNS to its address
  psdns proxy   [flags]   run local HTTP CONNECT + SOCKS5 proxies; point the browser at them
  psdns run     [flags]   run the resolver and proxies together

common flags:
  -doh URL          upstream DoH endpoint (default https://1.1.1.1/dns-query)
  -bootstrap IP     IP[:port] to dial for the DoH host (bypass system DNS)
  -frag STRATEGY    ClientHello fragmentation: none|split|tls-record (default split)
  -frag-delay D     delay between fragments, e.g. 10ms (default 0)
  -timeout D        dial/query timeout (default 10s)

resolve flags:
  -listen ADDR      DNS listen address (default 127.0.0.1:53; :53 needs privileges)

proxy / run flags:
  -http ADDR        HTTP CONNECT proxy address (default 127.0.0.1:8080)
  -socks ADDR       SOCKS5 proxy address (default 127.0.0.1:1080)
  -dns ADDR         (run only) DNS listen address (default 127.0.0.1:53)

examples:
  psdns proxy                      # browser -> HTTP proxy 127.0.0.1:8080, DNS+SNI bypass
  psdns resolve -listen 127.0.0.1:5353
  psdns run -frag tls-record
`)
}
