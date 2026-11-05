package terminal

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
	"github.com/cplieger/go/textsafe"
)

// OriginPolicy is the set of browser origins allowed to open a terminal
// WebSocket, in addition to the serving origin itself.
//
// It is the only cross-origin gate on these sockets: net/http's
// CrossOriginProtection skips GET, so it never sees a WebSocket handshake. A nil
// policy means same-origin only. Immutable and safe for concurrent use.
type OriginPolicy struct {
	allowed map[string]struct{}
}

var errNotAnOrigin = errors.New("not a complete http(s) origin")

// NewOriginPolicy validates an origin allowlist and returns the policy plus the
// entries it dropped, so a caller can warn without failing startup. An entry is
// an http or https origin (RFC 6454) with no path, query, fragment, userinfo or
// wildcard, such as "https://[::1]:7681"; "null" is refused. Blank entries are
// skipped. Matching is exact, lowercased and without a default port. An
// all-invalid list yields a same-origin-only policy.
func NewOriginPolicy(origins ...string) (policy *OriginPolicy, invalidEntries []string) {
	allowed := make(map[string]struct{}, len(origins))
	var invalid []string
	for _, raw := range origins {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		key, err := canonicalOrigin(entry)
		if err != nil {
			invalid = append(invalid, raw)
			continue
		}
		allowed[key] = struct{}{}
	}
	if len(allowed) == 0 {
		// nil is the single representation of "inactive", so there is no second
		// way to be inactive that a later check could miss.
		return nil, invalid
	}
	return &OriginPolicy{allowed: allowed}, invalid
}

// canonicalOrigin validates one operator entry and renders it as a comparison
// key, or reports why it is not a usable origin.
func canonicalOrigin(entry string) (string, error) {
	u, err := url.Parse(entry)
	if err != nil {
		return "", err
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", errNotAnOrigin
	case u.Host == "":
		return "", errNotAnOrigin
	case u.Opaque != "", u.User != nil, u.Path != "", u.RawQuery != "", u.ForceQuery, u.Fragment != "":
		return "", errNotAnOrigin
	case strings.ContainsAny(u.Host, "*?"):
		// Refuse a wildcard rather than store it. Matching is exact, so an entry
		// like "https://*.example.com" is not dangerous — it simply matches
		// nothing, because no browser sends that as an Origin. Silently
		// accepting it is the harm: the operator believes they allowed a
		// subdomain tree and allowed nobody. Reporting it invalid says so.
		return "", errNotAnOrigin
	}
	return originKey(u), nil
}

// originKey renders a parsed origin as the string both sides of the comparison
// are reduced to: host lowercased, and the scheme's default port dropped the way
// a browser's own Origin serialization drops it (RFC 6454 section 6.1), so an
// operator who writes "https://x:443" still matches the "https://x" a browser
// sends instead of silently matching nothing.
func originKey(u *url.URL) string {
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	}
	return u.Scheme + "://" + host
}

// Active reports whether the policy allows any origin beyond same-origin.
// Useful for a startup log line; nil-safe.
func (p *OriginPolicy) Active() bool { return p != nil && len(p.allowed) > 0 }

// Allows reports whether r may open a terminal session under this policy.
// Exported so a consumer can apply the same decision to its own routes.
//
// A request with no Origin header (a non-browser client) or a same-origin one
// is allowed; any other must match the allowlist. Same-origin compares hosts
// only, since Host carries no scheme. A nil policy allows same-origin only.
func (p *OriginPolicy) Allows(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		// Unparseable, or the literal "null" a sandboxed iframe sends.
		return false
	}
	if strings.EqualFold(r.Host, u.Host) {
		return true
	}
	if p == nil {
		return false
	}
	_, ok := p.allowed[originKey(u)]
	return ok
}

// acceptOptions renders the policy for coder/websocket.
//
// Inactive: nil, which leaves the library's own same-origin check as the gate.
//
// Active: InsecureSkipVerify, since OriginPatterns' path.Match globbing is
// weaker than Allows. Only acceptWS, after enforcing the policy, may use it.
func (p *OriginPolicy) acceptOptions() *websocket.AcceptOptions {
	if !p.Active() {
		return nil
	}
	return &websocket.AcceptOptions{InsecureSkipVerify: true}
}

// acceptWS enforces the origin policy and then upgrades the connection.
//
// It must stay the package's only websocket.Accept call, so the session and
// unknown-session sockets apply one origin policy.
func acceptWS(w http.ResponseWriter, r *http.Request, p *OriginPolicy) (*websocket.Conn, error) {
	if !p.Allows(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return nil, fmt.Errorf("terminal: origin %q is not allowed for host %q",
			logSafeHeader(r.Header.Get("Origin")), logSafeHeader(r.Host))
	}
	return websocket.Accept(w, r, p.acceptOptions())
}

// maxLoggedOriginBytes bounds one request-supplied value in the refusal error.
// A legitimate origin is a scheme, a host and maybe a port, so 128 bytes is
// generous for every real one while refusing to carry an arbitrarily long
// header into the logs.
const maxLoggedOriginBytes = 128

// logSafeHeader prepares a request-supplied Origin or Host value for the refusal
// error above, which the two callers log. Sanitizing at construction keeps the
// error safe from the moment it exists, so no caller can reach an unsafe form.
// net/http already ends a header value at CRLF, and the caller's %q escapes the
// C1, Bidi_Control and U+2028/U+2029 runes net/http admits, so on today's sink
// this call buys the CAP (TestAcceptWSRefusalIsLogSafe fails on it alone); the
// class stripping is for a sink that does not quote, such as slog's JSONHandler,
// which emits C1 raw.
func logSafeHeader(v string) string {
	return textsafe.SanitizeSingleLineBounded(v, maxLoggedOriginBytes)
}
