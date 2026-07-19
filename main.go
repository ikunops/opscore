package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"opscore/internal/handlers"
	"opscore/internal/metrics"
	"strings"
)

//go:embed all:web/dist
var dist embed.FS

func main() {
	metrics.Start() // 启动后台指标采集

	mux := http.NewServeMux()
	// ── 核心模块 API ──
	mux.HandleFunc("/api/manifest", handlers.Manifest)
	mux.HandleFunc("/api/core/resources", handlers.Resources)
	mux.HandleFunc("/api/core/disk/children", handlers.DiskChildren)
	mux.HandleFunc("/api/core/services", handlers.ServicesList)
	mux.HandleFunc("/api/core/services/action", handlers.ServiceAction)
	mux.HandleFunc("/api/core/network", handlers.Network)
	mux.HandleFunc("/api/core/firewall", handlers.FirewallStatusHandler)
	mux.HandleFunc("/api/core/firewall/rules", handlers.FirewallRules)
	mux.HandleFunc("/api/core/firewall/action", handlers.FirewallAction)
	mux.HandleFunc("/api/core/firewall/audit", handlers.FirewallAudit)

	// ── 前端静态资源(SPA) ──
	sub, err := fs.Sub(dist, "web/dist")
	if err != nil {
		log.Fatal("未找到 web/dist,请先在 web/ 执行 `npm install && npm run build`: ", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		// 无扩展名的路径视为前端路由,回退 index.html
		if p != "" && !strings.Contains(p, ".") {
			if b, err := fs.ReadFile(sub, "index.html"); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(b)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})

	addr := ":8080"
	log.Println("OpsCore demo 已启动 -> http://localhost:8080")
	log.Fatal(http.ListenAndServe(addr, cors(mux)))
}

// cors 允许跨域,便于 Vite 开发服务器(5173)直连后端(8080)。
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
