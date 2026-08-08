package utils

import (
	"crypto/md5"
	"encoding/hex"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/exp/rand"
)

// BcryptHash 使用 bcrypt 对密码进行加密
func BcryptHash(password string) string {
	bytes, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes)
}

// BcryptCheck 对比明文密码和数据库的哈希值
func BcryptCheck(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func Md5Check(password, hash string) bool {
	if CalculateMD5Hash(password) == hash {
		return true
	}
	return false
}

// CalculateMD5Hash 计算字符串的 MD5 哈希值 用于DNF密码加密
func CalculateMD5Hash(input string) string {
	// 将字符串转换为字节数组并计算 MD5 哈希值
	hash := md5.Sum([]byte(input))

	// 将哈希值转换为十六进制字符串表示
	return hex.EncodeToString(hash[:])
}

// VerifyDNFPassword 验证DNF密码
func VerifyDNFPassword(password, hash string) bool {
	// 计算密码的 MD5 哈希值
	passwordHash := CalculateMD5Hash(password)

	// 对比哈希值
	return passwordHash == hash
}

// GenerateRandomPassword 生成随机密码
func GenerateRandomPassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	seededRand := rand.New(rand.NewSource(uint64(time.Now().UnixNano())))

	b := make([]byte, length)
	for i := range b {
		b[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(b)
}
