package crawler

import (
	"errors"
	"net"
	"net/url"
	"path"
	"sort"
	"strings"
)

var trackingParameters = map[string]struct{}{
	"fbclid": {}, "gclid": {}, "mc_cid": {}, "mc_eid": {},
}

func Canonicalize(raw string, base *url.URL) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if base != nil {
		parsed = base.ResolveReference(parsed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("only http and https URLs are supported")
	}
	if parsed.User != nil || parsed.Hostname() == "" {
		return "", errors.New("URL credentials and empty hosts are not allowed")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if (parsed.Scheme == "http" && parsed.Port() == "80") || (parsed.Scheme == "https" && parsed.Port() == "443") {
		parsed.Host = parsed.Hostname()
	}
	parsed.Fragment = ""
	cleanedPath := path.Clean("/" + strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if strings.HasSuffix(parsed.Path, "/") && cleanedPath != "/" {
		cleanedPath += "/"
	}
	parsed.RawPath = ""
	parsed.Path = cleanedPath
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") {
			query.Del(key)
			continue
		}
		if _, tracked := trackingParameters[lower]; tracked {
			query.Del(key)
		}
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := url.Values{}
	for _, key := range keys {
		values := query[key]
		sort.Strings(values)
		ordered[key] = values
	}
	parsed.RawQuery = ordered.Encode()
	return parsed.String(), nil
}

func AllowedHost(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, candidate := range allowed {
		candidate = strings.ToLower(strings.TrimSuffix(candidate, "."))
		if host == candidate {
			return true
		}
	}
	return false
}

func publicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	// Cloud metadata and carrier-grade NAT ranges are not consistently covered by IsPrivate.
	blocked := []string{"100.64.0.0/10", "169.254.0.0/16", "0.0.0.0/8", "198.18.0.0/15", "::1/128", "fc00::/7", "fe80::/10"}
	for _, cidr := range blocked {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return false
		}
	}
	return true
}
