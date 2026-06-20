package api

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xy/LogAgent/internal/db"
)

// SSEHandler 实时推送任务进度
func SSEHandler(c *gin.Context) {
	taskID := c.Param("id")
	if taskID == "" {
		c.String(http.StatusBadRequest, "Task ID is required")
		return
	}

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Transfer-Encoding", "chunked")

	// 订阅 Redis 中的任务频道
	pubsub := db.RDB.Subscribe(context.Background(), fmt.Sprintf("task:trace:%s", taskID))
	defer pubsub.Close()

	// 监听数据并推送
	c.Stream(func(w io.Writer) bool {
		msg, err := pubsub.ReceiveMessage(context.Background())
		if err != nil {
			return false
		}

		// 发送 SSE 格式数据: data: <content>\n\n
		c.SSEvent("message", msg.Payload)
		return true
	})
}
