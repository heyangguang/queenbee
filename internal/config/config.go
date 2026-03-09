package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/queenbee-ai/queenbee/internal/db"
	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/types"
)

var (
	// QueenBeeHome 数据目录
	QueenBeeHome string
	// LogFile 日志文件路径
	LogFile string
	// ChatsDir 聊天历史目录
	ChatsDir string
	// FilesDir 文件交换目录
	FilesDir string
	// ScriptDir 项目根目录（用于模板文件查找）
	ScriptDir string
)

// Init 初始化路径
func Init() {
	// 获取可执行文件所在目录
	exe, _ := os.Executable()
	ScriptDir = filepath.Dir(exe)

	// QUEENBEE_HOME 优先级: 环境变量 > ~/.queenbee
	if env := os.Getenv("QUEENBEE_HOME"); env != "" {
		QueenBeeHome = env
	} else {
		home, _ := os.UserHomeDir()
		QueenBeeHome = filepath.Join(home, ".queenbee")
	}

	LogFile = filepath.Join(QueenBeeHome, "logs", "queue.log")
	ChatsDir = filepath.Join(QueenBeeHome, "chats")
	FilesDir = filepath.Join(QueenBeeHome, "files")

	// 确保目录存在
	for _, dir := range []string{
		QueenBeeHome,
		filepath.Dir(LogFile),
		ChatsDir,
		FilesDir,
	} {
		os.MkdirAll(dir, 0o755)
	}

	logging.Init(LogFile)
}

// ═══════════════════════════════════════════════════════════════════
// 配置读写 — 纯数据库驱动，不再依赖 settings.json
// ═══════════════════════════════════════════════════════════════════

// GetSettings 从数据库读取完整配置
func GetSettings() *types.Settings {
	settings := &types.Settings{}

	// 全局配置
	allConfig, err := db.GetAllGlobalConfig()
	if err != nil {
		return settings
	}

	// Workspace
	if path, ok := allConfig["workspace.path"]; ok {
		settings.Workspace = &types.WorkspaceConfig{Path: path}
		if name, ok := allConfig["workspace.name"]; ok {
			settings.Workspace.Name = name
		}
	}

	// Models
	if provider, ok := allConfig["models.provider"]; ok {
		settings.Models = &types.ModelsConfig{Provider: provider}
		if m, ok := allConfig["models.anthropic.model"]; ok {
			settings.Models.Anthropic = &types.ProviderModel{Model: m}
		}
		if m, ok := allConfig["models.openai.model"]; ok {
			settings.Models.OpenAI = &types.ProviderModel{Model: m}
		}
		if m, ok := allConfig["models.opencode.model"]; ok {
			settings.Models.OpenCode = &types.ProviderModel{Model: m}
		}
	}

	// Monitoring
	if hb, ok := allConfig["monitoring.heartbeat_interval"]; ok {
		if v, err := strconv.Atoi(hb); err == nil {
			settings.Monitoring = &types.MonitoringConfig{HeartbeatInterval: v}
		}
	}

	// Env
	if envJSON, ok := allConfig["env"]; ok {
		var env map[string]string
		if json.Unmarshal([]byte(envJSON), &env) == nil {
			settings.Env = env
		}
	}

	// 超时配置
	if v, ok := allConfig["max_messages"]; ok {
		settings.MaxMessages, _ = strconv.Atoi(v)
	}
	if v, ok := allConfig["agent_timeout"]; ok {
		settings.AgentTimeout, _ = strconv.Atoi(v)
	}
	if v, ok := allConfig["agent_idle_timeout"]; ok {
		settings.AgentIdleTimeout, _ = strconv.Atoi(v)
	}
	if v, ok := allConfig["conversation_timeout"]; ok {
		settings.ConversationTimeout, _ = strconv.Atoi(v)
	}
	if v, ok := allConfig["compaction_threshold"]; ok {
		settings.CompactionThreshold, _ = strconv.Atoi(v)
	}

	// Agents（从数据库）
	agents, err := db.ListAgents()
	if err == nil && len(agents) > 0 {
		settings.Agents = make(map[string]*types.AgentConfig)
		for _, a := range agents {
			settings.Agents[a.ID] = &types.AgentConfig{
				Name:             a.Name,
				Provider:         a.Provider,
				Model:            a.Model,
				FallbackProvider: a.FallbackProvider,
				FallbackModel:    a.FallbackModel,
				WorkingDirectory: a.WorkingDirectory,
				SystemPrompt:     a.SystemPrompt,
				PromptFile:       a.PromptFile,
				Env:              a.Env,
			}
		}
	}

	// Teams（从数据库）
	teams, err := db.ListTeams()
	if err == nil && len(teams) > 0 {
		settings.Teams = make(map[string]*types.TeamConfig)
		for _, t := range teams {
			settings.Teams[t.ID] = &types.TeamConfig{
				Name:             t.Name,
				Agents:           t.Agents,
				LeaderAgent:      t.LeaderAgent,
				ProjectDirectory: t.ProjectDirectory,
			}
		}
	}

	// Projects（从数据库）
	projects, err := db.ListProjects()
	if err == nil && len(projects) > 0 {
		settings.Projects = make(map[string]*types.ProjectConfig)
		for _, p := range projects {
			settings.Projects[p.ID] = &types.ProjectConfig{
				Name:        p.Name,
				Description: p.Description,
				RepoPath:    p.RepoPath,
				Teams:       p.Teams,
			}
		}
	}

	return settings
}

// SaveSettings 保存配置到数据库
func SaveSettings(settings *types.Settings) error {
	// 全局配置
	if settings.Workspace != nil {
		if settings.Workspace.Path != "" {
			db.SetGlobalConfig("workspace.path", settings.Workspace.Path)
		}
		if settings.Workspace.Name != "" {
			db.SetGlobalConfig("workspace.name", settings.Workspace.Name)
		}
	}

	if settings.Models != nil {
		if settings.Models.Provider != "" {
			db.SetGlobalConfig("models.provider", settings.Models.Provider)
		}
		if settings.Models.Anthropic != nil && settings.Models.Anthropic.Model != "" {
			db.SetGlobalConfig("models.anthropic.model", settings.Models.Anthropic.Model)
		}
		if settings.Models.OpenAI != nil && settings.Models.OpenAI.Model != "" {
			db.SetGlobalConfig("models.openai.model", settings.Models.OpenAI.Model)
		}
		if settings.Models.OpenCode != nil && settings.Models.OpenCode.Model != "" {
			db.SetGlobalConfig("models.opencode.model", settings.Models.OpenCode.Model)
		}
	}

	if settings.Monitoring != nil && settings.Monitoring.HeartbeatInterval > 0 {
		db.SetGlobalConfig("monitoring.heartbeat_interval", fmt.Sprintf("%d", settings.Monitoring.HeartbeatInterval))
	}

	if settings.Env != nil {
		envJSON, _ := json.Marshal(settings.Env)
		db.SetGlobalConfig("env", string(envJSON))
	}

	if settings.MaxMessages > 0 {
		db.SetGlobalConfig("max_messages", fmt.Sprintf("%d", settings.MaxMessages))
	}
	if settings.AgentTimeout > 0 {
		db.SetGlobalConfig("agent_timeout", fmt.Sprintf("%d", settings.AgentTimeout))
	}
	if settings.AgentIdleTimeout > 0 {
		db.SetGlobalConfig("agent_idle_timeout", fmt.Sprintf("%d", settings.AgentIdleTimeout))
	}
	if settings.ConversationTimeout > 0 {
		db.SetGlobalConfig("conversation_timeout", fmt.Sprintf("%d", settings.ConversationTimeout))
	}
	if settings.CompactionThreshold > 0 {
		db.SetGlobalConfig("compaction_threshold", fmt.Sprintf("%d", settings.CompactionThreshold))
	}

	// 同步 Agents 到数据库（upsert）
	if settings.Agents != nil {
		for id, agent := range settings.Agents {
			dbAgent := &db.DbAgent{
				ID:               id,
				Name:             agent.Name,
				Provider:         agent.Provider,
				Model:            agent.Model,
				FallbackProvider: agent.FallbackProvider,
				FallbackModel:    agent.FallbackModel,
				WorkingDirectory: agent.WorkingDirectory,
				SystemPrompt:     agent.SystemPrompt,
				PromptFile:       agent.PromptFile,
				Env:              agent.Env,
			}
			if db.AgentExists(id) {
				db.UpdateAgent(dbAgent)
			} else {
				db.CreateAgent(dbAgent)
			}
		}
	}

	// 同步 Teams 到数据库（upsert）
	if settings.Teams != nil {
		for id, team := range settings.Teams {
			dbTeam := &db.DbTeam{
				ID:               id,
				Name:             team.Name,
				LeaderAgent:      team.LeaderAgent,
				ProjectDirectory: team.ProjectDirectory,
				Agents:           team.Agents,
			}
			if db.TeamExists(id) {
				db.UpdateTeam(dbTeam)
			} else {
				db.CreateTeam(dbTeam)
			}
		}
	}

	return nil
}

// ═══════════════════════════════════════════════════════════════════
// 辅助函数
// ═══════════════════════════════════════════════════════════════════

// GetAgents 获取所有 Agent 配置
func GetAgents(settings *types.Settings) map[string]*types.AgentConfig {
	if settings.Agents != nil && len(settings.Agents) > 0 {
		return settings.Agents
	}
	// 数据库为空时返回空 map（合法初始状态）
	return map[string]*types.AgentConfig{}
}

// GetTeams 获取所有 Team 配置
func GetTeams(settings *types.Settings) map[string]*types.TeamConfig {
	if settings.Teams != nil {
		return settings.Teams
	}
	return map[string]*types.TeamConfig{}
}

// ResolveClaudeModel 解析 Claude 模型 ID
func ResolveClaudeModel(model string) string {
	if id, ok := types.ClaudeModelIDs[model]; ok {
		return id
	}
	return model
}

// ResolveCodexModel 解析 Codex 模型 ID
func ResolveCodexModel(model string) string {
	if id, ok := types.CodexModelIDs[model]; ok {
		return id
	}
	return model
}

// ResolveOpenCodeModel 解析 OpenCode 模型 ID
func ResolveOpenCodeModel(model string) string {
	if id, ok := types.OpenCodeModelIDs[model]; ok {
		return id
	}
	return model
}

// ResolveGeminiModel 解析 Gemini 模型 ID
func ResolveGeminiModel(model string) string {
	if id, ok := types.GeminiModelIDs[model]; ok {
		return id
	}
	return model
}
