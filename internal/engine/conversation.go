package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/db"
	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/types"
)

const MaxConversationMessages = 500

var (
	// Conversations 活跃的团队会话
	Conversations = &ConversationMap{m: make(map[string]*types.Conversation)}
)

// ConversationMap 线程安全的会话 map
type ConversationMap struct {
	mu sync.RWMutex
	m  map[string]*types.Conversation
}

func (cm *ConversationMap) Get(id string) (*types.Conversation, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	c, ok := cm.m[id]
	return c, ok
}

func (cm *ConversationMap) Set(id string, c *types.Conversation) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.m[id] = c
}

func (cm *ConversationMap) Delete(id string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.m, id)
}

func (cm *ConversationMap) Count() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.m)
}

// All 返回所有活跃会话的快照（加读锁）
func (cm *ConversationMap) All() []*types.Conversation {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	convs := make([]*types.Conversation, 0, len(cm.m))
	for _, c := range cm.m {
		convs = append(convs, c)
	}
	return convs
}

// CleanupStale 清理超时会话——先触发 CompleteConversation 回复用户，再清理
// 如果会话中仍有 agent 在活跃工作（最近 2 分钟有输出），跳过超时，避免误杀
func (cm *ConversationMap) CleanupStale(ttlMs int64) {
	cm.mu.Lock()
	cutoff := time.Now().UnixMilli() - ttlMs
	var staleConvs []*types.Conversation
	for id, conv := range cm.m {
		if conv.StartTime < cutoff {
			// 检查是否有 pending agent 仍在活跃工作
			hasActiveAgent := false
			for agentID := range conv.PendingAgents {
				if IsAgentActive(agentID) {
					hasActiveAgent = true
					break
				}
			}
			if hasActiveAgent {
				logging.Log("DEBUG", fmt.Sprintf("会话 %s 已超龄（%d 分钟），但仍有 agent 在活跃工作，跳过超时",
					id, (time.Now().UnixMilli()-conv.StartTime)/60000))
				continue
			}

			logging.Log("WARN", fmt.Sprintf("会话 %s 超时（已运行 %d 分钟）— 自动完成并回复用户",
				id, (time.Now().UnixMilli()-conv.StartTime)/60000))
			staleConvs = append(staleConvs, conv)
			// 先从 map 中移除，防止重复清理
			delete(cm.m, id)
			convLocks.Delete(id)
		}
	}
	cm.mu.Unlock()

	// 在锁外完成会话（CompleteConversation 会聚合已有响应并回复用户）
	for _, conv := range staleConvs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Log("ERROR", fmt.Sprintf("超时会话 %s 自动完成失败: %v", conv.ID, r))
					db.DeleteActiveConversation(conv.ID) // 确保 DB 记录也清理
				}
			}()

			// 发射 SSE 事件 + 存 DB，让前端知道会话因超时被强制完成
			elapsedMin := (time.Now().UnixMilli() - conv.StartTime) / 60000
			teamID := ""
			if conv.TeamContext != nil {
				teamID = conv.TeamContext.TeamID
			}
			timeoutData := map[string]interface{}{
				"conversationId": conv.ID,
				"teamId":         teamID,
				"elapsedMinutes": elapsedMin,
				"pendingAgents":  PendingAgentList(conv),
				"totalMessages":  conv.TotalMessages,
				"reason":         "conversation_timeout",
			}
			logging.EmitEvent("conversation_timeout", timeoutData)
			db.SaveConversationEvent(conv.ID, "conversation_timeout", timeoutData)

			CompleteConversation(conv)
		}()
	}
}

// 会话级别的互斥锁
var convLocks sync.Map // map[string]*sync.Mutex

func getConvLock(convID string) *sync.Mutex {
	val, _ := convLocks.LoadOrStore(convID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// WithConversationLock 对指定会话加锁执行
func WithConversationLock(convID string, fn func()) {
	mu := getConvLock(convID)
	mu.Lock()
	defer mu.Unlock()
	fn()
}

// AddPendingAgent 增加 agent 的引用计数
func AddPendingAgent(conv *types.Conversation, agentID string) {
	if conv.PendingAgents == nil {
		conv.PendingAgents = make(map[string]int)
	}
	conv.PendingAgents[agentID]++
	logging.Log("DEBUG", fmt.Sprintf("会话 %s: +pending @%s → 总pending=%d agents=%v", conv.ID, agentID, PendingCount(conv), PendingAgentList(conv)))
}

// RemovePendingAgent 减少 agent 的引用计数，归 0 时移除
func RemovePendingAgent(conv *types.Conversation, agentID string) {
	if conv.PendingAgents == nil {
		return
	}
	conv.PendingAgents[agentID]--
	if conv.PendingAgents[agentID] <= 0 {
		delete(conv.PendingAgents, agentID)
	}
	logging.Log("DEBUG", fmt.Sprintf("会话 %s: -pending @%s → 总pending=%d agents=%v", conv.ID, agentID, PendingCount(conv), PendingAgentList(conv)))
}

// PendingCount 从 PendingAgents map 直接计算总 pending 数（唯一 truth）
func PendingCount(conv *types.Conversation) int {
	total := 0
	for _, count := range conv.PendingAgents {
		total += count
	}
	return total
}

// PendingAgentList 返回当前 pending 的 agent ID 列表（用于日志和 API）
func PendingAgentList(conv *types.Conversation) []string {
	if conv.PendingAgents == nil {
		return []string{}
	}
	list := make([]string, 0, len(conv.PendingAgents))
	for id := range conv.PendingAgents {
		list = append(list, id)
	}
	sort.Strings(list)
	return list
}

// EnqueueInternalMessage 将 agent 间消息入队
func EnqueueInternalMessage(
	conversationID, fromAgent, targetAgent, message string,
	channel, sender, senderID, messageID, projectID string,
) {
	internalMsgID := fmt.Sprintf("internal_%s_%s_%d_%s",
		conversationID, targetAgent, time.Now().UnixMilli(),
		randomSuffix())

	db.EnqueueMessage(&db.EnqueueMessageData{
		Channel:        channel,
		Sender:         sender,
		SenderID:       senderID,
		Message:        message,
		MessageID:      internalMsgID,
		Agent:          targetAgent,
		ConversationID: conversationID,
		FromAgent:      fromAgent,
		ProjectID:      projectID,
	})

	logging.Log("INFO", fmt.Sprintf("入队内部消息: @%s → @%s (project: %s)", fromAgent, targetAgent, projectID))
}

func randomSuffix() string {
	return fmt.Sprintf("%x", time.Now().UnixNano()%0xFFFF)
}

// CompleteConversation 完成团队会话：聚合响应、保存历史、清理
func CompleteConversation(conv *types.Conversation) {
	settings := config.GetSettings()
	agents := config.GetAgents(settings)

	logging.Log("INFO", fmt.Sprintf("会话 %s 完成 — %d 个响应, %d 条消息",
		conv.ID, len(conv.Responses), conv.TotalMessages))
	// 收集所有参与过的 agent（不只是有响应的 — 确保前端能回 idle）
	agentIdSet := make(map[string]bool)
	for _, s := range conv.Responses {
		agentIdSet[s.AgentID] = true
	}
	for aid := range conv.ParticipatedAgents {
		agentIdSet[aid] = true
	}
	agentIds := make([]string, 0, len(agentIdSet))
	for aid := range agentIdSet {
		agentIds = append(agentIds, aid)
	}

	// 安全获取 teamID
	teamID := ""
	teamName := "unknown"
	if conv.TeamContext != nil {
		teamID = conv.TeamContext.TeamID
		if conv.TeamContext.Team != nil {
			teamName = conv.TeamContext.Team.Name
		}
	}

	chainEndData := map[string]interface{}{
		"teamId":         teamID,
		"totalSteps":     len(conv.Responses),
		"agents":         agentIds,
		"conversationId": conv.ID,
		"elapsedMs":      time.Now().UnixMilli() - conv.StartTime,
	}
	logging.EmitEvent("team_chain_end", chainEndData)
	db.SaveConversationEvent(conv.ID, "team_chain_end", chainEndData)

	// 从持久化存储中删除活跃会话
	db.DeleteActiveConversation(conv.ID)

	// 聚合响应
	var finalResponse string
	if len(conv.Responses) == 1 {
		finalResponse = conv.Responses[0].Response
	} else {
		var parts []string
		for _, step := range conv.Responses {
			parts = append(parts, fmt.Sprintf("@%s: %s", step.AgentID, step.Response))
		}
		finalResponse = strings.Join(parts, "\n\n------\n\n")
	}

	// 保存聊天历史
	chatFile := saveChatHistory(conv, agents)

	// 保存结构化会话历史到数据库
	now := time.Now().UnixMilli()
	durationSec := int((now - conv.StartTime) / 1000)
	db.SaveConversationHistory(&db.ConvHistory{
		ID:              conv.ID,
		TeamID:          teamID,
		TeamName:        teamName,
		Channel:         conv.Channel,
		Sender:          conv.Sender,
		OriginalMessage: conv.OriginalMessage,
		Agents:          ToJSON(agentIds),
		TotalMessages:   conv.TotalMessages,
		StartedAt:       conv.StartTime,
		EndedAt:         now,
		ChatFile:        chatFile,
		DurationSec:     durationSec,
		TotalRounds:     conv.TotalMessages,
	})

	// 收集文件
	finalResponse = strings.TrimSpace(finalResponse)
	outboundFiles := make(map[string]struct{})
	for f := range conv.Files {
		outboundFiles[f] = struct{}{}
	}
	CollectFiles(finalResponse, outboundFiles)

	filesList := make([]string, 0, len(outboundFiles))
	for f := range outboundFiles {
		filesList = append(filesList, f)
	}

	// 移除 [send_file: ...] 和 [@agent: ...] 标签
	if len(filesList) > 0 {
		re := regexp.MustCompile(`\[send_file:\s*[^\]]+\]`)
		finalResponse = strings.TrimSpace(re.ReplaceAllString(finalResponse, ""))
	}
	tagRe := regexp.MustCompile(`\[@\S+?:\s*[\s\S]*?\]`)
	finalResponse = strings.TrimSpace(tagRe.ReplaceAllString(finalResponse, ""))

	// 处理长响应
	responseMessage, allFiles := HandleLongResponse(finalResponse, filesList)

	// 入队响应
	var filesForQueue []string
	if len(allFiles) > 0 {
		filesForQueue = allFiles
	}
	db.EnqueueResponse(&db.EnqueueResponseData{
		Channel:         conv.Channel,
		Sender:          conv.Sender,
		Message:         responseMessage,
		OriginalMessage: conv.OriginalMessage,
		MessageID:       conv.MessageID,
		Files:           filesForQueue,
	})

	logging.Log("INFO", fmt.Sprintf("✓ 响应就绪 [%s] %s (%d 字符)", conv.Channel, conv.Sender, len(finalResponse)))
	// 确定 leader agent（用于前端 agentId 字段）
	leaderAgent := ""
	if len(conv.Responses) > 0 {
		leaderAgent = conv.Responses[len(conv.Responses)-1].AgentID
	}
	readyData := map[string]interface{}{
		"channel":        conv.Channel,
		"sender":         conv.Sender,
		"agentId":        leaderAgent,
		"responseLength": len(finalResponse),
		"responseText":   finalResponse,
		"messageId":      conv.MessageID,
		"conversationId": conv.ID,
	}
	logging.EmitEvent("response_ready", readyData)
	db.SaveConversationEvent(conv.ID, "response_ready", readyData)

	// 清理
	Conversations.Delete(conv.ID)
	convLocks.Delete(conv.ID) // Bug6 修复：清理会话锁，防止泄漏
}

func saveChatHistory(conv *types.Conversation, agents map[string]*types.AgentConfig) (chatFile string) {
	defer func() {
		if r := recover(); r != nil {
			logging.Log("ERROR", fmt.Sprintf("保存聊天历史失败: %v", r))
			// chatFile 保持当前值（命名返回值，可能为空）
		}
	}()

	teamID := "unknown"
	teamName := "unknown"
	if conv.TeamContext != nil {
		teamID = conv.TeamContext.TeamID
		if conv.TeamContext.Team != nil {
			teamName = conv.TeamContext.Team.Name
		}
	}

	teamChatsDir := filepath.Join(config.ChatsDir, teamID)
	os.MkdirAll(teamChatsDir, 0o755)

	var lines []string
	lines = append(lines, fmt.Sprintf("# Team Conversation: %s (@%s)", teamName, teamID))
	lines = append(lines, fmt.Sprintf("**Date:** %s", time.Now().Format(time.RFC3339)))
	lines = append(lines, fmt.Sprintf("**Channel:** %s | **Sender:** %s", conv.Channel, conv.Sender))
	lines = append(lines, fmt.Sprintf("**Messages:** %d", conv.TotalMessages))
	lines = append(lines, "", "------", "", "## 💬 User Message", "")
	lines = append(lines, conv.OriginalMessage, "")

	// 从数据库加载该会话的所有内部消息（内存 conv.Responses + DB 消息完整补全）
	dbMsgs, _ := db.GetMessagesByConversation(conv.ID)

	// 用 from_agent 不为空的消息（agent回复传给PM的）重建完整对话流
	// 同时也保留 conv.Responses 中的顺序记录
	type chatEntry struct {
		fromAgent string // 发出消息的 agent
		toAgent   string // 接收消息的 agent
		content   string
	}
	var entries []chatEntry

	// 先用 conv.Responses 作为基础（记录了每个 step 的 agent 响应）
	for _, step := range conv.Responses {
		entries = append(entries, chatEntry{
			fromAgent: step.AgentID,
			toAgent:   "",
			content:   step.Response,
		})
	}

	// 如果 DB 有更多消息（比如内存中 conv.Responses 不完整），用 DB 补全
	if len(dbMsgs) > 0 && len(entries) == 0 {
		for _, m := range dbMsgs {
			fromAgent := m.FromAgent.String
			toAgent := m.Agent.String
			if fromAgent == "" {
				continue // 跳过原始用户消息
			}
			entries = append(entries, chatEntry{
				fromAgent: fromAgent,
				toAgent:   toAgent,
				content:   m.Message,
			})
		}
	}

	// 写入所有对话步骤
	for _, e := range entries {
		agentCfg := agents[e.fromAgent]
		var label string
		if agentCfg != nil {
			label = fmt.Sprintf("%s (@%s)", agentCfg.Name, e.fromAgent)
		} else {
			label = fmt.Sprintf("@%s", e.fromAgent)
		}
		if e.toAgent != "" {
			label += fmt.Sprintf(" → @%s", e.toAgent)
		}
		lines = append(lines, "------", "", fmt.Sprintf("## %s", label), "", e.content, "")
	}

	now := time.Now()
	filename := now.Format("2006-01-02T15-04-05") + ".md"
	chatContent := strings.Join(lines, "\n")
	os.WriteFile(filepath.Join(teamChatsDir, filename), []byte(chatContent), 0o644)
	logging.Log("INFO", "聊天历史已保存")
	chatFile = filename
	return
}

// ToJSON 辅助函数
func ToJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// PersistConversation 将活跃会话状态持久化到数据库
func PersistConversation(conv *types.Conversation) {
	if conv == nil {
		return
	}
	teamID := ""
	if conv.TeamContext != nil {
		teamID = conv.TeamContext.TeamID
	}
	// 序列化 Files set 为 slice
	fileSlice := make([]string, 0, len(conv.Files))
	for f := range conv.Files {
		fileSlice = append(fileSlice, f)
	}
	row := &db.ActiveConversationRow{
		ID:                 conv.ID,
		Channel:            conv.Channel,
		Sender:             conv.Sender,
		OriginalMessage:    conv.OriginalMessage,
		MessageID:          conv.MessageID,
		TeamID:             teamID,
		PendingAgents:      ToJSON(conv.PendingAgents),
		Responses:          ToJSON(conv.Responses),
		Files:              ToJSON(fileSlice),
		TotalMessages:      conv.TotalMessages,
		MaxMessages:        conv.MaxMessages,
		StartTime:          conv.StartTime,
		OutgoingMentions:   ToJSON(conv.OutgoingMentions),
		PairExchanges:      ToJSON(conv.PairExchanges),
		ParticipatedAgents: ToJSON(conv.ParticipatedAgents),
	}
	db.SaveActiveConversation(row)
}

// RestoreConversations 启动时从数据库恢复活跃会话到内存
func RestoreConversations() int {
	rows := db.LoadActiveConversations()
	if len(rows) == 0 {
		return 0
	}

	settings := config.GetSettings()
	teams := config.GetTeams(settings)

	count := 0
	for _, r := range rows {
		// 反序列化 JSON 字段
		var pendingAgents map[string]int
		var responses []types.ChainStep
		var fileSlice []string
		var outgoingMentions map[string]int
		var pairExchanges map[string]int
		var participatedAgents map[string]bool

		json.Unmarshal([]byte(r.PendingAgents), &pendingAgents)
		json.Unmarshal([]byte(r.Responses), &responses)
		json.Unmarshal([]byte(r.Files), &fileSlice)
		json.Unmarshal([]byte(r.OutgoingMentions), &outgoingMentions)
		json.Unmarshal([]byte(r.PairExchanges), &pairExchanges)
		json.Unmarshal([]byte(r.ParticipatedAgents), &participatedAgents)

		// 初始化 nil maps
		if pendingAgents == nil {
			pendingAgents = make(map[string]int)
		}
		if outgoingMentions == nil {
			outgoingMentions = make(map[string]int)
		}
		if pairExchanges == nil {
			pairExchanges = make(map[string]int)
		}
		if participatedAgents == nil {
			participatedAgents = make(map[string]bool)
		}

		// 重建 Files set
		files := make(map[string]struct{})
		for _, f := range fileSlice {
			files[f] = struct{}{}
		}

		// 重建 TeamContext
		var teamContext *types.TeamContext
		if r.TeamID != "" {
			if team, ok := teams[r.TeamID]; ok {
				teamContext = &types.TeamContext{TeamID: r.TeamID, Team: team}
			}
		}
		if teamContext == nil {
			// 没有找到 team 配置，跳过（可能 team 已被删除）
			logging.Log("WARN", fmt.Sprintf("恢复会话 %s 失败: team %s 不存在，跳过", r.ID, r.TeamID))
			db.DeleteActiveConversation(r.ID)
			continue
		}

		conv := &types.Conversation{
			ID:                 r.ID,
			Channel:            r.Channel,
			Sender:             r.Sender,
			OriginalMessage:    r.OriginalMessage,
			MessageID:          r.MessageID,
			PendingAgents:      pendingAgents,
			Responses:          responses,
			Files:              files,
			TotalMessages:      r.TotalMessages,
			MaxMessages:        r.MaxMessages,
			TeamContext:        teamContext,
			StartTime:          r.StartTime,
			OutgoingMentions:   outgoingMentions,
			PairExchanges:      pairExchanges,
			ParticipatedAgents: participatedAgents,
		}

		Conversations.Set(r.ID, conv)
		count++
		logging.Log("INFO", fmt.Sprintf("恢复活跃会话: %s (team: %s, pending: %d)", r.ID, r.TeamID, len(pendingAgents)))
	}

	return count
}
