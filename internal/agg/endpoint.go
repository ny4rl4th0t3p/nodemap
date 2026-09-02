package agg

import (
	"net/url"
	"strings"
)

// tokenLen is the shortest path segment treated as a credential. Real RPC
// paths are short words ("cosmoshub", "rpc"); API keys, whatever their
// alphabet, are long. Length alone is the test so that a token with dots or
// other punctuation, a JWT for instance, cannot slip through.
const tokenLen = 20

// normalizeEndpoint reduces a dialed URL to what may be published: scheme,
// lower-cased host and port, and path. It refuses anything that could carry
// a credential: userinfo, a query string, a fragment, or a path segment
// long enough to be a token. A refused endpoint drops out of the directory
// only; the node still counts in every aggregate.
func normalizeEndpoint(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", false
	}
	path := strings.Trim(u.EscapedPath(), "/")
	if path != "" {
		for _, seg := range strings.Split(path, "/") {
			if len(seg) >= tokenLen {
				return "", false
			}
		}
		path = "/" + path
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + path, true
}
