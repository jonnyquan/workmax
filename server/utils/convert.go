package utils

import "strconv"

// ParseUint parses a string to uint with a default value of 0
func ParseUint(s string) uint {
	if val, err := strconv.ParseUint(s, 10, 32); err == nil {
		return uint(val)
	}
	return 0
}

// StringToUint parses a string to uint and returns both value and error
func StringToUint(s string) (uint, error) {
	val, err := strconv.ParseUint(s, 10, 32)
	return uint(val), err
}
