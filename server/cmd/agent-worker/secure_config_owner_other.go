//go:build !darwin && !linux

package main

import "os"

// Ownership semantics must be implemented and reviewed per target platform
// before the production Worker can consume a secret-bearing config there.
func workerConfigOwnedByProcess(os.FileInfo) bool { return false }
