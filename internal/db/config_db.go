package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/queenbee-ai/queenbee/internal/logging"
)

// ═══════════════════════════════════════════════════════════════════
// 配置存储层 — Agent / Team / Skills / GlobalConfig 的数据库 CRUD
// 替代 settings.json 文件存储，提供并发安全的持久化
// ═══════════════════════════════════════════════════════════════════

// === 数据结构 ===

// DbAgent 数据库中的 Agent 配置
type DbAgent struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Provider         string            `json:"provider"`
	Model            string            `json:"model"`
	FallbackProvider string            `json:"fallback_provider,omitempty"`
	FallbackModel    string            `json:"fallback_model,omitempty"`
	WorkingDirectory string            `json:"working_directory"`
	SystemPrompt     string            `json:"system_prompt,omitempty"`
	PromptFile       string            `json:"prompt_file,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	CreatedAt        int64             `json:"created_at"`
	UpdatedAt        int64             `json:"updated_at"`
}

// DbTeam 数据库中的 Team 配置
type DbTeam struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	LeaderAgent      string   `json:"leader_agent"`
	ProjectDirectory string   `json:"project_directory,omitempty"`
	Agents           []string `json:"agents"`
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
}

// DbProject 项目
type DbProject struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	RepoPath    string   `json:"repo_path"`
	Teams       []string `json:"teams"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// DbSkill Agent 关联的技能
type DbSkill struct {
	ID          int64  `json:"id"`
	AgentID     string `json:"agent_id"`
	SkillName   string `json:"skill_name"`
	SkillPath   string `json:"skill_path,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"` // "builtin" 或 "custom"
	Enabled     bool   `json:"enabled"`
	CreatedAt   int64  `json:"created_at"`
}

// DbSkillDefinition 技能定义（存储在数据库中，全 API 管理）
type DbSkillDefinition struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Content      string `json:"content"`       // SKILL.md 完整内容
	AllowedTools string `json:"allowed_tools"` // 允许的工具，如 Bash(agent-browser:*)
	Source       string `json:"source"`        // "builtin" / "custom" / "community"
	Category     string `json:"category"`      // 分类标签
	Version      string `json:"version"`       // 版本号
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// DbGlobalConfig 全局配置键值对
type DbGlobalConfig struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt int64  `json:"updated_at"`
}

// === 建表 ===

// InitConfigTables 创建配置相关的数据库表
// 在 InitQueueDb 中调用
func InitConfigTables() error {
	if database == nil {
		return fmt.Errorf("数据库未初始化")
	}

	_, err := database.Exec(`
		-- Agent 配置表
		CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT 'anthropic',
			model TEXT NOT NULL DEFAULT 'sonnet',
			working_directory TEXT DEFAULT '',
			system_prompt TEXT DEFAULT '',
			prompt_file TEXT DEFAULT '',
			env TEXT DEFAULT '{}',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);

		-- Team 配置表
		CREATE TABLE IF NOT EXISTS teams (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			leader_agent TEXT DEFAULT '',
			project_directory TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);

		-- Team 成员关系表
		CREATE TABLE IF NOT EXISTS team_agents (
			team_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			sort_order INTEGER DEFAULT 0,
			PRIMARY KEY (team_id, agent_id)
		);
		CREATE INDEX IF NOT EXISTS idx_team_agents_team ON team_agents(team_id);

		-- Project 项目表
		CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT DEFAULT '',
			repo_path TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);

		-- Project 与 Team 的关联表
		CREATE TABLE IF NOT EXISTS project_teams (
			project_id TEXT NOT NULL,
			team_id TEXT NOT NULL,
			PRIMARY KEY (project_id, team_id)
		);
		CREATE INDEX IF NOT EXISTS idx_project_teams_project ON project_teams(project_id);
		CREATE INDEX IF NOT EXISTS idx_project_teams_team ON project_teams(team_id);

		-- Agent 技能表
		CREATE TABLE IF NOT EXISTS agent_skills (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			skill_name TEXT NOT NULL,
			skill_path TEXT DEFAULT '',
			description TEXT DEFAULT '',
			source TEXT NOT NULL DEFAULT 'builtin',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			UNIQUE(agent_id, skill_name)
		);
		CREATE INDEX IF NOT EXISTS idx_agent_skills_agent ON agent_skills(agent_id);

		-- 技能定义表（全 API 管理，不依赖文件系统）
		CREATE TABLE IF NOT EXISTS skill_definitions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			description TEXT DEFAULT '',
			content TEXT DEFAULT '',
			allowed_tools TEXT DEFAULT '',
			source TEXT NOT NULL DEFAULT 'custom',
			category TEXT DEFAULT '',
			version TEXT DEFAULT '1.0.0',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);

		-- 全局配置表（key-value）
		CREATE TABLE IF NOT EXISTS global_config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("创建配置表失败: %w", err)
	}

	logging.Log("INFO", "配置数据库表已初始化")

	// 数据库迁移：增加 fallback_provider 和 fallback_model 列（已有表兼容）
	database.Exec("ALTER TABLE agents ADD COLUMN fallback_provider TEXT DEFAULT ''")
	database.Exec("ALTER TABLE agents ADD COLUMN fallback_model TEXT DEFAULT ''")

	// 迁移：Agent 只能属于一个团队，给 agent_id 加唯一索引
	database.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_team_agents_agent_unique ON team_agents(agent_id)")

	// 清理测试遗留的无用字段（DROP COLUMN 需要 SQLite ≥ 3.35.0）
	database.Exec("ALTER TABLE teams DROP COLUMN mode")
	database.Exec("ALTER TABLE teams DROP COLUMN project_settings")
	database.Exec("ALTER TABLE projects DROP COLUMN git_provider")
	database.Exec("ALTER TABLE projects DROP COLUMN git_repo")
	database.Exec("ALTER TABLE projects DROP COLUMN poll_interval")

	return nil
}

// === Agent CRUD ===

// CreateAgent 创建 Agent
func CreateAgent(agent *DbAgent) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	agent.CreatedAt = now
	agent.UpdatedAt = now

	envJSON := "{}"
	if agent.Env != nil {
		data, _ := json.Marshal(agent.Env)
		envJSON = string(data)
	}

	_, err := database.Exec(`
		INSERT INTO agents (id, name, provider, model, fallback_provider, fallback_model, working_directory, system_prompt, prompt_file, env, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agent.ID, agent.Name, agent.Provider, agent.Model,
		agent.FallbackProvider, agent.FallbackModel,
		agent.WorkingDirectory, agent.SystemPrompt, agent.PromptFile,
		envJSON, now, now,
	)
	return err
}

// GetAgent 获取单个 Agent
func GetAgent(id string) (*DbAgent, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	agent := &DbAgent{ID: id}
	var envJSON string
	err := database.QueryRow(`
		SELECT name, provider, model, 
			   COALESCE(fallback_provider, ''), COALESCE(fallback_model, ''),
			   working_directory, 
			   COALESCE(system_prompt, ''), COALESCE(prompt_file, ''), 
			   COALESCE(env, '{}'), created_at, updated_at
		FROM agents WHERE id = ?`, id).Scan(
		&agent.Name, &agent.Provider, &agent.Model,
		&agent.FallbackProvider, &agent.FallbackModel,
		&agent.WorkingDirectory,
		&agent.SystemPrompt, &agent.PromptFile,
		&envJSON, &agent.CreatedAt, &agent.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if envJSON != "" && envJSON != "{}" {
		json.Unmarshal([]byte(envJSON), &agent.Env)
	}

	return agent, nil
}

// ListAgents 获取所有 Agent
func ListAgents() ([]*DbAgent, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	rows, err := database.Query(`
		SELECT id, name, provider, model, 
			   COALESCE(fallback_provider, ''), COALESCE(fallback_model, ''),
			   working_directory, 
			   COALESCE(system_prompt, ''), COALESCE(prompt_file, ''), 
			   COALESCE(env, '{}'), created_at, updated_at
		FROM agents ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*DbAgent
	for rows.Next() {
		a := &DbAgent{}
		var envJSON string
		rows.Scan(
			&a.ID, &a.Name, &a.Provider, &a.Model,
			&a.FallbackProvider, &a.FallbackModel,
			&a.WorkingDirectory,
			&a.SystemPrompt, &a.PromptFile,
			&envJSON, &a.CreatedAt, &a.UpdatedAt,
		)
		if envJSON != "" && envJSON != "{}" {
			json.Unmarshal([]byte(envJSON), &a.Env)
		}
		agents = append(agents, a)
	}
	return agents, nil
}

// UpdateAgent 更新 Agent
func UpdateAgent(agent *DbAgent) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	agent.UpdatedAt = now

	envJSON := "{}"
	if agent.Env != nil {
		data, _ := json.Marshal(agent.Env)
		envJSON = string(data)
	}

	result, err := database.Exec(`
		UPDATE agents SET name=?, provider=?, model=?, fallback_provider=?, fallback_model=?,
			   working_directory=?, system_prompt=?, prompt_file=?, env=?, updated_at=?
		WHERE id=?`,
		agent.Name, agent.Provider, agent.Model, agent.FallbackProvider, agent.FallbackModel,
		agent.WorkingDirectory,
		agent.SystemPrompt, agent.PromptFile,
		envJSON, now, agent.ID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("agent '%s' 不存在", agent.ID)
	}
	return nil
}

// DeleteAgent 删除 Agent 及其关联数据（skills、team 成员关系）
func DeleteAgent(id string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 删除 Agent
	result, err := tx.Exec("DELETE FROM agents WHERE id = ?", id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("agent '%s' 不存在", id)
	}

	// 清理关联的 skills
	tx.Exec("DELETE FROM agent_skills WHERE agent_id = ?", id)
	// 清理 team 成员关系
	tx.Exec("DELETE FROM team_agents WHERE agent_id = ?", id)

	return tx.Commit()
}

// AgentExists 检查 Agent 是否存在
func AgentExists(id string) bool {
	if database == nil {
		return false
	}
	var count int
	database.QueryRow("SELECT COUNT(*) FROM agents WHERE id = ?", id).Scan(&count)
	return count > 0
}

// AgentCount 返回 Agent 总数
func AgentCount() int {
	if database == nil {
		return 0
	}
	var count int
	database.QueryRow("SELECT COUNT(*) FROM agents").Scan(&count)
	return count
}

// === Team CRUD ===

// CreateTeam 创建 Team（含成员关系）
// 约束：每个 Agent 只能属于一个团队
func CreateTeam(team *DbTeam) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	// 检查 Agent 是否已属于其他团队
	for _, agentID := range team.Agents {
		var existingTeamID, existingTeamName string
		err := database.QueryRow(
			"SELECT ta.team_id, t.name FROM team_agents ta JOIN teams t ON ta.team_id = t.id WHERE ta.agent_id = ?",
			agentID).Scan(&existingTeamID, &existingTeamName)
		if err == nil {
			return fmt.Errorf("Agent '%s' 已属于团队 '%s'，一个 Agent 只能加入一个团队", agentID, existingTeamName)
		}
	}

	now := time.Now().UnixMilli()
	team.CreatedAt = now
	team.UpdatedAt = now

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		INSERT INTO teams (id, name, leader_agent, project_directory, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		team.ID, team.Name, team.LeaderAgent, team.ProjectDirectory, now, now,
	)
	if err != nil {
		return err
	}

	// 插入成员关系
	for i, agentID := range team.Agents {
		_, err = tx.Exec("INSERT INTO team_agents (team_id, agent_id, sort_order) VALUES (?, ?, ?)",
			team.ID, agentID, i)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetTeam 获取单个 Team（含成员列表）
// 使用 LEFT JOIN 一次性查询，避免二段查询的并发竞态
func GetTeam(id string) (*DbTeam, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	rows, err := database.Query(`
		SELECT t.name, t.leader_agent, COALESCE(t.project_directory, ''),
		       t.created_at, t.updated_at, COALESCE(ta.agent_id, '')
		FROM teams t
		LEFT JOIN team_agents ta ON t.id = ta.team_id
		WHERE t.id = ?
		ORDER BY ta.sort_order ASC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	team := &DbTeam{ID: id}
	first := true
	for rows.Next() {
		var agentID string
		if first {
			rows.Scan(&team.Name, &team.LeaderAgent, &team.ProjectDirectory,
				&team.CreatedAt, &team.UpdatedAt, &agentID)
			first = false
		} else {
			var ignore1, ignore2, ignore3 string
			var ignore4, ignore5 int64
			rows.Scan(&ignore1, &ignore2, &ignore3, &ignore4, &ignore5, &agentID)
		}
		if agentID != "" {
			team.Agents = append(team.Agents, agentID)
		}
	}

	if first {
		// 没有任何行 → team 不存在
		return nil, fmt.Errorf("team '%s' 不存在", id)
	}

	return team, nil
}

// ListTeams 获取所有 Team
// 使用 LEFT JOIN 一次性查询，避免二段查询的并发竞态
func ListTeams() ([]*DbTeam, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	rows, err := database.Query(`
		SELECT t.id, t.name, t.leader_agent, COALESCE(t.project_directory, ''),
		       t.created_at, t.updated_at, COALESCE(ta.agent_id, '')
		FROM teams t
		LEFT JOIN team_agents ta ON t.id = ta.team_id
		ORDER BY t.created_at ASC, ta.sort_order ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	teamMap := make(map[string]*DbTeam) // 按 ID 去重
	var teamOrder []string              // 保持插入顺序

	for rows.Next() {
		var id, name, leader, projDir, agentID string
		var createdAt, updatedAt int64
		rows.Scan(&id, &name, &leader, &projDir, &createdAt, &updatedAt, &agentID)

		t, exists := teamMap[id]
		if !exists {
			t = &DbTeam{
				ID:               id,
				Name:             name,
				LeaderAgent:      leader,
				ProjectDirectory: projDir,
				CreatedAt:        createdAt,
				UpdatedAt:        updatedAt,
			}
			teamMap[id] = t
			teamOrder = append(teamOrder, id)
		}
		if agentID != "" {
			t.Agents = append(t.Agents, agentID)
		}
	}

	// 按原始顺序返回
	teams := make([]*DbTeam, 0, len(teamOrder))
	for _, id := range teamOrder {
		teams = append(teams, teamMap[id])
	}

	return teams, nil
}

// UpdateTeam 更新 Team（重建成员关系）
// 约束：每个 Agent 只能属于一个团队
func UpdateTeam(team *DbTeam) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	// 检查新加入的 Agent 是否已属于其他团队（排除当前团队自身）
	for _, agentID := range team.Agents {
		var existingTeamID, existingTeamName string
		err := database.QueryRow(
			"SELECT ta.team_id, t.name FROM team_agents ta JOIN teams t ON ta.team_id = t.id WHERE ta.agent_id = ? AND ta.team_id != ?",
			agentID, team.ID).Scan(&existingTeamID, &existingTeamName)
		if err == nil {
			return fmt.Errorf("Agent '%s' 已属于团队 '%s'，一个 Agent 只能加入一个团队", agentID, existingTeamName)
		}
	}

	now := time.Now().UnixMilli()
	team.UpdatedAt = now

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		UPDATE teams SET name=?, leader_agent=?, project_directory=?, updated_at=?
		WHERE id=?`,
		team.Name, team.LeaderAgent, team.ProjectDirectory, now, team.ID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("team '%s' 不存在", team.ID)
	}

	// 重建成员关系
	tx.Exec("DELETE FROM team_agents WHERE team_id = ?", team.ID)
	for i, agentID := range team.Agents {
		tx.Exec("INSERT INTO team_agents (team_id, agent_id, sort_order) VALUES (?, ?, ?)",
			team.ID, agentID, i)
	}

	return tx.Commit()
}

// DeleteTeam 删除 Team 及其成员关系
func DeleteTeam(id string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec("DELETE FROM teams WHERE id = ?", id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("team '%s' 不存在", id)
	}

	tx.Exec("DELETE FROM team_agents WHERE team_id = ?", id)

	return tx.Commit()
}

// TeamExists 检查 Team 是否存在
func TeamExists(id string) bool {
	if database == nil {
		return false
	}
	var count int
	database.QueryRow("SELECT COUNT(*) FROM teams WHERE id = ?", id).Scan(&count)
	return count > 0
}

// GetAgentTeam 查询 Agent 当前所属的团队 ID 和名称
// 如果 Agent 没有归属任何团队，返回空字符串
func GetAgentTeam(agentID string) (teamID string, teamName string) {
	if database == nil {
		return "", ""
	}
	database.QueryRow(
		"SELECT ta.team_id, t.name FROM team_agents ta JOIN teams t ON ta.team_id = t.id WHERE ta.agent_id = ?",
		agentID).Scan(&teamID, &teamName)
	return
}

// === Skills CRUD ===

// AddAgentSkill 为 Agent 添加技能
func AddAgentSkill(skill *DbSkill) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	skill.CreatedAt = now

	enabled := 0
	if skill.Enabled {
		enabled = 1
	}

	_, err := database.Exec(`
		INSERT INTO agent_skills (agent_id, skill_name, skill_path, description, source, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, skill_name) DO UPDATE SET
			skill_path = excluded.skill_path,
			description = excluded.description,
			source = excluded.source,
			enabled = excluded.enabled`,
		skill.AgentID, skill.SkillName, skill.SkillPath, skill.Description,
		skill.Source, enabled, now,
	)
	return err
}

// ListAgentSkills 获取 Agent 的所有技能
func ListAgentSkills(agentID string) ([]*DbSkill, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	rows, err := database.Query(`
		SELECT id, agent_id, skill_name, COALESCE(skill_path, ''), COALESCE(description, ''), source, enabled, created_at
		FROM agent_skills WHERE agent_id = ? ORDER BY skill_name ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var skills []*DbSkill
	for rows.Next() {
		s := &DbSkill{}
		var enabled int
		rows.Scan(&s.ID, &s.AgentID, &s.SkillName, &s.SkillPath, &s.Description, &s.Source, &enabled, &s.CreatedAt)
		s.Enabled = enabled != 0
		skills = append(skills, s)
	}
	return skills, nil
}

// RemoveAgentSkill 删除 Agent 的某个技能
func RemoveAgentSkill(agentID string, skillID int64) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	result, err := database.Exec("DELETE FROM agent_skills WHERE id = ? AND agent_id = ?", skillID, agentID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("技能不存在")
	}
	return nil
}

// ToggleAgentSkill 切换技能启用/禁用
func ToggleAgentSkill(agentID string, skillID int64, enabled bool) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	result, err := database.Exec("UPDATE agent_skills SET enabled = ? WHERE id = ? AND agent_id = ?",
		enabledInt, skillID, agentID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("技能不存在")
	}
	return nil
}

// === GlobalConfig ===

// SetGlobalConfig 设置全局配置
func SetGlobalConfig(key, value string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	_, err := database.Exec(`
		INSERT INTO global_config (key, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now,
	)
	return err
}

// GetGlobalConfig 获取全局配置
func GetGlobalConfig(key string) (string, error) {
	if database == nil {
		return "", fmt.Errorf("数据库未初始化")
	}

	var value string
	err := database.QueryRow("SELECT value FROM global_config WHERE key = ?", key).Scan(&value)
	if err != nil {
		return "", err
	}
	return value, nil
}

// GetAllGlobalConfig 获取所有全局配置
func GetAllGlobalConfig() (map[string]string, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	rows, err := database.Query("SELECT key, value FROM global_config ORDER BY key ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		rows.Scan(&key, &value)
		config[key] = value
	}
	return config, nil
}

// DeleteGlobalConfig 删除全局配置
func DeleteGlobalConfig(key string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	_, err := database.Exec("DELETE FROM global_config WHERE key = ?", key)
	return err
}

// === SkillDefinition CRUD ===

// CreateSkillDefinition 创建技能定义
func CreateSkillDefinition(skill *DbSkillDefinition) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	skill.CreatedAt = now
	skill.UpdatedAt = now

	if skill.Source == "" {
		skill.Source = "custom"
	}
	if skill.Version == "" {
		skill.Version = "1.0.0"
	}

	result, err := database.Exec(`
		INSERT INTO skill_definitions (name, description, content, allowed_tools, source, category, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		skill.Name, skill.Description, skill.Content, skill.AllowedTools,
		skill.Source, skill.Category, skill.Version, now, now,
	)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	skill.ID = id
	return nil
}

// GetSkillDefinition 获取单个技能定义
func GetSkillDefinition(id int64) (*DbSkillDefinition, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	s := &DbSkillDefinition{}
	err := database.QueryRow(`
		SELECT id, name, COALESCE(description,''), COALESCE(content,''), COALESCE(allowed_tools,''),
		       source, COALESCE(category,''), COALESCE(version,'1.0.0'), created_at, updated_at
		FROM skill_definitions WHERE id = ?`, id).Scan(
		&s.ID, &s.Name, &s.Description, &s.Content, &s.AllowedTools,
		&s.Source, &s.Category, &s.Version, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetSkillDefinitionByName 按名称获取技能定义
func GetSkillDefinitionByName(name string) (*DbSkillDefinition, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	s := &DbSkillDefinition{}
	err := database.QueryRow(`
		SELECT id, name, COALESCE(description,''), COALESCE(content,''), COALESCE(allowed_tools,''),
		       source, COALESCE(category,''), COALESCE(version,'1.0.0'), created_at, updated_at
		FROM skill_definitions WHERE name = ?`, name).Scan(
		&s.ID, &s.Name, &s.Description, &s.Content, &s.AllowedTools,
		&s.Source, &s.Category, &s.Version, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListSkillDefinitions 列出所有技能定义
func ListSkillDefinitions() ([]*DbSkillDefinition, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	rows, err := database.Query(`
		SELECT id, name, COALESCE(description,''), COALESCE(content,''), COALESCE(allowed_tools,''),
		       source, COALESCE(category,''), COALESCE(version,'1.0.0'), created_at, updated_at
		FROM skill_definitions ORDER BY source ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var skills []*DbSkillDefinition
	for rows.Next() {
		s := &DbSkillDefinition{}
		rows.Scan(
			&s.ID, &s.Name, &s.Description, &s.Content, &s.AllowedTools,
			&s.Source, &s.Category, &s.Version, &s.CreatedAt, &s.UpdatedAt,
		)
		skills = append(skills, s)
	}
	return skills, nil
}

// UpdateSkillDefinition 更新技能定义
func UpdateSkillDefinition(skill *DbSkillDefinition) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	skill.UpdatedAt = now

	result, err := database.Exec(`
		UPDATE skill_definitions SET name=?, description=?, content=?, allowed_tools=?,
		       source=?, category=?, version=?, updated_at=?
		WHERE id=?`,
		skill.Name, skill.Description, skill.Content, skill.AllowedTools,
		skill.Source, skill.Category, skill.Version, now, skill.ID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("技能定义 '%s' 不存在", skill.Name)
	}
	return nil
}

// DeleteSkillDefinition 删除技能定义
func DeleteSkillDefinition(id int64) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	result, err := database.Exec("DELETE FROM skill_definitions WHERE id = ?", id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("技能定义不存在")
	}
	return nil
}

// SkillDefinitionExists 检查技能定义是否存在
func SkillDefinitionExists(name string) bool {
	if database == nil {
		return false
	}
	var count int
	database.QueryRow("SELECT COUNT(*) FROM skill_definitions WHERE name = ?", name).Scan(&count)
	return count > 0
}

// === Project CRUD ===

// CreateProject 创建项目（含 Team 关联）
func CreateProject(project *DbProject) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	project.CreatedAt = now
	project.UpdatedAt = now

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		INSERT INTO projects (id, name, description, repo_path, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		project.ID, project.Name, project.Description, project.RepoPath, now, now,
	)
	if err != nil {
		return err
	}

	// 插入 Team 关联
	for _, teamID := range project.Teams {
		_, err = tx.Exec("INSERT INTO project_teams (project_id, team_id) VALUES (?, ?)",
			project.ID, teamID)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetProject 获取单个项目（含关联 Teams）
func GetProject(id string) (*DbProject, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	rows, err := database.Query(`
		SELECT p.name, p.description, COALESCE(p.repo_path, ''),
		       p.created_at, p.updated_at, COALESCE(pt.team_id, '')
		FROM projects p
		LEFT JOIN project_teams pt ON p.id = pt.project_id
		WHERE p.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	project := &DbProject{ID: id}
	found := false
	for rows.Next() {
		var teamID string
		err := rows.Scan(&project.Name, &project.Description, &project.RepoPath,
			&project.CreatedAt, &project.UpdatedAt, &teamID)
		if err != nil {
			return nil, err
		}
		found = true
		if teamID != "" {
			project.Teams = append(project.Teams, teamID)
		}
	}
	if !found {
		return nil, fmt.Errorf("项目 %s 不存在", id)
	}
	if project.Teams == nil {
		project.Teams = []string{}
	}
	return project, nil
}

// ListProjects 列出所有项目
func ListProjects() ([]*DbProject, error) {
	if database == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	rows, err := database.Query(`
		SELECT p.id, p.name, p.description, COALESCE(p.repo_path, ''),
		       p.created_at, p.updated_at, COALESCE(pt.team_id, '')
		FROM projects p
		LEFT JOIN project_teams pt ON p.id = pt.project_id
		ORDER BY p.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projectMap := make(map[string]*DbProject)
	var order []string
	for rows.Next() {
		var id, name, desc, repoPath, teamID string
		var createdAt, updatedAt int64
		err := rows.Scan(&id, &name, &desc, &repoPath, &createdAt, &updatedAt, &teamID)
		if err != nil {
			continue
		}
		if _, exists := projectMap[id]; !exists {
			projectMap[id] = &DbProject{
				ID: id, Name: name, Description: desc, RepoPath: repoPath,
				CreatedAt: createdAt, UpdatedAt: updatedAt, Teams: []string{},
			}
			order = append(order, id)
		}
		if teamID != "" {
			projectMap[id].Teams = append(projectMap[id].Teams, teamID)
		}
	}

	result := make([]*DbProject, 0, len(order))
	for _, id := range order {
		result = append(result, projectMap[id])
	}
	return result, nil
}

// UpdateProject 更新项目（含 Team 关联）
func UpdateProject(project *DbProject) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	now := time.Now().UnixMilli()
	project.UpdatedAt = now

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		UPDATE projects SET name=?, description=?, repo_path=?, updated_at=?
		WHERE id=?`,
		project.Name, project.Description, project.RepoPath, now, project.ID,
	)
	if err != nil {
		return err
	}

	// 重建 Team 关联
	tx.Exec("DELETE FROM project_teams WHERE project_id = ?", project.ID)
	for _, teamID := range project.Teams {
		_, err = tx.Exec("INSERT INTO project_teams (project_id, team_id) VALUES (?, ?)",
			project.ID, teamID)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// DeleteProject 删除项目
func DeleteProject(id string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tx.Exec("DELETE FROM project_teams WHERE project_id = ?", id)
	_, err = tx.Exec("DELETE FROM projects WHERE id = ?", id)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// FindProjectForTeam 根据 teamID 查找所属的 Project ID（返回第一个匹配）
func FindProjectForTeam(teamID string) string {
	if database == nil {
		return ""
	}
	var projectID string
	err := database.QueryRow("SELECT project_id FROM project_teams WHERE team_id = ? LIMIT 1", teamID).Scan(&projectID)
	if err != nil {
		return ""
	}
	return projectID
}

// FindProjectsForTeam 返回一个 Team 关联的所有 Project ID
func FindProjectsForTeam(teamID string) []string {
	if database == nil {
		return nil
	}
	rows, err := database.Query("SELECT project_id FROM project_teams WHERE team_id = ?", teamID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// GetProjectsForTeam 返回一个 Team 关联的所有 Project 完整信息（用于注入 AI 上下文）
func GetProjectsForTeam(teamID string) []DbProject {
	ids := FindProjectsForTeam(teamID)
	if len(ids) == 0 {
		return nil
	}
	var projects []DbProject
	for _, id := range ids {
		p, err := GetProject(id)
		if err == nil && p != nil {
			projects = append(projects, *p)
		}
	}
	return projects
}

// FindProjectByName 按名称模糊匹配项目（用于从用户消息中提取项目上下文）
func FindProjectByName(name string) *DbProject {
	if database == nil || name == "" {
		return nil
	}
	rows, err := database.Query("SELECT id, name, description, repo_path, created_at, updated_at FROM projects")
	if err != nil {
		return nil
	}
	defer rows.Close()

	nameLower := strings.ToLower(name)
	var candidates []*DbProject

	for rows.Next() {
		p := DbProject{}
		if rows.Scan(&p.ID, &p.Name, &p.Description, &p.RepoPath, &p.CreatedAt, &p.UpdatedAt) == nil {
			pNameLower := strings.ToLower(p.Name)
			pIDLower := strings.ToLower(p.ID)

			// 精确匹配项目名
			if strings.EqualFold(p.Name, name) {
				return &p
			}
			// 消息中包含完整项目名
			if strings.Contains(nameLower, pNameLower) {
				return &p
			}
			// 消息中包含项目 ID（如 "todo-app"）
			if len(pIDLower) > 3 && strings.Contains(nameLower, pIDLower) {
				return &p
			}
			// 部分匹配：项目名包含在消息中（去掉"应用"/"项目"等后缀再匹配）
			// 例如 项目名 "待办清单应用"，用户消息中说"待办清单项目"
			trimmed := pNameLower
			for _, suffix := range []string{"应用", "项目", "系统", "平台", "工具", "服务"} {
				trimmed = strings.TrimSuffix(trimmed, suffix)
			}
			if len([]rune(trimmed)) >= 2 && strings.Contains(nameLower, trimmed) {
				pp := p // copy
				candidates = append(candidates, &pp)
			}
		}
	}

	// 如果有部分匹配的候选项，返回第一个
	if len(candidates) > 0 {
		return candidates[0]
	}
	return nil
}
