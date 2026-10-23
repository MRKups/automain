package web

import (
	"embed"
	"io/fs"
)

// Assets holds the compiled frontend files embedded into the binary.
//
//go:embed dist/*
var Assets embed.FS

// HasIndex verifies that dist/index.html is present and non-empty in the embedded filesystem.
func HasIndex() bool {
	info, err := fs.Stat(Assets, "dist/index.html")
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Size() > 0
}
