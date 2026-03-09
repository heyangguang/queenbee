package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// ─── 类型定义 ────────────────────────────────────────────────────────────────

type vizAgentStatus string

const (
	vizStatusIdle    vizAgentStatus = "idle"
	vizStatusActive  vizAgentStatus = "active"
	vizStatusDone    vizAgentStatus = "done"
	vizStatusError   vizAgentStatus = "error"
	vizStatusWaiting vizAgentStatus = "waiting"
)

type vizAgentState struct {
	ID             string
	Name           string
	Provider       string
	Model          string
	Status         vizAgentStatus
	LastActivity   string
	ResponseLength int
}

type vizChainArrow struct {
	From string
	To   string
}

type vizLogCategory string

const (
	vizCatSystem  vizLogCategory = "system"  // 系统事件（启动、连接）
	vizCatRoute   vizLogCategory = "route"   // 路由事件
	vizCatMessage vizLogCategory = "message" // 对话消息（核心）
	vizCatSession vizLogCategory = "session" // 会话管理
)

type vizLogEntry struct {
	Time     string
	Icon     string
	Text     string
	Color    lipgloss.Color
	Category vizLogCategory
}

// ─── 配置加载 ────────────────────────────────────────────────────────────────

type vizTeamConfig struct {
	Name        string   `json:"name"`
	Agents      []string `json:"agents"`
	LeaderAgent string   `json:"leader_agent"`
}

type vizAgentConfig struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type vizSettingsJSON struct {
	Agents map[string]vizAgentConfig `json:"agents"`
	Teams  map[string]vizTeamConfig  `json:"teams"`
}

func vizLoadSettings() vizSettingsJSON {
	// 从 API 获取配置（纯数据库驱动，不再读 settings.json）
	resp, err := http.Get("http://localhost:3777/api/settings")
	if err != nil {
		return vizSettingsJSON{}
	}
	defer resp.Body.Close()
	var s vizSettingsJSON
	if json.NewDecoder(resp.Body).Decode(&s) != nil {
		return vizSettingsJSON{}
	}
	return s
}

// ─── 样式定义 ────────────────────────────────────────────────────────────────

var (
	vizClrMagenta = lipgloss.Color("#FF00FF")
	vizClrCyan    = lipgloss.Color("#00FFFF")
	vizClrGreen   = lipgloss.Color("#00FF00")
	vizClrYellow  = lipgloss.Color("#FFFF00")
	vizClrRed     = lipgloss.Color("#FF0000")
	vizClrGray    = lipgloss.Color("#666666")
	vizClrWhite   = lipgloss.Color("#FFFFFF")

	vizTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(vizClrMagenta)
	vizBoldWhite  = lipgloss.NewStyle().Bold(true).Foreground(vizClrWhite)
	vizDimStyle   = lipgloss.NewStyle().Foreground(vizClrGray)
	vizCyanStyle  = lipgloss.NewStyle().Foreground(vizClrCyan).Bold(true)
	vizGreenStyle = lipgloss.NewStyle().Foreground(vizClrGreen)
	vizRedStyle   = lipgloss.NewStyle().Foreground(vizClrRed)

	vizCardIdle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(vizClrGray).
			Padding(0, 1).Width(28)
	vizCardActive = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(vizClrCyan).
			Padding(0, 1).Width(28)
	vizCardDone = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(vizClrGreen).
			Padding(0, 1).Width(28)
	vizCardError = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(vizClrRed).
			Padding(0, 1).Width(28)
)

var vizStatusIcon = map[vizAgentStatus]string{
	vizStatusIdle:    "○",
	vizStatusActive:  "●",
	vizStatusDone:    "✓",
	vizStatusError:   "✗",
	vizStatusWaiting: "◔",
}

// ─── Bubbletea Model ─────────────────────────────────────────────────────────

type vizModel struct {
	settings       vizSettingsJSON
	filterTeamID   string
	apiPort        int
	agents         map[string]*vizAgentState
	agentOrder     []string
	arrows         []vizChainArrow
	logs           []vizLogEntry
	logOffset      int  // 滚动偏移
	autoScroll     bool // 自动滚动到最新
	totalProcessed int
	processorAlive bool
	startTime      time.Time
	tick           int
	quitting       bool
	width          int
	height         int
	pendingReset   bool // response_ready 后等待重置 done 状态
	resetCountdown int  // 倒计时，每个 tick 500ms，6 = 3秒
}

type vizSSEMsg struct{ Events []map[string]interface{} }
type vizSSEConnected struct{}
type vizSSEDisconnected struct{}
type vizTickMsg time.Time
type vizQueueStatusMsg struct{ completed int }

// 全局 SSE channel
var vizSSEChan = make(chan map[string]interface{}, 100)

func vizInitialModel(filterTeam string, port int) vizModel {
	settings := vizLoadSettings()
	agents := make(map[string]*vizAgentState)
	var agentOrder []string

	var agentIDs []string
	if filterTeam != "" {
		if team, ok := settings.Teams[filterTeam]; ok {
			agentIDs = team.Agents
		}
	}
	if len(agentIDs) == 0 {
		seen := map[string]bool{}
		for _, team := range settings.Teams {
			for _, aid := range team.Agents {
				if !seen[aid] {
					agentIDs = append(agentIDs, aid)
					seen[aid] = true
				}
			}
		}
		if len(agentIDs) == 0 {
			for id := range settings.Agents {
				agentIDs = append(agentIDs, id)
			}
		}
	}

	for _, id := range agentIDs {
		if agent, ok := settings.Agents[id]; ok {
			provider := agent.Provider
			if provider == "" {
				provider = "anthropic"
			}
			m := agent.Model
			if m == "" {
				m = "sonnet"
			}
			agents[id] = &vizAgentState{
				ID: id, Name: agent.Name, Provider: provider, Model: m, Status: vizStatusIdle,
			}
			agentOrder = append(agentOrder, id)
		}
	}

	return vizModel{
		settings: settings, filterTeamID: filterTeam, apiPort: port,
		agents: agents, agentOrder: agentOrder,
		logs: []vizLogEntry{}, startTime: time.Now(), autoScroll: true,
	}
}

func (m vizModel) Init() tea.Cmd {
	return tea.Batch(vizConnectSSE(m.apiPort), vizTickCmd())
}

func vizTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return vizTickMsg(t) })
}

func vizConnectSSE(port int) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://localhost:%d/api/events/stream", port)
		resp, err := http.Get(url)
		if err != nil {
			time.Sleep(3 * time.Second)
			return vizSSEDisconnected{}
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			time.Sleep(3 * time.Second)
			return vizSSEDisconnected{}
		}
		go func() {
			defer resp.Body.Close()
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					var event map[string]interface{}
					if json.Unmarshal([]byte(line[6:]), &event) == nil {
						vizSSEChan <- event
					}
				}
			}
		}()
		return vizSSEConnected{}
	}
}

func vizPollSSE() tea.Cmd {
	return func() tea.Msg {
		// 等第一个事件或超时
		select {
		case first := <-vizSSEChan:
			// 批量取出所有堆积的事件，避免 chain_step_start 延迟处理
			events := []map[string]interface{}{first}
			for {
				select {
				case e := <-vizSSEChan:
					events = append(events, e)
				default:
					return vizSSEMsg{Events: events}
				}
			}
		case <-time.After(100 * time.Millisecond):
			return nil
		}
	}
}

// vizFetchQueueStatus 查询队列状态，更新已处理数量
func vizFetchQueueStatus(port int) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://localhost:%d/api/queue/status", port)
		resp, err := http.Get(url)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		var status struct {
			Completed int `json:"completed"`
		}
		if json.NewDecoder(resp.Body).Decode(&status) == nil {
			return vizQueueStatusMsg{completed: status.Completed}
		}
		return nil
	}
}

// vizSessionsMsg 会话状态消息
type vizSessionsMsg struct {
	PendingAgents []string // 当前所有活跃会话里的 pending agent 集合
}

// vizFetchSessions 查询会话状态，获取 pending agents
func vizFetchSessions(port int) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://localhost:%d/api/sessions", port)
		resp, err := http.Get(url)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		var result struct {
			Sessions []struct {
				PendingAgents []string `json:"pending_agents"`
			} `json:"sessions"`
		}
		if json.NewDecoder(resp.Body).Decode(&result) != nil {
			return nil
		}
		// 汇总所有 pending agents
		seen := map[string]bool{}
		var all []string
		for _, s := range result.Sessions {
			for _, a := range s.PendingAgents {
				if !seen[a] {
					seen[a] = true
					all = append(all, a)
				}
			}
		}
		return vizSessionsMsg{PendingAgents: all}
	}
}

func (m vizModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "Q" || msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		switch msg.String() {
		case "up", "k":
			m.autoScroll = false
			if m.logOffset > 0 {
				m.logOffset--
			}
		case "down", "j":
			maxOffset := m.logTotalLines() - m.logVisibleCount()
			if maxOffset < 0 {
				maxOffset = 0
			}
			if m.logOffset < maxOffset {
				m.logOffset++
			}
			if m.logOffset >= maxOffset {
				m.autoScroll = true
			}
		case "G", "end":
			m.autoScroll = true
			maxOffset := m.logTotalLines() - m.logVisibleCount()
			if maxOffset < 0 {
				maxOffset = 0
			}
			m.logOffset = maxOffset
		case "g", "home":
			m.autoScroll = false
			m.logOffset = 0
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case vizTickMsg:
		m.tick++
		// Bug1 修复：用倒计时替代 goroutine
		if m.pendingReset {
			m.resetCountdown = 6 // 6 * 500ms = 3秒
			m.pendingReset = false
		}
		if m.resetCountdown > 0 {
			m.resetCountdown--
			if m.resetCountdown == 0 {
				// 安全地在 Update 中重置
				for _, a := range m.agents {
					if a.Status == vizStatusDone || a.Status == vizStatusError {
						a.Status = vizStatusIdle
						a.LastActivity = vizTimeAgo(time.Now())
					}
				}
			}
		}
		return m, tea.Batch(vizTickCmd(), vizPollSSE(), vizFetchQueueStatus(m.apiPort), vizFetchSessions(m.apiPort))
	case vizQueueStatusMsg:
		m.totalProcessed = msg.completed
	case vizSessionsMsg:
		// Bug2 修复：pending 中的设 waiting，不在 pending 且当前是 waiting 的恢复 idle
		pendingSet := map[string]bool{}
		for _, id := range msg.PendingAgents {
			pendingSet[id] = true
		}
		for id, a := range m.agents {
			if pendingSet[id] {
				if a.Status == vizStatusIdle || a.Status == vizStatusDone {
					a.Status = vizStatusWaiting
				}
			} else if a.Status == vizStatusWaiting {
				// 不在 pending 列表了，恢复为 idle
				a.Status = vizStatusIdle
			}
		}
	case vizSSEConnected:
		m.processorAlive = true
		m.vizAddLog("⚡", "队列处理器已连接", vizClrGreen)
		m.settings = vizLoadSettings()
		return m, vizPollSSE()
	case vizSSEDisconnected:
		m.processorAlive = false
		return m, vizConnectSSE(m.apiPort)
	case vizSSEMsg:
		for _, event := range msg.Events {
			m.vizHandleEvent(event)
		}
		return m, vizPollSSE()
	}
	return m, nil
}

func (m *vizModel) vizHandleEvent(event map[string]interface{}) {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "processor_start":
		m.processorAlive = true
		m.vizAddLog("⚡", "队列处理器已启动", vizClrGreen)
		m.settings = vizLoadSettings()
	case "message_received":
		ch, _ := event["channel"].(string)
		sender, _ := event["sender"].(string)
		message, _ := event["message"].(string)
		m.vizAddLogCat(vizCatMessage, "✉", fmt.Sprintf("[%s] %s: %s", ch, sender, message), vizClrWhite)
	case "agent_routed":
		agentID, _ := event["agentId"].(string)
		isTeam, _ := event["isTeamRouted"].(bool)
		if a, ok := m.agents[agentID]; ok {
			a.Status = vizStatusActive
			a.LastActivity = "路由中..."
		}
		if isTeam {
			m.vizAddLog("⚑", fmt.Sprintf("路由到团队 @%s", agentID), vizClrCyan)
		}
	case "team_chain_start":
		teamName, _ := event["teamName"].(string)
		agents, _ := event["agents"].([]interface{})
		var list []string
		for _, a := range agents {
			if s, ok := a.(string); ok {
				list = append(list, "@"+s)
			}
		}
		m.vizAddLogCat(vizCatSession, "⛓", fmt.Sprintf("会话开始: %s [%s]", teamName, strings.Join(list, ", ")), vizClrMagenta)
		m.arrows = nil
	case "chain_step_start":
		agentID, _ := event["agentId"].(string)
		fromAgent, _ := event["fromAgent"].(string)
		if a, ok := m.agents[agentID]; ok {
			a.Status = vizStatusActive
			if fromAgent != "" {
				a.LastActivity = fmt.Sprintf("来自 @%s", fromAgent)
			} else {
				a.LastActivity = "处理中"
			}
		}
	case "chain_step_done":
		agentID, _ := event["agentId"].(string)
		respLen, _ := event["responseLength"].(float64)
		respText, _ := event["responseText"].(string)
		if a, ok := m.agents[agentID]; ok {
			a.Status = vizStatusIdle
			a.ResponseLength = int(respLen)
			a.LastActivity = vizTimeAgo(time.Now())
		}
		// 只显示第一行摘要，详细内容在 chats/ 文件里
		summary := vizFirstLine(strings.TrimSpace(respText))
		if summary == "" {
			summary = fmt.Sprintf("(%d 字符)", int(respLen))
		} else if int(respLen) > 0 {
			summary = fmt.Sprintf("%s … (%d 字符)", summary, int(respLen))
		}
		m.vizAddLogCat(vizCatMessage, "💬", fmt.Sprintf("@%s 回复: %s", agentID, summary), vizClrWhite)
	case "chain_handoff":
		from, _ := event["fromAgent"].(string)
		to, _ := event["toAgent"].(string)
		m.arrows = append(m.arrows, vizChainArrow{From: from, To: to})
		// Bug3 修复：只在 idle/done 时设为 waiting，不覆盖 active
		if a, ok := m.agents[to]; ok {
			if a.Status == vizStatusIdle || a.Status == vizStatusDone {
				a.Status = vizStatusWaiting
				a.LastActivity = fmt.Sprintf("来自 @%s", from)
			}
		}
	case "team_chain_end":
		agents, _ := event["agents"].([]interface{})
		var list []string
		for _, a := range agents {
			if s, ok := a.(string); ok {
				list = append(list, "@"+s)
				// 会话结束，直接回 idle
				if ag, ok2 := m.agents[s]; ok2 {
					ag.Status = vizStatusIdle
					ag.LastActivity = vizTimeAgo(time.Now())
				}
			}
		}
		m.vizAddLogCat(vizCatSession, "✔", fmt.Sprintf("会话完成 [%s]", strings.Join(list, ", ")), vizClrGreen)
	case "response_ready":
		m.totalProcessed++
		// Bug1 修复：不再用 goroutine，改用 tea.Cmd 延迟 3 秒后发消息
		m.pendingReset = true
	}
}

func (m *vizModel) vizAddLog(icon, text string, color lipgloss.Color) {
	m.vizAddLogCat(vizCatSystem, icon, text, color)
}

func (m *vizModel) vizAddLogCat(cat vizLogCategory, icon, text string, color lipgloss.Color) {
	m.logs = append(m.logs, vizLogEntry{
		Time: time.Now().Format("15:04:05"), Icon: icon, Text: text, Color: color, Category: cat,
	})
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
	// 自动滚动到底部（按实际行数）
	if m.autoScroll {
		maxOff := m.logTotalLines() - m.logVisibleCount()
		if maxOff < 0 {
			maxOff = 0
		}
		m.logOffset = maxOff
	}
}

// ─── 渲染 ────────────────────────────────────────────────────────────────────

func (m vizModel) View() string {
	if m.quitting {
		return ""
	}
	tw := m.termWidth()
	var b strings.Builder

	// 顶部：header + cards
	headerStr := m.vizRenderHeader()
	cardsStr := m.vizRenderCards()

	b.WriteString(headerStr)
	b.WriteString("\n")
	b.WriteString(cardsStr)
	b.WriteString("\n")

	// 计算 header+cards 实际占了多少行
	topLines := strings.Count(headerStr, "\n") + 1 + strings.Count(cardsStr, "\n") + 2 // +2 for gaps

	// 下方：左右分区
	rightContent := tw * 25 / 100
	if rightContent < 18 {
		rightContent = 18
	}
	rightTotal := rightContent + 2 + 2
	leftW := tw - rightTotal - 1

	// 面板高度 = 终端高度 - 实际顶部行数 - status(1) - gaps(1)
	panelH := m.height - topLines - 2
	if panelH < 6 {
		panelH = 6
	}

	leftPanel := m.vizRenderLogPanel(leftW)
	rightPanel := m.vizRenderRoutePanel(rightContent)

	leftStyle := lipgloss.NewStyle().
		Width(leftW).MaxWidth(leftW).
		Height(panelH)

	rightStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(vizClrGray).
		Width(rightContent).MaxWidth(rightContent+2).
		Height(panelH-2).MaxHeight(panelH).
		Padding(0, 1)

	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftStyle.Render(leftPanel), rightStyle.Render(rightPanel)))
	b.WriteString("\n")
	b.WriteString(m.vizRenderStatus())

	// 严格截断：确保总输出不超过终端高度（像 top 命令一样）
	output := b.String()
	lines := strings.Split(output, "\n")
	maxLines := m.height
	if maxLines <= 0 {
		maxLines = 40
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

// vizRenderRoutePanel 右侧对话路由面板
func (m vizModel) vizRenderRoutePanel(w int) string {
	header := vizBoldWhite.Render("📡 对话路由")

	if len(m.arrows) == 0 {
		return header + "\n" + vizDimStyle.Render("等待消息...")
	}

	var lines []string
	for i, a := range m.arrows {
		num := lipgloss.NewStyle().Foreground(vizClrGray).Render(fmt.Sprintf("%d.", i+1))
		from := vizCyanStyle.Render("@" + a.From)
		arrow := lipgloss.NewStyle().Foreground(vizClrYellow).Render(" → ")
		to := lipgloss.NewStyle().Foreground(vizClrMagenta).Bold(true).Render("@" + a.To)
		lines = append(lines, num+" "+from+arrow+to)
	}

	// 截断超限内容
	maxW := w - 2
	if maxW < 10 {
		maxW = 10
	}
	for i, line := range lines {
		runeL := []rune(line)
		if len(runeL) > maxW {
			lines[i] = string(runeL[:maxW-1]) + "…"
		}
	}

	return header + "\n" + vizDimStyle.Render(strings.Repeat("─", w)) + "\n" + strings.Join(lines, "\n")
}

// vizRenderLogPanel 左侧活动日志面板
func (m vizModel) vizRenderLogPanel(w int) string {
	visCount := m.logVisibleCount()

	scrollHint := ""
	if len(m.logs) > visCount {
		if m.autoScroll {
			scrollHint = vizDimStyle.Render(" (↑/k 滚动)")
		} else {
			scrollHint = lipgloss.NewStyle().Foreground(vizClrYellow).Render(fmt.Sprintf(" (%d/%d G↓)", m.logOffset+visCount, len(m.logs)))
		}
	}
	header := vizBoldWhite.Render("☰ 活动") + scrollHint + "\n" + vizDimStyle.Render(strings.Repeat("─", w))

	if len(m.logs) == 0 {
		return header + "\n" + vizDimStyle.Render("  等待事件...（发送消息到团队触发）")
	}

	start := m.logOffset
	end := start + visCount
	if end > len(m.logs) {
		end = len(m.logs)
	}
	if start >= end {
		start = 0
		if end < start {
			end = len(m.logs)
		}
	}
	visible := m.logs[start:end]

	var lines []string
	for _, e := range visible {
		timeStr := vizDimStyle.Render(e.Time + " ")
		switch e.Category {
		case vizCatMessage:
			lines = append(lines, timeStr+e.Icon+" "+
				lipgloss.NewStyle().Foreground(e.Color).Bold(true).Render(e.Text))
		case vizCatRoute:
			lines = append(lines, timeStr+e.Icon+" "+
				lipgloss.NewStyle().Foreground(vizClrYellow).Render(e.Text))
		case vizCatSession:
			lines = append(lines, timeStr+e.Icon+" "+
				lipgloss.NewStyle().Foreground(vizClrMagenta).Render(e.Text))
		default:
			lines = append(lines, timeStr+e.Icon+" "+
				vizDimStyle.Render(e.Text))
		}
	}
	return header + "\n" + strings.Join(lines, "\n")
}

func (m vizModel) vizRenderHeader() string {
	uptime := vizTimeAgo(m.startTime)
	teamInfo := lipgloss.NewStyle().Foreground(vizClrYellow).Render("all teams")
	if m.filterTeamID != "" {
		if team, ok := m.settings.Teams[m.filterTeamID]; ok {
			teamInfo = vizCyanStyle.Render("@"+m.filterTeamID) + vizDimStyle.Render(" ("+team.Name+")")
		}
	}
	lineW := m.termWidth()
	return vizTitleStyle.Render("  ✦ ") + vizBoldWhite.Render("QueenBee Team Visualizer") +
		vizDimStyle.Render(" │ ") + teamInfo + vizDimStyle.Render(" │ ") + vizDimStyle.Render("up "+uptime) +
		"\n" + vizDimStyle.Render(strings.Repeat("─", lineW))
}

func (m vizModel) vizRenderCards() string {
	// 根据终端宽度和 Agent 数量计算卡片宽度
	n := len(m.agentOrder)
	if n == 0 {
		return ""
	}
	tw := m.termWidth()
	cardW := (tw - n*2) / n // 减去边框占用
	if cardW < 20 {
		cardW = 20
	}
	if cardW > 40 {
		cardW = 40
	}

	// 动态创建卡片样式
	makeStyle := func(borderColor lipgloss.Color) lipgloss.Style {
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Padding(0, 1).Width(cardW)
	}

	var cards []string
	for _, id := range m.agentOrder {
		agent, ok := m.agents[id]
		if !ok {
			continue
		}
		var style lipgloss.Style
		var iconColor lipgloss.Style
		switch agent.Status {
		case vizStatusActive:
			style = makeStyle(vizClrCyan)
			iconColor = lipgloss.NewStyle().Foreground(vizClrCyan)
		case vizStatusDone:
			style = makeStyle(vizClrGreen)
			iconColor = lipgloss.NewStyle().Foreground(vizClrGreen)
		case vizStatusError:
			style = makeStyle(vizClrRed)
			iconColor = lipgloss.NewStyle().Foreground(vizClrRed)
		case vizStatusWaiting:
			style = makeStyle(vizClrYellow)
			iconColor = lipgloss.NewStyle().Foreground(vizClrYellow)
		default:
			style = makeStyle(vizClrGray)
			iconColor = lipgloss.NewStyle().Foreground(vizClrGray)
		}
		isLeader := false
		for _, team := range m.settings.Teams {
			if team.LeaderAgent == id {
				isLeader = true
				break
			}
		}
		nameLine := iconColor.Render(vizStatusIcon[agent.Status]+" ") + vizBoldWhite.Render("@"+id)
		if isLeader {
			nameLine += lipgloss.NewStyle().Foreground(vizClrYellow).Render(" ★")
		}
		var statusLine string
		switch agent.Status {
		case vizStatusActive:
			dots := strings.Repeat(".", (m.tick/2)%4)
			statusLine = lipgloss.NewStyle().Foreground(vizClrCyan).Render("▸ 处理中" + dots)
		case vizStatusDone:
			statusLine = vizGreenStyle.Render(fmt.Sprintf("✓ 完成 (%d 字符)", agent.ResponseLength))
		case vizStatusError:
			statusLine = vizRedStyle.Render("✗ 错误")
		default:
			act := agent.LastActivity
			if act == "" {
				act = "空闲"
			}
			statusLine = vizDimStyle.Render(act)
		}
		content := nameLine + "\n" + vizDimStyle.Render(agent.Name) + "\n" +
			vizDimStyle.Render(agent.Provider+"/"+agent.Model) + "\n" + statusLine
		cards = append(cards, style.Render(content))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cards...)
}

// termWidth 获取终端宽度（默认 100）
func (m vizModel) termWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 100
}

func (m vizModel) vizRenderFlow() string {
	var parts []string
	for i, a := range m.arrows {
		part := vizCyanStyle.Render("@"+a.From) +
			lipgloss.NewStyle().Foreground(vizClrYellow).Render(" → ") +
			lipgloss.NewStyle().Foreground(vizClrMagenta).Bold(true).Render("@"+a.To)
		if i < len(m.arrows)-1 {
			part += vizDimStyle.Render(" │")
		}
		parts = append(parts, part)
	}
	return vizBoldWhite.Render("⇀ 消息流") + "\n" + strings.Join(parts, " ")
}

func (m vizModel) vizRenderTeams() string {
	var lines []string
	for id, team := range m.settings.Teams {
		var list []string
		for _, a := range team.Agents {
			list = append(list, "@"+a)
		}
		lines = append(lines, "  "+vizCyanStyle.Render("@"+id)+
			vizDimStyle.Render(" "+team.Name+" ["+strings.Join(list, ", ")+"]")+
			lipgloss.NewStyle().Foreground(vizClrYellow).Render(" ★ @"+team.LeaderAgent))
	}
	return vizBoldWhite.Render("≣ 团队") + "\n" + strings.Join(lines, "\n")
}

func (m vizModel) vizRenderLog() string {
	lineW := m.termWidth()
	visCount := m.logVisibleCount()

	// 先将所有 entry 展开成实际渲染行（支持多行消息文本）
	type renderedLine struct {
		text string
	}
	var allLines []renderedLine
	for _, e := range m.logs {
		timeStr := vizDimStyle.Render(e.Time + " ")
		// 将 text 按换行拆分成多行
		parts := strings.Split(strings.TrimRight(e.Text, "\n"), "\n")
		for i, part := range parts {
			var rendered string
			prefix := ""
			if i == 0 {
				prefix = timeStr + e.Icon + " "
			} else {
				// 续行缩进对齐
				prefix = strings.Repeat(" ", len(e.Time)+2)
			}
			switch e.Category {
			case vizCatMessage:
				rendered = prefix + lipgloss.NewStyle().Foreground(e.Color).Bold(i == 0).Render(part)
			case vizCatRoute:
				rendered = prefix + lipgloss.NewStyle().Foreground(vizClrYellow).Render(part)
			case vizCatSession:
				rendered = prefix + lipgloss.NewStyle().Foreground(vizClrMagenta).Render(part)
			default:
				rendered = prefix + vizDimStyle.Render(part)
			}
			allLines = append(allLines, renderedLine{text: rendered})
		}
	}

	totalLines := len(allLines)

	scrollHint := ""
	if totalLines > visCount {
		if m.autoScroll {
			scrollHint = vizDimStyle.Render(" (↑/k 向上滚动)")
		} else {
			scrollHint = lipgloss.NewStyle().Foreground(vizClrYellow).Render(
				fmt.Sprintf(" (%d/%d  G 到底部)", m.logOffset+visCount, totalLines))
		}
	}
	header := vizBoldWhite.Render("☰ 活动") + scrollHint + "\n" + vizDimStyle.Render(strings.Repeat("─", lineW))

	if totalLines == 0 {
		return header + "\n" + vizDimStyle.Render("  等待事件...（发送消息到团队触发）")
	}

	// 按行滚动
	start := m.logOffset
	if start > totalLines-visCount {
		start = totalLines - visCount
	}
	if start < 0 {
		start = 0
	}
	end := start + visCount
	if end > totalLines {
		end = totalLines
	}

	visible := allLines[start:end]
	var out []string
	for _, l := range visible {
		out = append(out, l.text)
	}
	return header + "\n" + strings.Join(out, "\n")
}

// logVisibleCount 根据终端高度计算可见日志行数
func (m vizModel) logVisibleCount() int {
	// 预留：header(2) + cards(5) + flow(2) + team(2) + log_header(2) + status(2) = ~15
	if m.height > 20 {
		return m.height - 15
	}
	return 12
}

// logTotalLines 计算所有 entry 展开成实际行后的总行数
func (m vizModel) logTotalLines() int {
	total := 0
	for _, e := range m.logs {
		total += len(strings.Split(strings.TrimRight(e.Text, "\n"), "\n"))
	}
	return total
}

func (m vizModel) vizRenderStatus() string {
	var parts []string
	if m.processorAlive {
		parts = append(parts, vizGreenStyle.Render("● 处理器在线"))
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(vizClrYellow).Render("○ 处理器离线"))
	}
	parts = append(parts, vizBoldWhite.Render("已处理: ")+vizCyanStyle.Render(fmt.Sprintf("%d", m.totalProcessed)))
	parts = append(parts, vizDimStyle.Render("q 退出"))
	return vizDimStyle.Render(strings.Repeat("─", m.termWidth())) + "\n" + strings.Join(parts, vizDimStyle.Render(" │ "))
}

// ─── 辅助函数 ────────────────────────────────────────────────────────────────

func vizTruncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// vizFirstLine 提取第一行有意义的内容作为消息预览
func vizFirstLine(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "```") && !strings.HasPrefix(line, "---") {
			runes := []rune(line)
			if len(runes) > 100 {
				return string(runes[:100]) + "…"
			}
			return line
		}
	}
	return ""
}

func vizTimeAgo(t time.Time) string {
	diff := int(time.Since(t).Seconds())
	if diff < 5 {
		return "刚刚"
	}
	if diff < 60 {
		return fmt.Sprintf("%ds", diff)
	}
	if diff < 3600 {
		return fmt.Sprintf("%dm", diff/60)
	}
	return fmt.Sprintf("%dh", diff/3600)
}

// ─── Cobra 命令 ──────────────────────────────────────────────────────────────

var visualizeCmd = &cobra.Command{
	Use:     "visualize",
	Aliases: []string{"viz", "dashboard"},
	Short:   "实时 TUI 仪表盘 — 监控团队协作",
	Long:    "启动终端仪表盘，通过 SSE 实时显示 Agent 状态、消息流和活动日志。",
	Run: func(cmd *cobra.Command, args []string) {
		filterTeam, _ := cmd.Flags().GetString("team")
		apiPort, _ := cmd.Flags().GetInt("port")

		if envPort := os.Getenv("QUEENBEE_API_PORT"); envPort != "" {
			fmt.Sscanf(envPort, "%d", &apiPort)
		}

		p := tea.NewProgram(
			vizInitialModel(filterTeam, apiPort),
			tea.WithAltScreen(),
		)
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	visualizeCmd.Flags().StringP("team", "t", "", "过滤指定团队 ID")
	visualizeCmd.Flags().IntP("port", "p", 3777, "API 服务器端口")
	rootCmd.AddCommand(visualizeCmd)
}
