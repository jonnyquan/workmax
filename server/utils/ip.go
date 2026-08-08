package utils

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

func GetClientIP(r *http.Request) string {
	// Prefer headers set by proxies/load balancers and fall back to RemoteAddr.
	if ip := selectPreferredIP(strings.Split(r.Header.Get("X-Forwarded-For"), ",")); ip != "" {
		return ip
	}

	headerCandidates := []string{
		r.Header.Get("X-Real-IP"),
		r.Header.Get("CF-Connecting-IP"),
		r.Header.Get("True-Client-IP"),
	}
	if ip := selectPreferredIP(headerCandidates); ip != "" {
		return ip
	}

	if ip := selectPreferredIP([]string{r.RemoteAddr}); ip != "" {
		return ip
	}

	return r.RemoteAddr
}

func selectPreferredIP(candidates []string) string {
	var firstValid string
	for _, candidate := range candidates {
		normalized, isIPv4 := normalizeIP(candidate)
		if normalized == "" {
			continue
		}
		if firstValid == "" {
			firstValid = normalized
		}
		if isIPv4 {
			return normalized
		}
	}
	return firstValid
}

func normalizeIP(candidate string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", false
	}

	if host, _, err := net.SplitHostPort(candidate); err == nil {
		candidate = host
	}

	ip := net.ParseIP(candidate)
	if ip == nil {
		return "", false
	}

	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String(), true
	}

	return ip.String(), false
}

func GetDeviceInfo(r *http.Request) string {
	return r.Header.Get("User-Agent")
}

func isValidIP(ip string) bool {
	parsedIP := net.ParseIP(ip)
	return parsedIP != nil
}

// 检查是否是IPv6地址
func isIPv6(ip string) bool {
	parsedIP := net.ParseIP(ip)
	return parsedIP != nil && parsedIP.To4() == nil
}

// 如果ip2region库不完全支持IPv6，可以尝试获取其对应的IPv4地址
func getIPv4MappingForIPv6(ipv6 string) (string, error) {
	// 尝试将IPv6转为IPv4映射（如果可能）
	parsedIP := net.ParseIP(ipv6)
	if parsedIP == nil {
		return "", fmt.Errorf("invalid IP address: %s", ipv6)
	}

	// 检查是否是IPv4映射的IPv6地址
	if ipv4 := parsedIP.To4(); ipv4 != nil {
		return ipv4.String(), nil
	}

	// 如果无法映射，返回空和错误
	return "", fmt.Errorf("IPv6 address cannot be mapped to IPv4: %s", ipv6)
}

func GetClientAddress(ip string) string {
	// 处理localhost地址
	if ip == "::1" || ip == "127.0.0.1" || ip == "localhost" {
		return "本地连接"
	}

	// 如果是IPv6地址，尝试获取其IPv4映射（如果可能）
	originalIP := ip
	var conversionError error
	if isIPv6(ip) {
		ipv4Mapped, err := getIPv4MappingForIPv6(ip)
		if err == nil {
			// 如果成功获取IPv4映射，使用该映射进行查询
			ip = ipv4Mapped
		} else {
			// 记录转换错误，但仍尝试使用原始IPv6地址查询
			conversionError = err
		}
	}

	if !isValidIP(ip) {
		return ""
	}

	// Resolve ip2region database path with environment override and fallbacks
	var xdbBytes []byte
	var readErr error

	// 1) Environment override
	if envPath, ok := os.LookupEnv("IP2REGION_XDB_PATH"); ok && envPath != "" {
		if b, err := ioutil.ReadFile(envPath); err == nil {
			xdbBytes = b
		} else {
			// Log but continue to other fallbacks
			fmt.Printf("failed to read IP2REGION_XDB_PATH (%s): %s\n", envPath, err.Error())
		}
	}

	// 2) Relative to executable directory (for deployed binaries)
	if xdbBytes == nil {
		if exePath, err := os.Executable(); err == nil {
			exeDir := filepath.Dir(exePath)
			exeCandidate := filepath.Join(exeDir, filepath.FromSlash("resource/ip2region/ip2region.xdb"))
			if _, statErr := os.Stat(exeCandidate); statErr == nil {
				if b, err := ioutil.ReadFile(exeCandidate); err == nil {
					xdbBytes = b
				} else {
					readErr = err
				}
			}
		}
	}

	// 3) Default relative to current working directory
	if xdbBytes == nil {
		candidates := []string{
			filepath.FromSlash("resource/ip2region/ip2region.xdb"),
			filepath.FromSlash("server/resource/ip2region/ip2region.xdb"),
		}
		for _, p := range candidates {
			if _, statErr := os.Stat(p); statErr == nil {
				if b, err := ioutil.ReadFile(p); err == nil {
					xdbBytes = b
					break
				} else {
					readErr = err
				}
			}
		}
	}

	if xdbBytes == nil {
		if readErr != nil {
			fmt.Printf("failed to read ip2region.xdb: %s\n", readErr.Error())
		}
		fmt.Printf("ip2region.xdb not found. Set IP2REGION_XDB_PATH or place file under 'resource/ip2region/' next to the binary, or under CWD 'resource/ip2region/' or 'server/resource/ip2region/'.\n")
		return ""
	}

	searcher, err := xdb.NewWithBuffer(xdbBytes)
	if err != nil {
		fmt.Printf("failed to create searcher: %s\n", err.Error())
		return ""
	}

	defer searcher.Close()

	// 使用处理后的IP地址进行查询
	region, err := searcher.SearchByStr(ip)
	if err != nil {
		// 如果使用处理后的IP地址查询失败，并且是IPv6地址，则记录详细错误
		if isIPv6(originalIP) {
			if conversionError != nil {
				fmt.Printf("failed to map IPv6 to IPv4: %s, then failed to SearchIP(%s): %s\n",
					conversionError, ip, err)
			} else {
				fmt.Printf("failed to SearchIP(IPv6: %s as %s): %s\n", originalIP, ip, err)
			}
		} else {
			fmt.Printf("failed to SearchIP(%s): %s\n", ip, err)
		}
		return ""
	}

	return region
}
