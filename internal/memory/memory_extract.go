package memory

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/queenbee-ai/queenbee/internal/logging"
)

// ═══════════════════════════════════════════════════════════════════
// Memory Extraction — 自动从 Agent 响应中提取记忆
//
// 在 Agent 完成对话后，使用 AI 分析响应内容，
// 提取值得长期记忆的关键信息（决策、偏好、事实等）。
// ═══════════════════════════════════════════════════════════════════

// extractionPrompt 用于提取记忆的系统提示词（支持多 scope）
const extractionPrompt = `You are a memory extraction assistant. Analyze the conversation and extract key facts worth remembering for future interactions.

Rules:
1. Extract ONLY important, reusable information (decisions, preferences, facts, learnings)
2. Skip trivial, one-time, or task-execution information (e.g. "task completed", "file created")
3. Each memory should be a single, self-contained sentence
4. Output one memory per line, prefixed with SCOPE, CATEGORY, and IMPORTANCE in brackets
5. Rate importance 1-5 (1=trivial, 5=critical). ONLY output memories with importance >= 3
6. Scopes (choose carefully!):
   - [project:category:N] — ANY knowledge about a project: tech stack, API design, database schema, code style rules, architecture decisions, deployment configs, etc. This is the MOST COMMON scope.
   - [user:category:N] — knowledge about the human user: their name, personal preferences, habits, communication style
   - [agent:category:N] — ONLY the agent's own behavioral learnings. This is RARE.
7. Categories: fact, preference, learning, decision
8. If nothing is worth remembering, output exactly: NONE
9. Maximum 5 memories per extraction
10. Write in the same language as the conversation
11. Do NOT include @mentions — these are chat routing markers, not knowledge
12. Do NOT store information about which agent does what role
13. Do NOT output meta-commentary like "Based on the conversation..." or "Here are the memories:"

CRITICAL: If the conversation discusses ANY technical details, those MUST be [project:*], NEVER [agent:*].

Example output:
[project:fact:4] The project uses Go 1.22 and SQLite for data storage
[project:decision:5] API responses should be paginated with a default limit of 20
[user:preference:3] The user prefers Chinese comments in code
[user:fact:4] The user's name is Zhang San`

// ExtractMemories 从 Agent 对话中自动提取记忆（支持 agent/project/user 三层）
// runUtilityPrompt: 通用 AI 调用函数（根据 provider 自动选择 CLI）
// 可选参数: projectID, senderID, projectName
func ExtractMemories(agentID, userMessage, agentResponse, convID string, runUtilityPrompt func(provider, prompt, systemPrompt string, timeoutMin int) (string, error), provider string, optionalArgs ...string) {
	// 只对足够长的对话提取记忆（太短的对话通常没有值得记忆的内容）
	totalLen := len([]rune(userMessage)) + len([]rune(agentResponse))
	if totalLen < 200 {
		return
	}

	// 解析可选参数
	projID := ""
	senderID := ""
	projectName := ""
	if len(optionalArgs) >= 1 {
		projID = optionalArgs[0]
	}
	if len(optionalArgs) >= 2 {
		senderID = optionalArgs[1]
	}
	if len(optionalArgs) >= 3 {
		projectName = optionalArgs[2]
	}

	// 限制输入长度，避免 token 过多
	maxChars := 3000
	msg := truncateRunes(userMessage, maxChars)
	resp := truncateRunes(agentResponse, maxChars)

	// 构建动态提示词：加入项目上下文，让 AI 精确区分
	systemPrompt := extractionPrompt
	if projectName != "" && projID != "" {
		systemPrompt += fmt.Sprintf(`

IMPORTANT CONTEXT:
The current project is "%s" (ID: %s).
- ONLY use [project:*] for facts directly about THIS project ("%s").
- If the conversation mentions OTHER projects, SKIP those facts entirely — do NOT store them.
- Do NOT mix up different projects.`, projectName, projID, projectName)
	}

	extractPrompt := fmt.Sprintf("Extract key memories from this conversation:\n\nUser: %s\n\nAgent Response: %s", msg, resp)

	output, err := runUtilityPrompt(provider, extractPrompt, systemPrompt, 5)
	if err != nil {
		logging.Log("DEBUG", fmt.Sprintf("记忆提取失败 (agent: %s): %v", agentID, err))
		return
	}

	output = strings.TrimSpace(output)
	if output == "" || strings.Contains(strings.ToUpper(output), "NONE") {
		return
	}

	// 解析提取结果
	lines := strings.Split(output, "\n")
	stored := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		scope, category, content := parseScopedLine(line)
		if content == "" || strings.Contains(strings.ToUpper(content), "NONE") {
			continue
		}

		// 清理 @mentions（如 @backend → backend）
		content = cleanAtMentions(content)
		if content == "" {
			continue
		}

		// 跳过描述 Agent 角色分工的内容（这是系统配置，不是知识）
		if isAgentRoleDescription(content) {
			continue
		}

		// 【改进1】内容质量过滤：跳过短文本、元评论、格式噪音
		if isLowQualityContent(content) {
			logging.Log("DEBUG", fmt.Sprintf("跳过低质量记忆: %s", truncateRunes(content, 50)))
			continue
		}

		// Project scope 的验证已在 AI prompt 中完成（scope 分类由 AI 决定）
		// 代码层只检查 projID 是否存在（第 141 行），不再做内容关键词过滤

		// 根据 scope 决定存储目标
		switch scope {
		case "project":
			if projID == "" {
				// 没有项目上下文，跳过项目知识（不降级到 agent scope，因为项目信息存 agent 没有意义）
				continue
			}
		case "user":
			if senderID == "" {
				// 没有用户 ID，跳过用户知识（不降级到 agent scope）
				continue
			}
		case "agent":
			// agent scope 正常存储
		default:
			scope = "agent"
		}

		// 确定 scopeID
		scopeID := agentID
		switch scope {
		case "project":
			scopeID = projID
		case "user":
			scopeID = senderID
		}

		// 去重检查：先精确搜索，再相似度检查
		if isDuplicateMemory(scopeID, scope, content) {
			logging.Log("DEBUG", fmt.Sprintf("跳过重复记忆: %s", truncateRunes(content, 50)))
			continue
		}

		_, err := Store(agentID, content, category, "auto", convID, scope, scopeID)
		if err != nil {
			logging.Log("WARN", fmt.Sprintf("存储记忆失败: %v", err))
			continue
		}
		stored++

		if stored >= 5 {
			break
		}
	}

	if stored > 0 {
		logging.Log("INFO", fmt.Sprintf("🧠 提取 %d 条记忆 (agent: %s, project: %s, user: %s)", stored, agentID, projID, senderID))

		// 【改进3】检查并合并过多的项目记忆
		if projID != "" {
			go maybeCompactMemories("project", projID)
		}
	}
}

// agentOnlyPrompt 非 Leader Agent 专用提示词：只提取行为学习
const agentOnlyPrompt = `You are a memory extraction assistant. Extract behavioral learnings for this agent.

Rules:
1. Extract ONLY the agent's own behavioral learnings and work patterns
2. Examples: "should ask for clarification before starting", "user prefers step-by-step explanations", "break complex tasks into smaller subtasks first"
3. Do NOT extract project technical details (tech stack, APIs, database schema) — handled by leader
4. Do NOT extract user personal information — handled by leader
5. Each learning should be a single, self-contained sentence
6. Output one learning per line, NO prefix needed
7. If nothing worth learning, output exactly: NONE
8. Maximum 3 learnings per extraction
9. Write in the same language as the conversation`

// ExtractAgentOnlyMemories 非 Leader Agent 专用：只提取 agent scope 行为学习（省资源）
func ExtractAgentOnlyMemories(agentID, userMessage, agentResponse, convID string, runUtilityPrompt func(provider, prompt, systemPrompt string, timeoutMin int) (string, error), provider string) {
	totalLen := len([]rune(userMessage)) + len([]rune(agentResponse))
	if totalLen < 200 {
		return
	}

	maxChars := 2000
	msg := truncateRunes(userMessage, maxChars)
	resp := truncateRunes(agentResponse, maxChars)

	extractPrompt := fmt.Sprintf("Extract behavioral learnings from this conversation:\n\nUser: %s\n\nAgent Response: %s", msg, resp)

	output, err := runUtilityPrompt(provider, extractPrompt, agentOnlyPrompt, 3)
	if err != nil {
		return
	}
	output = strings.TrimSpace(output)
	if output == "" || strings.Contains(strings.ToUpper(output), "NONE") {
		return
	}

	lines := strings.Split(output, "\n")
	stored := 0
	for _, line := range lines {
		content := strings.TrimSpace(line)
		if content == "" {
			continue
		}
		content = cleanAtMentions(content)
		if content == "" || isAgentRoleDescription(content) {
			continue
		}
		if isDuplicateMemory(agentID, "agent", content) {
			continue
		}
		if _, err := Store(agentID, content, "learning", "auto", convID, "agent", agentID); err != nil {
			continue
		}
		stored++
		if stored >= 3 {
			break
		}
	}
	if stored > 0 {
		logging.Log("INFO", fmt.Sprintf("🧠 提取 %d 条 Agent 行为学习 (agent: %s)", stored, agentID))
	}
}

// parseScopedLine 解析 "[scope:category:importance] content" 格式的行
// 兼容旧格式 "[scope:category] content"
func parseScopedLine(line string) (scope, category, content string) {
	// 默认值
	scope = "agent"
	category = "fact"

	if !strings.HasPrefix(line, "[") {
		return scope, category, line
	}

	idx := strings.Index(line, "]")
	if idx < 0 {
		return scope, category, line
	}

	tag := strings.ToLower(strings.TrimSpace(line[1:idx]))
	content = strings.TrimSpace(line[idx+1:])

	// 解析 scope:category:importance 或 scope:category
	parts := strings.SplitN(tag, ":", 3)
	if len(parts) >= 2 {
		scope = strings.TrimSpace(parts[0])
		category = strings.TrimSpace(parts[1])
		// importance 在 parts[2]，但我们不需要在这里使用它
		// AI 已经被指示只输出 importance >= 3 的记忆
	} else {
		// 兼容旧格式 [category]
		category = tag
	}

	// 验证 scope
	validScopes := map[string]bool{"agent": true, "project": true, "user": true}
	if !validScopes[scope] {
		scope = "agent"
	}

	// 验证 category
	validCategories := map[string]bool{
		"fact": true, "preference": true, "learning": true, "decision": true, "context": true,
	}
	if !validCategories[category] {
		category = "fact"
	}

	return scope, category, content
}

// isLowQualityContent 检查内容是否为低质量（应跳过）
func isLowQualityContent(content string) bool {
	// 1. 太短（少于 8 个字符，基本不可能是有价值的知识）
	if len([]rune(content)) < 8 {
		return true
	}

	// 2. AI 元评论模式（不是知识本身，是 AI 对分析过程的描述）
	metaPatterns := []string{
		"based on the conversation",
		"based on this conversation",
		"here are the memories",
		"here are the key",
		"i extracted",
		"the following memories",
		"key memories to extract",
		"memories extracted",
		"根据对话",
		"以下是提取的",
		"此次对话涉及",
		"对话分析",
		"提取结果",
		"以下记忆",
		"以下是",
	}
	lower := strings.ToLower(content)
	for _, p := range metaPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	// 3. 纯格式噪音（markdown 代码块标记等）
	trimmed := strings.TrimLeft(content, " \t`>#-*")
	if len([]rune(trimmed)) < 5 {
		return true
	}

	// 4. 任务执行步骤（不是持久化知识）
	taskPatterns := []string{
		"任务已完成", "文件已创建", "已成功",
		"task completed", "file created", "successfully",
		"已修复并验证", "代码已写入",
	}
	for _, p := range taskPatterns {
		if strings.Contains(lower, p) && len([]rune(content)) < 30 {
			// 短句 + 包含执行描述 → 低质量
			return true
		}
	}

	return false
}

// isValidProjectMemory 检查 project scope 的记忆是否真的包含项目相关内容
func isValidProjectMemory(content, projectName string) bool {
	lower := strings.ToLower(content)

	// 项目技术关键词：如果包含任一关键词，认为是有效的项目知识
	techKeywords := []string{
		// 通用技术
		"api", "数据库", "database", "表结构", "table", "端口", "port",
		"路径", "path", "目录", "directory", "文件", "file",
		"框架", "framework", "技术栈", "tech stack",
		"配置", "config", "环境变量", "env",
		"部署", "deploy", "运行", "serve",
		// 编程相关
		"函数", "function", "方法", "method", "接口", "interface", "endpoint",
		"路由", "route", "中间件", "middleware",
		"加密", "encrypt", "认证", "auth", "token", "jwt",
		"依赖", "dependency", "package", "import",
		// 架构相关
		"架构", "architecture", "设计", "design", "决策", "decision",
		"规范", "convention", "规则", "rule", "格式", "format",
	}

	for _, kw := range techKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}

	// 如果包含项目名，也认为有效
	if projectName != "" && strings.Contains(lower, strings.ToLower(projectName)) {
		return true
	}

	return false
}

// parseCategoryLine 解析 "[category] content" 格式的行
func parseCategoryLine(line string) (category, content string) {
	// 匹配 [category] content 格式
	if !strings.HasPrefix(line, "[") {
		return "fact", line
	}

	idx := strings.Index(line, "]")
	if idx < 0 {
		return "fact", line
	}

	category = strings.ToLower(strings.TrimSpace(line[1:idx]))
	content = strings.TrimSpace(line[idx+1:])

	// 验证 category
	validCategories := map[string]bool{
		"fact": true, "preference": true, "learning": true, "decision": true, "context": true,
	}
	if !validCategories[category] {
		category = "fact"
	}

	return category, content
}

// isSimilar 简单判断两段文本是否相似（避免重复存储）
func isSimilar(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))

	// 完全包含关系
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}

	// 字符级重叠检测（适合中文，中文不靠空格分词）
	runesA := []rune(a)
	runesB := []rune(b)
	if len(runesA) > 0 && len(runesB) > 0 {
		charSetA := make(map[rune]bool)
		for _, r := range runesA {
			charSetA[r] = true
		}
		charOverlap := 0
		for _, r := range runesB {
			if charSetA[r] {
				charOverlap++
			}
		}
		shorter := len(runesB)
		if len(runesA) < shorter {
			shorter = len(runesA)
		}
		// 字符重叠率超过 60%（基于较短文本）认为相似
		if float64(charOverlap)/float64(shorter) > 0.6 {
			return true
		}
	}

	// 词级重叠检测（适合英文或中英混合）
	wordsA := strings.Fields(a)
	wordsB := strings.Fields(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return false
	}

	wordSet := make(map[string]bool)
	for _, w := range wordsA {
		wordSet[w] = true
	}

	overlap := 0
	for _, w := range wordsB {
		if wordSet[w] {
			overlap++
		}
	}

	// 词重叠率超过 55% 认为相似（比之前 70% 更积极去重）
	shorter := len(wordsB)
	if len(wordsA) < shorter {
		shorter = len(wordsA)
	}
	ratio := float64(overlap) / float64(shorter)
	return ratio > 0.55
}

// truncateRunes 按 rune 数截断字符串
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// @mention 匹配正则: @xxx 格式
var atMentionRe = regexp.MustCompile(`@[\w-]+`)

// cleanAtMentions 移除知识内容中的 @mentions
// 例如 "@backend 的代码风格" → "后端的代码风格" (如果有中文名)
// 或 "团队成员 @backend 是后端工程师" → "团队成员是后端工程师"
func cleanAtMentions(content string) string {
	// 清除 @xxx 并去除多余空格
	cleaned := atMentionRe.ReplaceAllString(content, "")
	// 清理多余空格
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return strings.TrimSpace(cleaned)
}

// Agent 角色描述关键词
var agentRoleKeywords = []string{
	"是后端工程师", "是前端工程师", "是架构师", "是测试工程师", "是产品经理",
	"负责后端", "负责前端", "负责测试", "负责架构",
	"专注于 API", "专注于 UI", "专注于测试",
	"队友", "团队成员", "团队中有",
	"is a backend engineer", "is a frontend engineer", "is an architect",
}

// isAgentRoleDescription 检查内容是否在描述 Agent 角色分工（系统配置，不是知识）
func isAgentRoleDescription(content string) bool {
	lower := strings.ToLower(content)
	for _, kw := range agentRoleKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// isDuplicateMemory 检查目标 scope 内是否已有重复或相似记忆
func isDuplicateMemory(scopeID, scope, newContent string) bool {
	if db == nil {
		return false
	}

	// 方法 1：精确匹配（相同 scope+scopeID 下是否有完全相同的内容）
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM memories WHERE scope = ? AND scope_id = ? AND content = ?",
		scope, scopeID, newContent).Scan(&count)
	if err == nil && count > 0 {
		return true
	}

	// 方法 2：加载同 scope 的最近 50 条记忆做文本相似度检查
	rows, err := db.Query("SELECT content FROM memories WHERE scope = ? AND scope_id = ? ORDER BY created_at DESC LIMIT 50",
		scope, scopeID)
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			continue
		}
		if isSimilar(existing, newContent) {
			return true
		}
	}

	return false
}

// maybeCompactMemories 当某 scope 的记忆超过上限时，自动清理相似度最高的旧记忆
// 每个 scope+scopeID 上限 30 条，超过后删除与其他条目最相似的记忆（保留较长的）
func maybeCompactMemories(scope, scopeID string) {
	if db == nil {
		return
	}

	const maxMemories = 30

	// 查询当前条数
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM memories WHERE scope = ? AND scope_id = ?",
		scope, scopeID).Scan(&count)
	if err != nil || count <= maxMemories {
		return
	}

	excess := count - maxMemories
	if excess > 5 {
		excess = 5 // 每次最多清理 5 条
	}

	// 加载所有记忆
	rows, err := db.Query("SELECT id, content FROM memories WHERE scope = ? AND scope_id = ? ORDER BY created_at ASC",
		scope, scopeID)
	if err != nil {
		return
	}
	defer rows.Close()

	type memEntry struct {
		id      int64
		content string
	}
	var entries []memEntry
	for rows.Next() {
		var e memEntry
		if rows.Scan(&e.id, &e.content) == nil {
			entries = append(entries, e)
		}
	}

	// 找出相似度最高的旧记忆对，删除较短的
	deleted := 0
	deletedIDs := make(map[int64]bool)
	for deleted < excess {
		bestI, bestJ := -1, -1
		bestScore := 0.0

		for i := 0; i < len(entries); i++ {
			if deletedIDs[entries[i].id] {
				continue
			}
			for j := i + 1; j < len(entries); j++ {
				if deletedIDs[entries[j].id] {
					continue
				}
				score := similarityScore(entries[i].content, entries[j].content)
				if score > bestScore {
					bestScore = score
					bestI = i
					bestJ = j
				}
			}
		}

		if bestI < 0 || bestScore < 0.3 {
			break // 没有足够相似的对了
		}

		// 删除较短的那条（保留信息更多的）
		delIdx := bestI
		if len([]rune(entries[bestI].content)) > len([]rune(entries[bestJ].content)) {
			delIdx = bestJ
		}

		db.Exec("DELETE FROM memories WHERE id = ?", entries[delIdx].id)
		deletedIDs[entries[delIdx].id] = true
		deleted++
	}

	if deleted > 0 {
		logging.Log("INFO", fmt.Sprintf("🧹 合并清理 %d 条相似记忆 (scope: %s/%s, 原: %d → %d)",
			deleted, scope, scopeID, count, count-deleted))
	}
}

// similarityScore 计算两段文本的字符重叠相似度（0-1）
func similarityScore(a, b string) float64 {
	runesA := []rune(strings.ToLower(a))
	runesB := []rune(strings.ToLower(b))
	if len(runesA) == 0 || len(runesB) == 0 {
		return 0
	}

	charSetA := make(map[rune]int)
	for _, r := range runesA {
		charSetA[r]++
	}

	overlap := 0
	for _, r := range runesB {
		if charSetA[r] > 0 {
			overlap++
			charSetA[r]--
		}
	}

	shorter := len(runesA)
	if len(runesB) < shorter {
		shorter = len(runesB)
	}
	return float64(overlap) / float64(shorter)
}
