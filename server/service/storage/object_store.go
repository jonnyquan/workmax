package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"server/config"
)

func urlQueryPathEscape(value string) string {
	return url.PathEscape(value)
}

type PutObjectRequest struct {
	Key          string
	Body         io.Reader
	Size         int64
	ContentType  string
	CacheControl string
	Metadata     map[string]string
}

type PutObjectResult struct {
	Provider  string
	Bucket    string
	Key       string
	ETag      string
	PublicURL string
}

type ObjectStore interface {
	Put(ctx context.Context, req *PutObjectRequest) (*PutObjectResult, error)
	Get(ctx context.Context, key string) (io.ReadCloser, string, error)
	Delete(ctx context.Context, key string) error
	PublicURL(key string) string
	DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error)
	DownloadURLWithDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error)
	Provider() string
	Bucket() string
}

func BuildAttachmentContentDisposition(filename string) string {
	trimmed := strings.TrimSpace(filename)
	if trimmed == "" {
		return "attachment"
	}
	safe := strings.Map(func(r rune) rune {
		switch r {
		case '"', '\\', '\r', '\n':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, trimmed)
	encoded := urlQueryPathEscape(trimmed)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, safe, encoded)
}

type ObjectStoreConfig struct {
	Provider                string
	Endpoint                string
	Region                  string
	Bucket                  string
	AccessKeyID             string
	SecretAccessKey         string
	CDNUrl                  string
	PublicURL               string
	PathPrefix              string
	ForcePathStyle          bool
	UsePresignedURL         bool
	PresignTTLSeconds       int
	MultipartThresholdBytes int64
	MultipartPartSizeBytes  int64
}

func NewObjectStoreFromGeneratorConfig(storageCfg config.StorageConfig) (ObjectStore, bool, error) {
	cfg, ok := objectStoreConfigFromStorage(storageCfg)
	if !ok {
		return nil, false, nil
	}

	return NewObjectStoreFromConfig(cfg)
}

func NewObjectStoreConfigFromStorage(storageCfg config.StorageConfig) (ObjectStoreConfig, bool) {
	return objectStoreConfigFromStorage(storageCfg)
}

func NewObjectStoreForProviderBucket(storageCfg config.StorageConfig, provider, bucket string) (ObjectStore, bool, error) {
	cfg, ok := objectStoreConfigFromStorage(storageCfg)
	if !ok {
		return nil, false, nil
	}

	provider = strings.TrimSpace(provider)
	if provider != "" && provider != cfg.Provider {
		return nil, false, nil
	}
	if trimmedBucket := strings.TrimSpace(bucket); trimmedBucket != "" {
		cfg.Bucket = trimmedBucket
	}

	return NewObjectStoreFromConfig(cfg)
}

func NewObjectStoreFromConfig(cfg ObjectStoreConfig) (ObjectStore, bool, error) {
	store, err := NewR2CompatibleStore(cfg)
	if err != nil {
		return nil, true, err
	}
	return store, true, nil
}

func objectStoreConfigFromStorage(storageCfg config.StorageConfig) (ObjectStoreConfig, bool) {
	switch strings.TrimSpace(storageCfg.Type) {
	case "r2":
		r2Cfg := storageCfg.R2
		return ObjectStoreConfig{
			Provider:                "r2",
			Endpoint:                r2Cfg.Endpoint,
			Region:                  r2Cfg.Region,
			Bucket:                  r2Cfg.Bucket,
			AccessKeyID:             r2Cfg.AccessKeyID,
			SecretAccessKey:         r2Cfg.SecretAccessKey,
			CDNUrl:                  r2Cfg.CDNUrl,
			PublicURL:               r2Cfg.PublicURL,
			PathPrefix:              r2Cfg.PathPrefix,
			ForcePathStyle:          r2Cfg.ForcePathStyle,
			UsePresignedURL:         r2Cfg.UsePresignedURL,
			PresignTTLSeconds:       r2Cfg.PresignTTLSeconds,
			MultipartThresholdBytes: int64(r2Cfg.MultipartThresholdMB) * 1024 * 1024,
			MultipartPartSizeBytes:  int64(r2Cfg.MultipartPartSizeMB) * 1024 * 1024,
		}, true
	default:
		return ObjectStoreConfig{}, false
	}
}

func NormalizeMultipartThresholdBytes(value int64) int64 {
	if value >= 5*1024*1024 {
		return value
	}
	return 64 * 1024 * 1024
}

func NormalizeMultipartPartSizeBytes(value int64) int64 {
	if value >= 5*1024*1024 {
		return value
	}
	return 16 * 1024 * 1024
}

func BuildPublicObjectURL(cfg ObjectStoreConfig, key string) string {
	if cfg.CDNUrl != "" {
		return strings.TrimSuffix(cfg.CDNUrl, "/") + "/" + key
	}
	if cfg.PublicURL != "" {
		return strings.TrimSuffix(cfg.PublicURL, "/") + "/" + key
	}
	if cfg.Endpoint != "" {
		return strings.TrimSuffix(cfg.Endpoint, "/") + "/" + cfg.Bucket + "/" + key
	}
	return "/" + key
}
