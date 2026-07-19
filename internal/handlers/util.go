package handlers

import (
	"encoding/json"
	"net/http"
)

// WriteJSON 统一 JSON 响应。
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
