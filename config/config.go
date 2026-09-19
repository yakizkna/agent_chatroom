// Package config 读取 agent_chatroom 的 YAML 配置（默认 ./config.yaml）。
//
// 2026-09-19：配置由环境变量（.env）整体切换为 YAML —— 房间 / 鉴权 / 页面开关都在这里，
// 服务不再读任何 CHATROOM_* / AUTH_* 环境变量（.env 与 .env.example 已删除）。
//
// ⚠️ config.yaml **不入 git**（含 jwt_secret），仓库里只有 config.example.yaml 作模板。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPort 缺省监听端口（旧 PORT 缺省值）。
const DefaultPort = 8093

// defaultAutoRefreshSec 页面「自动刷新」缺省间隔（秒）；0 = 不自动刷新（页面隐藏勾选框）。
const defaultAutoRefreshSec = 30

// Room 单个聊天室。
//
// 房间 id（URL 的 ?room=）**固定取 path 的最后一段**，与旧实现的 basename 口径一致
// ⇒ 改 name（展示名）不会让已分享的链接失效。
type Room struct {
	Name          string `yaml:"name"`            // 下拉框展示名；留空取 path 的最后一段
	Path          string `yaml:"path"`            // 聊天室仓库目录（须含 CHAT.md）
	IsAuth        *bool  `yaml:"is_auth"`         // true=需登录；false=免鉴权。省略=**true**（与旧「不在白名单=需登录」一致）
	ShowInputForm *bool  `yaml:"show_input_form"` // false=只读：UI 隐藏输入表单 + 服务端拒绝发言。省略=**true**（保持现状）
}

// Auth JWT 鉴权（全局）。
type Auth struct {
	ServerURL string `yaml:"server_url"` // 空 = 本服务不鉴权（登录转发与 JWT 校验都关闭）
	JWTSecret string `yaml:"jwt_secret"` // HMAC-SHA256 共享密钥，须与 JWT 鉴权服务一致
	UseProxy  bool   `yaml:"use_proxy"`  // 登录转发是否走系统代理（默认直连）
}

// UI 页面级全局设置。
type UI struct {
	// 0 = 不自动刷新（页面隐藏勾选框）；省略 = 缺省 30。用指针区分「省略」与「显式 0」。
	AutoRefreshSec *int `yaml:"auto_refresh_sec"`
}

// Config 服务完整配置。
type Config struct {
	Port  int    `yaml:"port"`
	UI    UI     `yaml:"ui"`
	Auth  Auth   `yaml:"auth"`
	Rooms []Room `yaml:"rooms"`

	path     string // 配置文件实际路径（报错/日志用）
	resolved []ResolvedRoom
}

// ResolvedRoom 归一化后的房间（布尔字段已解引用，业务侧直接用）。
type ResolvedRoom struct {
	ID            string // 房间 id = path 的最后一段（URL ?room= 用）
	Name          string // 下拉框展示名
	Path          string // 仓库目录
	IsAuth        bool   // 是否需登录
	ShowInputForm bool   // 是否展示输入表单（false = 只读）
}

// DefaultPath 缺省配置文件路径（当前目录下的 config.yaml）。
func DefaultPath() string { return "config.yaml" }

// Load 读取并校验配置。path 为空时用 DefaultPath()。
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败（%s）：%w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("解析配置文件失败（%s）：%w", path, err)
	}
	c.path = path
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Path 返回配置文件实际路径。
func (c *Config) Path() string { return c.path }

// ResolvedRooms 返回归一化后的房间列表（按配置顺序）。
func (c *Config) ResolvedRooms() []ResolvedRoom { return c.resolved }

// RoomByID 按房间 id 取归一化后的房间；未找到返回 nil。
func (c *Config) RoomByID(id string) *ResolvedRoom {
	for i := range c.resolved {
		if c.resolved[i].ID == id {
			return &c.resolved[i]
		}
	}
	return nil
}

// normalize 补默认值并逐项校验（端口 / 刷新间隔 / 房间）。
func (c *Config) normalize() error {
	if c.Port <= 0 {
		c.Port = DefaultPort
	}
	// 自动刷新：省略 = 缺省 30；显式 0 = 关闭；负数按 0 处理（防误配）
	if c.UI.AutoRefreshSec == nil {
		c.UI.AutoRefreshSec = intPtr(defaultAutoRefreshSec)
	} else if *c.UI.AutoRefreshSec < 0 {
		c.UI.AutoRefreshSec = intPtr(0)
	}
	if len(c.Rooms) == 0 {
		return fmt.Errorf("配置里没有聊天室（%s 的 rooms 为空）", c.path)
	}
	seen := map[string]bool{}
	c.resolved = make([]ResolvedRoom, 0, len(c.Rooms))
	for i, r := range c.Rooms {
		dir := strings.TrimSpace(r.Path)
		if dir == "" {
			return fmt.Errorf("rooms[%d] 缺少 path（%s）", i, c.path)
		}
		id := filepath.Base(filepath.Clean(dir))
		if id == "" || id == "." || id == string(filepath.Separator) {
			return fmt.Errorf("rooms[%d] 的 path 无法取到房间名：%q", i, r.Path)
		}
		if seen[id] {
			return fmt.Errorf("房间 id 重复：%q（rooms[%d]，id 取 path 最后一段）", id, i)
		}
		seen[id] = true
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = id // 省略 name ⇒ 用 path 的最后一段（用户 2026-09-19 定）
		}
		c.resolved = append(c.resolved, ResolvedRoom{
			ID:            id,
			Name:          name,
			Path:          dir,
			IsAuth:        boolDefault(r.IsAuth, true),        // 省略 = 需鉴权
			ShowInputForm: boolDefault(r.ShowInputForm, true), // 省略 = 展示表单
		})
	}
	return nil
}

func intPtr(v int) *int { return &v }

func boolDefault(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// ---------------------------------------------------------------- 进程内单例

var current *Config

// Set 保存当前配置（main 启动时调用一次；handlers 通过 Get() 读取）。
func Set(c *Config) { current = c }

// Get 返回当前配置；未初始化时返回 nil（调用方需判空）。
func Get() *Config { return current }
