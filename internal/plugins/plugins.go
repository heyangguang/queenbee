package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/logging"
)

// HookContext 钩子上下文
type HookContext struct {
	Channel         string `json:"channel"`
	Sender          string `json:"sender"`
	MessageID       string `json:"message_id"`
	OriginalMessage string `json:"original_message"`
}

// HookResult 钩子返回值
type HookResult struct {
	Text     string                 `json:"text"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// PluginEvent 插件事件
type PluginEvent struct {
	Type      string                 `json:"type"`
	Timestamp int64                  `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// Plugin 已加载的插件
type Plugin struct {
	Name    string
	Dir     string
	Hooks   PluginHooks
	scripts map[string]string // hook名 → 脚本路径
}

// PluginHooks Go 接口形式的插件钩子
type PluginHooks struct {
	TransformIncoming func(message string, ctx *HookContext) (*HookResult, error)
	TransformOutgoing func(message string, ctx *HookContext) (*HookResult, error)
}

var (
	loadedPlugins []*Plugin
	pluginsMu     sync.RWMutex

	// 事件处理器
	eventHandlers   = make(map[string][]func(PluginEvent))
	eventHandlersMu sync.RWMutex
)

// RegisterEventHandler 注册事件处理器（供 Go 插件使用）
func RegisterEventHandler(eventType string, handler func(PluginEvent)) {
	eventHandlersMu.Lock()
	defer eventHandlersMu.Unlock()
	eventHandlers[eventType] = append(eventHandlers[eventType], handler)
}

// LoadPlugins 从多个位置加载插件
// 搜索顺序：
//  1. 项目根目录 ScriptDir/plugins/ （与 binary 同级）
//  2. workspace 下的 plugins/
//  3. QUEENBEE_HOME/plugins/ （配置目录回退）
func LoadPlugins() {
	settings := config.GetSettings()

	// 构建搜索路径列表
	var searchDirs []string

	// 1. 项目根目录（binary 所在目录）
	searchDirs = append(searchDirs, filepath.Join(config.ScriptDir, "plugins"))
	// 也查找上一级（开发模式下 binary 可能在 bin/ 子目录）
	searchDirs = append(searchDirs, filepath.Join(config.ScriptDir, "..", "plugins"))

	// 2. workspace 路径下
	if settings.Workspace != nil && settings.Workspace.Path != "" {
		searchDirs = append(searchDirs, filepath.Join(settings.Workspace.Path, "plugins"))
	}

	// 3. QUEENBEE_HOME 回退
	searchDirs = append(searchDirs, filepath.Join(config.QueenBeeHome, "plugins"))

	// 去重
	seen := make(map[string]bool)
	var uniqueDirs []string
	for _, d := range searchDirs {
		abs, _ := filepath.Abs(d)
		if !seen[abs] {
			seen[abs] = true
			uniqueDirs = append(uniqueDirs, abs)
		}
	}

	for _, pluginsDir := range uniqueDirs {
		if _, err := os.Stat(pluginsDir); os.IsNotExist(err) {
			continue
		}

		entries, err := os.ReadDir(pluginsDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			pluginName := entry.Name()
			pluginDir := filepath.Join(pluginsDir, pluginName)

			// 检查是否已加载（避免多个搜索路径重复加载）
			pluginsMu.RLock()
			alreadyLoaded := false
			for _, p := range loadedPlugins {
				if p.Name == pluginName {
					alreadyLoaded = true
					break
				}
			}
			pluginsMu.RUnlock()
			if alreadyLoaded {
				continue
			}

			plugin, err := loadPlugin(pluginName, pluginDir)
			if err != nil {
				logging.Log("ERROR", fmt.Sprintf("加载插件 '%s' 失败: %v", pluginName, err))
				continue
			}

			pluginsMu.Lock()
			loadedPlugins = append(loadedPlugins, plugin)
			pluginsMu.Unlock()
			logging.Log("INFO", fmt.Sprintf("已加载插件: %s (from %s)", pluginName, pluginsDir))
		}
	}

	pluginsMu.RLock()
	count := len(loadedPlugins)
	pluginsMu.RUnlock()

	if count > 0 {
		logging.Log("INFO", fmt.Sprintf("%d 个插件已加载", count))

		// 注册事件监听器，将所有事件广播到插件
		logging.OnEvent(func(eventType string, data map[string]interface{}) {
			BroadcastEvent(PluginEvent{
				Type: eventType,
				Data: data,
			})
		})
	} else {
		logging.Log("DEBUG", "未找到插件")
	}
}

// loadPlugin 加载单个插件
// 支持以下形式（优先级从高到低）：
//  1. plugin.json — 配置文件定义钩子命令
//  2. index.js / index.ts — 原版 TS 兼容（通过 Node.js 执行）
//  3. hook_incoming.sh/.py/.js + hook_outgoing.sh/.py/.js — 独立脚本钩子
func loadPlugin(name, dir string) (*Plugin, error) {
	plugin := &Plugin{
		Name:    name,
		Dir:     dir,
		scripts: make(map[string]string),
	}

	// 检查 plugin.json 配置
	configPath := filepath.Join(dir, "plugin.json")
	if data, err := os.ReadFile(configPath); err == nil {
		var cfg struct {
			Hooks struct {
				Incoming string `json:"incoming"` // 命令
				Outgoing string `json:"outgoing"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(data, &cfg); err == nil {
			if cfg.Hooks.Incoming != "" {
				plugin.scripts["incoming"] = cfg.Hooks.Incoming
			}
			if cfg.Hooks.Outgoing != "" {
				plugin.scripts["outgoing"] = cfg.Hooks.Outgoing
			}
		}
	}

	// 检查 index.js / index.ts（原版 TS 兼容）
	// 通过 Node.js 执行，插件通过 stdin/stdout JSON 通信
	if len(plugin.scripts) == 0 {
		for _, indexFile := range []string{"index.js", "index.ts"} {
			indexPath := filepath.Join(dir, indexFile)
			if _, err := os.Stat(indexPath); err == nil {
				// index.js/index.ts 同时处理 incoming 和 outgoing
				plugin.scripts["incoming"] = indexPath
				plugin.scripts["outgoing"] = indexPath
				break
			}
		}
	}

	// 自动发现独立脚本文件
	scriptNames := map[string][]string{
		"incoming": {"hook_incoming.sh", "hook_incoming.py", "hook_incoming.js", "hook_incoming"},
		"outgoing": {"hook_outgoing.sh", "hook_outgoing.py", "hook_outgoing.js", "hook_outgoing"},
	}

	for hookType, names := range scriptNames {
		if _, exists := plugin.scripts[hookType]; exists {
			continue // 已通过其他方式定义
		}
		for _, name := range names {
			scriptPath := filepath.Join(dir, name)
			if _, err := os.Stat(scriptPath); err == nil {
				plugin.scripts[hookType] = scriptPath
				break
			}
		}
	}

	// 构建 Hook 函数
	if script, ok := plugin.scripts["incoming"]; ok {
		plugin.Hooks.TransformIncoming = makeScriptHook(name, script, dir, "incoming")
	}
	if script, ok := plugin.scripts["outgoing"]; ok {
		plugin.Hooks.TransformOutgoing = makeScriptHook(name, script, dir, "outgoing")
	}

	return plugin, nil
}

// makeScriptHook 创建脚本钩子函数
// 脚本通过 stdin 接收 JSON {text, context, hookType}，通过 stdout 返回 JSON {text, metadata}
// 支持：.sh（Bash）、.py（Python3）、.js/.ts（Node.js）、可执行文件、自定义命令
func makeScriptHook(pluginName, script, dir, hookType string) func(string, *HookContext) (*HookResult, error) {
	return func(message string, ctx *HookContext) (*HookResult, error) {
		input := map[string]interface{}{
			"text":     message,
			"context":  ctx,
			"hookType": hookType,
		}
		inputJSON, _ := json.Marshal(input)

		// 确定执行方式
		var cmd *exec.Cmd
		switch {
		case strings.HasSuffix(script, ".ts"):
			// TypeScript — 尝试 tsx, ts-node, npx tsx
			cmd = exec.Command("npx", "tsx", script)
		case strings.HasSuffix(script, ".js"):
			cmd = exec.Command("node", script)
		case strings.HasSuffix(script, ".py"):
			cmd = exec.Command("python3", script)
		case strings.HasSuffix(script, ".sh"):
			cmd = exec.Command("bash", script)
		case filepath.IsAbs(script) || strings.HasPrefix(script, "./"):
			cmd = exec.Command(script)
		default:
			// plugin.json 中的自定义命令
			parts := strings.Fields(script)
			cmd = exec.Command(parts[0], parts[1:]...)
		}

		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(string(inputJSON))

		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("插件 '%s' 执行失败: %v", pluginName, err)
		}

		// 解析输出
		outputStr := strings.TrimSpace(string(output))
		if outputStr == "" {
			// 无输出 = 不修改
			return &HookResult{Text: message}, nil
		}

		// 尝试 JSON 解析
		var result HookResult
		if err := json.Unmarshal([]byte(outputStr), &result); err != nil {
			// 非 JSON = 纯文本替换
			return &HookResult{Text: outputStr}, nil
		}

		if result.Text == "" {
			result.Text = message
		}
		return &result, nil
	}
}

// RunIncomingHooks 执行所有输入钩子
func RunIncomingHooks(message string, ctx *HookContext) *HookResult {
	pluginsMu.RLock()
	defer pluginsMu.RUnlock()

	text := message
	metadata := make(map[string]interface{})

	for _, plugin := range loadedPlugins {
		if plugin.Hooks.TransformIncoming == nil {
			continue
		}

		result, err := plugin.Hooks.TransformIncoming(text, ctx)
		if err != nil {
			logging.Log("ERROR", fmt.Sprintf("插件 '%s' transformIncoming 错误: %v", plugin.Name, err))
			continue
		}

		text = result.Text
		for k, v := range result.Metadata {
			metadata[k] = v
		}
	}

	return &HookResult{Text: text, Metadata: metadata}
}

// RunOutgoingHooks 执行所有输出钩子
func RunOutgoingHooks(message string, ctx *HookContext) *HookResult {
	pluginsMu.RLock()
	defer pluginsMu.RUnlock()

	text := message
	metadata := make(map[string]interface{})

	for _, plugin := range loadedPlugins {
		if plugin.Hooks.TransformOutgoing == nil {
			continue
		}

		result, err := plugin.Hooks.TransformOutgoing(text, ctx)
		if err != nil {
			logging.Log("ERROR", fmt.Sprintf("插件 '%s' transformOutgoing 错误: %v", plugin.Name, err))
			continue
		}

		text = result.Text
		for k, v := range result.Metadata {
			metadata[k] = v
		}
	}

	return &HookResult{Text: text, Metadata: metadata}
}

// BroadcastEvent 广播事件到所有插件处理器
func BroadcastEvent(event PluginEvent) {
	eventHandlersMu.RLock()
	defer eventHandlersMu.RUnlock()

	// 特定事件类型处理器
	if handlers, ok := eventHandlers[event.Type]; ok {
		for _, handler := range handlers {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.Log("ERROR", fmt.Sprintf("插件事件处理器 panic: %v", r))
					}
				}()
				handler(event)
			}()
		}
	}

	// 通配符处理器
	if handlers, ok := eventHandlers["*"]; ok {
		for _, handler := range handlers {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.Log("ERROR", fmt.Sprintf("插件通配符处理器 panic: %v", r))
					}
				}()
				handler(event)
			}()
		}
	}
}

// RegisterGoPlugin 注册 Go 原生插件（供内置插件使用）
func RegisterGoPlugin(name string, hooks PluginHooks) {
	pluginsMu.Lock()
	defer pluginsMu.Unlock()
	loadedPlugins = append(loadedPlugins, &Plugin{
		Name:  name,
		Hooks: hooks,
	})
	logging.Log("INFO", fmt.Sprintf("已注册内置插件: %s", name))
}

// GetLoadedPlugins 获取已加载插件列表
func GetLoadedPlugins() []string {
	pluginsMu.RLock()
	defer pluginsMu.RUnlock()
	names := make([]string, len(loadedPlugins))
	for i, p := range loadedPlugins {
		names[i] = p.Name
	}
	return names
}
