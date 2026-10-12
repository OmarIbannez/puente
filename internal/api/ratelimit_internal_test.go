package api

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	cases := []struct {
		remote, xff string
		trust       bool
		want        string
	}{
		{"192.0.2.1:5000", "", false, "192.0.2.1"},
		{"192.0.2.1:5000", "198.51.100.7", false, "192.0.2.1"},
		{"192.0.2.1:5000", "6.6.6.6, 198.51.100.7", true, "198.51.100.7"},
		{"[2001:db8:1:2:3:4:5:6]:5000", "", false, "2001:db8:1:2::/64"},
		{"192.0.2.1:5000", "2001:db8:1:2:aaaa::1", true, "2001:db8:1:2::/64"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r, c.trust); got != c.want {
			t.Errorf("clientIP(%s, %q, %v) = %q, want %q", c.remote, c.xff, c.trust, got, c.want)
		}
	}
}
