package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDefaultConfigUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeDefaultConfig(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("配置文件权限 = %o，期望 600", permission)
	}
}
