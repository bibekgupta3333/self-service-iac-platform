package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxies is the set of reverse proxies whose X-Forwarded-For header may be
// believed (IACP_TRUSTED_PROXIES). The zero value trusts nobody, so the client address is
// always the TCP peer.
type TrustedProxies struct{ nets []netip.Prefix }

// ParseTrustedProxies parses CIDRs; a bare address means a single host. Pure.
func ParseTrustedProxies(entries []string) (TrustedProxies, error) {
	var t TrustedProxies
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		p, err := netip.ParsePrefix(e)
		if err != nil {
			a, aerr := netip.ParseAddr(e)
			if aerr != nil {
				return TrustedProxies{}, fmt.Errorf("trusted proxy %q is neither a CIDR nor an address", e)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		t.nets = append(t.nets, p.Masked())
	}
	return t, nil
}

// Empty reports whether no proxy is trusted.
func (t TrustedProxies) Empty() bool { return len(t.nets) == 0 }

func (t TrustedProxies) contains(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range t.nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP is the address of the client behind remoteAddr. Only when the immediate peer is
// a trusted proxy is X-Forwarded-For read, from the right: each hop appends the address it
// received the request from, so the right-most entries are written by our own proxies and
// the first one that is not trusted is the real client. Entries to its left are whatever
// the client claimed and are never used. If the peer is not trusted, the header is absent,
// every hop is trusted, or an entry is not an address, the peer itself is returned (an
// empty string when even that is unparseable). Pure.
func (t TrustedProxies) ClientIP(remoteAddr string, xff []string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	peer, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return ""
	}
	peer = peer.Unmap()
	if !t.contains(peer) {
		return peer.String()
	}
	var hops []string
	for _, v := range xff {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer.String() // garbage from a proxy we trust: do not guess
		}
		if a = a.Unmap(); !t.contains(a) {
			return a.String()
		}
	}
	return peer.String()
}

// Request is ClientIP for an HTTP request.
func (t TrustedProxies) Request(r *http.Request) string {
	return t.ClientIP(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
}
