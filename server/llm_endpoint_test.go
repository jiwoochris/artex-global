package server

import (
	"testing"

	"github.com/Autumn-27/artex/agent"
)

// TestLLMEndpointResolvesHostAndEffectivePort pins the P4 contract: the gateway
// endpoint is identified by host AND effective port, and an endpoint without an
// explicit port resolves to the scheme default.
func TestLLMEndpointResolvesHostAndEffectivePort(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		wantHost string
		wantPort int
	}{
		{"explicit port", "http://127.0.0.1:8799/v1", "127.0.0.1", 8799},
		{"https explicit port", "https://gateway.example.com:8443/v1", "gateway.example.com", 8443},
		{"http no port uses 80", "http://gateway.example.com/v1", "gateway.example.com", 80},
		{"https no port uses 443", "https://gateway.example.com/v1", "gateway.example.com", 443},
		{"host is lowercased", "http://Gateway.Example.COM:8799/v1", "gateway.example.com", 8799},
		{"empty base url", "", "", 0},
		{"unparsable base url", "://not a url", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{llmCfg: agent.Config{BaseURL: tc.baseURL}}
			host, port := s.llmEndpoint()
			if host != tc.wantHost || port != tc.wantPort {
				t.Errorf("llmEndpoint() = (%q, %d), want (%q, %d)", host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

// TestIsLLMGatewayTargetComparesHostAndPort is the P4 regression. The old guard
// compared bare hostnames, so a gateway on 127.0.0.1:8799 made EVERY 127.0.0.1
// target unusable — including the authorized test target on 127.0.0.1:8801. The
// comparison must be host AND effective port.
func TestIsLLMGatewayTargetComparesHostAndPort(t *testing.T) {
	const (
		gwHost = "127.0.0.1"
		gwPort = 8799
	)
	cases := []struct {
		name      string
		gwHost    string
		gwPort    int
		host      string
		port      int
		wantBlock bool
	}{
		{"the gateway itself is blocked", gwHost, gwPort, "127.0.0.1", 8799, true},
		{"other port on same host is a legitimate target", gwHost, gwPort, "127.0.0.1", 8801, false},
		{"same port on other host is a legitimate target", gwHost, gwPort, "192.168.1.5", 8799, false},
		{"different port and host", gwHost, gwPort, "example.com", 443, false},
		{"no gateway configured blocks nothing", "", 0, "127.0.0.1", 8799, false},
		{"gateway on https default port", "gateway.example.com", 443, "gateway.example.com", 443, true},
		{"gateway on https default port, other port", "gateway.example.com", 443, "gateway.example.com", 8443, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isLLMGatewayTarget(tc.gwHost, tc.gwPort, tc.host, tc.port)
			if got != tc.wantBlock {
				t.Errorf("isLLMGatewayTarget(%q, %d, %q, %d) = %v, want %v",
					tc.gwHost, tc.gwPort, tc.host, tc.port, got, tc.wantBlock)
			}
		})
	}
}

// TestIsLLMGatewayTargetMatchesTheRealLocalPair is the concrete case from the
// acceptance run: the fake LLM gateway listens on 127.0.0.1:8799 and the
// authorized test target on 127.0.0.1:8801. Only the gateway may be refused.
func TestIsLLMGatewayTargetMatchesTheRealLocalPair(t *testing.T) {
	if !isLLMGatewayTarget("127.0.0.1", 8799, "127.0.0.1", 8799) {
		t.Error("the gateway endpoint 127.0.0.1:8799 must be refused")
	}
	if isLLMGatewayTarget("127.0.0.1", 8799, "127.0.0.1", 8801) {
		t.Error("the authorized target 127.0.0.1:8801 must NOT be refused (P4 regression)")
	}
}
