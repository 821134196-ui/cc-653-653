package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"shorepower/internal/api"
	"shorepower/internal/service"
	"shorepower/internal/store"
)

func main() {
	dbPath := env("DB_PATH", "data/shorepower.db")
	addr := env("ADDR", ":8080")
	webDir := env("WEB_DIR", "web/dist")

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	svc := service.New(db)

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	h := api.New(svc)
	h.Register(r)

	// 托管前端构建产物（SPA）
	if st, err := os.Stat(webDir); err == nil && st.IsDir() {
		r.NoRoute(func(c *gin.Context) {
			p := c.Request.URL.Path
			if strings.HasPrefix(p, "/api/") {
				c.JSON(http.StatusNotFound, gin.H{"error": "接口不存在"})
				return
			}
			fp := filepath.Join(webDir, filepath.Clean(p))
			if _, err := os.Stat(fp); err != nil {
				c.File(filepath.Join(webDir, "index.html"))
				return
			}
			c.File(fp)
		})
		log.Printf("前端静态资源目录: %s", webDir)
	} else {
		log.Printf("未发现前端目录 %s，仅提供 API", webDir)
	}

	log.Printf("港口岸电结算系统已启动: http://localhost%s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
