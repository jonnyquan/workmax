package config

type Aihub struct {
	Text          TextLLMConfig          `mapstructure:"text" json:"text" yaml:"text"`
	ImageAnalysis ImageAnalysisLLMConfig `mapstructure:"image_analysis" json:"image_analysis" yaml:"image_analysis"`
	TTS           TTSConfig              `mapstructure:"tts" json:"tts" yaml:"tts"`
	BGM           BGMConfig              `mapstructure:"bgm" json:"bgm" yaml:"bgm"`
	ExportColor   ExportColorConfig      `mapstructure:"export_color" json:"export_color" yaml:"export_color"`
	LocalVector   LocalVectorConfig      `mapstructure:"local_vector" json:"local_vector" yaml:"local_vector"`
	RAG           RAGConfig              `mapstructure:"rag" json:"rag" yaml:"rag"`
}

// TTSConfig wires the text-to-speech provider used by the short-drama
// export pipeline. When Enabled=false (or the struct is absent from
// config), the runner skips audio synthesis entirely and exports
// ship as video-only. API key lives in config so a single missing
// value cleanly disables the feature across the whole service.
type TTSConfig struct {
	Enabled  bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Endpoint string `mapstructure:"endpoint" json:"endpoint" yaml:"endpoint"`
	ApiKey   string `mapstructure:"api_key" json:"api_key" yaml:"api_key"`
	// Model selects the provider's synthesis model (OpenAI offers
	// "tts-1" for speed / cost and "tts-1-hd" for quality). Defaults
	// to "tts-1" when empty.
	Model string `mapstructure:"model" json:"model" yaml:"model"`
}

// BGMConfig wires optional background-music mixing into the export
// runner. DefaultPath points at a server-local file the runner
// reads into the working directory before ffmpeg; URL downloads are
// deliberately out of scope for MVP (avoids an upload flow + SSRF
// surface for this first slice).
//
// When Enabled=false OR DefaultPath is empty OR the file doesn't
// exist at render time, the runner skips BGM entirely — exports
// ship with TTS audio only (or video-only if TTS is also disabled).
type BGMConfig struct {
	Enabled     bool    `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	DefaultPath string  `mapstructure:"default_path" json:"default_path" yaml:"default_path"`
	// DefaultGain is the linear volume multiplier applied to the
	// BGM track. 0.15 is a typical "ambient backdrop" level. Clamped
	// by formatGain to [0.0, 1.0] so misconfigured values can't
	// normalise BGM louder than the dialogue track.
	DefaultGain float64 `mapstructure:"default_gain" json:"default_gain" yaml:"default_gain"`
}

// ExportColorConfig sets the deployment-wide default color preset
// applied to every short-drama export. Empty DefaultPreset (or an
// unknown ID) means "no color filter" — same as if the config
// block were absent entirely. Per-project overrides can come in a
// later slice; this one is the deployment baseline.
type ExportColorConfig struct {
	DefaultPreset string `mapstructure:"default_preset" json:"default_preset" yaml:"default_preset"`
}

// TextLLMConfig 独立文本模型配置（用于通用文本/JSON输出场景）
type TextLLMConfig struct {
	Enabled  bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Endpoint string `mapstructure:"endpoint" json:"endpoint" yaml:"endpoint"`
	ApiKey   string `mapstructure:"api_key" json:"api_key" yaml:"api_key"`
	Model    string `mapstructure:"model" json:"model" yaml:"model"`
}

// ImageAnalysisLLMConfig 独立图片分析配置（用于视觉/多模态等专用场景）
type ImageAnalysisLLMConfig struct {
	Enabled  bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Endpoint string `mapstructure:"endpoint" json:"endpoint" yaml:"endpoint"`
	ApiKey   string `mapstructure:"api_key" json:"api_key" yaml:"api_key"`
	Model    string `mapstructure:"model" json:"model" yaml:"model"`
}

// LocalVectorConfig 本地向量服务配置
type LocalVectorConfig struct {
	Dimension       int    `mapstructure:"dimension" json:"dimension" yaml:"dimension"`
	MemoryLimitMB   int    `mapstructure:"memory_limit_mb" json:"memory_limit_mb" yaml:"memory_limit_mb"`
	CacheEnabled    bool   `mapstructure:"cache_enabled" json:"cache_enabled" yaml:"cache_enabled"`
	PersistencePath string `mapstructure:"persistence_path" json:"persistence_path" yaml:"persistence_path"`
}

// RAGConfig RAG向量化功能配置
type RAGConfig struct {
	Enabled             bool    `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	BatchSize           int     `mapstructure:"batch_size" json:"batch_size" yaml:"batch_size"`
	SimilarityThreshold float64 `mapstructure:"similarity_threshold" json:"similarity_threshold" yaml:"similarity_threshold"`
	MaxResults          int     `mapstructure:"max_results" json:"max_results" yaml:"max_results"`
	CacheTTL            int     `mapstructure:"cache_ttl" json:"cache_ttl" yaml:"cache_ttl"` // hours
}
