package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/queenbee-ai/queenbee/internal/logging"

	_ "modernc.org/sqlite"
)

// NullString 导出类型别名
type NullString = sql.NullString

// DbMessage 消息队列行
type DbMessage struct {
	ID             int64
	MessageID      string
	Channel        string
	Sender         string
	SenderID       sql.NullString
	Message        string
	Agent          sql.NullString
	Files          sql.NullString
	ConversationID sql.NullString
	FromAgent      sql.NullString
	ProjectID      sql.NullString // 可选：显式指定项目上下文
	Status         string         // pending, processing, completed, dead
	RetryCount     int
	LastError      sql.NullString
	CreatedAt      int64
	UpdatedAt      int64
	ClaimedBy      sql.NullString
}

// DbResponse 响应队列行
type DbResponse struct {
	ID              int64
	MessageID       string
	Channel         string
	Sender          string
	SenderID        sql.NullString
	Message         string
	OriginalMessage string
	Agent           sql.NullString
	Files           sql.NullString
	Metadata        sql.NullString
	Status          string // pending, acked
	CreatedAt       int64
	AckedAt         sql.NullInt64
}

// EnqueueMessageData 入队数据
type EnqueueMessageData struct {
	Channel        string
	Sender         string
	SenderID       string
	Message        string
	MessageID      string
	Agent          string
	Files          []string
	ConversationID string
	FromAgent      string
	ProjectID      string // 可选：显式指定项目上下文
}

// EnqueueResponseData 响应入队数据
type EnqueueResponseData struct {
	Channel         string
	Sender          string
	SenderID        string
	Message         string
	OriginalMessage string
	MessageID       string
	Agent           string
	Files           []string
	Metadata        map[string]interface{}
}

// QueueStatus 队列状态
type QueueStatus struct {
	Pending          int `json:"pending"`
	Processing       int `json:"processing"`
	Completed        int `json:"completed"`
	Dead             int `json:"dead"`
	ResponsesPending int `json:"responses_pending"`
}

var (
	database *sql.DB
	dbMu     sync.Mutex

	// MessageEnqueued 消息入队通知 channel
	MessageEnqueued = make(chan struct{}, 100)
)

const maxRetries = 5

// InitQueueDb 初始化数据库
// dbDir 为数据库文件所在目录（如 ~/.queenbee）
func InitQueueDb(dbDir string) error {
	dbPath := dbDir + "/queenbee.db"
	var err error
	database, err = sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=30000")
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}

	// SQLite 单连接模式：避免 SQLITE_BUSY 错误
	// modernc.org/sqlite (pure Go) 的多连接并发不可靠，
	// 单连接让 database/sql 在连接池层串行化所有操作
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	// 创建表
	_, err = database.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id TEXT NOT NULL,
			channel TEXT NOT NULL,
			sender TEXT NOT NULL,
			sender_id TEXT,
			message TEXT NOT NULL,
			agent TEXT,
			files TEXT,
			conversation_id TEXT,
			from_agent TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			claimed_by TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(status);
		CREATE INDEX IF NOT EXISTS idx_messages_agent ON messages(agent);
		CREATE INDEX IF NOT EXISTS idx_messages_status_agent ON messages(status, agent);

		CREATE TABLE IF NOT EXISTS responses (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id TEXT NOT NULL,
			channel TEXT NOT NULL,
			sender TEXT NOT NULL,
			sender_id TEXT,
			message TEXT NOT NULL,
			original_message TEXT NOT NULL DEFAULT '',
			agent TEXT,
			files TEXT,
			metadata TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at INTEGER NOT NULL,
			acked_at INTEGER
		);

		CREATE INDEX IF NOT EXISTS idx_responses_status ON responses(status);
		CREATE INDEX IF NOT EXISTS idx_responses_channel ON responses(channel);

		CREATE TABLE IF NOT EXISTS conversation_history (
			id TEXT PRIMARY KEY,
			team_id TEXT NOT NULL,
			team_name TEXT,
			channel TEXT,
			sender TEXT,
			original_message TEXT,
			agents TEXT,
			total_messages INTEGER DEFAULT 0,
			started_at INTEGER NOT NULL,
			ended_at INTEGER,
			chat_file TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_conv_history_team ON conversation_history(team_id);
		CREATE INDEX IF NOT EXISTS idx_conv_history_started ON conversation_history(started_at);

		CREATE TABLE IF NOT EXISTS conversation_steps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			from_agent TEXT,
			to_agent TEXT,
			message TEXT,
			message_type TEXT,
			timestamp INTEGER NOT NULL,
			FOREIGN KEY (conversation_id) REFERENCES conversation_history(id)
		);
		CREATE INDEX IF NOT EXISTS idx_steps_conv ON conversation_steps(conversation_id, seq);

		CREATE TABLE IF NOT EXISTS conversation_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			event_data TEXT,
			timestamp INTEGER NOT NULL,
			FOREIGN KEY (conversation_id) REFERENCES conversation_history(id)
		);
		CREATE INDEX IF NOT EXISTS idx_events_conv ON conversation_events(conversation_id, timestamp);

		CREATE TABLE IF NOT EXISTS active_conversations (
			id TEXT PRIMARY KEY,
			channel TEXT NOT NULL,
			sender TEXT NOT NULL,
			original_message TEXT,
			message_id TEXT,
			team_id TEXT,
			pending_agents TEXT,
			responses TEXT,
			files TEXT,
			total_messages INTEGER DEFAULT 0,
			max_messages INTEGER DEFAULT 20,
			start_time INTEGER,
			outgoing_mentions TEXT,
			pair_exchanges TEXT,
			participated_agents TEXT,
			updated_at INTEGER
		);

		CREATE TABLE IF NOT EXISTS agent_executions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT,
			agent_id TEXT NOT NULL,
			team_id TEXT,
			duration_sec INTEGER DEFAULT 0,
			stdout_bytes INTEGER DEFAULT 0,
			status TEXT DEFAULT 'completed',
			started_at INTEGER NOT NULL,
			ended_at INTEGER
		);
		CREATE INDEX IF NOT EXISTS idx_agent_exec_agent ON agent_executions(agent_id);
		CREATE INDEX IF NOT EXISTS idx_agent_exec_conv ON agent_executions(conversation_id);
		CREATE INDEX IF NOT EXISTS idx_agent_exec_started ON agent_executions(started_at);
	`)
	if err != nil {
		return fmt.Errorf("创建表失败: %w", err)
	}

	// conversation_history 扩展字段（已有表 ALTER TABLE，忽略错误）
	database.Exec("ALTER TABLE conversation_history ADD COLUMN duration_sec INTEGER DEFAULT 0")
	database.Exec("ALTER TABLE conversation_history ADD COLUMN total_rounds INTEGER DEFAULT 0")

	// messages 扩展字段：project_id
	database.Exec("ALTER TABLE messages ADD COLUMN project_id TEXT")

	// 清理测试遗留的无用字段（DROP COLUMN 需要 SQLite ≥ 3.35.0）
	// 注意：idx_conv_issue 引用了 project_id 列，必须先删索引再删列
	database.Exec("DROP INDEX IF EXISTS idx_conv_issue")
	database.Exec("ALTER TABLE messages DROP COLUMN issue_number")
	database.Exec("ALTER TABLE conversation_history DROP COLUMN project_id")
	database.Exec("ALTER TABLE conversation_history DROP COLUMN issue_number")

	// 初始化配置存储表（agents、teams、skills、global_config）
	if err := InitConfigTables(); err != nil {
		return fmt.Errorf("初始化配置表失败: %w", err)
	}

	return nil
}

// GetDb 获取数据库连接
func GetDb() *sql.DB {
	return database
}

// EnqueueMessage 入队消息
func EnqueueMessage(data *EnqueueMessageData) (int64, error) {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	filesJSON := ""
	if len(data.Files) > 0 {
		filesJSON = toJSON(data.Files)
	}

	result, err := database.Exec(`
		INSERT INTO messages (message_id, channel, sender, sender_id, message, agent, files, conversation_id, from_agent, project_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		data.MessageID, data.Channel, data.Sender,
		nullStr(data.SenderID), data.Message,
		nullStr(data.Agent), nullStr(filesJSON),
		nullStr(data.ConversationID), nullStr(data.FromAgent),
		nullStr(data.ProjectID),
		now, now,
	)
	if err != nil {
		return 0, err
	}

	id, _ := result.LastInsertId()

	// 通知有新消息
	select {
	case MessageEnqueued <- struct{}{}:
	default:
	}

	return id, nil
}

// ClaimNextMessage 原子领取下一条消息
func ClaimNextMessage(agentID string) (*DbMessage, error) {
	dbMu.Lock()
	defer dbMu.Unlock()

	tx, err := database.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	claimID := fmt.Sprintf("%s_%d", agentID, now)

	// 领取
	result, err := tx.Exec(`
		UPDATE messages SET status = 'processing', claimed_by = ?, updated_at = ?
		WHERE id = (
			SELECT id FROM messages
			WHERE status = 'pending' AND (agent = ? OR agent IS NULL)
			ORDER BY created_at ASC
			LIMIT 1
		)`, claimID, now, agentID)
	if err != nil {
		return nil, err
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, nil
	}

	// 读取领取的消息
	row := tx.QueryRow(`SELECT id, message_id, channel, sender, sender_id, message, agent, files, conversation_id, from_agent, project_id, status, retry_count, last_error, created_at, updated_at, claimed_by FROM messages WHERE claimed_by = ? AND status = 'processing' ORDER BY id DESC LIMIT 1`, claimID)

	msg := &DbMessage{}
	err = row.Scan(&msg.ID, &msg.MessageID, &msg.Channel, &msg.Sender, &msg.SenderID, &msg.Message, &msg.Agent, &msg.Files, &msg.ConversationID, &msg.FromAgent, &msg.ProjectID, &msg.Status, &msg.RetryCount, &msg.LastError, &msg.CreatedAt, &msg.UpdatedAt, &msg.ClaimedBy)
	if err != nil {
		return nil, err
	}

	return msg, tx.Commit()
}

// CompleteMessage 标记消息完成
func CompleteMessage(rowID int64) {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UnixMilli()
	database.Exec("UPDATE messages SET status = 'completed', updated_at = ? WHERE id = ?", now, rowID)
}

// FailMessage 标记消息失败（带重试）
func FailMessage(rowID int64, errMsg string) {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UnixMilli()

	var retryCount int
	database.QueryRow("SELECT retry_count FROM messages WHERE id = ?", rowID).Scan(&retryCount)

	if retryCount+1 >= maxRetries {
		database.Exec("UPDATE messages SET status = 'dead', retry_count = retry_count + 1, last_error = ?, updated_at = ? WHERE id = ?", errMsg, now, rowID)
		logging.Log("WARN", fmt.Sprintf("消息 %d 进入死信队列（重试 %d 次）", rowID, retryCount+1))
	} else {
		database.Exec("UPDATE messages SET status = 'pending', retry_count = retry_count + 1, last_error = ?, claimed_by = NULL, updated_at = ? WHERE id = ?", errMsg, now, rowID)
	}
}

// EnqueueResponse 入队响应
func EnqueueResponse(data *EnqueueResponseData) (int64, error) {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	filesJSON := ""
	if len(data.Files) > 0 {
		filesJSON = toJSON(data.Files)
	}
	metaJSON := ""
	if len(data.Metadata) > 0 {
		metaJSON = toJSON(data.Metadata)
	}

	result, err := database.Exec(`
		INSERT INTO responses (message_id, channel, sender, sender_id, message, original_message, agent, files, metadata, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		data.MessageID, data.Channel, data.Sender,
		nullStr(data.SenderID), data.Message, data.OriginalMessage,
		nullStr(data.Agent), nullStr(filesJSON), nullStr(metaJSON), now,
	)
	if err != nil {
		return 0, err
	}

	id, _ := result.LastInsertId()
	return id, nil
}

// GetResponsesForChannel 获取待发送响应
func GetResponsesForChannel(channel string) ([]*DbResponse, error) {
	rows, err := database.Query(`SELECT id, message_id, channel, sender, sender_id, message, original_message, agent, files, metadata, status, created_at, acked_at FROM responses WHERE status = 'pending' AND channel = ? ORDER BY created_at ASC`, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var responses []*DbResponse
	for rows.Next() {
		r := &DbResponse{}
		err := rows.Scan(&r.ID, &r.MessageID, &r.Channel, &r.Sender, &r.SenderID, &r.Message, &r.OriginalMessage, &r.Agent, &r.Files, &r.Metadata, &r.Status, &r.CreatedAt, &r.AckedAt)
		if err != nil {
			continue
		}
		responses = append(responses, r)
	}
	return responses, nil
}

// AckResponse 确认响应已送达
func AckResponse(responseID int64) {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UnixMilli()
	database.Exec("UPDATE responses SET status = 'acked', acked_at = ? WHERE id = ?", now, responseID)
}

// GetRecentResponses 获取最近响应
func GetRecentResponses(limit int) ([]*DbResponse, error) {
	rows, err := database.Query(`SELECT id, message_id, channel, sender, sender_id, message, original_message, agent, files, metadata, status, created_at, acked_at FROM responses ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var responses []*DbResponse
	for rows.Next() {
		r := &DbResponse{}
		rows.Scan(&r.ID, &r.MessageID, &r.Channel, &r.Sender, &r.SenderID, &r.Message, &r.OriginalMessage, &r.Agent, &r.Files, &r.Metadata, &r.Status, &r.CreatedAt, &r.AckedAt)
		responses = append(responses, r)
	}
	return responses, nil
}

// GetQueueStatus 获取队列状态
func GetQueueStatus() *QueueStatus {
	status := &QueueStatus{}
	database.QueryRow("SELECT COUNT(*) FROM messages WHERE status = 'pending'").Scan(&status.Pending)
	database.QueryRow("SELECT COUNT(*) FROM messages WHERE status = 'processing'").Scan(&status.Processing)
	database.QueryRow("SELECT COUNT(*) FROM messages WHERE status = 'completed'").Scan(&status.Completed)
	database.QueryRow("SELECT COUNT(*) FROM messages WHERE status = 'dead'").Scan(&status.Dead)
	database.QueryRow("SELECT COUNT(*) FROM responses WHERE status = 'pending'").Scan(&status.ResponsesPending)
	return status
}

// GetDeadMessages 获取死信消息
func GetDeadMessages() ([]*DbMessage, error) {
	rows, err := database.Query(`SELECT id, message_id, channel, sender, sender_id, message, agent, files, conversation_id, from_agent, status, retry_count, last_error, created_at, updated_at, claimed_by FROM messages WHERE status = 'dead' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*DbMessage
	for rows.Next() {
		m := &DbMessage{}
		rows.Scan(&m.ID, &m.MessageID, &m.Channel, &m.Sender, &m.SenderID, &m.Message, &m.Agent, &m.Files, &m.ConversationID, &m.FromAgent, &m.Status, &m.RetryCount, &m.LastError, &m.CreatedAt, &m.UpdatedAt, &m.ClaimedBy)
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// GetProcessingMessages 获取正在处理中的消息
func GetProcessingMessages() ([]*DbMessage, error) {
	rows, err := database.Query(`SELECT id, message_id, channel, sender, sender_id, message, agent, files, conversation_id, from_agent, status, retry_count, last_error, created_at, updated_at, claimed_by FROM messages WHERE status = 'processing' ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*DbMessage
	for rows.Next() {
		m := &DbMessage{}
		rows.Scan(&m.ID, &m.MessageID, &m.Channel, &m.Sender, &m.SenderID, &m.Message, &m.Agent, &m.Files, &m.ConversationID, &m.FromAgent, &m.Status, &m.RetryCount, &m.LastError, &m.CreatedAt, &m.UpdatedAt, &m.ClaimedBy)
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// RetryDeadMessage 重试死信消息
func RetryDeadMessage(rowID int64) bool {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UnixMilli()
	result, err := database.Exec("UPDATE messages SET status = 'pending', retry_count = 0, claimed_by = NULL, updated_at = ? WHERE id = ? AND status = 'dead'", now, rowID)
	if err != nil || result == nil {
		return false
	}
	affected, _ := result.RowsAffected()
	return affected > 0
}

// DeleteDeadMessage 删除死信消息
func DeleteDeadMessage(rowID int64) bool {
	dbMu.Lock()
	defer dbMu.Unlock()
	result, err := database.Exec("DELETE FROM messages WHERE id = ? AND status = 'dead'", rowID)
	if err != nil || result == nil {
		return false
	}
	affected, _ := result.RowsAffected()
	return affected > 0
}

// RecoverStaleMessages 恢复陈旧的 processing 消息
// 外部消息：恢复到 pending 重新处理
// 内部消息：检查会话是否仍活跃，活跃则恢复，不活跃则标记 completed
func RecoverStaleMessages(thresholdMs ...int64) int {
	dbMu.Lock()
	defer dbMu.Unlock()

	threshold := int64(10 * 60 * 1000) // 默认 10 分钟
	if len(thresholdMs) > 0 {
		threshold = thresholdMs[0]
	}

	cutoff := time.Now().UnixMilli() - threshold
	now := time.Now().UnixMilli()

	// 查出所有陈旧消息及其 conversation_id
	rows, err := database.Query(
		"SELECT id, agent, from_agent, conversation_id, SUBSTR(message, 1, 80) FROM messages WHERE status = 'processing' AND updated_at < ?", cutoff)
	if err != nil {
		return 0
	}

	// ⚠️ 先收集所有行数据并关闭游标，再做后续查询
	// 原因：MaxOpenConns=1 时，rows 持有唯一连接，嵌套 QueryRow 会死锁
	type staleMsg struct {
		id         int64
		agent      string
		fromAgent  string
		convID     string
		isInternal bool
		detail     string
	}
	var staleMsgs []staleMsg

	for rows.Next() {
		var id int64
		var agent, fromAgent, convID, msgPreview NullString
		rows.Scan(&id, &agent, &fromAgent, &convID, &msgPreview)

		detail := fmt.Sprintf("id=%d agent=%s", id, agent.String)
		if fromAgent.Valid && fromAgent.String != "" {
			detail += fmt.Sprintf(" from=@%s", fromAgent.String)
		}

		isInternal := convID.Valid && convID.String != ""
		cid := ""
		if convID.Valid {
			cid = convID.String
		}

		staleMsgs = append(staleMsgs, staleMsg{
			id: id, agent: agent.String, fromAgent: fromAgent.String,
			convID: cid, isInternal: isInternal, detail: detail,
		})
	}
	rows.Close() // 必须先关闭游标释放连接，再执行后续查询

	var recoverIDs []int64  // 外部消息 + 活跃会话内部消息 → 恢复
	var completeIDs []int64 // 无活跃会话的内部消息 → 完成

	for _, m := range staleMsgs {
		if !m.isInternal {
			// 外部消息：直接恢复
			recoverIDs = append(recoverIDs, m.id)
			logging.Log("WARN", fmt.Sprintf("恢复陈旧消息（外部）: %s", m.detail))
		} else {
			// 内部消息：检查会话是否仍活跃（游标已关闭，连接可用）
			var activeCount int
			database.QueryRow("SELECT COUNT(*) FROM active_conversations WHERE id = ?", m.convID).Scan(&activeCount)
			if activeCount > 0 {
				recoverIDs = append(recoverIDs, m.id)
				logging.Log("WARN", fmt.Sprintf("恢复陈旧消息（内部，会话活跃）: %s conv=%s", m.detail, m.convID))
			} else {
				completeIDs = append(completeIDs, m.id)
				logging.Log("INFO", fmt.Sprintf("丢弃陈旧消息（内部，会话已结束）: %s conv=%s", m.detail, m.convID))
			}
		}
	}

	// 恢复到 pending
	for _, id := range recoverIDs {
		database.Exec("UPDATE messages SET status = 'pending', claimed_by = NULL, updated_at = ? WHERE id = ?", now, id)
	}
	// 无活跃会话的内部消息标记为 completed
	for _, id := range completeIDs {
		database.Exec("UPDATE messages SET status = 'completed', updated_at = ? WHERE id = ?", now, id)
	}

	return len(recoverIDs)
}

// RecoverAgentMessages 恢复指定 agent 的所有 processing 消息
func RecoverAgentMessages(agentID string) int {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	result, err := database.Exec(
		"UPDATE messages SET status = 'pending', claimed_by = NULL, updated_at = ? WHERE status = 'processing' AND agent = ?",
		now, agentID)
	if err != nil {
		return 0
	}
	affected, _ := result.RowsAffected()
	return int(affected)
}

// ResetMessageToPending 将指定消息重置为 pending（不增加 retry_count）
// 用于手动强杀后让消息重新被消费
func ResetMessageToPending(rowID int64) {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UnixMilli()
	database.Exec("UPDATE messages SET status = 'pending', claimed_by = NULL, updated_at = ? WHERE id = ?", now, rowID)
}

// DiscardProcessingMessage 丢弃 processing 消息（标记为 completed）
func DiscardProcessingMessage(rowID int64) {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UnixMilli()
	database.Exec("UPDATE messages SET status = 'completed', claimed_by = NULL, updated_at = ? WHERE id = ? AND status = 'processing'", now, rowID)
}

// PruneAckedResponses 清理已确认响应
func PruneAckedResponses(olderThanMs ...int64) int {
	dbMu.Lock()
	defer dbMu.Unlock()

	threshold := int64(24 * 60 * 60 * 1000) // 默认 24 小时
	if len(olderThanMs) > 0 {
		threshold = olderThanMs[0]
	}

	cutoff := time.Now().UnixMilli() - threshold
	result, err := database.Exec("DELETE FROM responses WHERE status = 'acked' AND acked_at < ?", cutoff)
	if err != nil || result == nil {
		return 0
	}
	affected, _ := result.RowsAffected()
	return int(affected)
}

// PruneCompletedMessages 清理已完成消息
func PruneCompletedMessages(olderThanMs ...int64) int {
	dbMu.Lock()
	defer dbMu.Unlock()

	threshold := int64(24 * 60 * 60 * 1000)
	if len(olderThanMs) > 0 {
		threshold = olderThanMs[0]
	}

	cutoff := time.Now().UnixMilli() - threshold
	result, err := database.Exec("DELETE FROM messages WHERE status = 'completed' AND updated_at < ?", cutoff)
	if err != nil || result == nil {
		return 0
	}
	affected, _ := result.RowsAffected()
	return int(affected)
}

// GetPendingAgents 获取有待处理消息的 Agent 列表
func GetPendingAgents() []string {
	rows, err := database.Query("SELECT DISTINCT COALESCE(agent, 'default') FROM messages WHERE status = 'pending'")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var agents []string
	for rows.Next() {
		var agent string
		rows.Scan(&agent)
		agents = append(agents, agent)
	}
	return agents
}

// CloseQueueDb 关闭数据库
func CloseQueueDb() {
	if database != nil {
		database.Close()
	}
}

// --- 辅助函数 ---

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// GetMessagesByConversation 查询某会话的所有内部消息（按 id 顺序）
func GetMessagesByConversation(conversationID string) ([]*DbMessage, error) {
	rows, err := database.Query(`
		SELECT id, message_id, channel, sender, sender_id, message, agent, files, conversation_id, from_agent, status, retry_count, last_error, created_at, updated_at, claimed_by
		FROM messages WHERE conversation_id = ? ORDER BY id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*DbMessage
	for rows.Next() {
		m := &DbMessage{}
		rows.Scan(&m.ID, &m.MessageID, &m.Channel, &m.Sender, &m.SenderID, &m.Message, &m.Agent, &m.Files, &m.ConversationID, &m.FromAgent, &m.Status, &m.RetryCount, &m.LastError, &m.CreatedAt, &m.UpdatedAt, &m.ClaimedBy)
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func toJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// === 会话历史 API ===

// ConvHistory 会话历史记录
type ConvHistory struct {
	ID              string `json:"id"`
	TeamID          string `json:"team_id"`
	TeamName        string `json:"team_name"`
	Channel         string `json:"channel"`
	Sender          string `json:"sender"`
	OriginalMessage string `json:"original_message"`
	Agents          string `json:"agents"`
	TotalMessages   int    `json:"total_messages"`
	StartedAt       int64  `json:"started_at"`
	EndedAt         int64  `json:"ended_at"`
	ChatFile        string `json:"chat_file"`
	DurationSec     int    `json:"duration_sec"`
	TotalRounds     int    `json:"total_rounds"`
}

// ConvStep 会话中的一步
type ConvStep struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	Seq            int    `json:"seq"`
	FromAgent      string `json:"from_agent"`
	ToAgent        string `json:"to_agent"`
	Message        string `json:"message"`
	MessageType    string `json:"message_type"`
	Timestamp      int64  `json:"timestamp"`
}

// ConvEvent 会话事件
type ConvEvent struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	EventType      string `json:"event_type"`
	EventData      string `json:"event_data"`
	Timestamp      int64  `json:"timestamp"`
}

// SaveConversationHistory 保存会话历史
func SaveConversationHistory(h *ConvHistory) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	_, err := database.Exec(`
		INSERT OR REPLACE INTO conversation_history (id, team_id, team_name, channel, sender, original_message, agents, total_messages, started_at, ended_at, chat_file, duration_sec, total_rounds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.TeamID, h.TeamName, h.Channel, h.Sender, h.OriginalMessage, h.Agents, h.TotalMessages, h.StartedAt, h.EndedAt, h.ChatFile, h.DurationSec, h.TotalRounds)
	return err
}

// SaveConversationStep 保存会话步骤
func SaveConversationStep(step *ConvStep) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	_, err := database.Exec(`
		INSERT INTO conversation_steps (conversation_id, seq, from_agent, to_agent, message, message_type, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		step.ConversationID, step.Seq, step.FromAgent, step.ToAgent, step.Message, step.MessageType, step.Timestamp)
	return err
}

// SaveConversationEvent 保存会话事件
func SaveConversationEvent(convID, eventType string, eventData map[string]interface{}) {
	dbMu.Lock()
	defer dbMu.Unlock()
	now := time.Now().UnixMilli()
	dataJSON := toJSON(eventData)
	database.Exec(`
		INSERT INTO conversation_events (conversation_id, event_type, event_data, timestamp)
		VALUES (?, ?, ?, ?)`,
		convID, eventType, dataJSON, now)
}

// GetConversationHistoryList 查询会话历史列表
func GetConversationHistoryList(teamID string, limit, offset int) ([]*ConvHistory, error) {
	var query string
	var args []interface{}

	if teamID != "" {
		query = `SELECT id, team_id, team_name, channel, sender, original_message, agents, total_messages, started_at, COALESCE(ended_at, 0), COALESCE(chat_file, ''), COALESCE(duration_sec, 0), COALESCE(total_rounds, 0)
			FROM conversation_history WHERE team_id = ? ORDER BY started_at DESC LIMIT ? OFFSET ?`
		args = []interface{}{teamID, limit, offset}
	} else {
		query = `SELECT id, team_id, team_name, channel, sender, original_message, agents, total_messages, started_at, COALESCE(ended_at, 0), COALESCE(chat_file, ''), COALESCE(duration_sec, 0), COALESCE(total_rounds, 0)
			FROM conversation_history ORDER BY started_at DESC LIMIT ? OFFSET ?`
		args = []interface{}{limit, offset}
	}

	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*ConvHistory
	for rows.Next() {
		h := &ConvHistory{}
		rows.Scan(&h.ID, &h.TeamID, &h.TeamName, &h.Channel, &h.Sender, &h.OriginalMessage, &h.Agents, &h.TotalMessages, &h.StartedAt, &h.EndedAt, &h.ChatFile, &h.DurationSec, &h.TotalRounds)
		list = append(list, h)
	}
	return list, nil
}

// GetConversationDetail 查询单条会话详情（含步骤）
func GetConversationDetail(convID string) (*ConvHistory, []*ConvStep, []*ConvEvent, error) {
	// 查询历史（可能不存在——活跃会话还没写入 conversation_history）
	h := &ConvHistory{}
	err := database.QueryRow(`
		SELECT id, team_id, team_name, channel, sender, original_message, agents, total_messages, started_at, COALESCE(ended_at, 0), COALESCE(chat_file, ''), COALESCE(duration_sec, 0), COALESCE(total_rounds, 0)
		FROM conversation_history WHERE id = ?`, convID).Scan(
		&h.ID, &h.TeamID, &h.TeamName, &h.Channel, &h.Sender, &h.OriginalMessage, &h.Agents, &h.TotalMessages, &h.StartedAt, &h.EndedAt, &h.ChatFile, &h.DurationSec, &h.TotalRounds)
	if err != nil {
		// 找不到 history 记录 — 可能是活跃会话，用空 history 继续查 steps/events
		h = &ConvHistory{ID: convID}
	}

	// 查询步骤（显式关闭，避免与后续查询并发持有连接）
	var steps []*ConvStep
	{
		stepRows, err := database.Query(`
			SELECT id, conversation_id, seq, COALESCE(from_agent, ''), COALESCE(to_agent, ''), COALESCE(message, ''), COALESCE(message_type, ''), timestamp
			FROM conversation_steps WHERE conversation_id = ? ORDER BY seq ASC`, convID)
		if err == nil {
			for stepRows.Next() {
				s := &ConvStep{}
				stepRows.Scan(&s.ID, &s.ConversationID, &s.Seq, &s.FromAgent, &s.ToAgent, &s.Message, &s.MessageType, &s.Timestamp)
				steps = append(steps, s)
			}
			stepRows.Close()
		}
	}

	// 查询事件（同样显式关闭）
	var events []*ConvEvent
	{
		eventRows, err := database.Query(`
			SELECT id, conversation_id, event_type, COALESCE(event_data, ''), timestamp
			FROM conversation_events WHERE conversation_id = ? ORDER BY timestamp ASC`, convID)
		if err == nil {
			for eventRows.Next() {
				e := &ConvEvent{}
				eventRows.Scan(&e.ID, &e.ConversationID, &e.EventType, &e.EventData, &e.Timestamp)
				events = append(events, e)
			}
			eventRows.Close()
		}
	}

	return h, steps, events, nil
}

// ActiveConversationRow 活跃会话的 DB 行
type ActiveConversationRow struct {
	ID                 string
	Channel            string
	Sender             string
	OriginalMessage    string
	MessageID          string
	TeamID             string
	PendingAgents      string // JSON
	Responses          string // JSON
	Files              string // JSON
	TotalMessages      int
	MaxMessages        int
	StartTime          int64
	OutgoingMentions   string // JSON
	PairExchanges      string // JSON
	ParticipatedAgents string // JSON
}

// SaveActiveConversation 持久化活跃会话状态
func SaveActiveConversation(row *ActiveConversationRow) {
	dbMu.Lock()
	defer dbMu.Unlock()
	if database == nil {
		return
	}
	now := time.Now().UnixMilli()
	database.Exec(`
		INSERT INTO active_conversations (id, channel, sender, original_message, message_id, team_id, pending_agents, responses, files, total_messages, max_messages, start_time, outgoing_mentions, pair_exchanges, participated_agents, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			pending_agents = excluded.pending_agents,
			responses = excluded.responses,
			files = excluded.files,
			total_messages = excluded.total_messages,
			outgoing_mentions = excluded.outgoing_mentions,
			pair_exchanges = excluded.pair_exchanges,
			participated_agents = excluded.participated_agents,
			updated_at = excluded.updated_at`,
		row.ID, row.Channel, row.Sender, row.OriginalMessage, row.MessageID,
		row.TeamID, row.PendingAgents, row.Responses, row.Files,
		row.TotalMessages, row.MaxMessages, row.StartTime,
		row.OutgoingMentions, row.PairExchanges, row.ParticipatedAgents, now,
	)
}

// LoadActiveConversations 加载所有活跃会话（启动时恢复用）
func LoadActiveConversations() []*ActiveConversationRow {
	dbMu.Lock()
	defer dbMu.Unlock()
	if database == nil {
		return nil
	}
	rows, err := database.Query(`SELECT id, channel, sender, original_message, message_id, team_id, pending_agents, responses, files, total_messages, max_messages, start_time, outgoing_mentions, pair_exchanges, participated_agents FROM active_conversations`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []*ActiveConversationRow
	for rows.Next() {
		r := &ActiveConversationRow{}
		rows.Scan(&r.ID, &r.Channel, &r.Sender, &r.OriginalMessage, &r.MessageID,
			&r.TeamID, &r.PendingAgents, &r.Responses, &r.Files,
			&r.TotalMessages, &r.MaxMessages, &r.StartTime,
			&r.OutgoingMentions, &r.PairExchanges, &r.ParticipatedAgents)
		result = append(result, r)
	}
	return result
}

// DeleteActiveConversation 删除活跃会话记录（会话完成后清理）
func DeleteActiveConversation(convID string) {
	dbMu.Lock()
	defer dbMu.Unlock()
	if database == nil {
		return
	}
	database.Exec("DELETE FROM active_conversations WHERE id = ?", convID)
}

// HasActiveConversation 检查会话是否仍然活跃
func HasActiveConversation(convID string) bool {
	dbMu.Lock()
	defer dbMu.Unlock()
	if database == nil {
		return false
	}
	var count int
	database.QueryRow("SELECT COUNT(*) FROM active_conversations WHERE id = ?", convID).Scan(&count)
	return count > 0
}

// === Agent 执行历史 ===

// AgentExecution 单次 agent 执行记录
type AgentExecution struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	AgentID        string `json:"agent_id"`
	TeamID         string `json:"team_id"`
	DurationSec    int    `json:"duration_sec"`
	StdoutBytes    int    `json:"stdout_bytes"`
	Status         string `json:"status"` // completed, idle_killed, timeout_killed, error
	StartedAt      int64  `json:"started_at"`
	EndedAt        int64  `json:"ended_at"`
}

// AgentStats agent 执行统计
type AgentStats struct {
	AgentID        string `json:"agent_id"`
	TotalSessions  int    `json:"total_sessions"`
	TotalDurationS int    `json:"total_duration_sec"`
	AvgDurationS   int    `json:"avg_duration_sec"`
	TotalStdout    int64  `json:"total_stdout_bytes"`
	LastActiveAt   int64  `json:"last_active_at"`
}

// SaveAgentExecution 保存一次 agent 执行记录
func SaveAgentExecution(e *AgentExecution) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	if database == nil {
		return nil
	}
	_, err := database.Exec(`
		INSERT INTO agent_executions (conversation_id, agent_id, team_id, duration_sec, stdout_bytes, status, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nullStr(e.ConversationID), e.AgentID, nullStr(e.TeamID),
		e.DurationSec, e.StdoutBytes, e.Status, e.StartedAt, e.EndedAt)
	return err
}

// GetAgentStatsList 获取所有 agent 的执行统计
func GetAgentStatsList() ([]*AgentStats, error) {
	if database == nil {
		return nil, nil
	}
	rows, err := database.Query(`
		SELECT agent_id, COUNT(*), COALESCE(SUM(duration_sec), 0), CAST(ROUND(COALESCE(AVG(duration_sec), 0)) AS INTEGER), COALESCE(SUM(stdout_bytes), 0), COALESCE(MAX(ended_at), 0)
		FROM agent_executions
		GROUP BY agent_id
		ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*AgentStats
	for rows.Next() {
		s := &AgentStats{}
		rows.Scan(&s.AgentID, &s.TotalSessions, &s.TotalDurationS, &s.AvgDurationS, &s.TotalStdout, &s.LastActiveAt)
		list = append(list, s)
	}
	return list, nil
}

// GetRecentAgentExecutions 获取最近的 agent 执行记录
func GetRecentAgentExecutions(limit int) ([]*AgentExecution, error) {
	if database == nil {
		return nil, nil
	}
	rows, err := database.Query(`
		SELECT id, COALESCE(conversation_id, ''), agent_id, COALESCE(team_id, ''), duration_sec, stdout_bytes, status, started_at, COALESCE(ended_at, 0)
		FROM agent_executions ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*AgentExecution
	for rows.Next() {
		e := &AgentExecution{}
		rows.Scan(&e.ID, &e.ConversationID, &e.AgentID, &e.TeamID, &e.DurationSec, &e.StdoutBytes, &e.Status, &e.StartedAt, &e.EndedAt)
		list = append(list, e)
	}
	return list, nil
}
