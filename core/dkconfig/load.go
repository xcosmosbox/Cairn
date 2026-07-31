package dkconfig

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load 从 YAML 文件加载配置并校验。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dkconfig: 读取配置 %s: %w", path, err)
	}
	return LoadBytes(data)
}

// LoadBytes 从 YAML 字节加载配置并校验。
func LoadBytes(data []byte) (*Config, error) {
	c := Defaults()
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("dkconfig: 解析 YAML: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}
