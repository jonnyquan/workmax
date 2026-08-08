package utils

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
)

// Md5Encrypt returns the 32-char hex MD5 hash of a string.
// Used by image.go for generating hash-based filenames.
func Md5Encrypt(raw string) string {
	hash := md5.Sum([]byte(raw))
	return hex.EncodeToString(hash[:])
}

// Base64EncodeBytes encodes raw bytes to a standard base64 string.
// Used by image.go for image-to-base64 conversion.
func Base64EncodeBytes(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

// Base64Decode decodes a standard base64 string to raw bytes.
// Used by image.go for data URI image decoding.
func Base64Decode(raw string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(raw)
}
