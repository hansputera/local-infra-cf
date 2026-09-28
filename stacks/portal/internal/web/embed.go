package web

import (
	"embed"
	"io/fs"
)

//go:embed templates/* static/*
var FS embed.FS

func StaticFS() fs.FS {
	sub, err := fs.Sub(FS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
