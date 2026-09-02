package crawl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDialTarget(t *testing.T) {
	cases := []struct {
		name, remoteIP, rpc string
		want                string
		ok                  bool
	}{
		{"unspecified bind", "192.0.2.10", "tcp://0.0.0.0:26657", "http://192.0.2.10:26657", true},
		{"unspecified v6 bind", "192.0.2.10", "tcp://[::]:26657", "http://192.0.2.10:26657", true},
		{"custom port", "203.0.113.9", "tcp://0.0.0.0:2001", "http://203.0.113.9:2001", true},
		{"bound to own ip", "192.0.2.13", "tcp://192.0.2.13:26657", "http://192.0.2.13:26657", true},
		{"ipv6 peer", "2001:db8::1", "tcp://0.0.0.0:26657", "http://[2001:db8::1]:26657", true},
		{"no scheme", "192.0.2.10", "0.0.0.0:26657", "http://192.0.2.10:26657", true},
		{"loopback bind", "198.51.100.5", "tcp://127.0.0.1:26657", "", false},
		{"private bind", "198.51.100.6", "tcp://10.0.0.5:26657", "", false},
		{"bound elsewhere", "198.51.100.6", "tcp://198.51.100.7:26657", "", false},
		{"hostname bind", "198.51.100.6", "tcp://rpc.example:26657", "", false},
		{"empty rpc", "203.0.113.8", "", "", false},
		{"no port", "203.0.113.8", "tcp://0.0.0.0", "", false},
		{"bad port", "203.0.113.8", "tcp://0.0.0.0:http", "", false},
		{"port zero", "203.0.113.8", "tcp://0.0.0.0:0", "", false},
		{"port too big", "203.0.113.8", "tcp://0.0.0.0:65536", "", false},
		{"private peer", "10.1.2.3", "tcp://0.0.0.0:26657", "", false},
		{"loopback peer", "127.0.0.1", "tcp://0.0.0.0:26657", "", false},
		{"link-local peer", "169.254.1.1", "tcp://0.0.0.0:26657", "", false},
		{"unspecified peer", "0.0.0.0", "tcp://0.0.0.0:26657", "", false},
		{"garbage peer", "not-an-ip", "tcp://0.0.0.0:26657", "", false},
		{"empty peer", "", "tcp://0.0.0.0:26657", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := dialTarget(c.remoteIP, c.rpc)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestNodeKey(t *testing.T) {
	cases := []struct {
		name, observed, listen, fallback, wantKey, wantIP string
	}{
		{"unspecified listen", "192.0.2.10", "tcp://0.0.0.0:26656", "", "192.0.2.10:26656", "192.0.2.10"},
		{"advertised public ip wins", "192.0.2.10", "203.0.113.9:20390", "", "203.0.113.9:20390", "203.0.113.9"},
		{"advertised private ip ignored", "192.0.2.10", "tcp://10.0.0.1:26656", "", "192.0.2.10:26656", "192.0.2.10"},
		{"no listen", "192.0.2.10", "", "", "192.0.2.10", "192.0.2.10"},
		{"garbage listen", "192.0.2.10", "tcp://", "", "192.0.2.10", "192.0.2.10"},
		{"ipv6", "2001:db8::1", "tcp://[::]:26656", "", "[2001:db8::1]:26656", "2001:db8::1"},
		{"no ip at all uses fallback", "", "tcp://0.0.0.0:26656", "https://rpc.example", "https://rpc.example", ""},
		{"no ip but advertised", "", "203.0.113.9:26656", "https://rpc.example", "203.0.113.9:26656", "203.0.113.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, ip := nodeKey(c.observed, c.listen, c.fallback)
			assert.Equal(t, c.wantKey, key)
			assert.Equal(t, c.wantIP, ip)
		})
	}
}

func TestSeedTarget(t *testing.T) {
	cases := []struct {
		raw, url, host string
		ok             bool
	}{
		{"https://rpc.example:443/", "https://rpc.example:443", "rpc.example", true},
		{"http://192.0.2.1:26657", "http://192.0.2.1:26657", "192.0.2.1", true},
		{"tcp://192.0.2.1:26657", "", "", false},
		{"rpc.example", "", "", false},
		{"http://", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			u, h, ok := seedTarget(c.raw)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.url, u)
			assert.Equal(t, c.host, h)
		})
	}
}
