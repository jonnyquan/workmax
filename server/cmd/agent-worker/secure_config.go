package main

import (
	"errors"
	"io"
	"os"
)

const maxWorkerConfigBytes int64 = 1 << 20

var errWorkerConfigFileUnsafe = errors.New("agent-worker configuration file is unsafe")

// readSecureWorkerConfig is the production reader. The generic snapshot
// loader still accepts an injected reader for hermetic tests, while the real
// command refuses symlinks, non-regular files, group/world access and
// unexpectedly large inputs before retaining any configuration bytes.
func readSecureWorkerConfig(path string) ([]byte, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return nil, errWorkerConfigFileUnsafe
	}
	if linkInfo.Mode().Perm()&0o077 != 0 || !workerConfigOwnedByProcess(linkInfo) ||
		linkInfo.Size() < 0 || linkInfo.Size() > maxWorkerConfigBytes {
		return nil, errWorkerConfigFileUnsafe
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, errWorkerConfigFileUnsafe
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(linkInfo, openedInfo) ||
		openedInfo.Mode().Perm()&0o077 != 0 || !workerConfigOwnedByProcess(openedInfo) ||
		openedInfo.Size() < 0 || openedInfo.Size() > maxWorkerConfigBytes {
		return nil, errWorkerConfigFileUnsafe
	}

	reader := io.LimitReader(file, maxWorkerConfigBytes+1)
	raw, err := io.ReadAll(reader)
	if err != nil || int64(len(raw)) > maxWorkerConfigBytes {
		clear(raw)
		return nil, errWorkerConfigFileUnsafe
	}
	finalInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, finalInfo) ||
		finalInfo.Mode() != openedInfo.Mode() || !workerConfigOwnedByProcess(finalInfo) ||
		finalInfo.Size() != openedInfo.Size() || finalInfo.Size() != int64(len(raw)) ||
		!finalInfo.ModTime().Equal(openedInfo.ModTime()) {
		clear(raw)
		return nil, errWorkerConfigFileUnsafe
	}
	closeErr := file.Close()
	file = nil
	if closeErr != nil {
		clear(raw)
		return nil, errWorkerConfigFileUnsafe
	}
	return raw, nil
}
