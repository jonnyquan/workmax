package account

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"server/globals"
	"server/model"
	"time"

	"go.uber.org/zap"
)

type ApiKeyService struct{}

// GetApiKey 获取用户的 API Key
func (s *ApiKeyService) GetApiKey(uid uint) (map[string]interface{}, error) {
	var user model.User
	err := globals.GraDBs["system"].Where("id = ?", uid).First(&user).Error
	if err != nil {
		globals.GraLog.Error("获取用户信息失败", zap.Error(err))
		return nil, err
	}

	// 如果用户没有 API Key，返回空
	if user.ApiKey == "" {
		return nil, nil
	}

	// 返回 API Key 信息，但隐藏完整的 key
	apiKeyInfo := map[string]interface{}{
		"id":        user.Id,
		"apiKey":    user.ApiKey,
		"createdAt": user.UpdatedAt.Format(time.RFC3339),
		"status":    "active",
	}

	return apiKeyInfo, nil
}

// CreateApiKey 创建或重置 API Key
func (s *ApiKeyService) CreateApiKey(uid uint) (string, error) {
	// 生成新的 API Key
	apiKey, err := s.generateApiKey(uid)
	if err != nil {
		globals.GraLog.Error("生成 API Key 失败", zap.Error(err))
		return "", err
	}

	// 更新用户的 API Key
	err = globals.GraDBs["system"].Model(&model.User{}).Where("id = ?", uid).Update("api_key", apiKey).Error
	if err != nil {
		globals.GraLog.Error("更新 API Key 失败", zap.Error(err))
		return "", err
	}

	return apiKey, nil
}

// RevokeApiKey 撤销 API Key
func (s *ApiKeyService) RevokeApiKey(uid uint) error {
	// 清空用户的 API Key
	err := globals.GraDBs["system"].Model(&model.User{}).Where("id = ?", uid).Update("api_key", "").Error
	if err != nil {
		globals.GraLog.Error("撤销 API Key 失败", zap.Error(err))
		return err
	}

	return nil
}

// VerifyApiKey 验证 API Key 有效性
func (s *ApiKeyService) VerifyApiKey(key string) (bool, uint) {
	// 查找具有此 API Key 的用户
	var user model.User
	err := globals.GraDBs["system"].Where("api_key = ?", key).First(&user).Error
	if err != nil {
		return false, 0
	}

	// 检查用户是否被禁用
	if user.Ban {
		return false, 0
	}

	return true, user.Id
}

// generateApiKey 生成 API Key
func (s *ApiKeyService) generateApiKey(uid uint) (string, error) {
	// 生成随机字节
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	// 将随机字节编码为 base64 字符串
	encoded := base64.StdEncoding.EncodeToString(b)

	// 添加前缀，例如 "exgpt_"
	apiKey := fmt.Sprintf("exgpt_%s", encoded)

	return apiKey, nil
}
