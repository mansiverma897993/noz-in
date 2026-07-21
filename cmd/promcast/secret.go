package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const maxSecretFileBytes int64 = 64 << 10

// readBoundedSecretFile only accepts regular files and independently bounds
// the read. Checking both properties prevents devices such as /dev/zero and a
// file that grows after Stat from causing unbounded allocation.
func readBoundedSecretFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("secret source must be a regular file")
	}
	if info.Size() > maxSecretFileBytes {
		return "", fmt.Errorf("secret file exceeds the %d-byte limit", maxSecretFileBytes)
	}
	data, err := readBoundedSecret(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readBoundedSecret(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxSecretFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSecretFileBytes {
		return nil, fmt.Errorf("secret file exceeds the %d-byte limit", maxSecretFileBytes)
	}
	return data, nil
}
