package crawl

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	tcpScheme = "tcp://"
	maxPort   = 65535
)

// dialTarget derives the only RPC URL the crawler may dial for a peer: the
// IP the peer was observed at, on the port the peer itself advertised in
// rpc_address. Anything else is refused: no rpc_address, a bind address on
// loopback or a private network, a bind to some other host, or an observed
// IP that is not a public unicast address. The crawler never guesses ports
// and never follows a node into an address range it did not advertise.
func dialTarget(remoteIP, rpcAddress string) (string, bool) {
	ip := net.ParseIP(remoteIP)
	if !publicUnicast(ip) {
		return "", false
	}
	host, port, ok := splitAddr(rpcAddress)
	if !ok {
		return "", false
	}
	bound := net.ParseIP(host)
	if bound == nil {
		return "", false
	}
	if !bound.IsUnspecified() && !bound.Equal(ip) {
		return "", false
	}
	return "http://" + net.JoinHostPort(ip.String(), port), true
}

// publicUnicast reports whether ip is a routable public address: not nil,
// not unspecified, loopback, link-local, multicast, or private.
func publicUnicast(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

// splitAddr strips an optional tcp:// scheme and splits host and port,
// requiring a numeric port in range.
func splitAddr(addr string) (host, port string, ok bool) {
	addr = strings.TrimPrefix(addr, tcpScheme)
	if addr == "" {
		return "", "", false
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > maxPort {
		return "", "", false
	}
	return h, p, true
}

// nodeKey is a node's transient identity for this run only: the IP it
// advertises in listen_addr when that is a public address, otherwise the IP
// it was observed at, joined with its P2P listen port. It exists so a node
// seen from several vantage points is counted once and so the in-RAM graph
// has vertices. It contains no node id and is never persisted. fallback is
// used when no IP is known at all.
//
// The returned ip is the address the node is identified by, and therefore
// the one to enrich: a node reached through a load balancer or CDN that
// advertises its own listen address is placed where it says it is, not
// where the front door is.
func nodeKey(observedIP, listenAddr, fallback string) (key, ip string) {
	ip = observedIP
	host, port, ok := splitAddr(listenAddr)
	if ok {
		if adv := net.ParseIP(host); publicUnicast(adv) {
			ip = adv.String()
		}
	}
	if ip == "" {
		return fallback, ""
	}
	if !ok {
		return ip, ip
	}
	return net.JoinHostPort(ip, port), ip
}

// seedTarget validates a seed URL and returns it with its host. Seeds are
// operator-supplied, so hostnames and https are allowed here, unlike peers.
func seedTarget(raw string) (target, host string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", "", false
	}
	return strings.TrimRight(u.String(), "/"), u.Hostname(), true
}
