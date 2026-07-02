// Package doh implements a minimal DNS-over-HTTPS client (RFC 8484).
//
// Resolving names over an encrypted HTTPS channel defeats ISP DNS tampering
// (forged answers pointing at a block page). The default endpoint uses an
// IP-literal host (https://1.1.1.1/dns-query) so the DoH connection itself
// carries no SNI and needs no DNS bootstrap; the TLS certificate is validated
// against the IP via its SAN.
package doh

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/miekg/dns"
)

const dnsMessageMIME = "application/dns-message"

// Exchanger is the DoH operation used by the resolver and local DNS server.
// A single Client and a hedged group of clients both implement it.
type Exchanger interface {
	Exchange(context.Context, *dns.Msg) (*dns.Msg, error)
}

// Client is a DoH client bound to a single upstream endpoint.
type Client struct {
	endpoint string
	http     *http.Client
}

// NewExchanger creates the DoH exchanger used by runtime code. The primary
// endpoint uses bootstrap; fallback endpoints are dialed directly and are
// started with hedgeDelay spacing when the primary is slow or failed.
func NewExchanger(endpoint, bootstrap string, fallbacks []string, timeout, hedgeDelay time.Duration) (Exchanger, error) {
	primary, err := New(endpoint, bootstrap, timeout)
	if err != nil {
		return nil, err
	}
	upstreams := []Exchanger{primary}
	for _, fallback := range fallbacks {
		c, err := New(fallback, "", timeout)
		if err != nil {
			return nil, fmt.Errorf("fallback %q: %w", fallback, err)
		}
		upstreams = append(upstreams, c)
	}
	return NewHedged(upstreams, hedgeDelay), nil
}

// New creates a DoH client.
//
//   - endpoint is the DoH URL (e.g. https://1.1.1.1/dns-query).
//   - bootstrap, if non-empty, is the IP[:port] actually dialed for the
//     endpoint host, so reaching the resolver never depends on system DNS.
//     When set, the TLS handshake still validates against the endpoint
//     hostname (and sends it as SNI), so prefer an IP-literal endpoint to
//     avoid exposing any SNI at all.
//   - timeout bounds each request.
func New(endpoint, bootstrap string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse DoH endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("DoH endpoint must be http(s): %q", endpoint)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	dialTarget := net.JoinHostPort(host, port)
	if bootstrap != "" {
		bh, bp, splitErr := net.SplitHostPort(bootstrap)
		if splitErr != nil {
			bh, bp = bootstrap, port // bootstrap given as a bare IP
		}
		dialTarget = net.JoinHostPort(bh, bp)
		tlsCfg.ServerName = host // dial the IP, validate against the hostname
	}

	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		TLSClientConfig:   tlsCfg,
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, dialTarget)
		},
	}
	return &Client{
		endpoint: endpoint,
		http:     &http.Client{Timeout: timeout, Transport: transport},
	}, nil
}

// Hedged is an Exchanger that starts with the primary upstream and then starts
// fallbacks after a delay until one returns a successful response.
type Hedged struct {
	upstreams []Exchanger
	delay     time.Duration
}

// NewHedged returns an Exchanger over upstreams. A single upstream is returned
// directly, so callers pay no hedging overhead unless fallbacks are configured.
func NewHedged(upstreams []Exchanger, delay time.Duration) Exchanger {
	if len(upstreams) == 1 {
		return upstreams[0]
	}
	cp := append([]Exchanger(nil), upstreams...)
	return &Hedged{upstreams: cp, delay: delay}
}

type exchangeResult struct {
	resp *dns.Msg
	err  error
}

// Exchange returns the first successful upstream response. If an upstream fails
// quickly, the next fallback starts immediately; otherwise fallbacks are started
// one by one after h.delay.
func (h *Hedged) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	if len(h.upstreams) == 0 {
		return nil, errors.New("doh: no upstreams configured")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan exchangeResult, len(h.upstreams))
	start := func(upstream Exchanger) {
		go func() {
			query := q
			if q != nil {
				query = q.Copy()
			}
			resp, err := upstream.Exchange(ctx, query)
			results <- exchangeResult{resp: resp, err: err}
		}()
	}

	var (
		next      = 1
		completed int
		errs      []error
		timer     *time.Timer
	)
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer = nil
	}
	armTimer := func() {
		if next < len(h.upstreams) && timer == nil {
			timer = time.NewTimer(h.delay)
		}
	}
	startNext := func() {
		start(h.upstreams[next])
		next++
	}

	start(h.upstreams[0])
	armTimer()
	for completed < len(h.upstreams) {
		var timerC <-chan time.Time
		if timer != nil {
			timerC = timer.C
		}
		select {
		case res := <-results:
			completed++
			if res.err == nil {
				stopTimer()
				cancel()
				return res.resp, nil
			}
			errs = append(errs, res.err)
			if ctx.Err() != nil {
				stopTimer()
				return nil, ctx.Err()
			}
			if next < len(h.upstreams) {
				stopTimer()
				startNext()
				armTimer()
			}
		case <-timerC:
			timer = nil
			startNext()
			armTimer()
		case <-ctx.Done():
			stopTimer()
			return nil, ctx.Err()
		}
	}
	stopTimer()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return nil, errors.New("doh: all upstreams failed without an error")
}

// Exchange sends a DNS query and returns the decoded response.
func (c *Client) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	packed, err := q.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", dnsMessageMIME)
	req.Header.Set("Accept", dnsMessageMIME)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doh request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("doh status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read doh body: %w", err)
	}
	r := new(dns.Msg)
	if err := r.Unpack(body); err != nil {
		return nil, fmt.Errorf("unpack response: %w", err)
	}
	return r, nil
}
