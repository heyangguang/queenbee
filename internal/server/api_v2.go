package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/db"
	"github.com/queenbee-ai/queenbee/internal/engine"
	"github.com/queenbee-ai/queenbee/internal/memory"
	"github.com/queenbee-ai/queenbee/types"
)

// ═══════════════════════════════════════════════════════════════════
// Phase 2: 新增 API 端点
// 健康检查、Setup、Provider/Model、Skills、Prompt、Reset、系统状态
// ═══════════════════════════════════════════════════════════════════

// --- 健康检查 ---

func handleHealth(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":    "ok",
		"timestamp": time.Now().UnixMilli(),
		"version":   "1.0.0",
		"uptime":    time.Since(startTime).Seconds(),
	})
}

var startTime = time.Now()

// --- Setup 初始化 ---

func handleSetup(c *gin.Context) {
	var req struct {
		WorkspacePath     string `json:"workspace_path"`
		DefaultProvider   string `json:"default_provider"`
		DefaultModel      string `json:"default_model"`
		DefaultAgentName  string `json:"default_agent_name"`
		HeartbeatInterval int    `json:"heartbeat_interval"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}

	// 默认值
	if req.WorkspacePath == "" {
		home, _ := os.UserHomeDir()
		req.WorkspacePath = filepath.Join(home, "queenbee-workspace")
	}
	if req.DefaultProvider == "" {
		req.DefaultProvider = "anthropic"
	}
	if req.DefaultModel == "" {
		switch req.DefaultProvider {
		case "openai":
			req.DefaultModel = "gpt-5.3-codex"
		case "gemini":
			req.DefaultModel = "flash"
		default:
			req.DefaultModel = "sonnet"
		}
	}
	if req.DefaultAgentName == "" {
		req.DefaultAgentName = "Assistant"
	}

	// 构建 Settings 并保存
	settings := &types.Settings{
		Workspace: &types.WorkspaceConfig{
			Path: req.WorkspacePath,
			Name: filepath.Base(req.WorkspacePath),
		},
		Models: &types.ModelsConfig{
			Provider: req.DefaultProvider,
		},
		Agents: map[string]*types.AgentConfig{
			"default": {
				Name:             req.DefaultAgentName,
				Provider:         req.DefaultProvider,
				Model:            req.DefaultModel,
				WorkingDirectory: filepath.Join(req.WorkspacePath, "default"),
			},
		},
		Monitoring: &types.MonitoringConfig{
			HeartbeatInterval: req.HeartbeatInterval,
		},
	}

	// 设置 provider model 配置
	switch req.DefaultProvider {
	case "openai":
		settings.Models.OpenAI = &types.ProviderModel{Model: req.DefaultModel}

	case "gemini":
		// Gemini 暂无 ProviderModel 字段，存入全局配置
		db.SetGlobalConfig("models.gemini.model", req.DefaultModel)
	default:
		settings.Models.Anthropic = &types.ProviderModel{Model: req.DefaultModel}
	}

	if err := config.SaveSettings(settings); err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("保存配置失败: %v", err)})
		return
	}

	// 创建工作区目录
	os.MkdirAll(req.WorkspacePath, 0o755)

	// 初始化 default agent 目录
	engine.EnsureAgentDirectory(filepath.Join(req.WorkspacePath, "default"))

	c.JSON(200, gin.H{
		"ok":       true,
		"message":  "初始化完成",
		"settings": settings,
	})
}

// --- Provider & Model ---

type providerInfo struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	CLI    string      `json:"cli"`
	Models []modelInfo `json:"models"`
}

type modelInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	FullID string `json:"full_id"`
}

func handleListProviders(c *gin.Context) {
	providers := []providerInfo{
		{
			ID: "anthropic", Name: "Anthropic Claude", CLI: "claude",
			Models: buildModels(types.ClaudeModelIDs, map[string]string{
				"sonnet":            "Claude Sonnet 4.6 ⭐",
				"opus":              "Claude Opus 4.6",
				"haiku":             "Claude Haiku 4.5",
				"claude-sonnet-4-6": "Claude Sonnet 4.6",
				"claude-sonnet-4-5": "Claude Sonnet 4.5",
				"claude-opus-4-6":   "Claude Opus 4.6",
				"claude-opus-4-5":   "Claude Opus 4.5",
				"claude-haiku-4-5":  "Claude Haiku 4.5",
			}),
		},
		{
			ID: "openai", Name: "OpenAI Codex", CLI: "codex",
			Models: buildModels(types.CodexModelIDs, map[string]string{
				"gpt-5.3-codex":       "GPT-5.3 Codex ⭐",
				"gpt-5.3-codex-spark": "GPT-5.3 Codex Spark",
				"gpt-5.2":             "GPT-5.2",
				"codex-mini":          "Codex Mini",
				"o4-mini":             "o4-mini",
			}),
		},

		{
			ID: "gemini", Name: "Google Gemini", CLI: "gemini",
			Models: buildModels(types.GeminiModelIDs, map[string]string{
				"flash":                  "Gemini 2.5 Flash ⭐",
				"pro":                    "Gemini 2.5 Pro",
				"gemini-3.1-pro-preview": "Gemini 3.1 Pro (Preview)",
				"gemini-3-flash":         "Gemini 3 Flash",
				"gemini-2.5-pro":         "Gemini 2.5 Pro",
				"gemini-2.5-flash":       "Gemini 2.5 Flash",
				"gemini-2.5-flash-lite":  "Gemini 2.5 Flash Lite",
			}),
		},
	}
	c.JSON(200, gin.H{"providers": providers})
}

func buildModels(modelIDs map[string]string, names map[string]string) []modelInfo {
	seen := make(map[string]bool)
	var models []modelInfo
	for id, fullID := range modelIDs {
		if seen[fullID] {
			continue
		}
		seen[fullID] = true
		name := id
		if n, ok := names[id]; ok {
			name = n
		}
		models = append(models, modelInfo{ID: id, Name: name, FullID: fullID})
	}
	return models
}

func handleListProviderModels(c *gin.Context) {
	name := c.Param("name")
	var modelIDs map[string]string
	switch name {
	case "anthropic":
		modelIDs = types.ClaudeModelIDs
	case "openai":
		modelIDs = types.CodexModelIDs

	case "gemini":
		modelIDs = types.GeminiModelIDs
	default:
		c.JSON(404, gin.H{"error": "Provider 不存在"})
		return
	}

	seen := make(map[string]bool)
	var models []modelInfo
	for id, fullID := range modelIDs {
		if seen[fullID] {
			continue
		}
		seen[fullID] = true
		models = append(models, modelInfo{ID: id, Name: id, FullID: fullID})
	}
	c.JSON(200, gin.H{"provider": name, "models": models})
}

// --- Agent Skills ---

func handleListAgentSkills(c *gin.Context) {
	agentID := c.Param("id")
	skills, err := db.ListAgentSkills(agentID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if skills == nil {
		skills = []*db.DbSkill{}
	}
	c.JSON(200, gin.H{"skills": skills, "count": len(skills)})
}

func handleAddAgentSkill(c *gin.Context) {
	agentID := c.Param("id")
	var req struct {
		SkillName   string `json:"skill_name" binding:"required"`
		SkillPath   string `json:"skill_path"`
		Description string `json:"description"`
		Source      string `json:"source"` // "builtin" 或 "custom"
		Enabled     *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "无效请求，skill_name 必填"})
		return
	}

	source := "custom"
	if req.Source != "" {
		source = req.Source
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	skill := &db.DbSkill{
		AgentID:     agentID,
		SkillName:   req.SkillName,
		SkillPath:   req.SkillPath,
		Description: req.Description,
		Source:      source,
		Enabled:     enabled,
	}

	if err := db.AddAgentSkill(skill); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"ok": true, "skill": skill})
}

func handleRemoveAgentSkill(c *gin.Context) {
	agentID := c.Param("id")
	skillID := parseID(c.Param("skillId"))

	// 先查出技能名，删除后需要用来清理磁盘文件
	var skillName string
	if skills, err := db.ListAgentSkills(agentID); err == nil {
		for _, s := range skills {
			if s.ID == skillID {
				skillName = s.SkillName
				break
			}
		}
	}

	if err := db.RemoveAgentSkill(agentID, skillID); err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}

	// 清理 Agent 工作目录中的技能文件
	if skillName != "" {
		wsPath, _ := db.GetGlobalConfig("workspace.path")
		if wsPath != "" {
			agentDir := filepath.Join(wsPath, agentID)
			engine.RemoveSkillFiles(agentDir, skillName)
		}
	}

	c.JSON(200, gin.H{"ok": true})
}

// --- Skill Definitions CRUD ---

func handleListSkillDefs(c *gin.Context) {
	skills, err := db.ListSkillDefinitions()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if skills == nil {
		skills = []*db.DbSkillDefinition{}
	}
	c.JSON(200, gin.H{"skills": skills, "count": len(skills)})
}

func handleCreateSkillDef(c *gin.Context) {
	var req struct {
		Name         string `json:"name" binding:"required"`
		Description  string `json:"description"`
		Content      string `json:"content"` // SKILL.md 完整内容
		AllowedTools string `json:"allowed_tools"`
		Source       string `json:"source"`
		Category     string `json:"category"`
		Version      string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "无效请求，name 必填"})
		return
	}

	skill := &db.DbSkillDefinition{
		Name:         req.Name,
		Description:  req.Description,
		Content:      req.Content,
		AllowedTools: req.AllowedTools,
		Source:       req.Source,
		Category:     req.Category,
		Version:      req.Version,
	}
	if skill.Source == "" {
		skill.Source = "custom"
	}

	if err := db.CreateSkillDefinition(skill); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "skill": skill})
}

func handleGetSkillDef(c *gin.Context) {
	id := parseID(c.Param("skillId"))
	skill, err := db.GetSkillDefinition(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "技能不存在"})
		return
	}
	c.JSON(200, gin.H{"skill": skill})
}

func handleUpdateSkillDef(c *gin.Context) {
	id := parseID(c.Param("skillId"))
	existing, err := db.GetSkillDefinition(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "技能不存在"})
		return
	}

	var req struct {
		Name         *string `json:"name"`
		Description  *string `json:"description"`
		Content      *string `json:"content"`
		AllowedTools *string `json:"allowed_tools"`
		Category     *string `json:"category"`
		Version      *string `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Content != nil {
		existing.Content = *req.Content
	}
	if req.AllowedTools != nil {
		existing.AllowedTools = *req.AllowedTools
	}
	if req.Category != nil {
		existing.Category = *req.Category
	}
	if req.Version != nil {
		existing.Version = *req.Version
	}

	if err := db.UpdateSkillDefinition(existing); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "skill": existing})
}

func handleDeleteSkillDef(c *gin.Context) {
	id := parseID(c.Param("skillId"))
	if err := db.DeleteSkillDefinition(id); err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// handleImportBuiltinSkills 从文件系统导入内置技能到数据库
func handleImportBuiltinSkills(c *gin.Context) {
	imported := engine.ImportBuiltinSkillsToDB()
	skills, _ := db.ListSkillDefinitions()
	if skills == nil {
		skills = []*db.DbSkillDefinition{}
	}
	c.JSON(200, gin.H{"ok": true, "message": fmt.Sprintf("导入了 %d 个内置技能", imported), "skills": skills, "count": len(skills)})
}

// handleListCLIGlobalSkills 扫描各 CLI 的全局技能目录
func handleListCLIGlobalSkills(c *gin.Context) {
	skills := engine.ScanCLIGlobalSkills()
	if skills == nil {
		skills = []engine.CLIGlobalSkill{}
	}
	c.JSON(200, gin.H{"skills": skills, "count": len(skills)})
}

// --- Agent Prompt ---

func handleGetAgentPrompt(c *gin.Context) {
	id := c.Param("id")
	settings := config.GetSettings()
	agents := config.GetAgents(settings)
	agent, ok := agents[id]
	if !ok {
		c.JSON(404, gin.H{"error": "Agent 不存在"})
		return
	}

	// 组装实际生效的 prompt 信息
	prompt := agent.SystemPrompt
	promptFile := agent.PromptFile

	// 如果指定了 prompt_file，尝试读取内容
	promptFileContent := ""
	if promptFile != "" {
		data, err := os.ReadFile(promptFile)
		if err == nil {
			promptFileContent = string(data)
		}
	}

	c.JSON(200, gin.H{
		"agent_id":            id,
		"system_prompt":       prompt,
		"prompt_file":         promptFile,
		"prompt_file_content": promptFileContent,
	})
}

func handleUpdateAgentPrompt(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		SystemPrompt string `json:"system_prompt"`
		PromptFile   string `json:"prompt_file"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}

	settings := config.GetSettings()
	if settings.Agents == nil || settings.Agents[id] == nil {
		c.JSON(404, gin.H{"error": "Agent 不存在"})
		return
	}

	settings.Agents[id].SystemPrompt = req.SystemPrompt
	settings.Agents[id].PromptFile = req.PromptFile
	config.SaveSettings(settings)

	c.JSON(200, gin.H{"ok": true})
}

// --- Agent Soul ---

// handleGetAgentSoul 读取 Agent 的 SOUL.md 文件内容
func handleGetAgentSoul(c *gin.Context) {
	id := c.Param("id")
	settings := config.GetSettings()
	agents := config.GetAgents(settings)
	agent, ok := agents[id]
	if !ok {
		c.JSON(404, gin.H{"error": "Agent 不存在"})
		return
	}

	// 解析 agent 工作目录（与 handleResetAgent 等保持一致）
	agentDir := agent.WorkingDirectory
	if agentDir == "" {
		home, _ := os.UserHomeDir()
		workspacePath := filepath.Join(home, "queenbee-workspace")
		if settings.Workspace != nil && settings.Workspace.Path != "" {
			workspacePath = settings.Workspace.Path
		}
		agentDir = filepath.Join(workspacePath, id)
	}

	// 读取工作目录下的 SOUL.md
	soulPath := filepath.Join(agentDir, "SOUL.md")
	data, err := os.ReadFile(soulPath)
	if err != nil {
		c.JSON(200, gin.H{
			"agent_id":     id,
			"soul_content": "",
			"exists":       false,
		})
		return
	}

	c.JSON(200, gin.H{
		"agent_id":     id,
		"soul_content": string(data),
		"exists":       true,
	})
}

// --- Agent Reset ---

func handleResetAgent(c *gin.Context) {
	id := c.Param("id")
	settings := config.GetSettings()
	agents := config.GetAgents(settings)
	if _, ok := agents[id]; !ok {
		c.JSON(404, gin.H{"error": "Agent 不存在"})
		return
	}

	home, _ := os.UserHomeDir()
	workspacePath := filepath.Join(home, "queenbee-workspace")
	if settings.Workspace != nil && settings.Workspace.Path != "" {
		workspacePath = settings.Workspace.Path
	}

	resetFlag := filepath.Join(workspacePath, id, "reset_flag")
	os.MkdirAll(filepath.Dir(resetFlag), 0o755)
	os.WriteFile(resetFlag, []byte("reset"), 0o644)

	c.JSON(200, gin.H{"ok": true, "message": fmt.Sprintf("Agent '%s' 对话将在下次消息时重置", id)})
}

// --- 系统状态 ---

func handleSystemStatus(c *gin.Context) {
	settings := config.GetSettings()
	agents := config.GetAgents(settings)
	teams := config.GetTeams(settings)
	status := db.GetQueueStatus()

	agentList := make([]gin.H, 0, len(agents))
	for id, a := range agents {
		agentList = append(agentList, gin.H{
			"id": id, "name": a.Name, "provider": a.Provider, "model": a.Model,
			"working_directory": a.WorkingDirectory,
		})
	}

	teamList := make([]gin.H, 0, len(teams))
	for id, t := range teams {
		teamList = append(teamList, gin.H{
			"id": id, "name": t.Name, "agents": t.Agents, "leader_agent": t.LeaderAgent,
		})
	}

	// 获取所有活跃度信息
	activities := engine.GetAllActivities()
	activityList := make([]json.RawMessage, 0, len(activities))
	for _, a := range activities {
		activityList = append(activityList, json.RawMessage(engine.ActivityToJSON(a)))
	}

	c.JSON(200, gin.H{
		"status":               "running",
		"uptime_sec":           time.Since(startTime).Seconds(),
		"pid":                  os.Getpid(),
		"go_version":           runtime.Version(),
		"queue":                status,
		"agents":               agentList,
		"teams":                teamList,
		"activities":           activityList,
		"active_conversations": engine.Conversations.Count(),
		"config": gin.H{
			"workspace":            settings.Workspace,
			"monitoring":           settings.Monitoring,
			"max_messages":         settings.MaxMessages,
			"agent_timeout":        settings.AgentTimeout,
			"agent_idle_timeout":   settings.AgentIdleTimeout,
			"conversation_timeout": settings.ConversationTimeout,
		},
	})
}

// --- Memory（Agent 长期记忆）---

// handleListMemories 列出 Agent 的记忆
func handleListMemories(c *gin.Context) {
	agentID := c.Param("id")
	limitStr := c.DefaultQuery("limit", "20")
	offsetStr := c.DefaultQuery("offset", "0")

	limit := 20
	offset := 0
	fmt.Sscanf(limitStr, "%d", &limit)
	fmt.Sscanf(offsetStr, "%d", &offset)

	memories, count, err := memory.List(agentID, limit, offset)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if memories == nil {
		memories = []*memory.Memory{}
	}
	c.JSON(200, gin.H{"memories": memories, "count": count, "agent_id": agentID})
}

// handleSearchMemories 搜索 Agent 的相关记忆
func handleSearchMemories(c *gin.Context) {
	agentID := c.Param("id")
	query := c.Query("q")
	if query == "" {
		c.JSON(400, gin.H{"error": "搜索关键词 q 不能为空"})
		return
	}

	topKStr := c.DefaultQuery("top_k", "5")
	topK := 5
	fmt.Sscanf(topKStr, "%d", &topK)

	memories, err := memory.Search(agentID, query, topK)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if memories == nil {
		memories = []*memory.Memory{}
	}
	c.JSON(200, gin.H{"memories": memories, "count": len(memories), "query": query})
}

// handleStoreMemory 手动存储一条记忆
func handleStoreMemory(c *gin.Context) {
	agentID := c.Param("id")
	var req struct {
		Content  string `json:"content" binding:"required"`
		Category string `json:"category"`
		Scope    string `json:"scope"`
		ScopeID  string `json:"scope_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "content 必填"})
		return
	}

	m, err := memory.Store(agentID, req.Content, req.Category, "manual", "", req.Scope, req.ScopeID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "memory": m})
}

// handleForgetMemory 删除一条记忆
func handleForgetMemory(c *gin.Context) {
	agentID := c.Param("id")
	memoryID := parseID(c.Param("memoryId"))
	if err := memory.Forget(agentID, memoryID); err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// handleForgetAllMemories 清空 Agent 的所有记忆
func handleForgetAllMemories(c *gin.Context) {
	agentID := c.Param("id")
	count, err := memory.ForgetAll(agentID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "deleted": count})
}

// === Project API ===

// handleListProjects 列出所有项目
func handleListProjects(c *gin.Context) {
	projects, err := db.ListProjects()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"projects": projects})
}

// handleCreateProject 创建项目
func handleCreateProject(c *gin.Context) {
	var req struct {
		ID          string   `json:"id" binding:"required"`
		Name        string   `json:"name" binding:"required"`
		Description string   `json:"description"`
		RepoPath    string   `json:"repo_path"`
		Teams       []string `json:"teams"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "id 和 name 必填"})
		return
	}
	if req.Teams == nil {
		req.Teams = []string{}
	}

	project := &db.DbProject{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		RepoPath:    req.RepoPath,
		Teams:       req.Teams,
	}
	if err := db.CreateProject(project); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "project": project})
}

// handleGetProject 获取项目详情
func handleGetProject(c *gin.Context) {
	id := c.Param("id")
	project, err := db.GetProject(id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, project)
}

// handleUpdateProject 更新项目
func handleUpdateProject(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Name        string   `json:"name" binding:"required"`
		Description string   `json:"description"`
		RepoPath    string   `json:"repo_path"`
		Teams       []string `json:"teams"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "name 必填"})
		return
	}
	if req.Teams == nil {
		req.Teams = []string{}
	}

	project := &db.DbProject{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		RepoPath:    req.RepoPath,
		Teams:       req.Teams,
	}
	if err := db.UpdateProject(project); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "project": project})
}

// handleDeleteProject 删除项目
func handleDeleteProject(c *gin.Context) {
	id := c.Param("id")
	if err := db.DeleteProject(id); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// === 通用 Memory API（按 scope 管理） ===

// handleListMemoriesByScope 按 scope 列出记忆
// GET /api/memories?scope=project&scope_id=queenbee&limit=50
func handleListMemoriesByScope(c *gin.Context) {
	scope := c.Query("scope")
	scopeID := c.Query("scope_id")
	if scope == "" || scopeID == "" {
		c.JSON(400, gin.H{"error": "scope 和 scope_id 必填"})
		return
	}
	limit := 50
	if l := c.Query("limit"); l != "" {
		fmt.Sscan(l, &limit)
	}
	memories, count, err := memory.ListByScope(scope, scopeID, limit, 0)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if memories == nil {
		memories = []*memory.Memory{}
	}
	c.JSON(200, gin.H{"memories": memories, "count": count, "scope": scope, "scope_id": scopeID})
}

// handleStoreMemoryByScope 按 scope 存储记忆
// POST /api/memories { scope, scope_id, content, category }
func handleStoreMemoryByScope(c *gin.Context) {
	var req struct {
		Scope    string `json:"scope" binding:"required"`
		ScopeID  string `json:"scope_id" binding:"required"`
		Content  string `json:"content" binding:"required"`
		Category string `json:"category"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "scope, scope_id, content 必填"})
		return
	}
	if req.Category == "" {
		req.Category = "fact"
	}
	m, err := memory.StoreByScope(req.Scope, req.ScopeID, req.Content, req.Category)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "memory": m})
}

// handleForgetMemoriesByScope 按 scope 清空记忆
// DELETE /api/memories?scope=project&scope_id=queenbee
func handleForgetMemoriesByScope(c *gin.Context) {
	scope := c.Query("scope")
	scopeID := c.Query("scope_id")
	if scope == "" || scopeID == "" {
		c.JSON(400, gin.H{"error": "scope 和 scope_id 必填"})
		return
	}
	count, err := memory.ForgetByScope(scope, scopeID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "deleted": count})
}

// handleForgetMemoryByID 按 ID 删除单条记忆
// DELETE /api/memories/:memId
func handleForgetMemoryByID(c *gin.Context) {
	idStr := c.Param("memId")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的记忆 ID"})
		return
	}
	if err := memory.ForgetByID(id); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
