package bus

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// HostedClientAddress enables per-client rate limits on the unauthenticated
// OAuth and device-pairing endpoints. The hosted listener runs behind a TLS
// proxy, so every TCP peer is the proxy; the client address therefore comes
// from a forwarding header that only trusted proxies may set.
type HostedClientAddress struct {
	// Header names the forwarding header the proxy sets, for example
	// X-Forwarded-For. Its value is a comma-separated list of addresses with
	// the address the nearest proxy saw last.
	Header string
	// TrustedProxies lists the networks whose entries in Header are skipped.
	// The TCP peer must belong to one of them, or the header is ignored and
	// the peer itself is the client. Empty means loopback only.
	TrustedProxies []netip.Prefix
}

func (c *HostedClientAddress) validate() error {
	if c == nil {
		return nil
	}
	if c.Header == "" || strings.ContainsAny(c.Header, " \t\r\n,:") {
		return errors.New("client address header must be a single header name")
	}
	for _, prefix := range c.TrustedProxies {
		if !prefix.IsValid() {
			return errors.New("trusted proxy networks must be valid CIDR prefixes")
		}
	}
	return nil
}

func (c *HostedClientAddress) trusted(addr netip.Addr) bool {
	addr = addr.Unmap()
	if len(c.TrustedProxies) == 0 {
		return addr.IsLoopback()
	}
	for _, prefix := range c.TrustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientKey returns the rate-limit key for a request: the first address,
// reading the forwarding header from the right, that is not a trusted proxy.
// IPv6 clients are grouped by /64 so a single host cannot rotate addresses
// within its own allocation to escape its limit.
func (c *HostedClientAddress) clientKey(r *http.Request) (netip.Prefix, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Prefix{}, false
	}
	client := peer.Unmap()
	if c.trusted(client) {
		values := r.Header.Values(c.Header)
		hops := []string{}
		for _, value := range values {
			hops = append(hops, strings.Split(value, ",")...)
		}
		for i := len(hops) - 1; i >= 0; i-- {
			addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				// A malformed hop cannot be attributed; keep the last trusted
				// address so the request still counts against a bucket.
				break
			}
			client = addr.Unmap()
			if !c.trusted(client) {
				break
			}
		}
	}
	bits := 32
	if client.Is6() {
		bits = 64
	}
	prefix, err := client.Prefix(bits)
	return prefix, err == nil
}

const (
	clientLimiterCapacity = 10_000
	clientLimiterIdle     = 10 * time.Minute
)

type clientLimiterEntry struct {
	limiter *rate.Limiter
	seen    time.Time
}

// clientLimiter keeps a bounded token bucket per client. When the table is
// full of recently active clients, new clients are admitted without a
// per-client bucket; the endpoint's global limit still applies to them.
type clientLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	clients map[netip.Prefix]*clientLimiterEntry
	now     func() time.Time
}

func newClientLimiter(limit rate.Limit, burst int) *clientLimiter {
	return &clientLimiter{limit: limit, burst: burst, clients: map[netip.Prefix]*clientLimiterEntry{}, now: time.Now}
}

func (l *clientLimiter) allow(key netip.Prefix) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry := l.clients[key]
	if entry == nil {
		if len(l.clients) >= clientLimiterCapacity {
			for k, e := range l.clients {
				if now.Sub(e.seen) > clientLimiterIdle {
					delete(l.clients, k)
				}
			}
			if len(l.clients) >= clientLimiterCapacity {
				return true
			}
		}
		entry = &clientLimiterEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[key] = entry
	}
	entry.seen = now
	return entry.limiter.AllowN(now, 1)
}

// endpointLimit applies an optional per-client limit before the endpoint's
// global limit, so a client over its own allowance does not consume capacity
// shared with everyone else.
type endpointLimit struct {
	global  *rate.Limiter
	client  *clientLimiter
	address *HostedClientAddress
}

func newEndpointLimit(address *HostedClientAddress, global, perClient rate.Limit, globalBurst, clientBurst int) *endpointLimit {
	l := &endpointLimit{global: rate.NewLimiter(global, globalBurst), address: address}
	if address != nil {
		l.client = newClientLimiter(perClient, clientBurst)
	}
	return l
}

func (l *endpointLimit) allow(r *http.Request) bool {
	if l.client != nil {
		if key, ok := l.address.clientKey(r); ok && !l.client.allow(key) {
			return false
		}
	}
	return l.global.Allow()
}
