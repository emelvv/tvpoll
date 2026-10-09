// Package web contains the browser application, served without a build step or CDN.
package web

import (
	"embed"
	"io/fs"
)

//go:embed *.html *.css *.js
var assets embed.FS

func Files() fs.FS { return assets }
