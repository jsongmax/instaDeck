package netguard

import "net/netip"

func SafeAddress(address netip.Addr, allowLoopback bool) bool {
	address = address.Unmap()
	if address.IsLoopback() {
		return allowLoopback
	}
	for _, reserved := range []string{
		"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15",
		"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24",
		"2001:db8::/32", "3fff::/20",
	} {
		if netip.MustParsePrefix(reserved).Contains(address) {
			return false
		}
	}
	return address.IsValid() && address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLinkLocalUnicast() && !address.IsUnspecified() && !address.IsMulticast()
}
