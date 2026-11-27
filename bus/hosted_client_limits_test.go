package bus

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func clientRequest(remote string, forwarded ...string) *http.Request {
	r := httptest.NewRequest("POST", "https://bus.example.test/oauth/register", nil)
	r.RemoteAddr = remote
	for _, value := range forwarded {
		r.Header.Add("X-Forwarded-For", value)
	}
	return r
}

func TestHostedClientAddressUsesRightmostUntrustedHop(t *testing.T) {
	loopback := &HostedClientAddress{Header: "X-Forwarded-For"}
	proxies := &HostedClientAddress{Header: "X-Forwarded-For", TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")}}
	for _, tc := range []struct {
		name    string
		policy  *HostedClientAddress
		request *http.Request
		want    string
	}{
		{"proxy-supplied client", loopback, clientRequest("127.0.0.1:5000", "203.0.113.7"), "203.0.113.7/32"},
		// A client cannot choose its key by prepending its own entries.
		{"spoofed leftmost entries", loopback, clientRequest("127.0.0.1:5000", "198.51.100.1, 203.0.113.7"), "203.0.113.7/32"},
		{"repeated header lines", loopback, clientRequest("127.0.0.1:5000", "198.51.100.1", "203.0.113.7"), "203.0.113.7/32"},
		{"chained trusted proxies", proxies, clientRequest("127.0.0.1:5000", "198.51.100.1, 203.0.113.7, 10.1.2.3"), "203.0.113.7/32"},
		{"untrusted peer ignores header", loopback, clientRequest("192.0.2.10:5000", "203.0.113.7"), "192.0.2.10/32"},
		{"missing header counts against the proxy", loopback, clientRequest("127.0.0.1:5000"), "127.0.0.1/32"},
		{"malformed hop is not attributed", loopback, clientRequest("127.0.0.1:5000", "203.0.113.7, not-an-ip"), "127.0.0.1/32"},
		{"IPv6 clients group by /64", loopback, clientRequest("[::1]:5000", "2001:db8:1:2:aaaa::1"), "2001:db8:1:2::/64"},
		{"IPv4-mapped IPv6", loopback, clientRequest("127.0.0.1:5000", "::ffff:203.0.113.7"), "203.0.113.7/32"},
	} {
		got, ok := tc.policy.clientKey(tc.request)
		require(t, ok && got.String() == tc.want, "%s: got %v (%v), want %s", tc.name, got, ok, tc.want)
	}
}

func TestHostedClientAddressValidation(t *testing.T) {
	var none *HostedClientAddress
	requireNoError(t, none.validate())
	requireNoError(t, (&HostedClientAddress{Header: "X-Forwarded-For"}).validate())
	for _, bad := range []*HostedClientAddress{{}, {Header: "X-Forwarded-For, X-Real-IP"}, {Header: "X-Real-IP:"}, {Header: "X-Forwarded-For", TrustedProxies: []netip.Prefix{{}}}} {
		require(t, bad.validate() != nil, "accepted invalid client address policy %+v", bad)
	}
}

func TestEndpointLimitIsolatesClients(t *testing.T) {
	policy := &HostedClientAddress{Header: "X-Forwarded-For"}
	limit := newEndpointLimit(policy, rate.Every(time.Hour), rate.Every(time.Hour), 10, 2)
	noisy := clientRequest("127.0.0.1:1", "203.0.113.1")
	for range 2 {
		require(t, limit.allow(noisy), "client refused within its burst")
	}
	// Requests over the client's own limit must not drain the shared budget.
	for range 50 {
		require(t, !limit.allow(noisy), "client exceeded its own limit")
	}
	for i := range 8 {
		other := clientRequest("127.0.0.1:1", "198.51.100."+string(rune('1'+i)))
		require(t, limit.allow(other), "a noisy client starved client %d", i)
	}
	require(t, !limit.allow(clientRequest("127.0.0.1:1", "192.0.2.99")), "global limit no longer applies")

	// Without a client address policy only the global limit applies.
	global := newEndpointLimit(nil, rate.Every(time.Hour), rate.Every(time.Hour), 3, 1)
	for range 3 {
		require(t, global.allow(noisy), "global-only limit refused within its burst")
	}
	require(t, !global.allow(clientRequest("127.0.0.1:1", "198.51.100.1")), "global-only limit exceeded")
}

func TestClientLimiterBoundsItsTable(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newClientLimiter(rate.Every(time.Hour), 1)
	l.now = func() time.Time { return now }
	key := func(i int) netip.Prefix {
		return netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 32)
	}
	for i := range clientLimiterCapacity {
		require(t, l.allow(key(i)), "new client %d refused", i)
	}
	require(t, !l.allow(key(0)), "known client bypassed its limit")
	// A full table of active clients admits newcomers to the global limit only.
	overflow := key(clientLimiterCapacity)
	require(t, l.allow(overflow) && l.allow(overflow), "overflow client was refused")
	require(t, len(l.clients) == clientLimiterCapacity, "table grew past its capacity: %d", len(l.clients))
	// Idle entries are evicted to make room again.
	now = now.Add(clientLimiterIdle + time.Second)
	require(t, l.allow(overflow), "overflow client refused after idle eviction")
	require(t, !l.allow(overflow), "overflow client not tracked after eviction")
	require(t, len(l.clients) == 1, "idle clients were not evicted: %d", len(l.clients))
}

func TestHostedRegistrationLimitIsPerClient(t *testing.T) {
	f := newHostedOAuthFixture(t)
	h, err := NewHostedServer(f.h.accounts, HostedServerOptions{PublicURL: "https://bus.example.test", Identity: f.h.identity, ClientAddress: &HostedClientAddress{Header: "X-Forwarded-For"}})
	requireNoError(t, err)
	register := func(client string) int {
		r := httptest.NewRequest("POST", "https://bus.example.test/oauth/register", strings.NewReader(`{"client_name":"Limit test","redirect_uris":["http://127.0.0.1:5555/callback"],"token_endpoint_auth_method":"none"}`))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("X-Forwarded-For", client)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	codes := map[int]int{}
	for range 10 {
		codes[register("203.0.113.1")]++
	}
	require(t, codes[201] == 5 && codes[429] == 5, "per-client registration limit not applied: %v", codes)
	require(t, register("198.51.100.1") == 201, "another client was starved by the first")
	_, err = NewHostedServer(f.h.accounts, HostedServerOptions{PublicURL: "https://bus.example.test", Identity: f.h.identity, ClientAddress: &HostedClientAddress{}})
	require(t, err != nil, "accepted a client address policy without a header")
}
