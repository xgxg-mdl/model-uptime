package httpserver

import (
	"embed"
	"io/fs"
)

//go:embed web
var embeddedWeb embed.FS

// webAssets 返回以 web 目录为根的只读静态资源文件系统。
func webAssets() fs.FS {
	assets, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		// go:embed 在编译期保证 web 目录存在；失败意味着构建产物已损坏。
		panic(err)
	}
	return assets
}
