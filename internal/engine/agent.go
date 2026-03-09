package engine

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/templates"
	"github.com/queenbee-ai/queenbee/types"
)

// CopyDir 递归复制目录
func CopyDir(src, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		destPath := filepath.Join(dest, entry.Name())

		if entry.IsDir() {
			if err := CopyDir(srcPath, destPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, destPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// getTemplateBytes 读取模板文件内容
// 优先从外部 templates/ 目录读取（支持用户自定义覆盖），回退到编译时嵌入的模板
func getTemplateBytes(filename string) ([]byte, bool) {
	// 优先：binary 同级的 templates/ 目录（允许用户自定义覆盖）
	exe, _ := os.Executable()
	externalPath := filepath.Join(filepath.Dir(exe), "templates", filename)
	if data, err := os.ReadFile(externalPath); err == nil {
		return data, true
	}
	// 回退：编译时嵌入的模板
	if data, err := templates.FS.ReadFile(filename); err == nil {
		return data, true
	}
	return nil, false
}

// writeTemplateToFile 将模板文件写入目标路径
func writeTemplateToFile(templateName, destPath string) bool {
	data, ok := getTemplateBytes(templateName)
	if !ok {
		return false
	}
	os.MkdirAll(filepath.Dir(destPath), 0o755)
	return os.WriteFile(destPath, data, 0o644) == nil
}

// EnsureAgentDirectory 确保 Agent 目录存在并包含模板文件
// 模板优先从外部 templates/ 目录读取，回退到编译时嵌入的模板
func EnsureAgentDirectory(agentDir string) {
	// 不管目录是否存在，每次都从模板更新 AGENTS.md 和各 CLI 的专属配置文件
	os.MkdirAll(agentDir, 0o755)
	writeTemplateToFile("AGENTS.md", filepath.Join(agentDir, "AGENTS.md"))
	// Claude CLI: .claude/CLAUDE.md
	targetClaudeDir := filepath.Join(agentDir, ".claude")
	os.MkdirAll(targetClaudeDir, 0o755)
	writeTemplateToFile("AGENTS.md", filepath.Join(targetClaudeDir, "CLAUDE.md"))
	// Gemini CLI: .gemini/GEMINI.md（Gemini 不识别 AGENTS.md，必须用此文件）
	targetGeminiDir := filepath.Join(agentDir, ".gemini")
	os.MkdirAll(targetGeminiDir, 0o755)
	writeTemplateToFile("AGENTS.md", filepath.Join(targetGeminiDir, "GEMINI.md"))
	// 注：Codex / OpenCode 原生读根目录 AGENTS.md，无需专属文件

	// 目录已存在时只更新 AGENTS.md 和确保 SOUL.md 存在，跳过完整初始化
	if _, err := os.Stat(filepath.Join(agentDir, "heartbeat.md")); err == nil {
		// SOUL.md 只在不存在时初始化（不覆盖 agent 的个性化内容）
		soulDest := filepath.Join(agentDir, "SOUL.md")
		if _, err := os.Stat(soulDest); os.IsNotExist(err) {
			writeTemplateToFile("SOUL.md", soulDest)
		}
		// 确保是 git 仓库（Claude CLI 需要 git 项目根来发现 .claude/skills/）
		ensureGitRepo(agentDir)
		return
	}

	// 复制 .claude/ 整个源目录（如果外部存在）
	exe, _ := os.Executable()
	templatesDir := filepath.Join(filepath.Dir(exe), "templates")
	sourceClaudeDir := filepath.Join(templatesDir, ".claude")
	if _, err := os.Stat(sourceClaudeDir); err == nil {
		CopyDir(sourceClaudeDir, targetClaudeDir)
	}

	// 复制 heartbeat.md
	writeTemplateToFile("heartbeat.md", filepath.Join(agentDir, "heartbeat.md"))

	// AGENTS.md → 各 CLI 专属配置文件（已在上面写过）

	// 复制 .agents/skills → 各 CLI 的技能目录
	sourceSkills := filepath.Join(templatesDir, ".agents", "skills")
	if _, err := os.Stat(sourceSkills); err == nil {
		targetAgentsSkills := filepath.Join(agentDir, ".agents", "skills")
		os.MkdirAll(targetAgentsSkills, 0o755)
		CopyDir(sourceSkills, targetAgentsSkills)

		targetClaudeSkills := filepath.Join(targetClaudeDir, "skills")
		os.MkdirAll(targetClaudeSkills, 0o755)
		CopyDir(targetAgentsSkills, targetClaudeSkills)

		// Gemini CLI 也识别 .gemini/skills/（双写保险）
		targetGeminiSkills := filepath.Join(targetGeminiDir, "skills")
		os.MkdirAll(targetGeminiSkills, 0o755)
		CopyDir(targetAgentsSkills, targetGeminiSkills)
	}

	// 复制 SOUL.md 到工作目录根
	writeTemplateToFile("SOUL.md", filepath.Join(agentDir, "SOUL.md"))

	// 初始化 git 仓库（Claude CLI 需要 git 项目根来发现 .claude/skills/）
	ensureGitRepo(agentDir)
}

// ensureGitRepo 确保目录是 git 仓库
// Claude CLI 需要 git 项目根来发现 .claude/ 目录（包括 skills、CLAUDE.md）
// 如果没有 .git，CLI 不会加载 project 级设置，skills 将不可见
func ensureGitRepo(dir string) {
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return // 已经是 git 仓库
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		logging.Log("WARN", fmt.Sprintf("git init 失败 (%s): %v", dir, err))
		return
	}
	// 添加 .gitignore 避免追踪不需要的文件
	gitignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		os.WriteFile(gitignore, []byte("# Agent 工作目录，仅为 Claude CLI 项目识别而 git init\n*\n"), 0o644)
	}
	logging.Log("INFO", fmt.Sprintf("初始化 git 仓库: %s", dir))
}

func copyTemplateFile(src, dest string) {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return
	}
	os.MkdirAll(filepath.Dir(dest), 0o755)
	copyFile(src, dest)
}

// UpdateAgentTeammates 更新 AGENTS.md 中的队友信息
func UpdateAgentTeammates(agentDir, agentID string, agents map[string]*types.AgentConfig, teams map[string]*types.TeamConfig) {
	agentsMdPath := filepath.Join(agentDir, "AGENTS.md")
	data, err := os.ReadFile(agentsMdPath)
	if err != nil {
		return
	}

	content := string(data)
	const startMarker = "<!-- TEAMMATES_START -->"
	const endMarker = "<!-- TEAMMATES_END -->"

	startIdx := strings.Index(content, startMarker)
	endIdx := strings.Index(content, endMarker)
	if startIdx == -1 || endIdx == -1 {
		return
	}

	// 收集队友信息
	type teammateInfo struct {
		ID    string
		Name  string
		Model string
	}
	var teammates []teammateInfo
	seen := make(map[string]bool)

	for _, team := range teams {
		found := false
		for _, a := range team.Agents {
			if a == agentID {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		for _, tid := range team.Agents {
			if tid == agentID || seen[tid] {
				continue
			}
			if agent, ok := agents[tid]; ok {
				teammates = append(teammates, teammateInfo{ID: tid, Name: agent.Name, Model: agent.Model})
				seen[tid] = true
			}
		}
	}

	// 构建替换内容
	var block strings.Builder
	if self, ok := agents[agentID]; ok {
		block.WriteString(fmt.Sprintf("\n### You\n\n- `@%s` — **%s** (%s)\n", agentID, self.Name, self.Model))
	}
	if len(teammates) > 0 {
		block.WriteString("\n### Your Teammates\n\n")
		for _, t := range teammates {
			block.WriteString(fmt.Sprintf("- `@%s` — **%s** (%s)\n", t.ID, t.Name, t.Model))
		}
	}

	newContent := content[:startIdx+len(startMarker)] + block.String() + content[endIdx:]

	// 注入项目目录信息
	newContent = injectProjectDirectory(newContent, agentID, teams)

	os.WriteFile(agentsMdPath, []byte(newContent), 0o644)

	// 同步到 .claude/CLAUDE.md（对齐 tinyclaw agent.ts 第 128-146 行）
	claudeDir := filepath.Join(agentDir, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	claudeMdPath := filepath.Join(claudeDir, "CLAUDE.md")
	claudeContent := ""
	if data, err := os.ReadFile(claudeMdPath); err == nil {
		claudeContent = string(data)
	}
	// 读不到就用空字符串继续（对齐 tinyclaw：不 return，而是追加 marker）
	cStartIdx := strings.Index(claudeContent, startMarker)
	cEndIdx := strings.Index(claudeContent, endMarker)
	if cStartIdx != -1 && cEndIdx != -1 {
		claudeContent = claudeContent[:cStartIdx+len(startMarker)] + block.String() + claudeContent[cEndIdx:]
	} else {
		claudeContent = strings.TrimRight(claudeContent, "\n") + "\n\n" + startMarker + block.String() + endMarker + "\n"
	}
	claudeContent = injectProjectDirectory(claudeContent, agentID, teams)
	os.WriteFile(claudeMdPath, []byte(claudeContent), 0o644)

	// 同步到 .gemini/GEMINI.md（Gemini CLI 不识别 AGENTS.md，需要此文件）
	geminiDir := filepath.Join(agentDir, ".gemini")
	os.MkdirAll(geminiDir, 0o755)
	geminiMdPath := filepath.Join(geminiDir, "GEMINI.md")
	geminiContent := ""
	if data, err := os.ReadFile(geminiMdPath); err == nil {
		geminiContent = string(data)
	}
	gStartIdx := strings.Index(geminiContent, startMarker)
	gEndIdx := strings.Index(geminiContent, endMarker)
	if gStartIdx != -1 && gEndIdx != -1 {
		geminiContent = geminiContent[:gStartIdx+len(startMarker)] + block.String() + geminiContent[gEndIdx:]
	} else {
		geminiContent = strings.TrimRight(geminiContent, "\n") + "\n\n" + startMarker + block.String() + endMarker + "\n"
	}
	geminiContent = injectProjectDirectory(geminiContent, agentID, teams)
	os.WriteFile(geminiMdPath, []byte(geminiContent), 0o644)

	// 注：Codex / OpenCode 原生读根目录 AGENTS.md，无需专属文件

	logging.Log("DEBUG", fmt.Sprintf("更新 agent %s 的队友信息（%d 个队友）", agentID, len(teammates)))
}

// injectProjectDirectory 将项目目录信息注入到内容中的 PROJECT_DIR marker 区域
func injectProjectDirectory(content, agentID string, teams map[string]*types.TeamConfig) string {
	const projStart = "<!-- PROJECT_DIR_START -->"
	const projEnd = "<!-- PROJECT_DIR_END -->"

	pStartIdx := strings.Index(content, projStart)
	pEndIdx := strings.Index(content, projEnd)
	if pStartIdx == -1 || pEndIdx == -1 {
		return content
	}

	// 查找当前 Agent 所在团队的 project_directory
	var projectDir string
	var teamName string
	for _, team := range teams {
		for _, a := range team.Agents {
			if a == agentID {
				if team.ProjectDirectory != "" {
					projectDir = team.ProjectDirectory
					teamName = team.Name
				}
				break
			}
		}
		if projectDir != "" {
			break
		}
	}

	var projBlock strings.Builder
	if projectDir != "" {
		projBlock.WriteString(fmt.Sprintf("\n\n## 📁 Project Directory\n\n"))
		projBlock.WriteString(fmt.Sprintf("**团队 %s 的共享项目目录**: `%s`\n\n", teamName, projectDir))
		projBlock.WriteString("**重要规则**：\n")
		projBlock.WriteString(fmt.Sprintf("- 所有项目代码、文档、配置文件**必须**写入 `%s` 目录\n", projectDir))
		projBlock.WriteString("- 不要在 Agent 工作目录（当前目录）下创建项目文件\n")
		projBlock.WriteString("- 团队所有成员共享此目录，请确保文件路径和命名清晰\n")
	}

	return content[:pStartIdx+len(projStart)] + projBlock.String() + content[pEndIdx:]
}
