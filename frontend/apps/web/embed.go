package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distEmbed embed.FS

func Dist() fs.FS {
	sub, err := fs.Sub(distEmbed, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
