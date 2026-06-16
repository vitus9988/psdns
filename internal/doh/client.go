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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/miekg/dns"
)

const dnsMessageMIME = "application/dns-message"

// Client is a DoH client bound to a single upstream endpoint.
type Client struct {
	endpoint string
	http     *http.Client
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
	defer resp.Body.Close()
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
