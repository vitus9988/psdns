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

// maxLookupTime bounds the shared upstream lookup that runLookup performs for
// all joined callers. Each caller still returns the instant its own context is
// done (via the select in Resolve); this is only a safety ceiling so that a
// pathological upstream cannot leave the lookup goroutine running forever.
const maxLookupTime = 30 * time.Second

// Resolve returns the IPs for host. An IP literal is returned unchanged.
func (r *Resolver) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}

	// DNS names are case-insensitive, so normalise the cache key; otherwise
	// "Example.com" and "example.com" would be cached (and resolved) separately.
	key := strings.ToLower(host)

	r.mu.Lock()
	if e, ok := r.cache[key]; ok && time.Now().Before(e.expires) {
		ips := e.ips
		r.mu.Unlock()
		return cloneIPs(ips), nil // copy so a caller cannot mutate the cached slice
	}
	// Join the in-flight lookup for this host, or become its leader. The leader
	// launches the single shared lookup; every caller — leader included — then
	// waits on the same result while independently honouring its own context.
	call, leader := r.inflight[key], false
	if call == nil {
		call = &inflightCall{done: make(chan struct{})}
		r.inflight[key] = call
		leader = true
	}
	r.mu.Unlock()

	if leader {
		go r.runLookup(key, call)
	}

	select {
	case <-call.done:
		return cloneIPs(call.ips), call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runLookup performs the one shared upstream lookup for key and publishes the
// result to every caller waiting on call. It runs under a context detached from
// any single caller's deadline (bounded only by maxLookupTime) so that a caller
// giving up — its own context cancelled — cannot abort a lookup that other,
// still-waiting callers depend on. On success the answer is cached.
func (r *Resolver) runLookup(key string, call *inflightCall) {
	ctx, cancel := context.WithTimeout(context.Background(), maxLookupTime)
	defer cancel()

	ips, ttl, err := r.lookup(ctx, key)
	if err == nil && len(ips) == 0 {
		err = fmt.Errorf("no addresses for %s", key)
	}

	now := time.Now()
	r.mu.Lock()
	if err == nil {
		r.storeLocked(key, entry{ips: ips, expires: now.Add(ttl)}, now)
	}
	delete(r.inflight, key)
	r.mu.Unlock()

	call.ips, call.err = ips, err
	close(call.done)
}

// cloneIPs returns a shallow copy of ips so a caller may sort or append to its
// result without corrupting the slice held in the cache — which the inflight
// leader's result and every later cache hit share.
func cloneIPs(ips []net.IP) []net.IP {
	if ips == nil {
		return nil
	}
	return append([]net.IP(nil), ips...)
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

// resolutionDelay bounds how long lookup waits for the second address family
// once the first has already returned usable addresses (RFC 8305 §3 "Resolution
// Delay"). A dual-stack host still returns both A and AAAA in the common case,
// but a slow or unanswered AAAA can no longer stall a name whose A already
// resolved — a stall that, with all traffic forced through the proxy, shows up as
// a normally-working site hanging for the full timeout.
const resolutionDelay = 50 * time.Millisecond

// familyResult is one address family's answer from the DoH upstream.
type familyResult struct {
	qtype   uint16
	ips     []net.IP
	ttl     uint32
	haveTTL bool
	err     error
}

// lookup queries A and AAAA concurrently over DoH and merges the answers IPv4
// first. It returns as soon as one family yields addresses, waiting only
// resolutionDelay for the other, so a hung query for one family cannot hold up a
// name the other already resolved. An error is returned only when neither family
// produced any address (both empty or both failed).
func (r *Resolver) lookup(ctx context.Context, host string) ([]net.IP, time.Duration, error) {
	fqdn := dns.Fqdn(host)

	query := func(qtype uint16) familyResult {
		m := new(dns.Msg)
		m.SetQuestion(fqdn, qtype)
		m.RecursionDesired = true
		m.SetEdns0(4096, false) // advertise a larger UDP buffer to the upstream
		resp, err := r.doh.Exchange(ctx, m)
		if err != nil {
			return familyResult{qtype: qtype, err: err}
		}
		res := familyResult{qtype: qtype}
		for _, rr := range resp.Answer {
			switch v := rr.(type) {
			case *dns.A:
				res.ips = append(res.ips, v.A)
			case *dns.AAAA:
				res.ips = append(res.ips, v.AAAA)
			default:
				continue
			}
			if t := rr.Header().Ttl; !res.haveTTL || t < res.ttl {
				res.ttl = t
				res.haveTTL = true
			}
		}
		return res
	}

	// Buffered so a family that arrives after we have already returned can still
	// deliver its result and its goroutine exit without leaking.
	ch := make(chan familyResult, 2)
	go func() { ch <- query(dns.TypeA) }()
	go func() { ch <- query(dns.TypeAAAA) }()

	var (
		v4, v6   []net.IP
		ttl      uint32
		haveTTL  bool
		firstErr error
	)
	merge := func(res familyResult) {
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			return
		}
		if res.qtype == dns.TypeA {
			v4 = append(v4, res.ips...)
		} else {
			v6 = append(v6, res.ips...)
		}
		if res.haveTTL && (!haveTTL || res.ttl < ttl) {
			ttl = res.ttl
			haveTTL = true
		}
	}

	// Wait for the first family to return.
	select {
	case res := <-ch:
		merge(res)
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}

	// If it produced addresses, wait only a short grace period for the other
	// family; otherwise wait for it unconditionally (we have nothing yet).
	if len(v4)+len(v6) > 0 {
		timer := time.NewTimer(resolutionDelay)
		defer timer.Stop()
		select {
		case res := <-ch:
			merge(res)
		case <-timer.C:
		case <-ctx.Done():
		}
	} else {
		select {
		case res := <-ch:
			merge(res)
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}

	// Merge IPv4 before IPv6 so the dial order is deterministic (the two queries
	// race, so raw arrival order is not) and IPv4-first: on a host with broken
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
