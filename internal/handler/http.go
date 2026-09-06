package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"jp-address-cleaner/internal/address"
)

// Handler HTTP处理器
type Handler struct {
	Cleaner *address.AddressCleaner
}

func NewHandler(c *address.AddressCleaner) *Handler {
	return &Handler{Cleaner: c}
}

// CleanRequest POST /clean/address 请求体
type CleanRequest struct {
	Address        string `json:"address"`        // 原始地址
	Mode           string `json:"mode"`           // strict / relaxed，默认strict
	EnableAIAssist bool   `json:"enableAIAssist"` // 中/低置信度时是否启用AI复核
}

// CleanAddress 地址清洗接口
func (h *Handler) CleanAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CleanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
		return
	}
	if strings.TrimSpace(req.Address) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "address is required"})
		return
	}
	res, err := h.Cleaner.Clean(r.Context(), req.Address, address.CleanOptions{
		Mode:           req.Mode,
		EnableAIAssist: req.EnableAIAssist,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
