package main

import "testing"

func TestHostedClientAddressFlags(t *testing.T) {
	if address, err := hostedClientAddress("", nil); err != nil || address != nil {
		t.Fatalf("per-client limits enabled without a header: %v %v", address, err)
	}
	if _, err := hostedClientAddress("", []string{"10.0.0.1"}); err == nil {
		t.Fatal("accepted --trusted-proxy without --client-ip-header")
	}
	address, err := hostedClientAddress("X-Forwarded-For", []string{"10.0.0.1", "192.168.0.0/16", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.1/32", "192.168.0.0/16", "::1/128"}
	if address.Header != "X-Forwarded-For" || len(address.TrustedProxies) != len(want) {
		t.Fatalf("unexpected policy %+v", address)
	}
	for i, prefix := range address.TrustedProxies {
		if prefix.String() != want[i] {
			t.Fatalf("proxy %d = %s, want %s", i, prefix, want[i])
		}
	}
	if _, err := hostedClientAddress("X-Forwarded-For", []string{"proxy.example"}); err == nil {
		t.Fatal("accepted a host name as a trusted proxy")
	}
}
