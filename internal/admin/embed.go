package admin

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web
var webFS embed.FS

// handleIndex 服务嵌入式前端：/ → index.html，/static/* → 静态资源。
// 同源部署，无 CORS；session/CSRF 均不进入前端存储（仅内存）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		serveEmbedded(w, r, "web/index.html")
	case r.URL.Path == "/static/" || len(r.URL.Path) > 8 && r.URL.Path[:8] == "/static/":
		serveEmbedded(w, r, "web"+r.URL.Path)
	default:
		http.NotFound(w, r)
	}
}

// serveEmbedded 从 embed FS 读取并返回文件。
func serveEmbedded(w http.ResponseWriter, r *http.Request, name string) {
	data, err := fs.ReadFile(webFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := "text/plain; charset=utf-8"
	switch {
	case len(name) > 5 && name[len(name)-5:] == ".html":
		ct = "text/html; charset=utf-8"
	case len(name) > 4 && name[len(name)-4:] == ".css":
		ct = "text/css; charset=utf-8"
	case len(name) > 3 && name[len(name)-3:] == ".js":
		ct = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	// 前端资源嵌入二进制、随版本变化，一律 no-cache 避免浏览器缓存旧版
	// （页面 no-store：登录态变化不缓存旧页面）
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
