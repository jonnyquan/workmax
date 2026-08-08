package utils

import (
	"regexp"
)

// WordCount counts words in text using regex (most accurate method)
func WordCount(text string) int {
	if text == "" {
		return 0
	}

	// Use regex to count words - handles punctuation, multiple spaces, etc.
	re := regexp.MustCompile(`\b\w+\b`)
	matches := re.FindAllString(text, -1)
	return len(matches)
}

// CharCount calculates non-whitespace character count
func CharCount(text string) int {
	count := 0
	for _, char := range text {
		if char != ' ' && char != '\t' && char != '\n' && char != '\r' {
			count++
		}
	}
	return count
}

// SentenceCount counts sentences by looking for sentence-ending punctuation
func SentenceCount(text string) int {
	if text == "" {
		return 0
	}

	// Count sentences by looking for ending punctuation followed by space or end of string
	re := regexp.MustCompile(`[.!?]+(\s|$)`)
	matches := re.FindAllString(text, -1)
	count := len(matches)

	// If no sentence endings found but text exists, assume one sentence
	if count == 0 && len(text) > 0 {
		return 1
	}

	return count
}
