package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func main() {
	db := openDB()
	srv := &Server{DB: db, sim: map[string]*simState{}}
	srv.startSimTicker()

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	api := r.Group("/api")
	{
		api.GET("/meta", srv.meta)

		api.GET("/connections", srv.listConns)
		api.POST("/connections", srv.createConn)
		api.GET("/connections/:id", srv.connDetail)
		api.POST("/connections/:id/change-berth", srv.changeBerth)
		api.POST("/connections/:id/outage", srv.outageStart)
		api.POST("/connections/:id/resume", srv.outageResume)
		api.POST("/connections/:id/close", srv.closeConn)
		api.POST("/connections/:id/readings", srv.manualReading)
		api.POST("/connections/:id/supplement", srv.submitSupplement)
		api.POST("/connections/:id/bill", srv.genBill)

		api.POST("/devices/readings", srv.deviceReading)
		api.GET("/readings", srv.listReadings)
		api.POST("/readings/:id/review", srv.reviewReading)
		api.POST("/readings/:id/resolve", srv.resolveAnomaly)
		api.POST("/readings/:id/replay", srv.simReplay)

		api.GET("/bills", srv.listBills)
		api.GET("/bills/:id", srv.billDetail)
		api.POST("/bills/:id/confirm", srv.confirmBill)
		api.POST("/bills/:id/pay", srv.payBill)
		api.POST("/bills/:id/dispute", srv.raiseDispute)

		api.GET("/disputes", srv.listDisputes)
		api.POST("/disputes/:id/resolve", srv.resolveDispute)

		api.GET("/sim/status", srv.simStatus)
		api.POST("/sim/auto", srv.simAuto)
	}

	// 生产模式：托管前端构建产物
	dist := os.Getenv("SP_FRONTEND_DIST")
	if dist == "" {
		dist = filepath.Join("..", "frontend", "dist")
	}
	if st, err := os.Stat(filepath.Join(dist, "index.html")); err == nil && !st.IsDir() {
		r.Static("/assets", filepath.Join(dist, "assets"))
		r.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			c.File(filepath.Join(dist, "index.html"))
		})
	}

	addr := os.Getenv("SP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	r.Run(addr)
}
