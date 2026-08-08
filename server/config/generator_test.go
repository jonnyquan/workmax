package config

import "testing"

func TestGeneratorValidate(t *testing.T) {
	t.Run("accepts local storage", func(t *testing.T) {
		cfg := Generator{
			Storage: StorageConfig{
				Type: "local",
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected local storage config to be valid, got %v", err)
		}
	})

	t.Run("rejects unsupported storage type", func(t *testing.T) {
		cfg := Generator{
			Storage: StorageConfig{
				Type: "unknown",
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected unsupported storage type to fail validation")
		}
	})

	t.Run("rejects incomplete r2 config", func(t *testing.T) {
		cfg := Generator{
			Storage: StorageConfig{
				Type: "r2",
				R2: R2Storage{
					Bucket: "workmax-generations",
				},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected incomplete r2 config to fail validation")
		}
	})

	t.Run("rejects invalid presign ttl", func(t *testing.T) {
		cfg := Generator{
			Storage: StorageConfig{
				Type: "r2",
				R2: R2Storage{
					Endpoint:        "https://example.r2.cloudflarestorage.com",
					Bucket:          "workmax-generations",
					AccessKeyID:     "key",
					SecretAccessKey: "secret",
					UsePresignedURL: true,
				},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected invalid presign ttl to fail validation")
		}
	})

	t.Run("accepts complete r2 config", func(t *testing.T) {
		cfg := Generator{
			Storage: StorageConfig{
				Type: "r2",
				R2: R2Storage{
					Endpoint:             "https://example.r2.cloudflarestorage.com",
					Bucket:               "workmax-generations",
					AccessKeyID:          "key",
					SecretAccessKey:      "secret",
					MultipartThresholdMB: 64,
					MultipartPartSizeMB:  16,
				},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected complete r2 config to be valid, got %v", err)
		}
	})
}
