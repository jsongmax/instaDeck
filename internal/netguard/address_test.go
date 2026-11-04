package netguard

import (
	"net/netip"
	"testing"
)

func TestReservedAndMappedAddresses(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "192.0.2.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "2001:db8::1", "0.0.0.0"} {
		if SafeAddress(netip.MustParseAddr(value), false) {
			t.Fatal("allowed reserved", value)
		}
	}
	if !SafeAddress(netip.MustParseAddr("8.8.8.8"), false) || !SafeAddress(netip.MustParseAddr("127.0.0.1"), true) {
		t.Fatal("public or test loopback rejected")
	}
}
