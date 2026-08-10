//go:build desktop

package cloud_proxy

import "regexp"

var (
	proxyURLCredentialsRE = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^@\s/]+@`)
	proxyBearerRE         = regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]+`)
	proxyBasicRE          = regexp.MustCompile(`(?i)\bBasic\s+[A-Za-z0-9._~+/=-]+`)
	proxyAuthHeaderRE     = regexp.MustCompile(`(?i)\b(Authorization:\s*(?:Bearer|Basic)\s+)[^\s"']+`)
	proxyTokenPairRE      = regexp.MustCompile(`(?i)\b((?:access_token|refresh_token|id_token|api[_-]?key|apikey|client_secret|password|secret|token)=)[^&\s"']+`)
	proxyJSONTokenFieldRE = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|api[_-]?key|apikey|client_secret|password|secret|token)"\s*:\s*")[^"]*(")`)
	proxyLocalTokenRE     = regexp.MustCompile(`(?i)\b(X-Local-Token[=:]\s*)[^\s"']+`)
	proxySensitiveKeyRE   = regexp.MustCompile(`(?i)^(authorization|x-local-token|workmax_local_token|access_token|refresh_token|id_token|token|api[_-]?key|apikey|client_secret|password|secret)$`)
)

func redactProxyErrorString(value string) string {
	value = proxyURLCredentialsRE.ReplaceAllString(value, "$1[REDACTED]@")
	value = proxyAuthHeaderRE.ReplaceAllString(value, "$1[REDACTED]")
	value = proxyBearerRE.ReplaceAllString(value, "$1[REDACTED]")
	value = proxyBasicRE.ReplaceAllString(value, "Basic [REDACTED]")
	value = proxyTokenPairRE.ReplaceAllString(value, "$1[REDACTED]")
	value = proxyJSONTokenFieldRE.ReplaceAllString(value, "$1[REDACTED]$2")
	value = proxyLocalTokenRE.ReplaceAllString(value, "$1[REDACTED]")
	return value
}

// SanitizeProxyError is the exported form of the redaction every classified
// error already passes through. Callers that ENRICH a ProxyError after
// classification (adding a detail of their own) must send it back through
// here: the classifier's guarantee is about what it produced, not about what
// somebody appended afterwards.
func SanitizeProxyError(pe ProxyError) ProxyError { return sanitizeProxyError(pe) }

func sanitizeProxyError(pe ProxyError) ProxyError {
	pe.Message = redactProxyErrorString(pe.Message)
	pe.LogID = redactProxyErrorString(pe.LogID)
	if pe.Details != nil {
		pe.Details = sanitizeProxyErrorMap(pe.Details)
	}
	return pe
}

func sanitizeProxyErrorMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		redactedKey := redactProxyErrorString(key)
		if proxySensitiveKeyRE.MatchString(key) {
			out[redactedKey] = "[REDACTED]"
			continue
		}
		out[redactedKey] = sanitizeProxyErrorValue(value)
	}
	return out
}

func sanitizeProxyErrorValue(value any) any {
	switch v := value.(type) {
	case string:
		return redactProxyErrorString(v)
	case map[string]any:
		return sanitizeProxyErrorMap(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeProxyErrorValue(item)
		}
		return out
	default:
		return value
	}
}
