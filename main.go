package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"opscore/internal/handlers"
	"opscore/internal/metrics"
	"strings"
)

func main() {
	metrics.Start() // 启动后台指标采集

	// 监听地址(自适应部署):
	//  - 默认 :8088,避免与 Prometheus(9090)、nginx(8080/8081) 冲突。
	//  - 有 nginx 反代时建议 -addr 127.0.0.1:8088,仅本机监听,由 nginx 对外暴露。
	//  - 优先级:-addr 参数 > OPCORE_ADDR 环境变量 > 默认 :8088。
	addr := ":8088"
	if env := os.Getenv("OPCORE_ADDR"); env != "" {
		addr = env
	}
	flagAddr := flag.String("addr", "", "监听地址,如 :8088 或 127.0.0.1:8088(默认 :8088,OPCORE_ADDR 可覆盖)")
	flagDist := flag.String("dist", "./web/dist", "前端静态资源目录(默认 ./web/dist,相对二进制路径)")
	flag.Parse()
	if *flagAddr != "" {
		addr = *flagAddr
	}

	mux := http.NewServeMux()
	// ── 核心模块 API ──
	mux.HandleFunc("/api/manifest", handlers.Manifest)
	mux.HandleFunc("/api/core/resources", handlers.Resources)
	mux.HandleFunc("/api/core/disk/children", handlers.DiskChildren)
	mux.HandleFunc("/api/core/services", handlers.ServicesList)
	mux.HandleFunc("/api/core/services/action", handlers.ServiceAction)
	mux.HandleFunc("/api/core/services/logs", handlers.ServiceLogsHandler)
	mux.HandleFunc("/api/core/network", handlers.Network)
	mux.HandleFunc("/api/core/firewall", handlers.FirewallStatusHandler)
	mux.HandleFunc("/api/core/firewall/rules", handlers.FirewallRules)
	mux.HandleFunc("/api/core/firewall/action", handlers.FirewallAction)
	mux.HandleFunc("/api/core/firewall/audit", handlers.FirewallAudit)

	// ── 前端静态资源(SPA) ──
	fileServer := http.FileServer(http.Dir(*flagDist))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		// 无扩展名的路径视为前端路由,回退 index.html
		if p != "" && !strings.Contains(p, ".") {
			indexPath := *flagDist + "/index.html"
			if b, err := os.ReadFile(indexPath); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(b)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})

	log.Println("OpsCore demo 已启动 -> http://" + addr)
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
