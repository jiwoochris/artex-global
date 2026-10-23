// Package orcarouter holds the OrcaRouter-specific integration pieces: the
// two-origin endpoint resolution, the OAuth 2.0 + PKCE (S256) connect flow, the
// credential seam shared by the API-key and PKCE adapters, and the model
// catalog parser used to build capability-filtered model pickers.
//
// OrcaRouter exposes an OpenAI-compatible inference API at
// https://api.orcarouter.ai/v1 and its account authorization endpoints at
// https://www.orcarouter.ai. The two origins are deliberately independent:
// https://api.orcarouter.ai/v1/auth/keys is a 404, so nothing here derives one
// origin from the other.
package orcarouter

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	// DefaultAuthBase is where the browser consent screen and the code
	// exchange live.
	DefaultAuthBase = "https://www.orcarouter.ai"
	// DefaultAPIBase is the OpenAI-compatible inference and model-list origin.
	DefaultAPIBase = "https://api.orcarouter.ai/v1"
	// AppName is shown on the consent screen.
	AppName = "ARTEX"
	// ScopeAPI is the only scope the inference path can use.
	ScopeAPI = "api"
)

// Endpoints is the resolved pair of OrcaRouter origins.
type Endpoints struct {
	AuthBase string // e.g. https://www.orcarouter.ai (no trailing slash)
	APIBase  string // e.g. https://api.orcarouter.ai/v1 (no trailing slash)
}

// ResolveEndpoints reads the environment and applies the documented
// precedence: an explicit per-origin override wins, then the shared
// self-hosted fallback (ORCA_BASE_URL), then the public default. The shared
// fallback addresses both roles from one origin and appends /v1 to the
// inference base when needed.
func ResolveEndpoints() (Endpoints, error) {
	shared := normalizeBase(os.Getenv("ORCA_BASE_URL"))
	auth := normalizeBase(os.Getenv("ORCA_AUTH_BASE_URL"))
	api := normalizeBase(os.Getenv("ORCA_API_BASE_URL"))

	if auth == "" {
		if shared != "" {
			auth = shared
		} else {
			auth = DefaultAuthBase
		}
	}
	if api == "" {
		if shared != "" {
			api = shared
			if !strings.HasSuffix(api, "/v1") {
				api += "/v1"
			}
		} else {
			api = DefaultAPIBase
		}
	}
	if err := validateOrigin("auth", auth); err != nil {
		return Endpoints{}, err
	}
	if err := validateOrigin("api", api); err != nil {
		return Endpoints{}, err
	}
	return Endpoints{AuthBase: auth, APIBase: api}, nil
}

func normalizeBase(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// validateOrigin requires HTTPS for remote hosts and permits plain HTTP only
// for loopback development endpoints.
func validateOrigin(label, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("orcarouter: invalid %s origin", label)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("orcarouter: %s origin must use https for non-loopback hosts", label)
	default:
		return fmt.Errorf("orcarouter: %s origin must use https", label)
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
