package utils

import (
	"crypto/sha256"
	"fmt"
	"github.com/google/uuid"
)

// GenerateUUID 生成一个新的UUID字符串
// 用于各种需要唯一标识符的场景，如API响应、临时文件等
func GenerateUUID() string {
	return uuid.New().String()
}

// GenerateTextHash 生成文本的SHA256哈希值
// 用于文本去重和缓存标识
func GenerateTextHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", hash)
}
