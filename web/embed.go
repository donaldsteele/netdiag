// Package web holds the embedded single-page UI assets.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html app.js styles.css vendor/*
var files embed.FS

// FS returns the static asset tree rooted at the web directory.
func FS() fs.FS { return files }
