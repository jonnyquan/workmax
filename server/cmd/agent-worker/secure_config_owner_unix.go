//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

func workerConfigOwnedByProcess(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Uid) == uint64(os.Geteuid())
}
