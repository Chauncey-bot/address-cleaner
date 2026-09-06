package main

import (
	"bufio"
	"log"
	"net/http"
	"os"
	"strings"

	"jp-address-cleaner/internal/address"
	"jp-address-cleaner/internal/handler"
	"jp-address-cleaner/internal/web"
)

func main() {
	loadDotEnv(".env")

	cleaner := address.NewCleaner()
	// AI辅助复核为可选能力：配置了 AI_API_KEY 才启用
	if key := os.Getenv("AI_API_KEY"); key != "" {
		cleaner.AIAssist = address.NewAIAssistClient(
			getenv("AI_BASE_URL", "https://ai.zhisales.com/v1"),
			key,
			getenv("AI_MODEL", "gpt-4o-mini"),
			3000,
		)
		log.Printf("AI辅助复核已启用: base=%s model=%s", getenv("AI_BASE_URL", ""), getenv("AI_MODEL", ""))
	}

	h := handler.NewHandler(cleaner)
	mux := http.NewServeMux()
	mux.HandleFunc("/clean/address", h.CleanAddress)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// 清关数据 Excel 上传清洗页面
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(web.IndexHTML)
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

// loadDotEnv 从 KEY=VALUE 格式的 .env 文件加载环境变量（文件不存在则跳过）。
// 不覆盖已存在的环境变量，便于用真实环境变量覆盖配置。
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k == "" {
			continue
		}
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
