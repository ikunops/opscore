package handlers

import (
	"net/http"

	"opscore/internal/metrics"
)

// Resources 返回实时系统指标快照(后端后台采集,这里只是读取)。
func Resources(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, metrics.Get())
}
