package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xy/LogAgent/internal/config"
)

type ConfigResp struct {
	BaseURL       string `json:"base_url"`
	MaxIterations int    `json:"max_iterations"`
	LogPath       string `json:"log_path"`
}

// GetConfigHandler 获取系统部分配置
func GetConfigHandler(c *gin.Context) {
	c.JSON(http.StatusOK, ConfigResp{
		BaseURL:       config.GlobalConfig.DeepSeek.BaseURL,
		MaxIterations: 5, // 暂时写死，或者可以从 GlobalConfig 扩展
		LogPath:       "./logs/*.log",
	})
}

// UpdateConfigHandler 更新系统部分配置 (模拟更新)
func UpdateConfigHandler(c *gin.Context) {
	var req ConfigResp
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	
	// 这里可以实际更新 config.GlobalConfig 并写回文件
	// 目前先返回成功模拟逻辑
	c.JSON(http.StatusOK, gin.H{"message": "Settings updated successfully (Mock)"})
}
