package globals

import (
	"server/config"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/songzhibin97/gkit/cache/local_cache"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

var (
	GraLog                *zap.Logger
	GraCache              local_cache.Cache
	GraConf               config.Server
	GraVp                 *viper.Viper
	GraConcurrencyControl = &singleflight.Group{}
	GraDBs                map[string]*gorm.DB
	GraRedis              map[string]*redis.Client

	// Global service instances
)

// Helper functions
func Now() time.Time {
	return time.Now()
}

// Response helpers
func Fail(message string) map[string]interface{} {
	return map[string]interface{}{
		"success": false,
		"message": message,
	}
}

func Success(data interface{}) map[string]interface{} {
	return map[string]interface{}{
		"success": true,
		"data":    data,
	}
}
