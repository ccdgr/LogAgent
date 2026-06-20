package api

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/xy/LogAgent/internal/agent"
	"github.com/xy/LogAgent/internal/db"
	"github.com/xy/LogAgent/internal/model"
	"github.com/xy/LogAgent/internal/util"
)

// CreateTaskReq 创建任务请求
type CreateTaskReq struct {
	Description string `json:"description" binding:"required"`
}

// CreateTaskHandler 提交诊断任务
func CreateTaskHandler(c *gin.Context) {
	// 1. 滑动窗口限流：限制全局每分钟最多创建 10 个诊断任务
	allowed, err := util.AllowRequest(c.Request.Context(), "global_task_creation", 10, 1*time.Minute)
	if err != nil {
		log.Printf("Rate limit check error: %v", err)
	}
	if !allowed {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "系统繁忙，请稍后再试（触发限流保护）"})
		return
	}

	var req CreateTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	taskID := uuid.New().String()
	task := model.Task{
		ID:          taskID,
		Description: req.Description,
		Status:      "running",
	}

	// 存入数据库
	if err := db.DB.Create(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task"})
		return
	}

	// 异步启动 Agent
	go func() {
		engine := agent.NewAgent(taskID)
		engine.Start(req.Description)
	}()

	c.JSON(http.StatusOK, gin.H{
		"message": "Task submitted",
		"task_id": taskID,
	})
}

// ListTasksHandler 获取任务列表 (支持分页与模糊搜索)
func ListTasksHandler(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("size", "10"))
	query := c.Query("q")

	var tasks []model.Task
	var total int64
	
	dbConn := db.DB.Model(&model.Task{})
	if query != "" {
		dbConn = dbConn.Where("description LIKE ?", "%"+query+"%")
	}

	// 统计总数
	dbConn.Count(&total)

	// 分页查询
	offset := (page - 1) * pageSize
	if err := dbConn.Order("created_at desc").Offset(offset).Limit(pageSize).Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tasks"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items": tasks,
		"total": total,
		"page":  page,
		"size":  pageSize,
	})
}

// DeleteTaskHandler 删除诊断任务
func DeleteTaskHandler(c *gin.Context) {
	taskID := c.Param("id")
	// 删除任务本身
	if err := db.DB.Delete(&model.Task{}, "id = ?", taskID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete task"})
		return
	}
	// 同时删除关联的轨迹日志
	db.DB.Delete(&model.TraceLog{}, "task_id = ?", taskID)

	c.JSON(http.StatusOK, gin.H{"message": "Task deleted successfully"})
}

// GetTaskDetailHandler 获取任务详情及推理轨迹
func GetTaskDetailHandler(c *gin.Context) {
	taskID := c.Param("id")
	var task model.Task
	if err := db.DB.First(&task, "id = ?", taskID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	var traces []model.TraceLog
	db.DB.Where("task_id = ?", taskID).Order("created_at asc").Find(&traces)

	c.JSON(http.StatusOK, gin.H{
		"task":   task,
		"traces": traces,
	})
}
