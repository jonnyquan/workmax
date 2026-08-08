package config

import (
	"fmt"
	"strings"
)

// Statics 静态资产配置（前端 images/videos + prompts 镜像）
//
// 与 generator.storage 解耦：generator 是 AI 生成产物（按任务上传），
// statics 是相对长期、批量同步的资产。两者可独立切换 backend。
type Statics struct {
	// CDNBaseURL 是浏览器最终访问静态资源时的根 URL（带 scheme，无尾斜杠）。
	// 留空 = 走本地原路径（dev 模式）。
	// 例如：https://vstatic.rllx.com
	CDNBaseURL string `mapstructure:"cdn_base_url" json:"cdnBaseUrl" yaml:"cdn_base_url"`

	// R2 是上传/同步脚本和服务端写入静态资源时使用的对象存储凭证。
	// 仅在执行 sync / mirror 类工具时才会用到，运行时 API 只依赖 CDNBaseURL。
	R2 R2Storage `mapstructure:"r2" json:"r2" yaml:"r2"`
}

func (s Statics) Validate() error {
	base := strings.TrimSpace(s.CDNBaseURL)
	if base == "" {
		return nil
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return fmt.Errorf("statics.cdn_base_url must start with http:// or https://")
	}
	return nil
}

// PromptCDNURL 把 DB 里存的 /uploads/prompts/... 相对路径拼成绝对 URL。
// 当 CDNBaseURL 为空时返回原路径（保持本地行为）。
func (s Statics) PromptCDNURL(path string) string {
	if path == "" {
		return ""
	}
	base := strings.TrimSpace(s.CDNBaseURL)
	if base == "" {
		return path
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/uploads/prompts/") {
		return path
	}
	return strings.TrimSuffix(base, "/") + path
}
