// Package resolver turns host names into IP addresses over DoH, with a small
// TTL cache. The SNI-bypass proxy uses it so that name resolution never
// touches the (tampered) system DNS.
package resolver

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
)

const (
	minTTL = 30 * time.Second
	maxTTL = time.Hour

	// maxCacheEntries bounds the resolver cache so a long-running process cannot
	// grow it without limit. When full, expired entries are dropped first.
	maxCacheEntries = 4096
)

type entry struct {
	ips     []net.IP
	expires time.Time
}

// Resolver resolves names via a DoH client.
type Resolver struct {
	doh   doh.Exchanger
	mu    sync.Mutex
	cache map[string]entry
	// inflight collapses concurrent misses for the same host into a single
	// upstream lookup; late callers wait on the leader's result. Guarded by mu.
	inflight map[string]*inflightCall
}

// inflightCall is one in-progress lookup shared by every caller that asked for
// the same host while it was running.
type inflightCall struct {
	done chan struct{}
	ips  []net.IP
	err  error
}

// New returns a Resolver backed by the given DoH client.
func New(c doh.Exchanger) *Resolver {
	return &Resolver{doh: c, cache: make(map[string]entry), inflight: make(map[string]*inflightCall)}
}

// Resolve returns the IPs for host. An IP literal is returned unchanged.
func (r *Resolver) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}

	// DNS names are case-insensitive, so normalise the cache key; otherwise
	// "Example.com" and "example.com" would be cached (and resolved) separately.
	key := strings.ToLower(host)

	now := time.Now()
	r.mu.Lock()
	if e, ok := r.cache[key]; ok && now.Before(e.expires) {
		ips := e.ips
		r.mu.Unlock()
		return ips, nil
	}
	// Join an in-flight lookup for the same host instead of firing a duplicate.
	if call, ok := r.inflight[key]; ok {
		r.mu.Unlock()
		select {
		case <-call.done:
			return call.ips, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Become the leader: publish the in-flight call before releasing the lock so
	// every later caller joins it.
	call := &inflightCall{done: make(chan struct{})}
	r.inflight[key] = call
	r.mu.Unlock()

	ips, ttl, err := r.lookup(ctx, key)
	if err == nil && len(ips) == 0 {
		err = fmt.Errorf("no addresses for %s", host)
	}

	r.mu.Lock()
	if err == nil {
		r.storeLocked(key, entry{ips: ips, expires: now.Add(ttl)}, now)
	}
	delete(r.inflight, key)
	r.mu.Unlock()

	call.ips, call.err = ips, err
	close(call.done)
	return ips, err
}

// storeLocked inserts e under key while enforcing maxCacheEntries. The caller
// must hold r.mu. When the cache is full it evicts expired entries first; if
// every entry is still live it drops arbitrary ones so the map stays bounded.
func (r *Resolver) storeLocked(key string, e entry, now time.Time) {
	if len(r.cache) >= maxCacheEntries {
		for k, v := range r.cache {
			if !now.Before(v.expires) {
				delete(r.cache, k)
			}
		}
		// If every entry is still live, evict the soonest-to-expire ones (least
		// useful to keep) until back under the cap.
		for len(r.cache) >= maxCacheEntries {
			soonKey, first := "", true
			var soonExp time.Time
			for k, v := range r.cache {
				if first || v.expires.Before(soonExp) {
					soonKey, soonExp, first = k, v.expires, false
				}
			}
			delete(r.cache, soonKey)
		}
	}
	r.cache[key] = e
}

// lookup queries A and AAAA concurrently over DoH and merges the answers.
func (r *Resolver) lookup(ctx context.Context, host string) ([]net.IP, time.Duration, error) {
	fqdn := dns.Fqdn(host)

	var (
		mu       sync.Mutex
		v4, v6   []net.IP
		ttl      uint32
		haveTTL  bool
		firstErr error
	)

	query := func(qtype uint16) {
		m := new(dns.Msg)
		m.SetQuestion(fqdn, qtype)
		m.RecursionDesired = true
		m.SetEdns0(4096, false) // advertise a larger UDP buffer to the upstream
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
			switch v := rr.(type) {
			case *dns.A:
				v4 = append(v4, v.A)
			case *dns.AAAA:
				v6 = append(v6, v.AAAA)
			default:
				continue
			}
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

	// Merge IPv4 before IPv6 so the dial order is deterministic (the two queries
	// race, so raw append order is not) and IPv4-first: on a host with broken
	// IPv6 the reachable address is tried before the dead one.
	ips := append(v4, v6...)

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
