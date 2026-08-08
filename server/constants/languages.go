package constants

// SupportedLanguages 系统支持的语言列表
var SupportedLanguages = []map[string]interface{}{
	{
		"value": "en",
		"name":  "English",
		"flag":  "🇺🇸",
	},
	{
		"value": "zh",
		"name":  "中文",
		"flag":  "🇨🇳",
	},
	{
		"value": "zh-tw",
		"name":  "繁體中文",
		"flag":  "🇨🇳",
	},
	{
		"value": "ja",
		"name":  "日本語",
		"flag":  "🇯🇵",
	},
	{
		"value": "ko",
		"name":  "한국어",
		"flag":  "🇰🇷",
	},
	{
		"value": "fr",
		"name":  "Français",
		"flag":  "🇫🇷",
	},
	{
		"value": "de",
		"name":  "Deutsch",
		"flag":  "🇩🇪",
	},
	{
		"value": "es",
		"name":  "Español",
		"flag":  "🇪🇸",
	},
	{
		"value": "pt",
		"name":  "Português",
		"flag":  "🇵🇹",
	},
	{
		"value": "it",
		"name":  "Italiano",
		"flag":  "🇮🇹",
	},
	{
		"value": "ru",
		"name":  "Русский",
		"flag":  "🇷🇺",
	},
	{
		"value": "ar",
		"name":  "العربية",
		"flag":  "🇸🇦",
	},
	{
		"value": "hi",
		"name":  "हिन्दी",
		"flag":  "🇮🇳",
	},
	{
		"value": "th",
		"name":  "ไทย",
		"flag":  "🇹🇭",
	},
	{
		"value": "vi",
		"name":  "Tiếng Việt",
		"flag":  "🇻🇳",
	},
	{
		"value": "id",
		"name":  "Bahasa Indonesia",
		"flag":  "🇮🇩",
	},
	{
		"value": "ms",
		"name":  "Bahasa Melayu",
		"flag":  "🇲🇾",
	},
	{
		"value": "tr",
		"name":  "Türkçe",
		"flag":  "🇹🇷",
	},
	{
		"value": "pl",
		"name":  "Polski",
		"flag":  "🇵🇱",
	},
	{
		"value": "nl",
		"name":  "Nederlands",
		"flag":  "🇳🇱",
	},
}

// GetMultiLanguageList 获取多语言列表（排除英语）
func GetMultiLanguageList() []map[string]interface{} {
	var result []map[string]interface{}
	for _, lang := range SupportedLanguages {
		if lang["value"] != "en" {
			result = append(result, lang)
		}
	}
	return result
}

// GetLanguageByCode 根据语言代码获取语言信息
func GetLanguageByCode(code string) map[string]interface{} {
	for _, lang := range SupportedLanguages {
		if lang["value"] == code {
			return lang
		}
	}
	return nil
}

// IsLanguageSupported 检查语言是否被支持
func IsLanguageSupported(code string) bool {
	return GetLanguageByCode(code) != nil
}
