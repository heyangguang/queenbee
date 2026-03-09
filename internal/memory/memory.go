package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/queenbee-ai/queenbee/internal/logging"
)

// ═════════════════════════════════════════════════════════════════
// Memory — Agent 长期记忆
//
// 支持三层记忆作用域：
//   - agent  — Agent 私有记忆（默认）
//   - team   — Team 共享记忆，同团队 Agent 可读
//   - user   — 用户全局记忆，所有 Agent 可读
// ═══════════════════════════════════════════════════════════════════

// Memory 单条记忆
type Memory struct {
	ID        int64     `json:"id"`
	AgentID   string    `json:"agent_id"`
	Content   string    `json:"content"`
	Category  string    `json:"category"` // "fact" | "preference" | "context" | "learning" | "decision"
	Source    string    `json:"source"`   // "auto" | "manual" | "conversation"
	ConvID    string    `json:"conv_id"`  // 来源会话 ID
	Scope     string    `json:"scope"`    // "agent" | "team" | "user"
	ScopeID   string    `json:"scope_id"` // scope 对应的 ID（agent_id / team_id / sender_id）
	Embedding []float64 `json:"-"`        // 向量（不序列化到 JSON）
	CreatedAt int64     `json:"created_at"`
}

var db *sql.DB

// Init 初始化 Memory 模块（复用主数据库连接）
func Init(database *sql.DB) error {
	db = database

	// 创建 memories 表
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS memories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			content TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'fact',
			source TEXT NOT NULL DEFAULT 'auto',
			conv_id TEXT DEFAULT '',
			embedding TEXT DEFAULT '',
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_memories_agent ON memories(agent_id);
		CREATE INDEX IF NOT EXISTS idx_memories_agent_cat ON memories(agent_id, category);
	`)
	if err != nil {
		return fmt.Errorf("创建 memories 表失败: %w", err)
	}

	// 创建 FTS5 虚拟表用于全文搜索
	_, err = db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
			content,
			content='memories',
			content_rowid='id'
		);
	`)
	if err != nil {
		// FTS5 可能不可用（编译时未启用），降级为 LIKE 搜索
		logging.Log("WARN", fmt.Sprintf("FTS5 不可用，降级为 LIKE 搜索: %v", err))
	}

	// 创建触发器自动同步 FTS 索引
	db.Exec(`
		CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
			INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
		END;
	`)
	db.Exec(`
		CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
			INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
		END;
	`)
	db.Exec(`
		CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
			INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
			INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
		END;
	`)

	// 迁移：增加 embedding / scope / scope_id 列
	db.Exec("ALTER TABLE memories ADD COLUMN embedding TEXT DEFAULT ''")
	db.Exec("ALTER TABLE memories ADD COLUMN scope TEXT DEFAULT 'agent'")
	db.Exec("ALTER TABLE memories ADD COLUMN scope_id TEXT DEFAULT ''")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(scope, scope_id)")

	// 检测 Ollama
	if IsOllamaAvailable() {
		logging.Log("INFO", "✨ Memory 模块已初始化（Ollama 语义搜索 + FTS5 关键词搜索）")
	} else {
		logging.Log("INFO", "Memory 模块已初始化（FTS5 关键词搜索，Ollama 不可用）")
	}
	return nil
}

// Store 存储一条记忆
// scope: "agent"(Agent 私有) | "team"(团队共享) | "user"(用户全局)
// scopeID: scope 对应的 ID（留空则默认用 agentID）
func Store(agentID, content, category, source, convID string, opts ...string) (*Memory, error) {
	if db == nil {
		return nil, fmt.Errorf("memory 模块未初始化")
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("记忆内容不能为空")
	}
	if category == "" {
		category = "fact"
	}
	if source == "" {
		source = "auto"
	}

	// 解析可选参数: scope, scopeID
	scope := "agent"
	scopeID := agentID
	if len(opts) >= 1 && opts[0] != "" {
		scope = opts[0]
	}
	if len(opts) >= 2 && opts[1] != "" {
		scopeID = opts[1]
	}

	now := time.Now().UnixMilli()

	// 尝试生成 embedding
	var embeddingJSON string
	if IsOllamaAvailable() {
		if vec, err := GetEmbedding(content); err == nil {
			data, _ := json.Marshal(vec)
			embeddingJSON = string(data)
		}
	}

	result, err := db.Exec(`
		INSERT INTO memories (agent_id, content, category, source, conv_id, embedding, scope, scope_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, content, category, source, convID, embeddingJSON, scope, scopeID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("存储记忆失败: %w", err)
	}

	id, _ := result.LastInsertId()
	logging.Log("INFO", fmt.Sprintf("💾 存储记忆: agent=%s, scope=%s/%s, category=%s, %d 字符",
		agentID, scope, scopeID, category, len([]rune(content))))

	return &Memory{
		ID:        id,
		AgentID:   agentID,
		Content:   content,
		Category:  category,
		Source:    source,
		ConvID:    convID,
		Scope:     scope,
		ScopeID:   scopeID,
		CreatedAt: now,
	}, nil
}

// Search 搜索相关记忆（仅搜索当前 Agent 的记忆）
func Search(agentID, query string, topK int) ([]*Memory, error) {
	if db == nil {
		return nil, fmt.Errorf("memory 模块未初始化")
	}
	if topK <= 0 {
		topK = 5
	}
	return searchByScope(agentID, query, topK)
}

// SearchMultiScope 多层搜索：Agent 私有 + Project 共享 + 用户全局
// projectID 和 senderID 可为空，为空则跳过该层
func SearchMultiScope(agentID, projectID, senderID, query string, topK int) ([]*Memory, error) {
	if db == nil {
		return nil, fmt.Errorf("memory 模块未初始化")
	}
	if topK <= 0 {
		topK = 5
	}

	// 收集所有层级的记忆
	var allMemories []*Memory
	seen := make(map[int64]bool)

	add := func(memories []*Memory) {
		for _, m := range memories {
			if !seen[m.ID] {
				seen[m.ID] = true
				allMemories = append(allMemories, m)
			}
		}
	}

	// 1. Agent 私有记忆
	if ms, err := searchByScope(agentID, query, topK); err == nil {
		add(ms)
	}

	// 2. Project 共享记忆
	if projectID != "" {
		if ms, err := searchByScopeID("project", projectID, query, topK); err == nil {
			add(ms)
		}
	}

	// 3. 用户全局记忆
	if senderID != "" {
		if ms, err := searchByScopeID("user", senderID, query, topK); err == nil {
			add(ms)
		}
	}

	// 截取 topK
	if len(allMemories) > topK {
		allMemories = allMemories[:topK]
	}

	return allMemories, nil
}

// searchByScope 按 agent_id 搜索（原有逻辑，三级降级）
func searchByScope(agentID, query string, topK int) ([]*Memory, error) {
	// 优先 Ollama 向量搜索
	if IsOllamaAvailable() {
		memories, err := searchEmbedding(agentID, query, topK)
		if err == nil && len(memories) > 0 {
			return memories, nil
		}
	}
	// FTS5 搜索
	memories, err := searchFTS(agentID, query, topK)
	if err != nil {
		// LIKE 模糊搜索兜底
		memories, err = searchLike(agentID, query, topK)
		if err != nil {
			return nil, err
		}
	}
	return memories, nil
}

// searchByScopeID 按 scope+scope_id 搜索（用于 team/user 级别记忆）
func searchByScopeID(scope, scopeID, query string, topK int) ([]*Memory, error) {
	// 优先 Ollama 向量搜索
	if IsOllamaAvailable() {
		memories, err := searchEmbeddingByScope(scope, scopeID, query, topK)
		if err == nil && len(memories) > 0 {
			return memories, nil
		}
	}
	// LIKE 模糊搜索
	keywords := strings.Fields(query)
	if len(keywords) == 0 {
		return nil, nil
	}
	conditions := make([]string, 0, len(keywords))
	args := []interface{}{scope, scopeID}
	for _, kw := range keywords {
		if len(kw) < 2 {
			continue
		}
		conditions = append(conditions, "content LIKE ?")
		args = append(args, "%"+kw+"%")
	}
	if len(conditions) == 0 {
		return nil, nil
	}
	whereClause := strings.Join(conditions, " OR ")
	args = append(args, topK)
	rows, err := db.Query(fmt.Sprintf(`
		SELECT id, agent_id, content, category, source, conv_id, created_at
		FROM memories
		WHERE scope = ? AND scope_id = ? AND (%s)
		ORDER BY created_at DESC
		LIMIT ?`, whereClause), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

// searchEmbeddingByScope 按 scope+scope_id 做向量搜索
func searchEmbeddingByScope(scope, scopeID, query string, topK int) ([]*Memory, error) {
	queryVec, err := GetEmbedding(query)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT id, agent_id, content, category, source, conv_id, embedding, created_at
		FROM memories
		WHERE scope = ? AND scope_id = ? AND embedding != '' AND embedding IS NOT NULL
		ORDER BY created_at DESC
		LIMIT 200`, scope, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		memory *Memory
		score  float64
	}
	var candidates []scored
	for rows.Next() {
		m := &Memory{}
		var embJSON string
		err := rows.Scan(&m.ID, &m.AgentID, &m.Content, &m.Category, &m.Source, &m.ConvID, &embJSON, &m.CreatedAt)
		if err != nil || embJSON == "" {
			continue
		}
		var vec []float64
		if err := json.Unmarshal([]byte(embJSON), &vec); err != nil || len(vec) == 0 {
			continue
		}
		sim := CosineSimilarity(queryVec, vec)
		if sim > 0.3 {
			candidates = append(candidates, scored{memory: m, score: sim})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}
	memories := make([]*Memory, len(candidates))
	for i, c := range candidates {
		memories[i] = c.memory
	}
	return memories, nil
}

// searchEmbedding 使用 Ollama 向量做语义搜索
func searchEmbedding(agentID, query string, topK int) ([]*Memory, error) {
	// 获取查询的 embedding
	queryVec, err := GetEmbedding(query)
	if err != nil {
		return nil, err
	}

	// 加载该 agent 所有有 embedding 的记忆
	rows, err := db.Query(`
		SELECT id, agent_id, content, category, source, conv_id, embedding, created_at
		FROM memories
		WHERE agent_id = ? AND embedding != '' AND embedding IS NOT NULL
		ORDER BY created_at DESC
		LIMIT 200`,
		agentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		memory *Memory
		score  float64
	}
	var candidates []scored

	for rows.Next() {
		m := &Memory{}
		var embJSON string
		err := rows.Scan(&m.ID, &m.AgentID, &m.Content, &m.Category, &m.Source, &m.ConvID, &embJSON, &m.CreatedAt)
		if err != nil || embJSON == "" {
			continue
		}

		var vec []float64
		if err := json.Unmarshal([]byte(embJSON), &vec); err != nil || len(vec) == 0 {
			continue
		}

		sim := CosineSimilarity(queryVec, vec)
		if sim > 0.3 { // 相似度阈值
			candidates = append(candidates, scored{memory: m, score: sim})
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// 按相似度降序
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// 取 top-K
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}

	memories := make([]*Memory, len(candidates))
	for i, c := range candidates {
		memories[i] = c.memory
	}

	logging.Log("DEBUG", fmt.Sprintf("🔍 向量搜索: agent=%s, 匹配 %d 条 (top score=%.3f)", agentID, len(memories), candidates[0].score))
	return memories, nil
}

// searchFTS 使用 FTS5 全文搜索
func searchFTS(agentID, query string, topK int) ([]*Memory, error) {
	// 将 query 中的特殊字符转义
	safeQuery := strings.ReplaceAll(query, "\"", "\"\"")

	rows, err := db.Query(`
		SELECT m.id, m.agent_id, m.content, m.category, m.source, m.conv_id, m.created_at
		FROM memories m
		JOIN memories_fts f ON m.id = f.rowid
		WHERE m.agent_id = ? AND memories_fts MATCH ?
		ORDER BY rank
		LIMIT ?`,
		agentID, "\""+safeQuery+"\"", topK,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMemories(rows)
}

// searchLike 使用 LIKE 模糊搜索（FTS5 不可用时的降级方案）
func searchLike(agentID, query string, topK int) ([]*Memory, error) {
	// 提取关键词
	keywords := strings.Fields(query)
	if len(keywords) == 0 {
		return listRecent(agentID, topK)
	}

	// 构建 LIKE 条件
	conditions := make([]string, 0, len(keywords))
	args := []interface{}{agentID}
	for _, kw := range keywords {
		if len(kw) < 2 {
			continue // 跳过太短的关键词
		}
		conditions = append(conditions, "m.content LIKE ?")
		args = append(args, "%"+kw+"%")
	}

	if len(conditions) == 0 {
		return listRecent(agentID, topK)
	}

	// 至少匹配一个关键词
	whereClause := strings.Join(conditions, " OR ")
	args = append(args, topK)

	rows, err := db.Query(fmt.Sprintf(`
		SELECT m.id, m.agent_id, m.content, m.category, m.source, m.conv_id, m.created_at
		FROM memories m
		WHERE m.agent_id = ? AND (%s)
		ORDER BY m.created_at DESC
		LIMIT ?`, whereClause),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMemories(rows)
}

// listRecent 列出最近的记忆
func listRecent(agentID string, limit int) ([]*Memory, error) {
	rows, err := db.Query(`
		SELECT id, agent_id, content, category, source, conv_id, created_at
		FROM memories
		WHERE agent_id = ?
		ORDER BY created_at DESC
		LIMIT ?`,
		agentID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMemories(rows)
}

// List 列出指定 agent 的所有记忆（分页）
func List(agentID string, limit, offset int) ([]*Memory, int, error) {
	if db == nil {
		return nil, 0, fmt.Errorf("memory 模块未初始化")
	}
	if limit <= 0 {
		limit = 20
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM memories WHERE agent_id = ?", agentID).Scan(&count)

	rows, err := db.Query(`
		SELECT id, agent_id, content, category, source, conv_id, created_at
		FROM memories
		WHERE agent_id = ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`,
		agentID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	memories, err := scanMemories(rows)
	return memories, count, err
}

// Forget 删除一条记忆
func Forget(agentID string, memoryID int64) error {
	if db == nil {
		return fmt.Errorf("memory 模块未初始化")
	}

	result, err := db.Exec("DELETE FROM memories WHERE id = ? AND agent_id = ?", memoryID, agentID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("记忆不存在")
	}

	logging.Log("INFO", fmt.Sprintf("🗑️ 删除记忆: agent=%s, id=%d", agentID, memoryID))
	return nil
}

// ForgetByID 按 ID 删除记忆（不限 scope，用于通用删除）
func ForgetByID(memoryID int64) error {
	if db == nil {
		return fmt.Errorf("memory 模块未初始化")
	}

	result, err := db.Exec("DELETE FROM memories WHERE id = ?", memoryID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("记忆不存在")
	}

	logging.Log("INFO", fmt.Sprintf("🗑️ 删除记忆: id=%d", memoryID))
	return nil
}

// ForgetAll 清除 agent 的所有记忆
func ForgetAll(agentID string) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("memory 模块未初始化")
	}

	result, err := db.Exec("DELETE FROM memories WHERE agent_id = ?", agentID)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()

	logging.Log("INFO", fmt.Sprintf("🗑️ 清除所有记忆: agent=%s, count=%d", agentID, affected))
	return int(affected), nil
}

// Count 统计 agent 的记忆总数
func Count(agentID string) int {
	if db == nil {
		return 0
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM memories WHERE agent_id = ?", agentID).Scan(&count)
	return count
}

// scanMemories 从 rows 中扫描出 Memory 列表
func scanMemories(rows *sql.Rows) ([]*Memory, error) {
	var memories []*Memory
	for rows.Next() {
		m := &Memory{}
		err := rows.Scan(&m.ID, &m.AgentID, &m.Content, &m.Category, &m.Source, &m.ConvID, &m.CreatedAt)
		if err != nil {
			continue
		}
		memories = append(memories, m)
	}
	return memories, nil
}

// ListByScope 按 scope/scope_id 列出记忆（分页）
func ListByScope(scope, scopeID string, limit, offset int) ([]*Memory, int, error) {
	if db == nil {
		return nil, 0, fmt.Errorf("memory 模块未初始化")
	}
	if limit <= 0 {
		limit = 50
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM memories WHERE scope = ? AND scope_id = ?", scope, scopeID).Scan(&count)

	rows, err := db.Query(`
		SELECT id, agent_id, content, category, source, conv_id, created_at
		FROM memories
		WHERE scope = ? AND scope_id = ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`,
		scope, scopeID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	memories, err := scanMemories(rows)
	return memories, count, err
}

// StoreByScope 按 scope 存储记忆（Project/User 级别，不需要具体 agent_id）
func StoreByScope(scope, scopeID, content, category string) (*Memory, error) {
	return Store("_system", content, category, "manual", "", scope, scopeID)
}

// ForgetByScope 删除指定 scope 下的所有记忆
func ForgetByScope(scope, scopeID string) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("memory 模块未初始化")
	}
	result, err := db.Exec("DELETE FROM memories WHERE scope = ? AND scope_id = ?", scope, scopeID)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// FormatMemoriesForContext 将记忆格式化为注入到 Agent 上下文的文本
func FormatMemoriesForContext(memories []*Memory) string {
	if len(memories) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n------\n[Long-term Memory — relevant information from previous conversations]\n")
	for i, m := range memories {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, m.Category, m.Content))
	}
	sb.WriteString("[End of memory context]\n------\n")

	return sb.String()
}
