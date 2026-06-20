package db

import (
	"context"
	"github.com/redis/go-redis/v9"
	"github.com/xy/LogAgent/internal/config"
	"github.com/xy/LogAgent/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var (
	DB  *gorm.DB
	RDB *redis.Client
)

func Init() error {
	// 初始化 MySQL
	var err error
	DB, err = gorm.Open(mysql.Open(config.GlobalConfig.Database.MySQL), &gorm.Config{})
	if err != nil {
		return err
	}

	// 自动迁移
	err = DB.AutoMigrate(&model.Task{}, &model.TraceLog{})
	if err != nil {
		return err
	}

	// 初始化 Redis
	RDB = redis.NewClient(&redis.Options{
		Addr: config.GlobalConfig.Database.Redis,
	})

	return RDB.Ping(context.Background()).Err()
}
