package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xy/LogAgent/internal/api"
	"github.com/xy/LogAgent/internal/config"
	"github.com/xy/LogAgent/internal/db"
)

func main() {
	// 1. 加载配置
	if err := config.LoadConfig("config.yaml"); err != nil {
		log.Printf("Warning: Failed to load config.yaml: %v", err)
	}

	// 2. 初始化数据库 (必须成功才能运行)
	if err := db.Init(); err != nil {
		log.Fatalf("Fatal: Database initialization failed: %v. Please check your MySQL/Redis connection and config.yaml", err)
	}

	r := gin.Default()

	// 托管静态文件
	r.Static("/static", "./static")
	r.LoadHTMLGlob("static/*.html")

	// 首页路由
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	// 诊断任务提交 API
	r.POST("/api/tasks", api.CreateTaskHandler)
	r.GET("/api/tasks", api.ListTasksHandler)
	r.GET("/api/tasks/:id", api.GetTaskDetailHandler)
	r.DELETE("/api/tasks/:id", api.DeleteTaskHandler)

	// SSE 进度推送 API
	r.GET("/api/tasks/:id/stream", api.SSEHandler)

	// 系统配置 API
	r.GET("/api/config", api.GetConfigHandler)
	r.POST("/api/config", api.UpdateConfigHandler)

	// 启动服务器
	port := config.GlobalConfig.Server.Port
	if port == "" {
		port = ":8080"
	}
	r.Run(port)
}
