package mcp_connector

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateRemoteConnectorURL applies the hosted-platform boundary for
// user-managed remote MCP servers. The SDK will connect to this URL from
// backend infrastructure, so local/private/link-local targets are not
// valid user input here.
func ValidateRemoteConnectorURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return newValidationError("connector url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return newValidationError("connector url is invalid")
	}
	if parsed.Scheme != "https" {
		return newValidationError("connector url must use https")
	}
	if parsed.User != nil {
		return newValidationError("connector url must not include credentials")
	}
	host := parsed.Hostname()
	if host == "" {
		return newValidationError("connector url must include a host")
	}
	if isBlockedConnectorHost(host) {
		return newValidationError("connector url host is not allowed")
	}
	return nil
}

func isBlockedConnectorHost(host string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if normalized == "" {
		return true
	}
	if normalized == "localhost" || strings.HasSuffix(normalized, ".localhost") || strings.HasSuffix(normalized, ".local") {
		return true
	}
	if ip := net.ParseIP(normalized); ip != nil {
		return !isPublicConnectorIP(ip)
	}
	return false
}

func isPublicConnectorIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsGlobalUnicast() &&
		!ip.IsPrivate() &&
		!ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() &&
		!ip.IsUnspecified()
}

// ValidateUserManagedConnectorRuntimePolicy mirrors write-time validation
// before the agent SDK sees persisted rows. This blocks legacy/seeded unsafe
// rows from being re-enabled by accident.
func ValidateUserManagedConnectorRuntimePolicy(transport, rawURL string) error {
	switch transport {
	case "stdio":
		return fmt.Errorf("stdio transport is disabled for user-managed connectors")
	case "sse", "http":
		if err := ValidateRemoteConnectorURL(rawURL); err != nil {
			return err
		}
	}
	return nil
}
