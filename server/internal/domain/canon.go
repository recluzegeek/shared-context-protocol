package domain

import (
	"errors"
	"net/url"
	"slices"
	"strings"
)

// DefaultCaseInsensitiveHosts are the hosts spec §4.7.1 requires to be treated as
// case-insensitive about repository paths.
var DefaultCaseInsensitiveHosts = []string{"github.com", "gitlab.com", "bitbucket.org"}

// ErrBadResource is returned for a resource URI that is neither a Git remote nor absolute.
var ErrBadResource = errors.New("resource must be a Git remote or an absolute URI")

var defaultPorts = map[string]string{"ssh": "22", "https": "443", "http": "80", "git": "9418"}

// CanonicalResource returns the canonical form of a resource URI (spec §4.7.1). Git remotes and
// git: URIs become git:host[:port]/path; any other absolute URI is returned unchanged.
func CanonicalResource(raw string, caseInsensitiveHosts []string) (string, error) {
	raw = strings.TrimSpace(raw)
	var host, port, path string
	switch {
	case strings.HasPrefix(raw, "git:") && !strings.HasPrefix(raw, "git://"):
		rest := strings.TrimPrefix(raw, "git:")
		hp, p, _ := strings.Cut(rest, "/")
		host, port, _ = strings.Cut(hp, ":")
		path = p
	case isSCPLike(raw):
		userHost, p, _ := strings.Cut(raw, ":")
		_, host, _ = strings.Cut(userHost, "@")
		path = p
	default:
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" {
			return "", ErrBadResource
		}
		if _, gitScheme := defaultPorts[u.Scheme]; !gitScheme || u.Host == "" {
			return raw, nil // not a Git remote: any absolute URI, compared as given
		}
		host, port, path = u.Hostname(), u.Port(), u.Path
		if port == defaultPorts[u.Scheme] {
			port = ""
		}
	}
	host = strings.ToLower(host)
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return "", ErrBadResource
	}
	if slices.Contains(caseInsensitiveHosts, host) {
		path = strings.ToLower(path)
	}
	if port != "" {
		host += ":" + port
	}
	return "git:" + host + "/" + path, nil
}

// isSCPLike reports the scp-style Git remote form user@host:path.
func isSCPLike(s string) bool {
	if strings.Contains(s, "://") {
		return false
	}
	at := strings.Index(s, "@")
	colon := strings.Index(s, ":")
	return at > 0 && colon > at
}
