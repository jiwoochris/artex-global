package db

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Normalized natural keys (nkey): ported from the old graph/id.go, minus the StableID
// hash (PG uses a BIGSERIAL primary key + UNIQUE(type, nkey) for dedup). A child asset's
// nkey embeds its parent's int64 id, encoding the hierarchy into the key.

func DomainKey(fqdn string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fqdn)), ".")
}

func IPKey(ip string) string { return strings.TrimSpace(ip) }

// RootDomain returns the registrable domain (eTLD+1) for a host and whether the
// host itself IS that apex (§3.1). Edge cases (§3.1 boundaries): an IP literal or a
// host publicsuffix can't classify (localhost / internal / non-ICANN TLD) is
// returned unchanged as its own root with isApex=true — best-effort, never treated
// as a subdomain.
func RootDomain(host string) (root string, isApex bool) {
	h := DomainKey(host)
	if h == "" || net.ParseIP(h) != nil {
		return h, true
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil || etld1 == "" {
		return h, true
	}
	return etld1, h == etld1
}

func PortKey(ipID int64, proto string, port int) string {
	return itoa(ipID) + "|" + strings.ToLower(proto) + "|" + strconv.Itoa(port)
}

func ServiceKey(portID int64, svcName string) string {
	return itoa(portID) + "|" + strings.ToLower(svcName)
}

func SiteKey(scheme, host string, port int) string {
	return strings.ToLower(scheme) + "|" + strings.ToLower(host) + "|" + strconv.Itoa(port)
}

func EndpointKey(siteID int64, method, urlTemplate string) string {
	return itoa(siteID) + "|" + strings.ToUpper(method) + "|" + urlTemplate
}

func ParameterKey(endpointID int64, location, name string) string {
	return itoa(endpointID) + "|" + strings.ToLower(location) + "|" + name
}

// NormalizeParamName normalizes a parameter name for the "same reference" test on
// endpoint.params elements. Rule: lower + trim, no synonym folding (userId/user_id/uid
// stay distinct). Writes and lookups share this implementation so "find this company's
// endpoint by parameter name" stays reproducible.
func NormalizeParamName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func TechKey(name, version string) string {
	return strings.ToLower(name) + "|" + version
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

var (
	reNumeric = regexp.MustCompile(`^\d+$`)
	reUUID    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reHex     = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
	reLong    = regexp.MustCompile(`^[A-Za-z0-9_-]{24,}$`)
)

// TemplatePath collapses high-cardinality path segments into placeholders so the
// graph is not flooded by instances: /user/123 -> /user/{id}.
func TemplatePath(path string) string {
	if path == "" {
		return "/"
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		switch {
		case s == "":
			continue
		case reNumeric.MatchString(s):
			segs[i] = "{id}"
		case reUUID.MatchString(s):
			segs[i] = "{uuid}"
		case reHex.MatchString(s):
			segs[i] = "{hex}"
		case reLong.MatchString(s):
			segs[i] = "{token}"
		}
	}
	return strings.Join(segs, "/")
}

// SplitURL parses a raw URL into scheme/host/port/urlTemplate/params for building
// site/endpoint/param keys.
func SplitURL(raw, method string) (scheme, host string, port int, urlTemplate string, params []string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, "", nil, err
	}
	scheme = strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Hostname())
	port = defaultPort(scheme, u.Port())
	urlTemplate = TemplatePath(u.EscapedPath())
	for k := range u.Query() {
		params = append(params, k)
	}
	return scheme, host, port, urlTemplate, params, nil
}

func defaultPort(scheme, p string) int {
	if p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	switch scheme {
	case "https":
		return 443
	case "http":
		return 80
	default:
		return 0
	}
}
