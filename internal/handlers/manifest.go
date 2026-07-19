package handlers

import (
	"net/http"

	"opscore/internal/module"
)

// Manifest 返回模块清单,Host Shell 据此动态生成侧栏与路由。
func Manifest(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, module.CoreModules())
}
