package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/db"
	"github.com/queenbee-ai/queenbee/internal/engine"
	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/types"
)

// StartAPIServer 启动 API Server
func StartAPIServer() *http.Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// CORS 中间件
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	api := r.Group("/api")

	// 系统
	api.GET("/health", handleHealth)
	api.POST("/setup", handleSetup)
	api.GET("/system/status", handleSystemStatus)

	// Provider 和 Model 列表
	api.GET("/providers", handleListProviders)
	api.GET("/providers/:name/models", handleListProviderModels)

	// 消息
	api.POST("/messages", handleSendMessage)

	// Agent CRUD
	api.GET("/agents", handleListAgents)
	api.GET("/agents/:id", handleGetAgent)
	api.POST("/agents", handleCreateAgent)
	api.PUT("/agents/:id", handleUpdateAgent)
	api.DELETE("/agents/:id", handleDeleteAgent)

	// Agent Skills（Agent 与 Skill 的关联）
	api.GET("/agents/:id/skills", handleListAgentSkills)
	api.POST("/agents/:id/skills", handleAddAgentSkill)
	api.DELETE("/agents/:id/skills/:skillId", handleRemoveAgentSkill)

	// Skill Definitions（技能定义 CRUD，全 API 管理）
	api.GET("/skills", handleListSkillDefs)
	api.POST("/skills", handleCreateSkillDef)
	api.GET("/skills/:skillId", handleGetSkillDef)
	api.PUT("/skills/:skillId", handleUpdateSkillDef)
	api.DELETE("/skills/:skillId", handleDeleteSkillDef)
	api.POST("/skills/import-builtin", handleImportBuiltinSkills)
	api.GET("/skills/cli-global", handleListCLIGlobalSkills)

	// Agent Prompt
	api.GET("/agents/:id/prompt", handleGetAgentPrompt)
	api.PUT("/agents/:id/prompt", handleUpdateAgentPrompt)

	// Agent Soul（读取 SOUL.md）
	api.GET("/agents/:id/soul", handleGetAgentSoul)

	// Agent Reset
	api.POST("/agents/:id/reset", handleResetAgent)

	// Agent Kill（强杀卡死进程）
	api.POST("/agents/:id/kill", handleKillAgent)

	// Team CRUD
	api.GET("/teams", handleListTeams)
	api.GET("/teams/:id", handleGetTeam)
	api.POST("/teams", handleCreateTeam)
	api.PUT("/teams/:id", handleUpdateTeam)
	api.DELETE("/teams/:id", handleDeleteTeam)

	// Settings
	api.GET("/settings", handleGetSettings)
	api.PUT("/settings", handleUpdateSettings)

	// Queue
	api.GET("/queue/status", handleQueueStatus)
	api.GET("/queue/dead", handleDeadMessages)
	api.GET("/queue/processing", handleProcessingMessages)
	api.POST("/queue/processing/:id/recover", handleRecoverSingleMessage)
	api.POST("/queue/processing/:id/discard", handleDiscardProcessingMessage)
	api.POST("/queue/dead/:id/retry", handleRetryDead)
	api.DELETE("/queue/dead/:id", handleDeleteDead)
	api.POST("/queue/recover", handleRecoverStale)
	api.GET("/responses", handleRecentResponses)
	api.GET("/responses/pending", handlePendingResponses)
	api.POST("/responses", handleEnqueueProactiveResponse)
	api.POST("/responses/:id/ack", handleAckResponse)

	// Tasks
	api.GET("/tasks", handleListTasks)
	api.POST("/tasks", handleCreateTask)
	api.PUT("/tasks/:id", handleUpdateTask)
	api.DELETE("/tasks/:id", handleDeleteTask)

	// Logs
	api.GET("/logs/:type", handleGetLogs)

	// Chats
	api.GET("/chats", handleListChats)
	api.GET("/chats/:team", handleGetTeamChats)
	api.GET("/chats/:team/:filename", handleGetChatFile)

	// 会话历史（结构化）
	api.GET("/conversations/history", handleListConversationHistory)
	api.GET("/conversations/history/:id", handleGetConversationDetail)
	api.GET("/conversations/history/:id/timeline", handleGetConversationTimeline)

	// Sessions（活跃会话和 pending 分支）
	api.GET("/sessions", handleListSessions)
	api.POST("/sessions/:id/kill", handleKillSession)

	// SSE 事件流
	api.GET("/events/stream", handleSSE)

	// Agent 活跃度 SSE（独立通道）
	api.GET("/activity/stream", handleActivitySSE)

	// 执行历史统计 API
	api.GET("/stats/agents", handleAgentStats)
	api.GET("/stats/timeline", handleTimeline)

	// Memory（Agent 长期记忆）
	api.GET("/agents/:id/memories", handleListMemories)
	api.GET("/agents/:id/memories/search", handleSearchMemories)
	api.POST("/agents/:id/memories", handleStoreMemory)
	api.DELETE("/agents/:id/memories/:memoryId", handleForgetMemory)
	api.DELETE("/agents/:id/memories", handleForgetAllMemories)

	// Project（项目管理）
	api.GET("/projects", handleListProjects)
	api.POST("/projects", handleCreateProject)
	api.GET("/projects/:id", handleGetProject)
	api.PUT("/projects/:id", handleUpdateProject)
	api.DELETE("/projects/:id", handleDeleteProject)

	// 通用 Memory（按 scope 查询，用于 Project/User 知识管理）
	api.GET("/memories", handleListMemoriesByScope)
	api.POST("/memories", handleStoreMemoryByScope)
	api.DELETE("/memories", handleForgetMemoriesByScope)
	api.DELETE("/memories/:memId", handleForgetMemoryByID)

	port := 3777
	addr := fmt.Sprintf(":%d", port)
	server := &http.Server{Addr: addr, Handler: r}

	go func() {
		logging.Log("INFO", fmt.Sprintf("API 服务器监听 http://localhost:%d", port))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Log("ERROR", fmt.Sprintf("API 服务器错误: %v", err))
		}
	}()

	return server
}

// --- 消息 ---

func handleSendMessage(c *gin.Context) {
	var req struct {
		Channel   string   `json:"channel"`
		Sender    string   `json:"sender"`
		SenderID  string   `json:"sender_id"`
		Message   string   `json:"message"`
		MessageID string   `json:"message_id"`
		Agent     string   `json:"agent"`
		Files     []string `json:"files"`
		ProjectID string   `json:"project_id"` // 可选：显式指定项目上下文
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}
	if req.Message == "" {
		c.JSON(400, gin.H{"error": "消息不能为空"})
		return
	}
	// 限制用户输入消息长度（1MB），防止恶意超大请求占满数据库
	// 注意：此限制只影响外部 API 入口，不影响 AI 回复和内部消息
	if len(req.Message) > 1024*1024 {
		c.JSON(400, gin.H{"error": "消息过长（最大 1MB）"})
		return
	}
	if req.Channel == "" {
		req.Channel = "api"
	}
	if req.Sender == "" {
		req.Sender = "api_user"
	}
	if req.MessageID == "" {
		req.MessageID = fmt.Sprintf("%d_%x", time.Now().UnixMilli(), time.Now().UnixNano()%0xFFFF)
	}

	// 添加 channel/sender 前缀（仅当显式提供时）
	fullMessage := req.Message
	if req.Channel != "" && req.Sender != "" && req.Channel != "api" {
		fullMessage = fmt.Sprintf("[%s/%s]: %s", req.Channel, req.Sender, req.Message)
	}

	id, err := db.EnqueueMessage(&db.EnqueueMessageData{
		Channel:   req.Channel,
		Sender:    req.Sender,
		SenderID:  req.SenderID,
		Message:   fullMessage,
		MessageID: req.MessageID,
		Agent:     req.Agent,
		Files:     req.Files,
		ProjectID: req.ProjectID,
	})
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "id": id, "message_id": req.MessageID})
}

// --- Agents ---

func handleListAgents(c *gin.Context) {
	settings := config.GetSettings()
	agents := config.GetAgents(settings)
	c.JSON(200, agents)
}

func handleGetAgent(c *gin.Context) {
	id := c.Param("id")
	settings := config.GetSettings()
	agents := config.GetAgents(settings)
	agent, ok := agents[id]
	if !ok {
		c.JSON(404, gin.H{"error": "Agent 不存在"})
		return
	}
	c.JSON(200, agent)
}

func handleCreateAgent(c *gin.Context) {
	var req struct {
		ID    string            `json:"id"`
		Agent types.AgentConfig `json:"agent"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}

	settings := config.GetSettings()
	if settings.Agents == nil {
		settings.Agents = make(map[string]*types.AgentConfig)
	}

	// 唯一性校验：ID 已存在则拒绝
	if settings.Agents[req.ID] != nil {
		c.JSON(409, gin.H{"error": fmt.Sprintf("Agent ID '%s' 已存在", req.ID)})
		return
	}

	// 解析工作目录（对齐原版 agents.ts:90-92）
	workspacePath := ""
	if settings.Workspace != nil && settings.Workspace.Path != "" {
		workspacePath = settings.Workspace.Path
	} else {
		home, _ := os.UserHomeDir()
		workspacePath = filepath.Join(home, "queenbee-workspace")
	}
	if req.Agent.WorkingDirectory == "" {
		req.Agent.WorkingDirectory = filepath.Join(workspacePath, req.ID)
	}

	settings.Agents[req.ID] = &req.Agent
	if err := config.SaveSettings(settings); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 自动配置工作区
	agentDir := req.Agent.WorkingDirectory
	engine.EnsureAgentDirectory(agentDir)
	logging.Log("INFO", fmt.Sprintf("[API] Agent '%s' 工作区已配置: %s", req.ID, agentDir))

	c.JSON(201, gin.H{"ok": true, "provisioned": true})
}

func handleUpdateAgent(c *gin.Context) {
	id := c.Param("id")
	var agent types.AgentConfig
	if err := c.ShouldBindJSON(&agent); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}

	settings := config.GetSettings()
	if settings.Agents == nil || settings.Agents[id] == nil {
		c.JSON(404, gin.H{"error": "Agent 不存在"})
		return
	}
	settings.Agents[id] = &agent
	config.SaveSettings(settings)
	c.JSON(200, gin.H{"ok": true})
}

func handleDeleteAgent(c *gin.Context) {
	id := c.Param("id")
	settings := config.GetSettings()
	if settings.Agents == nil || settings.Agents[id] == nil {
		c.JSON(404, gin.H{"error": "Agent 不存在"})
		return
	}
	delete(settings.Agents, id)
	config.SaveSettings(settings)
	c.JSON(200, gin.H{"ok": true})
}

// --- Teams ---

func handleListTeams(c *gin.Context) {
	dbTeams, err := db.ListTeams()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 转换为前端期望的 map[id]TeamConfig 格式
	result := make(map[string]gin.H, len(dbTeams))
	for _, t := range dbTeams {
		agents := t.Agents
		if agents == nil {
			agents = []string{}
		}
		result[t.ID] = gin.H{
			"name":              t.Name,
			"agents":            agents,
			"leader_agent":      t.LeaderAgent,
			"project_directory": t.ProjectDirectory,
		}
	}
	c.JSON(200, result)
}

func handleGetTeam(c *gin.Context) {
	id := c.Param("id")
	t, err := db.GetTeam(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "Team 不存在"})
		return
	}
	agents := t.Agents
	if agents == nil {
		agents = []string{}
	}
	c.JSON(200, gin.H{
		"name":              t.Name,
		"agents":            agents,
		"leader_agent":      t.LeaderAgent,
		"project_directory": t.ProjectDirectory,
	})
}

func handleCreateTeam(c *gin.Context) {
	var req struct {
		ID   string           `json:"id"`
		Team types.TeamConfig `json:"team"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}

	// 唯一性校验：ID 已存在则拒绝
	if db.TeamExists(req.ID) {
		c.JSON(409, gin.H{"error": fmt.Sprintf("Team ID '%s' 已存在", req.ID)})
		return
	}

	dbTeam := &db.DbTeam{
		ID:               req.ID,
		Name:             req.Team.Name,
		LeaderAgent:      req.Team.LeaderAgent,
		ProjectDirectory: req.Team.ProjectDirectory,
		Agents:           req.Team.Agents,
	}
	if err := db.CreateTeam(dbTeam); err != nil {
		// Agent 冲突错误返回 409
		if strings.Contains(err.Error(), "只能加入一个团队") {
			c.JSON(409, gin.H{"error": err.Error()})
		} else {
			c.JSON(500, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(201, gin.H{"ok": true})
}

func handleUpdateTeam(c *gin.Context) {
	id := c.Param("id")
	var team types.TeamConfig
	if err := c.ShouldBindJSON(&team); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}

	if !db.TeamExists(id) {
		c.JSON(404, gin.H{"error": "Team 不存在"})
		return
	}

	dbTeam := &db.DbTeam{
		ID:               id,
		Name:             team.Name,
		LeaderAgent:      team.LeaderAgent,
		ProjectDirectory: team.ProjectDirectory,
		Agents:           team.Agents,
	}
	if err := db.UpdateTeam(dbTeam); err != nil {
		// Agent 冲突错误返回 409
		if strings.Contains(err.Error(), "只能加入一个团队") {
			c.JSON(409, gin.H{"error": err.Error()})
		} else {
			c.JSON(500, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func handleDeleteTeam(c *gin.Context) {
	id := c.Param("id")
	if err := db.DeleteTeam(id); err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// --- Settings ---

func handleGetSettings(c *gin.Context) {
	settings := config.GetSettings()
	c.JSON(200, settings)
}

func handleUpdateSettings(c *gin.Context) {
	var settings types.Settings
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}
	config.SaveSettings(&settings)
	c.JSON(200, gin.H{"ok": true})
}

// --- Queue ---

func handleQueueStatus(c *gin.Context) {
	status := db.GetQueueStatus()
	status2 := map[string]interface{}{
		"pending":              status.Pending,
		"processing":           status.Processing,
		"completed":            status.Completed,
		"dead":                 status.Dead,
		"responses_pending":    status.ResponsesPending,
		"active_conversations": engine.Conversations.Count(),
	}
	c.JSON(200, status2)
}

func handleDeadMessages(c *gin.Context) {
	msgs, _ := db.GetDeadMessages()
	c.JSON(200, msgs)
}

func handleRetryDead(c *gin.Context) {
	id := parseID(c.Param("id"))
	if db.RetryDeadMessage(id) {
		c.JSON(200, gin.H{"ok": true})
	} else {
		c.JSON(404, gin.H{"error": "未找到"})
	}
}

func handleDeleteDead(c *gin.Context) {
	id := parseID(c.Param("id"))
	if db.DeleteDeadMessage(id) {
		c.JSON(200, gin.H{"ok": true})
	} else {
		c.JSON(404, gin.H{"error": "未找到"})
	}
}

// handleProcessingMessages 返回正在处理中的消息
func handleProcessingMessages(c *gin.Context) {
	msgs, _ := db.GetProcessingMessages()
	if msgs == nil {
		msgs = []*db.DbMessage{}
	}
	c.JSON(200, msgs)
}

// handleRecoverSingleMessage 将单条 processing 消息恢复为 pending
func handleRecoverSingleMessage(c *gin.Context) {
	id := parseID(c.Param("id"))
	if id == 0 {
		c.JSON(400, gin.H{"error": "无效消息 ID"})
		return
	}
	db.ResetMessageToPending(id)
	logging.Log("INFO", fmt.Sprintf("[API] 手动恢复消息 %d", id))
	c.JSON(200, gin.H{"ok": true})
}

// handleDiscardProcessingMessage 丢弃单条 processing 消息（标记为 completed）
func handleDiscardProcessingMessage(c *gin.Context) {
	id := parseID(c.Param("id"))
	if id == 0 {
		c.JSON(400, gin.H{"error": "无效消息 ID"})
		return
	}
	db.DiscardProcessingMessage(id)
	logging.Log("INFO", fmt.Sprintf("[API] 丢弃 processing 消息 %d", id))
	c.JSON(200, gin.H{"ok": true})
}

// handleRecoverStale 手动恢复卡在 processing 状态的消息
// 阈值为 0，立即恢复所有 processing 消息
// 可选 JSON body: { "kill": true } 先强杀所有 agent 进程再恢复
func handleRecoverStale(c *gin.Context) {
	// 解析可选 body
	var req struct {
		Kill bool `json:"kill"`
	}
	c.ShouldBindJSON(&req) // 忽略错误，body 可选

	var killed []string
	if req.Kill {
		// 先强杀所有正在运行的 agent 进程
		killed = engine.KillAllAgentProcesses()
		if len(killed) > 0 {
			logging.Log("INFO", fmt.Sprintf("[API] 强杀 %d 个 agent 进程: %v", len(killed), killed))
			// 等待进程组退出（让 RunCommand 的 goroutine 有时间清理）
			time.Sleep(500 * time.Millisecond)
		}
	}

	// 阈值 0 = 立即恢复所有 processing 消息
	recovered := db.RecoverStaleMessages(0)
	logging.Log("INFO", fmt.Sprintf("[API] 手动恢复 %d 条卡死消息 (kill=%v, killed=%v)", recovered, req.Kill, killed))
	c.JSON(200, gin.H{"ok": true, "recovered": recovered, "killed": killed})
}

// handleKillAgent 强杀指定 agent 的进程并恢复其 processing 消息
func handleKillAgent(c *gin.Context) {
	agentID := c.Param("id")
	if agentID == "" {
		c.JSON(400, gin.H{"error": "agent ID 不能为空"})
		return
	}

	// 强杀进程
	killed := engine.KillAgentProcess(agentID)
	if killed {
		logging.Log("INFO", fmt.Sprintf("[API] 强杀 agent %s 进程成功", agentID))
		// 等 500ms 让进程退出并释放 agentChains mutex
		time.Sleep(500 * time.Millisecond)
	} else {
		logging.Log("WARN", fmt.Sprintf("[API] agent %s 没有正在运行的进程", agentID))
	}

	// 恢复该 agent 的 processing 消息
	recovered := db.RecoverAgentMessages(agentID)
	logging.Log("INFO", fmt.Sprintf("[API] 恢复 agent %s 的 %d 条消息", agentID, recovered))

	c.JSON(200, gin.H{"ok": true, "killed": killed, "recovered": recovered})
}

func handleRecentResponses(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "20")
	limit := 20
	fmt.Sscanf(limitStr, "%d", &limit)
	responses, _ := db.GetRecentResponses(limit)
	if responses == nil {
		responses = []*db.DbResponse{}
	}
	c.JSON(200, responses)
}

func handlePendingResponses(c *gin.Context) {
	channel := c.Query("channel")
	if channel == "" {
		channel = "api"
	}
	responses, _ := db.GetResponsesForChannel(channel)
	if responses == nil {
		responses = []*db.DbResponse{}
	}
	c.JSON(200, responses)
}

func handleEnqueueProactiveResponse(c *gin.Context) {
	var req struct {
		Channel  string   `json:"channel"`
		Sender   string   `json:"sender"`
		SenderID string   `json:"sender_id"`
		Message  string   `json:"message"`
		Agent    string   `json:"agent"`
		Files    []string `json:"files"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}
	if req.Channel == "" || req.Sender == "" || req.Message == "" {
		c.JSON(400, gin.H{"error": "channel, sender, message 必填"})
		return
	}
	msgID := fmt.Sprintf("proactive_%d_%x", time.Now().UnixMilli(), time.Now().UnixNano()%0xFFFF)
	db.EnqueueResponse(&db.EnqueueResponseData{
		Channel:         req.Channel,
		Sender:          req.Sender,
		SenderID:        req.SenderID,
		Message:         req.Message,
		OriginalMessage: "",
		MessageID:       msgID,
		Agent:           req.Agent,
		Files:           req.Files,
	})
	c.JSON(200, gin.H{"ok": true, "message_id": msgID})
}

func handleAckResponse(c *gin.Context) {
	id := parseID(c.Param("id"))
	db.AckResponse(id)
	c.JSON(200, gin.H{"ok": true})
}

// --- Tasks ---

func handleListTasks(c *gin.Context) {
	tasksMu.Lock()
	tasks := loadTasks()
	tasksMu.Unlock()
	c.JSON(200, tasks)
}

func handleCreateTask(c *gin.Context) {
	var task types.Task
	if err := c.ShouldBindJSON(&task); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}
	if task.ID == "" {
		task.ID = fmt.Sprintf("task_%d", time.Now().UnixMilli())
	}
	task.CreatedAt = time.Now().UnixMilli()
	task.UpdatedAt = task.CreatedAt

	tasksMu.Lock()
	tasks := loadTasks()
	tasks = append(tasks, task)
	saveTasks(tasks)
	tasksMu.Unlock()
	c.JSON(200, task)
}

func handleUpdateTask(c *gin.Context) {
	id := c.Param("id")
	var update types.Task
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(400, gin.H{"error": "无效请求"})
		return
	}

	tasksMu.Lock()
	tasks := loadTasks()
	for i, t := range tasks {
		if t.ID == id {
			update.ID = id
			update.CreatedAt = t.CreatedAt
			update.UpdatedAt = time.Now().UnixMilli()
			tasks[i] = update
			saveTasks(tasks)
			tasksMu.Unlock()
			c.JSON(200, update)
			return
		}
	}
	tasksMu.Unlock()
	c.JSON(404, gin.H{"error": "任务不存在"})
}

func handleDeleteTask(c *gin.Context) {
	id := c.Param("id")
	tasksMu.Lock()
	tasks := loadTasks()
	for i, t := range tasks {
		if t.ID == id {
			tasks = append(tasks[:i], tasks[i+1:]...)
			saveTasks(tasks)
			tasksMu.Unlock()
			c.JSON(200, gin.H{"ok": true})
			return
		}
	}
	tasksMu.Unlock()
	c.JSON(404, gin.H{"error": "任务不存在"})
}

// --- Logs ---

// 允许查看的日志类型白名单，防止路径穿越
var allowedLogTypes = map[string]bool{
	"queue": true, "error": true, "access": true, "debug": true,
}

func handleGetLogs(c *gin.Context) {
	logType := c.Param("type")
	if !allowedLogTypes[logType] {
		c.JSON(400, gin.H{"error": "不支持的日志类型"})
		return
	}
	logPath := fmt.Sprintf("%s/logs/%s.log", config.QueenBeeHome, logType)
	data, err := readLastLines(logPath, 200)
	if err != nil {
		c.JSON(404, gin.H{"error": "日志不存在"})
		return
	}
	c.JSON(200, gin.H{"lines": data})
}

// --- Sessions ---

// handleListSessions 返回所有活跃会话及其 pending 分支信息
func handleListSessions(c *gin.Context) {
	type SessionInfo struct {
		ID             string   `json:"id"`
		Team           string   `json:"team"`
		Sender         string   `json:"sender"`
		OriginalMsg    string   `json:"original_message"`
		Pending        int      `json:"pending"`
		PendingAgents  []string `json:"pending_agents"`
		TotalMessages  int      `json:"total_messages"`
		ElapsedSeconds float64  `json:"elapsed_seconds"`
	}

	var sessions []SessionInfo
	for _, conv := range engine.Conversations.All() {
		teamName := ""
		if conv.TeamContext != nil && conv.TeamContext.Team != nil {
			teamName = conv.TeamContext.Team.Name
		}
		msg := conv.OriginalMessage
		runes := []rune(msg)
		if len(runes) > 120 {
			msg = string(runes[:120]) + "..."
		}
		elapsed := float64(time.Now().UnixMilli()-conv.StartTime) / 1000.0

		// 在会话锁内读取 pending 数据，防止并发读写 map panic
		var pending int
		var pendingAgents []string
		var totalMessages int
		engine.WithConversationLock(conv.ID, func() {
			pending = engine.PendingCount(conv)
			pendingAgents = engine.PendingAgentList(conv)
			totalMessages = conv.TotalMessages
		})

		sessions = append(sessions, SessionInfo{
			ID:             conv.ID,
			Team:           teamName,
			Sender:         conv.Sender,
			OriginalMsg:    msg,
			Pending:        pending,
			PendingAgents:  pendingAgents,
			TotalMessages:  totalMessages,
			ElapsedSeconds: elapsed,
		})
	}
	if sessions == nil {
		sessions = []SessionInfo{}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions, "count": len(sessions)})
}

// handleKillSession 终止指定会话：强杀所有 pending agent、完成会话、恢复消息
func handleKillSession(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "session ID 不能为空"})
		return
	}

	// 查找活跃会话
	conv, ok := engine.Conversations.Get(sessionID)
	if !ok {
		c.JSON(404, gin.H{"error": "会话不存在或已结束"})
		return
	}

	// 获取所有 pending agent 并逐个强杀
	var killedAgents []string
	engine.WithConversationLock(conv.ID, func() {
		for agentID := range conv.PendingAgents {
			if engine.KillAgentProcess(agentID) {
				killedAgents = append(killedAgents, agentID)
			}
		}
		// 清空 pending，防止 CompleteConversation 再等待
		conv.PendingAgents = make(map[string]int)
	})

	// 等待进程退出
	if len(killedAgents) > 0 {
		time.Sleep(500 * time.Millisecond)
	}

	// 恢复该会话相关的 processing 消息
	recovered := 0
	for _, agentID := range killedAgents {
		recovered += db.RecoverAgentMessages(agentID)
	}

	// 完成会话（聚合已有的响应并回复用户）
	engine.CompleteConversation(conv)

	logging.Log("INFO", fmt.Sprintf("[API] 终止会话 %s: 强杀 %v, 恢复 %d 条消息", sessionID, killedAgents, recovered))
	c.JSON(200, gin.H{
		"ok":            true,
		"session_id":    sessionID,
		"killed_agents": killedAgents,
		"recovered":     recovered,
	})
}

// --- Chats ---

func handleListChats(c *gin.Context) {
	entries, _ := readDir(config.ChatsDir)
	c.JSON(200, entries)
}

func handleGetTeamChats(c *gin.Context) {
	team := c.Param("team")
	chatDir := fmt.Sprintf("%s/%s", config.ChatsDir, team)
	entries, _ := readDir(chatDir)
	c.JSON(200, entries)
}

// handleGetChatFile 读取单个聊天文件内容
func handleGetChatFile(c *gin.Context) {
	team := c.Param("team")
	filename := c.Param("filename")
	// 安全检查：防止路径穿越
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") ||
		strings.Contains(team, "..") || strings.Contains(team, "/") {
		c.JSON(400, gin.H{"error": "无效参数"})
		return
	}
	chatPath := filepath.Join(config.ChatsDir, team, filename)
	data, err := os.ReadFile(chatPath)
	if err != nil {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	c.JSON(200, gin.H{"content": string(data), "filename": filename, "team": team})
}

// --- 会话历史 ---

func handleListConversationHistory(c *gin.Context) {
	teamID := c.Query("team")
	limitStr := c.DefaultQuery("limit", "20")
	offsetStr := c.DefaultQuery("offset", "0")

	limit := 20
	offset := 0
	fmt.Sscanf(limitStr, "%d", &limit)
	fmt.Sscanf(offsetStr, "%d", &offset)

	if limit > 100 {
		limit = 100
	}

	list, err := db.GetConversationHistoryList(teamID, limit, offset)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if list == nil {
		list = []*db.ConvHistory{}
	}
	c.JSON(200, gin.H{"conversations": list, "count": len(list)})
}

func handleGetConversationDetail(c *gin.Context) {
	id := c.Param("id")
	history, steps, events, err := db.GetConversationDetail(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "会话不存在"})
		return
	}
	if steps == nil {
		steps = []*db.ConvStep{}
	}
	if events == nil {
		events = []*db.ConvEvent{}
	}
	c.JSON(200, gin.H{"history": history, "steps": steps, "events": events})
}

func handleGetConversationTimeline(c *gin.Context) {
	id := c.Param("id")
	history, steps, events, err := db.GetConversationDetail(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "会话不存在"})
		return
	}

	// 合并 steps + events 为统一时间线
	type TimelineItem struct {
		Type      string      `json:"type"` // "step" 或 "event"
		Timestamp int64       `json:"timestamp"`
		Data      interface{} `json:"data"`
	}

	timeline := make([]TimelineItem, 0, len(steps)+len(events))
	for _, s := range steps {
		timeline = append(timeline, TimelineItem{Type: "step", Timestamp: s.Timestamp, Data: s})
	}
	for _, e := range events {
		timeline = append(timeline, TimelineItem{Type: "event", Timestamp: e.Timestamp, Data: e})
	}

	// 按时间排序
	sort.Slice(timeline, func(i, j int) bool {
		return timeline[i].Timestamp < timeline[j].Timestamp
	})

	c.JSON(200, gin.H{"history": history, "timeline": timeline, "count": len(timeline)})
}

// --- SSE ---

func handleSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// 支持按 conversation_id 过滤
	filterConvID := c.Query("conversation_id")

	ch := make(chan string, 200)
	AddSSEClient(c.Writer, ch)
	defer RemoveSSEClient(c.Writer)

	// 发送连接确认
	c.Writer.WriteString(fmt.Sprintf("event: connected\ndata: {\"timestamp\":%d}\n\n", time.Now().UnixMilli()))
	c.Writer.Flush()

	notify := c.Request.Context().Done()
	// 30 秒心跳，防止代理/负载均衡器因空闲超时断开连接
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// 如果指定了 conversation_id 过滤，只推送含该 ID 的事件
			if filterConvID != "" {
				if !strings.Contains(msg, filterConvID) {
					continue
				}
			}
			c.Writer.WriteString(msg)
			c.Writer.Flush()
		case <-heartbeat.C:
			c.Writer.WriteString(": ping\n\n")
			c.Writer.Flush()
		case <-notify:
			return
		}
	}
}

// handleActivitySSE Agent 活跃度独立 SSE 端点
func handleActivitySSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// 支持按 agentId 过滤
	filterAgent := c.Query("agent_id")

	ch := make(chan string, 100)
	AddActivityClient(c.Writer, ch)
	defer RemoveActivityClient(c.Writer)

	// 发送当前所有活跃 agent 的快照
	for _, a := range engine.GetAllActivities() {
		if filterAgent != "" && a.AgentID != filterAgent {
			continue
		}
		msg := fmt.Sprintf("event: agent_activity\ndata: %s\n\n", engine.ActivityToJSON(a))
		c.Writer.WriteString(msg)
	}
	c.Writer.Flush()

	notify := c.Request.Context().Done()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// 按 agentId 过滤
			if filterAgent != "" {
				if !strings.Contains(msg, filterAgent) {
					continue
				}
			}
			c.Writer.WriteString(msg)
			c.Writer.Flush()
		case <-heartbeat.C:
			c.Writer.WriteString(": ping\n\n")
			c.Writer.Flush()
		case <-notify:
			return
		}
	}
}

// handleAgentStats 返回所有 agent 的执行统计
func handleAgentStats(c *gin.Context) {
	stats, err := db.GetAgentStatsList()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if stats == nil {
		stats = []*db.AgentStats{}
	}
	c.JSON(200, stats)
}

// handleTimeline 返回最近的会话时间线
func handleTimeline(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "20")
	limit := 20
	fmt.Sscanf(limitStr, "%d", &limit)
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	list, err := db.GetConversationHistoryList("", limit, 0)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if list == nil {
		list = []*db.ConvHistory{}
	}
	c.JSON(200, list)
}

// --- 辅助函数 ---

func parseID(s string) int64 {
	var id int64
	fmt.Sscanf(s, "%d", &id)
	return id
}

var tasksMu sync.Mutex

// loadTasks / saveTasks 不加锁——调用方必须持有 tasksMu
func loadTasks() []types.Task {
	tasksFile := config.QueenBeeHome + "/tasks.json"
	data, err := readFile(tasksFile)
	if err != nil {
		return []types.Task{}
	}
	var tasks []types.Task
	json.Unmarshal(data, &tasks)
	return tasks
}

func saveTasks(tasks []types.Task) {
	// 调用方已持有 tasksMu 或在同一操作中先调了 loadTasks
	tasksFile := config.QueenBeeHome + "/tasks.json"
	data, _ := json.MarshalIndent(tasks, "", "  ")
	writeFile(tasksFile, data)
}

func readFile(path string) ([]byte, error) {
	return readFileBytes(path)
}

func writeFile(path string, data []byte) error {
	return writeFileBytes(path, data)
}
