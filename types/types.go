package types

// AgentConfig 定义单个 Agent 的配置
type AgentConfig struct {
	Name             string            `json:"name"`
	Provider         string            `json:"provider"`                    // "anthropic", "openai", "opencode", "gemini"
	Model            string            `json:"model"`                       // e.g. "sonnet", "opus", "gpt-5.3-codex"
	FallbackProvider string            `json:"fallback_provider,omitempty"` // 备用 provider，主 provider 失败时自动切换
	FallbackModel    string            `json:"fallback_model,omitempty"`    // 备用 model
	WorkingDirectory string            `json:"working_directory"`
	SystemPrompt     string            `json:"system_prompt,omitempty"`
	PromptFile       string            `json:"prompt_file,omitempty"`
	Env              map[string]string `json:"env,omitempty"` // Agent 级环境变量
}

// TeamConfig 定义团队配置
type TeamConfig struct {
	Name             string   `json:"name"`
	Agents           []string `json:"agents"`
	LeaderAgent      string   `json:"leader_agent"`
	ProjectDirectory string   `json:"project_directory,omitempty"` // 团队共享项目目录
}

// ProjectConfig 项目配置
type ProjectConfig struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	RepoPath    string   `json:"repo_path"`
	Teams       []string `json:"teams"`
}

// TaskStatus 看板任务状态
type TaskStatus string

const (
	TaskStatusBacklog    TaskStatus = "backlog"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusReview     TaskStatus = "review"
	TaskStatusDone       TaskStatus = "done"
)

// Task 看板任务
type Task struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Status       TaskStatus `json:"status"`
	Assignee     string     `json:"assignee"`      // agent 或 team id
	AssigneeType string     `json:"assignee_type"` // "agent", "team", ""
	CreatedAt    int64      `json:"created_at"`
	UpdatedAt    int64      `json:"updated_at"`
}

// Settings 全局配置
type Settings struct {
	Workspace           *WorkspaceConfig          `json:"workspace,omitempty"`
	Models              *ModelsConfig             `json:"models,omitempty"`
	Agents              map[string]*AgentConfig   `json:"agents,omitempty"`
	Teams               map[string]*TeamConfig    `json:"teams,omitempty"`
	Projects            map[string]*ProjectConfig `json:"projects,omitempty"`
	Monitoring          *MonitoringConfig         `json:"monitoring,omitempty"`
	Env                 map[string]string         `json:"env,omitempty"`                  // 全局环境变量
	MaxMessages         int                       `json:"max_messages,omitempty"`         // 会话最大消息数，默认 500
	AgentTimeout        int                       `json:"agent_timeout,omitempty"`        // Agent 绝对超时（分钟），默认 60
	AgentIdleTimeout    int                       `json:"agent_idle_timeout,omitempty"`   // Agent 空闲超时（分钟），无输出判定卡死，默认 5
	ConversationTimeout int                       `json:"conversation_timeout,omitempty"` // 会话超时（分钟），默认 15
	CompactionThreshold int                       `json:"compaction_threshold,omitempty"` // 消息压缩阈值（字符数），默认 8000
}

type WorkspaceConfig struct {
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

type ModelsConfig struct {
	Provider  string         `json:"provider,omitempty"` // "anthropic", "openai", "opencode"
	Anthropic *ProviderModel `json:"anthropic,omitempty"`
	OpenAI    *ProviderModel `json:"openai,omitempty"`
	OpenCode  *ProviderModel `json:"opencode,omitempty"`
}

type ProviderModel struct {
	Model string `json:"model,omitempty"`
}

type MonitoringConfig struct {
	HeartbeatInterval int `json:"heartbeat_interval,omitempty"` // 秒
}

// ChainStep 团队会话中的一步
type ChainStep struct {
	AgentID  string `json:"agent_id"`
	Response string `json:"response"`
}

// Conversation 活跃的团队会话
type Conversation struct {
	ID                 string
	Channel            string
	Sender             string
	OriginalMessage    string
	MessageID          string
	PendingAgents      map[string]int // agent ID → 引用计数（唯一 pending truth）
	Responses          []ChainStep
	Files              map[string]struct{} // set
	TotalMessages      int
	MaxMessages        int
	TeamContext        *TeamContext
	StartTime          int64
	OutgoingMentions   map[string]int
	PairExchanges      map[string]int  // "agentA→agentB" → 交互次数（循环检测）
	ParticipatedAgents map[string]bool // 所有参与过会话的 agent（用于 team_chain_end）
	LeaderSummaryDone  bool            // Leader 总结是否已完成（防止重复触发）
}

// TeamContext 团队上下文
type TeamContext struct {
	TeamID string
	Team   *TeamConfig
}

// MessageData 消息数据
type MessageData struct {
	Channel        string   `json:"channel"`
	Sender         string   `json:"sender"`
	SenderID       string   `json:"sender_id,omitempty"`
	Message        string   `json:"message"`
	Timestamp      int64    `json:"timestamp"`
	MessageID      string   `json:"message_id"`
	Agent          string   `json:"agent,omitempty"`
	Files          []string `json:"files,omitempty"`
	ConversationID string   `json:"conversation_id,omitempty"`
	FromAgent      string   `json:"from_agent,omitempty"`
}

// ResponseData 响应数据
type ResponseData struct {
	Channel         string                 `json:"channel"`
	Sender          string                 `json:"sender"`
	SenderID        string                 `json:"sender_id,omitempty"`
	Message         string                 `json:"message"`
	OriginalMessage string                 `json:"original_message"`
	Timestamp       int64                  `json:"timestamp"`
	MessageID       string                 `json:"message_id"`
	Agent           string                 `json:"agent,omitempty"`
	Files           []string               `json:"files,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// 模型映射
var ClaudeModelIDs = map[string]string{
	"sonnet":            "claude-sonnet-4-6",
	"opus":              "claude-opus-4-6",
	"haiku":             "claude-haiku-4-5",
	"claude-sonnet-4-6": "claude-sonnet-4-6",
	"claude-sonnet-4-5": "claude-sonnet-4-5",
	"claude-opus-4-6":   "claude-opus-4-6",
	"claude-opus-4-5":   "claude-opus-4-5",
	"claude-haiku-4-5":  "claude-haiku-4-5",
}

var CodexModelIDs = map[string]string{
	"gpt-5.3-codex":       "gpt-5.3-codex",
	"gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
	"gpt-5.2":             "gpt-5.2",
	"codex-mini":          "codex-mini-latest",
	"o4-mini":             "o4-mini",
}

var OpenCodeModelIDs = map[string]string{
	"opencode/claude-opus-4-6":    "opencode/claude-opus-4-6",
	"opencode/claude-sonnet-4-5":  "opencode/claude-sonnet-4-5",
	"opencode/gemini-3-flash":     "opencode/gemini-3-flash",
	"opencode/gemini-3-pro":       "opencode/gemini-3-pro",
	"opencode/glm-5":              "opencode/glm-5",
	"opencode/kimi-k2.5":          "opencode/kimi-k2.5",
	"opencode/kimi-k2.5-free":     "opencode/kimi-k2.5-free",
	"opencode/minimax-m2.5":       "opencode/minimax-m2.5",
	"opencode/minimax-m2.5-free":  "opencode/minimax-m2.5-free",
	"anthropic/claude-opus-4-6":   "anthropic/claude-opus-4-6",
	"anthropic/claude-sonnet-4-5": "anthropic/claude-sonnet-4-5",
	"openai/gpt-5.2":              "openai/gpt-5.2",
	"openai/gpt-5.3-codex":        "openai/gpt-5.3-codex",
	"openai/gpt-5.3-codex-spark":  "openai/gpt-5.3-codex-spark",
	"sonnet":                      "opencode/claude-sonnet-4-5",
	"opus":                        "opencode/claude-opus-4-6",
}

// Gemini CLI 模型别名
var GeminiModelIDs = map[string]string{
	"flash":                  "gemini-2.5-flash",
	"pro":                    "gemini-2.5-pro",
	"gemini-3.1-pro-preview": "gemini-3.1-pro-preview",
	"gemini-3-flash":         "gemini-3-flash",
	"gemini-2.5-pro":         "gemini-2.5-pro",
	"gemini-2.5-flash":       "gemini-2.5-flash",
	"gemini-2.5-flash-lite":  "gemini-2.5-flash-lite",
}
