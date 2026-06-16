// Package resolver turns host names into IP addresses over DoH, with a small
// TTL cache. The SNI-bypass proxy uses it so that name resolution never
// touches the (tampered) system DNS.
package resolver

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
)

const (
	minTTL = 30 * time.Second
	maxTTL = time.Hour
)

type entry struct {
	ips     []net.IP
	expires time.Time
}

// Resolver resolves names via a DoH client.
type Resolver struct {
	doh   *doh.Client
	mu    sync.Mutex
	cache map[string]entry
}

// New returns a Resolver backed by the given DoH client.
func New(c *doh.Client) *Resolver {
	return &Resolver{doh: c, cache: make(map[string]entry)}
}

// Resolve returns the IPs for host. An IP literal is returned unchanged.
func (r *Resolver) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}

	now := time.Now()
	r.mu.Lock()
	if e, ok := r.cache[host]; ok && now.Before(e.expires) {
		ips := e.ips
		r.mu.Unlock()
		return ips, nil
	}
	r.mu.Unlock()

	ips, ttl, err := r.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}

	r.mu.Lock()
	r.cache[host] = entry{ips: ips, expires: now.Add(ttl)}
	r.mu.Unlock()
	return ips, nil
}

// lookup queries A and AAAA concurrently over DoH and merges the answers.
func (r *Resolver) lookup(ctx context.Context, host string) ([]net.IP, time.Duration, error) {
	fqdn := dns.Fqdn(host)

	var (
		mu       sync.Mutex
		ips      []net.IP
		ttl      uint32
		haveTTL  bool
		firstErr error
	)

	query := func(qtype uint16) {
		m := new(dns.Msg)
		m.SetQuestion(fqdn, qtype)
		m.RecursionDesired = true
		resp, err := r.doh.Exchange(ctx, m)

		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		for _, rr := range resp.Answer {
			var ip net.IP
			switch v := rr.(type) {
			case *dns.A:
				ip = v.A
			case *dns.AAAA:
				ip = v.AAAA
			default:
				continue
			}
			ips = append(ips, ip)
			if t := rr.Header().Ttl; !haveTTL || t < ttl {
				ttl = t
				haveTTL = true
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); query(dns.TypeA) }()
	go func() { defer wg.Done(); query(dns.TypeAAAA) }()
	wg.Wait()

	if len(ips) == 0 {
		if firstErr != nil {
			return nil, 0, firstErr
		}
		return nil, 0, nil
	}

	d := time.Duration(ttl) * time.Second
	if d < minTTL {
		d = minTTL
	}
	if d > maxTTL {
		d = maxTTL
	}
	return ips, d, nil
}
