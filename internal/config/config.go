package config

import (
	"os"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
	Database struct {
		MySQL string `yaml:"mysql"`
		Redis string `yaml:"redis"`
	} `yaml:"database"`
	DeepSeek struct {
		APIKey string `yaml:"api_key"`
		BaseURL string `yaml:"base_url"`
	} `yaml:"deepseek"`
}

var GlobalConfig Config

func LoadConfig(path string) error {
	file, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(file, &GlobalConfig)
}
