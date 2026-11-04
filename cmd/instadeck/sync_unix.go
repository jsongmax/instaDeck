//go:build !windows

package main

import (
	"errors"
	"os"
)

func syncKeyDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("key directory unavailable")
	}
	defer directory.Close()
	if directory.Sync() != nil {
		return errors.New("key directory sync failed")
	}
	return nil
}
