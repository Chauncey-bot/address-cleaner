package main

import (
	"log"
	"net/http"
	"os"

	"jp-address-cleaner/internal/address"
	"jp-address-cleaner/internal/handler"
)

func main() {
	cleaner := address.NewCleaner()
	// AI辅助复核为可选能力：配置了 AI_API_KEY 才启用
	if key := os.Getenv("AI_API_KEY"); key != "" {
		cleaner.AIAssist = address.NewAIAssistClient(
			getenv("AI_BASE_URL", "https://ai.zhisales.com/v1"),
			key,
			getenv("AI_MODEL", "gpt-4o-mini"),
			3000,
		)
	}

	h := handler.NewHandler(cleaner)
	mux := http.NewServeMux()
	mux.HandleFunc("/clean/address", h.CleanAddress)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := getenv("ADDR", ":8080")
	log.Printf("jp-address-cleaner listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
