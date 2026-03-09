package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/queenbee-ai/queenbee/internal/db"
	"github.com/queenbee-ai/queenbee/internal/logging"
	"gopkg.in/yaml.v3"
)

// ═══════════════════════════════════════════════════════════════════
// Skills 管理系统
// 全 API 驱动：Skill 定义存数据库，按需注入 Agent 工作目录
// ═══════════════════════════════════════════════════════════════════

// SkillMeta SKILL.md 的 YAML frontmatter 元数据
type SkillMeta struct {
	Name         string `yaml:"name" json:"name"`
	Description  string `yaml:"description" json:"description"`
	AllowedTools string `yaml:"allowed-tools" json:"allowed_tools,omitempty"`
}

// SyncAgentSkills 同步 Agent 的技能到工作目录
// 从数据库读取 Skill 定义，生成 SKILL.md 写入各 CLI 的技能目录
// 支持: .agents/skills (通用) + .claude/skills (Claude) + .gemini/skills (Gemini)
func SyncAgentSkills(agentID string, agentDir string) error {
	skills, err := db.ListAgentSkills(agentID)
	if err != nil {
		return err
	}

	// 收集 enabled 技能名集合
	enabledSkills := make([]*db.DbSkill, 0)
	enabledNames := make(map[string]bool)
	for _, s := range skills {
		if s.Enabled {
			enabledSkills = append(enabledSkills, s)
			enabledNames[s.SkillName] = true
		}
	}

	// 目标目录（各 CLI 技能发现路径）
	agentsSkillsDir := filepath.Join(agentDir, ".agents", "skills")
	claudeSkillsDir := filepath.Join(agentDir, ".claude", "skills")
	geminiSkillsDir := filepath.Join(agentDir, ".gemini", "skills")
	os.MkdirAll(agentsSkillsDir, 0o755)
	os.MkdirAll(claudeSkillsDir, 0o755)
	os.MkdirAll(geminiSkillsDir, 0o755)

	// 清理磁盘上不在数据库中的残留技能目录
	cleanOrphanedSkills(agentsSkillsDir, enabledNames)
	cleanOrphanedSkills(claudeSkillsDir, enabledNames)
	cleanOrphanedSkills(geminiSkillsDir, enabledNames)

	if len(enabledSkills) == 0 {
		return nil
	}

	synced := 0
	for _, agentSkill := range enabledSkills {
		// 从数据库获取 Skill 定义
		skillDef, err := db.GetSkillDefinitionByName(agentSkill.SkillName)
		if err != nil {
			// 回退：尝试用 skill_path 从文件系统复制（兼容旧模式）
			if agentSkill.SkillPath != "" && dirExists(agentSkill.SkillPath) {
				targetAgentsDir := filepath.Join(agentsSkillsDir, agentSkill.SkillName)
				CopyDir(agentSkill.SkillPath, targetAgentsDir)
				CopyDir(agentSkill.SkillPath, filepath.Join(claudeSkillsDir, agentSkill.SkillName))
				CopyDir(agentSkill.SkillPath, filepath.Join(geminiSkillsDir, agentSkill.SkillName))
				synced++
			}
			continue
		}

		// 从数据库定义生成 SKILL.md 并写入目录
		skillContent := skillDef.Content
		if skillContent == "" {
			// 自动生成最小 SKILL.md
			skillContent = generateSkillMD(skillDef)
		}

		// 写入 .agents/skills/<name>/SKILL.md
		agentsTarget := filepath.Join(agentsSkillsDir, agentSkill.SkillName)
		os.MkdirAll(agentsTarget, 0o755)
		os.WriteFile(filepath.Join(agentsTarget, "SKILL.md"), []byte(skillContent), 0o644)

		// 写入 .claude/skills/<name>/SKILL.md
		claudeTarget := filepath.Join(claudeSkillsDir, agentSkill.SkillName)
		os.MkdirAll(claudeTarget, 0o755)
		os.WriteFile(filepath.Join(claudeTarget, "SKILL.md"), []byte(skillContent), 0o644)

		// 写入 .gemini/skills/<name>/SKILL.md
		geminiTarget := filepath.Join(geminiSkillsDir, agentSkill.SkillName)
		os.MkdirAll(geminiTarget, 0o755)
		os.WriteFile(filepath.Join(geminiTarget, "SKILL.md"), []byte(skillContent), 0o644)

		synced++
	}

	if synced > 0 {
		logging.Log("INFO", fmt.Sprintf("Agent '%s' 同步了 %d 个技能到工作目录", agentID, synced))
	}
	return nil
}

// RemoveSkillFiles 从 Agent 工作目录中删除已卸载技能的文件
// 清理所有 CLI 的技能目录: .agents/skills/ + .claude/skills/ + .gemini/skills/
func RemoveSkillFiles(agentDir string, skillName string) {
	targets := []string{
		filepath.Join(agentDir, ".agents", "skills", skillName),
		filepath.Join(agentDir, ".claude", "skills", skillName),
		filepath.Join(agentDir, ".gemini", "skills", skillName),
	}
	for _, dir := range targets {
		if dirExists(dir) {
			os.RemoveAll(dir)
		}
	}
	logging.Log("INFO", fmt.Sprintf("已清理技能文件: %s (目录: %s)", skillName, agentDir))
}

// cleanOrphanedSkills 扫描技能目录，删除不在 enabledNames 中的子目录
func cleanOrphanedSkills(skillsDir string, enabledNames map[string]bool) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !enabledNames[entry.Name()] {
			os.RemoveAll(filepath.Join(skillsDir, entry.Name()))
			logging.Log("INFO", fmt.Sprintf("清理残留技能目录: %s/%s", skillsDir, entry.Name()))
		}
	}
}

func generateSkillMD(skill *db.DbSkillDefinition) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", skill.Name))
	if skill.Description != "" {
		sb.WriteString(fmt.Sprintf("description: %q\n", skill.Description))
	}
	if skill.AllowedTools != "" {
		sb.WriteString(fmt.Sprintf("allowed-tools: %s\n", skill.AllowedTools))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(fmt.Sprintf("# %s\n\n", skill.Name))
	if skill.Description != "" {
		sb.WriteString(skill.Description + "\n")
	}
	return sb.String()
}

// AutoRegisterBuiltinSkills 自动从文件系统发现并注册内置技能到数据库
// 同时为指定 Agent 装载
func AutoRegisterBuiltinSkills(agentID string) {
	exe, _ := os.Executable()
	builtinDir := filepath.Join(filepath.Dir(exe), "templates", ".agents", "skills")
	if _, err := os.Stat(builtinDir); os.IsNotExist(err) {
		return
	}

	entries, err := os.ReadDir(builtinDir)
	if err != nil {
		return
	}

	registered := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(builtinDir, entry.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}

		meta := parseSkillFrontmatter(string(data))
		if meta.Name == "" {
			meta.Name = entry.Name()
		}

		// 如果数据库中不存在此技能定义，创建它
		if !db.SkillDefinitionExists(meta.Name) {
			db.CreateSkillDefinition(&db.DbSkillDefinition{
				Name:         meta.Name,
				Description:  meta.Description,
				Content:      string(data),
				AllowedTools: meta.AllowedTools,
				Source:       "builtin",
				Category:     "工具",
				Version:      "1.0.0",
			})
		}

		// 为 Agent 装载
		db.AddAgentSkill(&db.DbSkill{
			AgentID:     agentID,
			SkillName:   meta.Name,
			SkillPath:   skillDir,
			Description: meta.Description,
			Source:      "builtin",
			Enabled:     true,
		})
		registered++
	}

	if registered > 0 {
		logging.Log("INFO", fmt.Sprintf("Agent '%s' 自动注册了 %d 个内置技能", agentID, registered))
	}
}

// ImportBuiltinSkillsToDB 从文件系统导入所有内置技能到数据库
// 通过 POST /api/skills/import-builtin 调用
// 搜索多个候选目录：可执行文件旁/templates、QUEENBEE_HOME、项目根目录/tinyclaw
func ImportBuiltinSkillsToDB() int {
	// 候选目录列表
	candidateDirs := findSkillDirs()
	if len(candidateDirs) == 0 {
		logging.Log("WARN", "未找到任何内置技能目录")
		return 0
	}

	imported := 0
	for _, dir := range candidateDirs {
		imported += importSkillsFromDir(dir)
	}

	if imported > 0 {
		logging.Log("INFO", fmt.Sprintf("导入了 %d 个内置技能到数据库", imported))
	}
	return imported
}

// findSkillDirs 搜索所有可能包含 SKILL.md 的技能目录
func findSkillDirs() []string {
	var dirs []string
	seen := map[string]bool{}

	addIfExists := func(path string) {
		abs, _ := filepath.Abs(path)
		if seen[abs] {
			return
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			seen[abs] = true
			dirs = append(dirs, abs)
		}
	}

	// 1. 可执行文件旁的 templates
	exe, _ := os.Executable()
	addIfExists(filepath.Join(filepath.Dir(exe), "templates", ".agents", "skills"))

	// 2. QUEENBEE_HOME 下
	if home := os.Getenv("QUEENBEE_HOME"); home != "" {
		addIfExists(filepath.Join(home, "templates", ".agents", "skills"))
		addIfExists(filepath.Join(home, ".agents", "skills"))
	}

	// 3. 可执行文件所在项目中搜索 tinyclaw/.agents/skills
	exeDir := filepath.Dir(exe)
	for _, rel := range []string{
		"tinyclaw/.agents/skills",
		"../tinyclaw/.agents/skills",
		"../../tinyclaw/.agents/skills",
	} {
		addIfExists(filepath.Join(exeDir, rel))
	}

	// 4. 工作目录相关
	if cwd, err := os.Getwd(); err == nil {
		addIfExists(filepath.Join(cwd, "tinyclaw", ".agents", "skills"))
		addIfExists(filepath.Join(cwd, ".agents", "skills"))
		// 向上查找项目根
		parent := filepath.Dir(cwd)
		addIfExists(filepath.Join(parent, "tinyclaw", ".agents", "skills"))
	}

	return dirs
}

// importSkillsFromDir 从单个目录导入技能
func importSkillsFromDir(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	imported := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}

		meta := parseSkillFrontmatter(string(data))
		if meta.Name == "" {
			meta.Name = entry.Name()
		}

		if !db.SkillDefinitionExists(meta.Name) {
			db.CreateSkillDefinition(&db.DbSkillDefinition{
				Name:         meta.Name,
				Description:  meta.Description,
				Content:      string(data),
				AllowedTools: meta.AllowedTools,
				Source:       "builtin",
				Category:     "工具",
				Version:      "1.0.0",
			})
			imported++
			logging.Log("INFO", fmt.Sprintf("导入内置技能: %s (来自 %s)", meta.Name, dir))
		}
	}
	return imported
}

// parseSkillFrontmatter 解析 SKILL.md 的 YAML frontmatter
func parseSkillFrontmatter(content string) SkillMeta {
	var meta SkillMeta
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return meta
	}
	lines := strings.SplitN(content, "---", 3)
	if len(lines) < 3 {
		return meta
	}
	yaml.Unmarshal([]byte(lines[1]), &meta)
	return meta
}

// dirExists 检查目录是否存在
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// CLIGlobalSkill 表示各 CLI 自带的全局技能
type CLIGlobalSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "codex" / "claude" / "opencode" / "gemini"
	FilePath    string `json:"file_path"`
	Content     string `json:"content,omitempty"`
}

// ScanCLIGlobalSkills 扫描各 CLI 的全局技能目录，返回发现的技能列表
func ScanCLIGlobalSkills() []CLIGlobalSkill {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	// 各 CLI 的全局技能扫描路径
	type scanTarget struct {
		source string
		dirs   []string
	}
	targets := []scanTarget{
		{
			source: "codex",
			dirs: []string{
				filepath.Join(home, ".codex", "skills"),
				filepath.Join(home, ".codex", "skills", ".system"),
			},
		},
		{
			source: "claude",
			dirs: []string{
				filepath.Join(home, ".claude", "skills"),
			},
		},
		{
			source: "opencode",
			dirs: []string{
				filepath.Join(home, ".config", "opencode", "skills"),
				filepath.Join(home, ".opencode", "skills"),
			},
		},
		{
			source: "gemini",
			dirs: []string{
				filepath.Join(home, ".gemini", "skills"),
			},
		},
	}

	seen := map[string]bool{}
	var result []CLIGlobalSkill

	for _, target := range targets {
		for _, dir := range target.dirs {
			if !dirExists(dir) {
				continue
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
				data, err := os.ReadFile(skillFile)
				if err != nil {
					continue
				}
				// 用 source:name 去重（同一 CLI 不重复计数）
				key := target.source + ":" + entry.Name()
				if seen[key] {
					continue
				}
				seen[key] = true

				meta := parseSkillFrontmatter(string(data))
				name := meta.Name
				if name == "" {
					name = entry.Name()
				}

				result = append(result, CLIGlobalSkill{
					Name:        name,
					Description: meta.Description,
					Source:      target.source,
					FilePath:    skillFile,
					Content:     string(data),
				})
			}
		}
	}

	return result
}
