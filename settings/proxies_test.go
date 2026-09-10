package settings

import "testing"

func TestTrustedProxyConfiguration(t *testing.T) {
	s := Server{TrustedProxies: []string{"192.0.2.1", "10.0.0.0/24", "2001:db8::/32", "::ffff:192.0.2.1"}}
	prefixes, err := s.TrustedProxyPrefixes()
	if err != nil || len(prefixes) != 4 {
		t.Fatalf("prefixes=%v err=%v", prefixes, err)
	}
	for _, bad := range []string{"", "example.com", "10.0.0.1/99", "fe80::1%eth0"} {
		s.TrustedProxies = []string{bad}
		if _, err := s.TrustedProxyPrefixes(); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
