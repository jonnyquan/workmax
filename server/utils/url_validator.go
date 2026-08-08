package utils

import (
	"net/url"
	"path"
	"strings"
)

// ValidateRedirectURL 验证重定向URL的安全性
// 只允许以下两种情况：
// 1. 相对路径（以单个/开头，不包含//或../）
// 2. 完整URL且与allowedHost完全匹配
func ValidateRedirectURL(redirectURL string, allowedHost string) (string, bool) {
	// 空字符串直接返回
	if redirectURL == "" {
		return "", false
	}

	// 去除首尾空白
	redirectURL = strings.TrimSpace(redirectURL)

	// 安全检查1：防止协议相对URL（//evil.com）
	if strings.HasPrefix(redirectURL, "//") {
		return "", false
	}

	// 安全检查2：防止其他协议（javascript:, data:, etc）
	if strings.Contains(redirectURL, ":") {
		// 如果包含冒号，必须是 http:// 或 https://
		if !strings.HasPrefix(redirectURL, "http://") && !strings.HasPrefix(redirectURL, "https://") {
			return "", false
		}
	}

	// 情况1：相对路径
	if strings.HasPrefix(redirectURL, "/") && !strings.HasPrefix(redirectURL, "//") {
		// 首先检查是否包含路径遍历符号（在clean之前检查）
		if strings.Contains(redirectURL, "..") {
			return "", false
		}

		// 禁止连续的斜杠
		if strings.Contains(redirectURL, "//") {
			return "", false
		}

		// 解析URL以分离路径和query
		parsedURL, err := url.Parse(redirectURL)
		if err != nil {
			return "", false
		}

		// 清理路径部分
		cleanPath := path.Clean(parsedURL.Path)

		// 确保清理后仍然是以/开头
		if !strings.HasPrefix(cleanPath, "/") {
			return "", false
		}

		// 重新拼接完整URL（包含query和fragment）
		result := cleanPath
		if parsedURL.RawQuery != "" {
			result += "?" + parsedURL.RawQuery
		}
		if parsedURL.Fragment != "" {
			result += "#" + parsedURL.Fragment
		}

		return result, true
	}

	// 情况2：完整URL
	parsedURL, err := url.Parse(redirectURL)
	if err != nil {
		return "", false
	}

	// 必须是 http 或 https
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", false
	}

	// Host 必须完全匹配（包括子域名检查）
	redirectHost := strings.ToLower(parsedURL.Host)
	allowedHostLower := strings.ToLower(allowedHost)

	// 完全匹配
	if redirectHost == allowedHostLower {
		return redirectURL, true
	}

	// 允许匹配 www. 子域名
	if redirectHost == "www."+allowedHostLower || "www."+redirectHost == allowedHostLower {
		return redirectURL, true
	}

	// 其他情况都不允许
	return "", false
}

// ExtractDomainFromURL 从完整URL中提取域名
// 例如: https://www.workmax.app/path -> workmax.app
func ExtractDomainFromURL(fullURL string) string {
	if !strings.HasPrefix(fullURL, "http://") && !strings.HasPrefix(fullURL, "https://") {
		return ""
	}

	parsedURL, err := url.Parse(fullURL)
	if err != nil {
		return ""
	}

	host := parsedURL.Host
	// 移除 www. 前缀
	host = strings.TrimPrefix(host, "www.")

	return host
}

// IsSafeRelativePath 检查是否是安全的相对路径
func IsSafeRelativePath(urlPath string) bool {
	if urlPath == "" {
		return false
	}

	// 必须以单个/开头
	if !strings.HasPrefix(urlPath, "/") || strings.HasPrefix(urlPath, "//") {
		return false
	}

	// 禁止路径遍历符号（在clean之前检查）
	if strings.Contains(urlPath, "..") {
		return false
	}

	// 清理路径
	cleanPath := path.Clean(urlPath)

	// 确保清理后仍然是以/开头
	if !strings.HasPrefix(cleanPath, "/") {
		return false
	}

	return true
}
