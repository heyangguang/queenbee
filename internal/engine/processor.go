package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/db"
	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/internal/memory"
	"github.com/queenbee-ai/queenbee/internal/plugins"
	"github.com/queenbee-ai/queenbee/types"
)

// 每个 Agent 的处理链（保证同 Agent 消息串行）
var agentChains sync.Map // map[string]*sync.Mutex

// ProcessQueue 处理队列中所有待处理消息
// 不同 agent 之间完全并行、互不阻塞
// 同一 agent 通过 agentChains mutex 保证串行
func ProcessQueue() {
	pendingAgents := db.GetPendingAgents()
	if len(pendingAgents) == 0 {
		return
	}

	for _, agentID := range pendingAgents {
		msg, err := db.ClaimNextMessage(agentID)
		if err != nil || msg == nil {
			continue
		}

		// 获取或创建 agent 的互斥锁（保证同 agent 串行）
		val, _ := agentChains.LoadOrStore(agentID, &sync.Mutex{})
		mu := val.(*sync.Mutex)

		go func(m *db.DbMessage, agentMu *sync.Mutex, aid string) {
			agentMu.Lock()
			defer agentMu.Unlock()
			ProcessMessage(m)
		}(msg, mu, agentID)
	}
}

// ProcessMessage 处理单条消息
func ProcessMessage(dbMsg *db.DbMessage) {
	defer func() {
		if r := recover(); r != nil {
			logging.Log("ERROR", fmt.Sprintf("处理消息 panic: %v", r))
			db.FailMessage(dbMsg.ID, fmt.Sprintf("panic: %v", r))
		}
	}()

	channel := dbMsg.Channel
	sender := dbMsg.Sender
	rawMessage := dbMsg.Message
	messageID := dbMsg.MessageID
	isInternal := dbMsg.ConversationID.Valid && dbMsg.ConversationID.String != ""

	var files []string
	if dbMsg.Files.Valid && dbMsg.Files.String != "" {
		json.Unmarshal([]byte(dbMsg.Files.String), &files)
	}

	if isInternal {
		logging.Log("INFO", fmt.Sprintf("处理 [internal] @%s→@%s: %s...",
			dbMsg.FromAgent.String, dbMsg.Agent.String, truncate(rawMessage, 50)))
	} else {
		logging.Log("INFO", fmt.Sprintf("处理 [%s] from %s: %s...", channel, sender, truncate(rawMessage, 50)))
		logging.EmitEvent("message_received", map[string]interface{}{
			"channel": channel, "sender": sender, "message": truncate(rawMessage, 2000), "messageId": messageID,
		})
	}

	// 获取配置
	settings := config.GetSettings()
	agents := config.GetAgents(settings)
	teams := config.GetTeams(settings)

	home, _ := os.UserHomeDir()
	workspacePath := filepath.Join(home, "queenbee-workspace")
	if settings.Workspace != nil && settings.Workspace.Path != "" {
		workspacePath = settings.Workspace.Path
	}

	// 路由消息
	var agentID, message string
	var isTeamRouted bool

	if dbMsg.Agent.Valid && dbMsg.Agent.String != "" {
		if _, ok := agents[dbMsg.Agent.String]; ok {
			agentID = dbMsg.Agent.String
			message = rawMessage
		}
	}
	if agentID == "" {
		agentID, message, isTeamRouted = ParseAgentRouting(rawMessage, agents, teams)
	}

	// 外部消息必须指定 agent 或 team，否则拒绝
	if _, ok := agents[agentID]; !ok {
		if !isInternal {
			// 非内部消息：未指定有效 agent/team，拒绝处理
			logging.Log("WARN", fmt.Sprintf("消息未指定有效的 agent 或 team，已拒绝: %s", truncate(rawMessage, 80)))
			db.EnqueueResponse(&db.EnqueueResponseData{
				Channel:         channel,
				Sender:          sender,
				Message:         "⚠️ 请指定一个 agent 或 team（使用 @agent_id 或 @team_id 前缀）。",
				OriginalMessage: rawMessage,
				MessageID:       messageID,
			})
			db.CompleteMessage(dbMsg.ID)
			return
		}
		// 内部消息但 agent 不存在（异常情况），记日志并跳过
		logging.Log("ERROR", fmt.Sprintf("内部消息的目标 agent '%s' 不存在，丢弃", agentID))
		db.FailMessage(dbMsg.ID, fmt.Sprintf("目标 agent '%s' 不存在", agentID))
		return
	}

	agent := agents[agentID]
	logging.Log("INFO", fmt.Sprintf("路由到 agent: %s (%s) [%s/%s]", agent.Name, agentID, agent.Provider, agent.Model))
	if !isInternal {
		logging.EmitEvent("agent_routed", map[string]interface{}{
			"agentId": agentID, "agentName": agent.Name, "provider": agent.Provider, "model": agent.Model, "isTeamRouted": isTeamRouted,
		})
	}

	// 记录参与过的 agent（用于 team_chain_end 发送完整列表）
	if isInternal && dbMsg.ConversationID.Valid {
		if c, ok := Conversations.Get(dbMsg.ConversationID.String); ok {
			if c.ParticipatedAgents == nil {
				c.ParticipatedAgents = make(map[string]bool)
			}
			c.ParticipatedAgents[agentID] = true
		}
	}

	// 计算当前会话 ID（用于 SSE 事件和历史记录）
	currentConvID := ""
	if isInternal && dbMsg.ConversationID.Valid {
		currentConvID = dbMsg.ConversationID.String
	}

	// 确定团队上下文
	var teamContext *types.TeamContext
	if isInternal {
		convID := dbMsg.ConversationID.String
		if conv, ok := Conversations.Get(convID); ok {
			teamContext = conv.TeamContext
		}
	} else {
		// 非内部消息：查找 agent 所属的 team
		teamContext = FindTeamForAgent(agentID, teams)
	}

	// 检查重置标志（仅在手动设置 reset flag 文件时才重置对话）
	// 默认所有消息都续接上次对话（-c），让 Agent 保持工作记忆
	agentResetFlag := GetAgentResetFlag(agentID, workspacePath)
	shouldReset := false
	if _, err := os.Stat(agentResetFlag); err == nil {
		shouldReset = true
		os.Remove(agentResetFlag)
	}

	// 内部消息：附加 pending 提示
	hadPendingHint := false
	if isInternal && dbMsg.ConversationID.Valid {
		if conv, ok := Conversations.Get(dbMsg.ConversationID.String); ok {
			WithConversationLock(conv.ID, func() {
				othersPending := PendingCount(conv) - 1
				if othersPending > 0 {
					message += fmt.Sprintf("\n\n------\n\n[%d other teammate response(s) are still being processed and will be delivered when ready. Do not re-mention teammates who haven't responded yet.]", othersPending)
					hadPendingHint = true
				}
			})
		}
	}

	// 运行输入钩子（插件 transformIncoming）
	hookCtx := &plugins.HookContext{
		Channel: channel, Sender: sender, MessageID: messageID, OriginalMessage: rawMessage,
	}
	incomingResult := plugins.RunIncomingHooks(message, hookCtx)
	message = incomingResult.Text

	// ── Memory Pre-Extract：已移除 ──
	// 原先在压缩前先提取记忆，但该调用在 projectID 确定之前执行，
	// 没有项目上下文也没有 Leader 保护，会导致记忆混乱。
	// 压缩后的消息会在 Agent 回复后由正式提取流程处理。
	if ShouldCompactMessage(message) {
		logging.Log("DEBUG", fmt.Sprintf("消息过长将被压缩 (agent: %s, len: %d)", agentID, len([]rune(message))))
	}

	// ── Context Compaction：过长消息自动压缩 ──
	if ShouldCompactMessage(message) {
		compacted, ok := CompactMessage(message, agentID, agent.Provider)
		if ok {
			message = compacted
		}
	}

	// ── Memory Injection：注入相关长期记忆（三层搜索：Agent + Project + User）──
	// 项目上下文优先级链：
	//   1. 消息自带 project_id（前端选择器或 API 显式指定）→ 精确
	//   2. 消息文本匹配项目名 → 精确
	//   3. Team 只关联 1 个 Project → 直接用
	//   4. Team 关联多个 Project → 注入项目清单让 AI 判断
	projectID := ""
	projectName := ""
	projectListInjected := false

	// 优先级 1：消息显式指定 project_id
	if dbMsg.ProjectID.Valid && dbMsg.ProjectID.String != "" {
		projectID = dbMsg.ProjectID.String
		logging.Log("INFO", fmt.Sprintf("📁 项目上下文（显式指定）: %s", projectID))
	}

	// 优先级 2：消息文本匹配项目名
	if projectID == "" {
		if matched := db.FindProjectByName(rawMessage); matched != nil {
			projectID = matched.ID
			logging.Log("INFO", fmt.Sprintf("📁 项目上下文（名称匹配）: %s (%s)", matched.Name, matched.ID))
		}
	}

	// 优先级 3/4：根据 Team 关联的项目数量决定
	if projectID == "" && teamContext != nil {
		teamProjects := db.GetProjectsForTeam(teamContext.TeamID)
		if len(teamProjects) == 1 {
			// 只有一个项目 → 直接用
			projectID = teamProjects[0].ID
			logging.Log("INFO", fmt.Sprintf("📁 项目上下文（唯一关联）: %s (%s)", teamProjects[0].Name, teamProjects[0].ID))
		} else if len(teamProjects) > 1 {
			// 多个项目 → 注入项目清单到消息中，让 AI 自行判断或询问用户
			var projectListParts []string
			for _, p := range teamProjects {
				desc := p.Description
				if desc == "" {
					desc = "无描述"
				}
				repo := p.RepoPath
				if repo == "" {
					repo = "未指定"
				}
				projectListParts = append(projectListParts, fmt.Sprintf("  - %s (@%s): %s | 仓库路径: %s", p.Name, p.ID, desc, repo))
			}
			projectListText := fmt.Sprintf("\n\n[系统提示] 当前团队关联了以下项目，请根据对话内容判断用户在说哪个项目（如不确定请询问用户）：\n%s\n注意: 操作项目文件时请使用对应项目的仓库路径，不要使用你当前的工作目录。\n", strings.Join(projectListParts, "\n"))
			message = message + projectListText
			projectListInjected = true
			logging.Log("INFO", fmt.Sprintf("📁 注入 %d 个项目清单供 AI 判断", len(teamProjects)))
		}
	}

	// ── 注入项目上下文提示：告诉 AI 用户当前讨论的是哪个项目 ──
	if projectID != "" {
		if proj, err := db.GetProject(projectID); err == nil && proj != nil {
			projectName = proj.Name
			desc := proj.Description
			if desc == "" {
				desc = "无描述"
			}
			repoPath := proj.RepoPath
			if repoPath == "" {
				repoPath = "未指定"
			}
			projectContext := fmt.Sprintf("\n\n[系统提示] 当前项目信息:\n- 项目名: %s (ID: %s)\n- 项目描述: %s\n- 项目仓库路径: %s\n\n重要: 你的当前工作目录是 Agent 配置目录，不是项目仓库。当用户要求创建、修改或读取项目文件时，必须使用「项目仓库路径」(%s) 作为根目录，而不是你当前所在的工作目录。\n", proj.Name, proj.ID, desc, repoPath, repoPath)
			message = message + projectContext
			logging.Log("INFO", fmt.Sprintf("📁 注入项目上下文提示: %s (%s), repo: %s", proj.Name, proj.ID, repoPath))
		}
	}

	senderID := ""
	if dbMsg.SenderID.Valid && dbMsg.SenderID.String != "" {
		senderID = dbMsg.SenderID.String
	} else if sender != "" {
		senderID = sender // 回退到 sender 字段
	}
	if memories, err := memory.SearchMultiScope(agentID, projectID, senderID, rawMessage, 5); err == nil && len(memories) > 0 {
		memoryContext := memory.FormatMemoriesForContext(memories)
		message = message + memoryContext
		logExtra := ""
		if projectListInjected {
			logExtra = " (+ 项目清单)"
		}
		logging.Log("INFO", fmt.Sprintf("🧠 注入 %d 条相关记忆 (agent: %s, project: %s, user: %s)%s", len(memories), agentID, projectID, senderID, logExtra))
	}

	// 调用 Agent
	// 对齐原版：chain_step_start 包含 fromAgent（原版 queue-processor.ts:163）
	stepStartMs := time.Now().UnixMilli()
	stepStartData := map[string]interface{}{
		"agentId": agentID, "agentName": agent.Name, "fromAgent": nullToString(dbMsg.FromAgent),
		"startedAt": stepStartMs,
	}
	if currentConvID != "" {
		stepStartData["conversationId"] = currentConvID
	}
	logging.EmitEvent("chain_step_start", stepStartData)
	if currentConvID != "" {
		db.SaveConversationEvent(currentConvID, "chain_step_start", stepStartData)
	}

	response, err := InvokeAgent(agent, agentID, message, workspacePath, shouldReset, agents, teams)
	if err == nil {
		// 异步触发 soul 更新（不阻塞主流程）
		soulAgentDir := filepath.Join(workspacePath, agentID)
		go MaybeTriggerSoulUpdate(agentID, soulAgentDir, agent)

		// 异步提取记忆
		// Leader Agent：提取三层（agent + project + user）
		// 非 Leader Agent：只提取 agent scope 行为学习（省资源）
		isLeader := teamContext == nil || teamContext.Team == nil || teamContext.Team.LeaderAgent == agentID
		if isLeader {
			go memory.ExtractMemories(agentID, rawMessage, response, currentConvID, RunUtilityPrompt, agent.Provider, projectID, senderID, projectName)
		} else {
			go memory.ExtractAgentOnlyMemories(agentID, rawMessage, response, currentConvID, RunUtilityPrompt, agent.Provider)
		}
	}

	// 检测手动强杀：如果是人工 kill，不走正常错误处理，直接重置消息让其重新消费
	if err != nil && IsAgentManualKilled(agentID) {
		ClearAgentManualKilled(agentID)
		logging.Log("INFO", fmt.Sprintf("🔄 agent %s 被手动强杀，重置消息 %d 为 pending 等待重新处理", agentID, dbMsg.ID))
		// 重置消息为 pending（不是 FailMessage，不增加 retry_count）
		db.ResetMessageToPending(dbMsg.ID)
		return // 跳过 CompleteMessage 和错误处理，消息将被重新消费
	}

	if err != nil {
		provider := agent.Provider
		if provider == "" {
			provider = "anthropic"
		}
		logging.Log("ERROR", fmt.Sprintf("%s 错误 (agent: %s): %v", provider, agentID, err))

		// 区分超时和其他错误
		isTimeout := strings.Contains(err.Error(), "超时")
		isIdleTimeout := strings.Contains(err.Error(), "空闲")

		// 发射 SSE 事件 + 存 DB，让前端能实时感知
		eventType := "agent_error"
		if isTimeout {
			eventType = "agent_timeout"
			if isIdleTimeout {
				response = fmt.Sprintf("⚡ Agent @%s 疑似卡死（长时间无输出，已自动终止）。错误: %v", agentID, err)
			} else {
				response = fmt.Sprintf("⏰ Agent @%s 达到最大执行时间限制，已自动终止。错误: %v", agentID, err)
			}
		} else {
			response = fmt.Sprintf("❌ Agent @%s 处理出错: %v", agentID, err)
		}
		errorEventData := map[string]interface{}{
			"agentId":   agentID,
			"agentName": agent.Name,
			"provider":  provider,
			"error":     err.Error(),
			"isTimeout": isTimeout,
		}
		if currentConvID != "" {
			errorEventData["conversationId"] = currentConvID
		}
		logging.EmitEvent(eventType, errorEventData)
		if currentConvID != "" {
			db.SaveConversationEvent(currentConvID, eventType, errorEventData)
		}
	}

	// 对齐原版：chain_step_done 包含 responseText（原版 queue-processor.ts:174）
	stepElapsedMs := time.Now().UnixMilli() - stepStartMs
	stepDoneData := map[string]interface{}{
		"agentId": agentID, "agentName": agent.Name, "responseLength": len(response), "responseText": response,
		"elapsedMs": stepElapsedMs, "startedAt": stepStartMs,
	}
	if currentConvID != "" {
		stepDoneData["conversationId"] = currentConvID
	}
	// 内部消息的 chain_step_done 延后发射（等 hadPendingHint 检查后决定是否发）
	// 非内部消息立即发射
	if !isInternal {
		logging.EmitEvent("chain_step_done", stepDoneData)
		if currentConvID != "" {
			db.SaveConversationEvent(currentConvID, "chain_step_done", stepDoneData)
		}
	}

	// --- 无团队上下文：直接回复 ---
	if teamContext == nil {
		finalResponse := strings.TrimSpace(response)

		outboundFiles := make(map[string]struct{})
		CollectFiles(finalResponse, outboundFiles)
		filesList := mapKeys(outboundFiles)
		if len(filesList) > 0 {
			re := regexp.MustCompile(`\[send_file:\s*[^\]]+\]`)
			finalResponse = strings.TrimSpace(re.ReplaceAllString(finalResponse, ""))
		}

		// 运行输出钩子（插件 transformOutgoing）
		outgoingResult := plugins.RunOutgoingHooks(finalResponse, hookCtx)
		finalResponse = outgoingResult.Text
		var hookMetadata map[string]interface{}
		if len(outgoingResult.Metadata) > 0 {
			hookMetadata = outgoingResult.Metadata
		}

		responseMessage, allFiles := HandleLongResponse(finalResponse, filesList)

		db.EnqueueResponse(&db.EnqueueResponseData{
			Channel:         channel,
			Sender:          sender,
			SenderID:        nullToString(dbMsg.SenderID),
			Message:         responseMessage,
			OriginalMessage: rawMessage,
			MessageID:       messageID,
			Agent:           agentID,
			Files:           allFiles,
			Metadata:        hookMetadata,
		})

		logging.Log("INFO", fmt.Sprintf("✓ 响应就绪 [%s] %s via agent:%s (%d 字符)", channel, sender, agentID, len(finalResponse)))

		// 保存单 Agent 会话历史（便于回放）
		singleConvID := fmt.Sprintf("single_%s_%d", messageID, time.Now().UnixMilli())

		// 对齐原版：非团队 response_ready 包含 responseText
		soloReadyData := map[string]interface{}{
			"channel": channel, "sender": sender, "agentId": agentID, "responseLength": len(finalResponse), "responseText": finalResponse, "messageId": messageID, "conversationId": singleConvID,
		}
		logging.EmitEvent("response_ready", soloReadyData)
		db.SaveConversationEvent(singleConvID, "response_ready", soloReadyData)

		db.SaveConversationHistory(&db.ConvHistory{
			ID:              singleConvID,
			TeamID:          "_solo",
			TeamName:        "Solo Agent",
			Channel:         channel,
			Sender:          sender,
			OriginalMessage: rawMessage,
			Agents:          ToJSON([]string{agentID}),
			TotalMessages:   1,
			StartedAt:       dbMsg.CreatedAt,
			EndedAt:         time.Now().UnixMilli(),
		})
		db.SaveConversationStep(&db.ConvStep{
			ConversationID: singleConvID,
			Seq:            1,
			FromAgent:      "user",
			ToAgent:        agentID,
			Message:        rawMessage,
			MessageType:    "user_message",
			Timestamp:      dbMsg.CreatedAt,
		})
		db.SaveConversationStep(&db.ConvStep{
			ConversationID: singleConvID,
			Seq:            2,
			FromAgent:      agentID,
			ToAgent:        "user",
			Message:        finalResponse,
			MessageType:    "agent_response",
			Timestamp:      time.Now().UnixMilli(),
		})

		db.CompleteMessage(dbMsg.ID)
		return
	}

	// --- 团队上下文：会话级消息传递 ---
	var conv *types.Conversation

	if isInternal && dbMsg.ConversationID.Valid {
		if c, ok := Conversations.Get(dbMsg.ConversationID.String); ok {
			conv = c
		} else {
			// 会话已被 CleanupStale 完成或清理（竞态：goroutine 超时后回来时会话已不在）
			// 直接丢弃，不创建新会话，避免僵尸会话
			logging.Log("WARN", fmt.Sprintf("内部消息的会话 %s 已不存在（可能已超时完成），丢弃 @%s 的响应",
				dbMsg.ConversationID.String, agentID))
			db.CompleteMessage(dbMsg.ID)
			return
		}
	}

	if conv == nil {
		convID := fmt.Sprintf("%s_%d", messageID, time.Now().UnixMilli())
		maxMsgs := settings.MaxMessages
		if maxMsgs <= 0 {
			maxMsgs = MaxConversationMessages
		}
		conv = &types.Conversation{
			ID:                 convID,
			Channel:            channel,
			Sender:             sender,
			OriginalMessage:    rawMessage,
			MessageID:          messageID,
			PendingAgents:      map[string]int{agentID: 1},
			Responses:          nil,
			Files:              make(map[string]struct{}),
			TotalMessages:      0,
			MaxMessages:        maxMsgs,
			TeamContext:        teamContext,
			StartTime:          time.Now().UnixMilli(),
			OutgoingMentions:   make(map[string]int),
			PairExchanges:      make(map[string]int),
			ParticipatedAgents: map[string]bool{agentID: true},
		}
		Conversations.Set(convID, conv)
		currentConvID = convID
		teamName := "unknown"
		if teamContext != nil && teamContext.Team != nil {
			teamName = teamContext.Team.Name
		}
		logging.Log("INFO", fmt.Sprintf("会话启动: %s (team: %s)", convID, teamName))
		// 对齐原版：team_chain_start 包含 agents 和 leader（原版 queue-processor.ts:239）
		chainStartData := map[string]interface{}{
			"conversationId": convID,
			"teamId":         teamContext.TeamID,
		}
		if teamContext.Team != nil {
			chainStartData["teamName"] = teamContext.Team.Name
			chainStartData["agents"] = teamContext.Team.Agents
			chainStartData["leader"] = teamContext.Team.LeaderAgent
		}
		logging.EmitEvent("team_chain_start", chainStartData)
		db.SaveConversationEvent(convID, "team_chain_start", chainStartData)
		PersistConversation(conv) // 会话创建后立即持久化

		// 保存用户初始消息作为第一步
		db.SaveConversationStep(&db.ConvStep{
			ConversationID: convID,
			Seq:            0,
			FromAgent:      "user",
			ToAgent:        agentID,
			Message:        rawMessage,
			MessageType:    "user_message",
			Timestamp:      dbMsg.CreatedAt,
		})
	}

	// 检查队友 mention（在锁外提取，避免持锁调用 AI）
	teamID := ""
	if conv.TeamContext != nil {
		teamID = conv.TeamContext.TeamID
	}
	teammateMentions := ExtractTeammateMentions(response, agentID, teamID, teams, agents)
	fromAgent := nullToString(dbMsg.FromAgent)

	// 所有对 conv 字段的修改都在会话锁内进行，防止并发数据竞争
	WithConversationLock(conv.ID, func() {
		// 记录响应
		conv.Responses = append(conv.Responses, types.ChainStep{AgentID: agentID, Response: response})
		PersistConversation(conv) // response 添加后持久化
		conv.TotalMessages++
		CollectFiles(response, conv.Files)

		// 保存 agent 回复步骤：有 handoff 时只保存 handoff（避免冗余），无 handoff 时保存完整回复
		if len(teammateMentions) > 0 && conv.TotalMessages < conv.MaxMessages {
			conv.OutgoingMentions[agentID] = len(teammateMentions)

			// 先保存一条 agent_response（完整回复），避免 handoff 重复存消息
			db.SaveConversationStep(&db.ConvStep{
				ConversationID: conv.ID,
				Seq:            conv.TotalMessages,
				FromAgent:      agentID,
				ToAgent:        fromAgent,
				Message:        response,
				MessageType:    "agent_response",
				Timestamp:      time.Now().UnixMilli(),
			})

			for i, mention := range teammateMentions {
				// 循环检测：同方向超 5 次才跳过（纯计数兜底，不做内容判断）
				if fromAgent != "" && mention.TeammateID == fromAgent {
					pairKey := agentID + "→" + mention.TeammateID
					conv.PairExchanges[pairKey]++
					exchanges := conv.PairExchanges[pairKey]
					if exchanges > 5 {
						logging.Log("INFO", fmt.Sprintf("跳过循环: @%s → @%s（第 %d 次回弹，超过 5 次安全阈值）",
							agentID, mention.TeammateID, exchanges))
						continue
					}
				}

				logging.Log("INFO", fmt.Sprintf("@%s → @%s", agentID, mention.TeammateID))
				handoffData := map[string]interface{}{
					"teamId": teamID, "fromAgent": agentID, "toAgent": mention.TeammateID, "conversationId": conv.ID,
				}
				logging.EmitEvent("chain_handoff", handoffData)
				db.SaveConversationEvent(conv.ID, "chain_handoff", handoffData)
				// 记录 pending agent（计数自动 +1）
				AddPendingAgent(conv, mention.TeammateID)
				PersistConversation(conv) // +pending 后持久化

				internalMsg := fmt.Sprintf("[Message from teammate @%s]:\n%s\n\n------\n[When you finish: if your work produced substantive output (code, design, analysis, test results), reply back to @%s with a `[@%s: summary]` tag. If it was just an acknowledgment or greeting with no actionable output, you may end without tagging back.]", agentID, mention.Message, agentID, agentID)

				// handoff 步骤只存路由摘要，不重复存完整消息
				db.SaveConversationStep(&db.ConvStep{
					ConversationID: conv.ID,
					Seq:            conv.TotalMessages + i + 1,
					FromAgent:      agentID,
					ToAgent:        mention.TeammateID,
					Message:        fmt.Sprintf("@%s → @%s", agentID, mention.TeammateID),
					MessageType:    "handoff",
					Timestamp:      time.Now().UnixMilli(),
				})

				EnqueueInternalMessage(conv.ID, agentID, mention.TeammateID, internalMsg,
					channel, sender, nullToString(dbMsg.SenderID), messageID, projectID)
			}
		} else if len(teammateMentions) > 0 {
			logging.Log("WARN", fmt.Sprintf("会话 %s 达到消息上限 (%d)", conv.ID, conv.MaxMessages))
			// 达到上限无法交接，保存完整回复
			db.SaveConversationStep(&db.ConvStep{
				ConversationID: conv.ID,
				Seq:            conv.TotalMessages,
				FromAgent:      agentID,
				ToAgent:        fromAgent,
				Message:        response,
				MessageType:    "agent_response",
				Timestamp:      time.Now().UnixMilli(),
			})
		} else {
			// 没有 @mention 交接，保存完整回复
			db.SaveConversationStep(&db.ConvStep{
				ConversationID: conv.ID,
				Seq:            conv.TotalMessages,
				FromAgent:      agentID,
				ToAgent:        fromAgent,
				Message:        response,
				MessageType:    "agent_response",
				Timestamp:      time.Now().UnixMilli(),
			})
		}

		// 移除当前 agent 的 pending 状态（计数自动 -1）
		RemovePendingAgent(conv, agentID)
		PersistConversation(conv) // -pending 后持久化

		pending := PendingCount(conv)

		if hadPendingHint && len(teammateMentions) == 0 && pending > 0 {
			// agent 收到"还有人在处理"提示后发了无标签回复
			// 仍然发射 chain_step_done 让前端展示回复内容，同时发 chain_step_waiting 通知等待状态
			pendingAgents := PendingAgentList(conv)
			logging.Log("DEBUG", fmt.Sprintf("会话 %s: agent %s 等待队友完成，pending=%d agents=%v",
				conv.ID, agentID, pending, pendingAgents))

			// 先发 chain_step_done（让前端显示回复内容）
			if isInternal {
				logging.EmitEvent("chain_step_done", stepDoneData)
				if conv.ID != "" {
					db.SaveConversationEvent(conv.ID, "chain_step_done", stepDoneData)
				}
			}

			// 再发 chain_step_waiting（通知前端等待状态）
			logging.EmitEvent("chain_step_waiting", map[string]interface{}{
				"conversationId": conv.ID,
				"agentId":        agentID,
				"waitingFor":     pendingAgents,
				"pending":        pending,
			})
			return
		}

		// 内部消息的 chain_step_done 延后到此处发射（确认不被丢弃后再发）
		if isInternal {
			logging.EmitEvent("chain_step_done", stepDoneData)
			if conv.ID != "" {
				db.SaveConversationEvent(conv.ID, "chain_step_done", stepDoneData)
			}
		}

		if pending == 0 {
			// 检查是否需要 Leader 做最终总结
			needsLeaderSummary := false
			leaderID := ""
			if conv.TeamContext != nil && conv.TeamContext.Team != nil {
				leaderID = conv.TeamContext.Team.LeaderAgent
			}
			if leaderID != "" && agentID != leaderID && !conv.LeaderSummaryDone {
				needsLeaderSummary = true
			}

			if needsLeaderSummary {
				// 所有队友已完成，但最后回复的不是 Leader → 让 Leader 做总结
				conv.LeaderSummaryDone = true
				AddPendingAgent(conv, leaderID)
				PersistConversation(conv)

				// 收集所有队友的工作摘要
				var summaryParts []string
				for _, step := range conv.Responses {
					if step.AgentID != leaderID {
						preview := step.Response
						if len([]rune(preview)) > 500 {
							preview = string([]rune(preview)[:500]) + "..."
						}
						summaryParts = append(summaryParts, fmt.Sprintf("[@%s 的工作结果]:\n%s", step.AgentID, preview))
					}
				}

				summaryMsg := fmt.Sprintf("[系统提示] 所有队友已完成工作。请根据以下各成员的工作成果，对用户做一个最终总结汇报。如果发现有队友提出需要修复的问题且尚未解决，请指派对应的队友进行修复。\n\n原始需求: %s\n\n%s",
					conv.OriginalMessage,
					strings.Join(summaryParts, "\n\n------\n\n"))

				logging.Log("INFO", fmt.Sprintf("📋 触发 Leader(%s) 做最终总结", leaderID))
				EnqueueInternalMessage(conv.ID, "system", leaderID, summaryMsg,
					conv.Channel, conv.Sender, "", conv.MessageID, projectID)
			} else {
				CompleteConversation(conv)
			}
		} else {
			logging.Log("INFO", fmt.Sprintf("会话 %s: %d 个分支仍在处理 %v",
				conv.ID, pending, PendingAgentList(conv)))
		}
	})

	db.CompleteMessage(dbMsg.ID)
}

// StartProcessor 启动队列处理器
func StartProcessor() {
	// 初始化数据库
	if err := db.InitQueueDb(config.QueenBeeHome); err != nil {
		logging.Log("ERROR", fmt.Sprintf("数据库初始化失败: %v", err))
		return
	}

	// 恢复陈旧消息
	recovered := db.RecoverStaleMessages()
	if recovered > 0 {
		logging.Log("INFO", fmt.Sprintf("恢复 %d 条陈旧消息", recovered))
	}

	// 启动时恢复活跃会话
	restoredConvs := RestoreConversations()
	if restoredConvs > 0 {
		logging.Log("INFO", fmt.Sprintf("恢复 %d 个活跃会话", restoredConvs))
	}

	// 打印配置
	logAgentConfig()

	logging.Log("INFO", "队列处理器已启动 (SQLite)")

	// 初始化 Memory 模块
	if err := memory.Init(db.GetDb()); err != nil {
		logging.Log("WARN", fmt.Sprintf("Memory 模块初始化失败: %v", err))
	}
	// 对齐原版：processor_start 包含 agents 和 teams（原版 queue-processor.ts:375）
	settings := config.GetSettings()
	startAgents := config.GetAgents(settings)
	startTeams := config.GetTeams(settings)
	agentKeys := make([]string, 0, len(startAgents))
	for k := range startAgents {
		agentKeys = append(agentKeys, k)
	}
	teamKeys := make([]string, 0, len(startTeams))
	for k := range startTeams {
		teamKeys = append(teamKeys, k)
	}
	logging.EmitEvent("processor_start", map[string]interface{}{"agents": agentKeys, "teams": teamKeys})

	// 启动时确保所有配置的 agent 目录都已初始化模板文件
	// 对每个 agent 调用 EnsureAgentDirectory，它会自动处理：
	//   - 首次初始化（创建 AGENTS.md, SOUL.md 等）
	//   - 已存在的目录（只更新 AGENTS.md，不覆盖 SOUL.md 等个性化文件）
	for agentID, agent := range startAgents {
		agentDir := agent.WorkingDirectory
		if agentDir == "" {
			home, _ := os.UserHomeDir()
			workspacePath := filepath.Join(home, "queenbee-workspace")
			if settings.Workspace != nil && settings.Workspace.Path != "" {
				workspacePath = settings.Workspace.Path
			}
			agentDir = filepath.Join(workspacePath, agentID)
		}
		EnsureAgentDirectory(agentDir)
	}

	// 事件驱动处理
	go func() {
		for range db.MessageEnqueued {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.Log("ERROR", fmt.Sprintf("ProcessQueue panic (event): %v", r))
					}
				}()
				ProcessQueue()
			}()
		}
	}()

	// 轮询兖底：防止 MessageEnqueued 信号丢失导致消息卡在 pending
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.Log("ERROR", fmt.Sprintf("ProcessQueue panic (poll): %v", r))
					}
				}()
				ProcessQueue()
			}()
		}
	}()

	// 定时维护
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.Log("ERROR", fmt.Sprintf("RecoverStaleMessages panic: %v", r))
					}
				}()
				count := db.RecoverStaleMessages()
				if count > 0 {
					logging.Log("INFO", fmt.Sprintf("恢复 %d 条陈旧消息", count))
				}
			}()
		}
	}()

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.Log("ERROR", fmt.Sprintf("CleanupStale panic: %v", r))
					}
				}()
				s := config.GetSettings()
				ttlMin := 15
				if s.ConversationTimeout > 0 {
					ttlMin = s.ConversationTimeout
				}
				Conversations.CleanupStale(int64(ttlMin) * 60 * 1000)
			}()
		}
	}()

	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.Log("ERROR", fmt.Sprintf("Prune panic: %v", r))
					}
				}()
				pruned := db.PruneAckedResponses()
				if pruned > 0 {
					logging.Log("INFO", fmt.Sprintf("清理 %d 条已确认响应", pruned))
				}
				pruned = db.PruneCompletedMessages()
				if pruned > 0 {
					logging.Log("INFO", fmt.Sprintf("清理 %d 条已完成消息", pruned))
				}
			}()
		}
	}()
}

func logAgentConfig() {
	settings := config.GetSettings()
	agents := config.GetAgents(settings)
	teams := config.GetTeams(settings)

	logging.Log("INFO", fmt.Sprintf("已加载 %d 个 agent:", len(agents)))
	for id, agent := range agents {
		logging.Log("INFO", fmt.Sprintf("  %s: %s [%s/%s] cwd=%s", id, agent.Name, agent.Provider, agent.Model, agent.WorkingDirectory))
	}

	if len(teams) > 0 {
		logging.Log("INFO", fmt.Sprintf("已加载 %d 个 team:", len(teams)))
		for id, team := range teams {
			logging.Log("INFO", fmt.Sprintf("  %s: %s [agents: %s] leader=%s", id, team.Name, strings.Join(team.Agents, ", "), team.LeaderAgent))
		}
	}
}

// --- 辅助函数 ---

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func mapKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func nullToString(ns db.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// isAckMessage 检测消息是否为确认/感谢类短回复（用于循环检测）
// 只有当消息很短（< 200 字符）且包含确认关键词时才认为是"确认类"
// 工作汇报（如 "bug 已修复，代码如下..."）通常远超 200 字符，不会被误判
func isAckMessage(msg string) bool {
	clean := strings.TrimSpace(msg)
	// 超过 200 字符的消息大概率是工作汇报而非简单确认
	if len([]rune(clean)) > 200 {
		return false
	}
	lower := strings.ToLower(clean)
	ackPatterns := []string{
		// 中文确认
		"收到", "好的", "了解", "明白", "感谢", "谢谢", "知道了",
		"没问题", "可以", "已收到",
		// 英文确认
		"got it", "thanks", "thank you", "understood", "acknowledged",
		"noted", "roger", "copy that", "sounds good", "perfect",
	}
	for _, pat := range ackPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}
