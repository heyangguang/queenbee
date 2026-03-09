package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/types"
)

// soul 更新触发频率：每次任务完成后都触发更新
const soulUpdateInterval = 1

// soul 更新独立超时（分钟）
const soulUpdateTimeoutMin = 3

// 每个 agent 的任务计数器
var soulCounters sync.Map // map[string]*atomic.Int64

// soulUpdatePrompt 是让 agent 反思并更新 SOUL.md 的 prompt
const soulUpdatePrompt = `Reflect on the task you just completed. Read your SOUL.md file and update it with:
- Who you are becoming through this work
- Opinions you formed or strengthened
- Skills you developed or demonstrated
- Any new perspectives or lessons learned

Keep updates incremental — add to or refine what's there, don't rewrite everything.
If SOUL.md is still a template with placeholders like [Your Name], fill in the sections that are relevant based on your work so far.
Be specific and opinionated — generic statements like "I'm helpful" are useless.
Only update SOUL.md, do not do anything else.`

// MaybeTriggerSoulUpdate 在 agent 成功完成任务后调用
// 每 soulUpdateInterval 次触发一次实际的 soul 更新
// 该函数应在 goroutine 中异步调用，不会阻塞主流程
func MaybeTriggerSoulUpdate(agentID, agentDir string, agent *types.AgentConfig) {
	defer func() {
		if r := recover(); r != nil {
			logging.Log("ERROR", fmt.Sprintf("soul 更新 panic (agent: %s): %v", agentID, r))
		}
	}()

	// 获取或初始化计数器
	val, _ := soulCounters.LoadOrStore(agentID, &atomic.Int64{})
	counter := val.(*atomic.Int64)
	count := counter.Add(1)

	// 每 N 次触发一次
	if count%soulUpdateInterval != 0 {
		return
	}

	logging.Log("INFO", fmt.Sprintf("🧠 触发 soul 更新 (agent: %s, 第 %d 次任务)", agentID, count))

	// 检查 SOUL.md 是否存在
	soulPath := filepath.Join(agentDir, "SOUL.md")
	if _, err := os.Stat(soulPath); os.IsNotExist(err) {
		logging.Log("WARN", fmt.Sprintf("soul 更新跳过: %s 不存在 SOUL.md", agentID))
		return
	}

	// 合并环境变量
	settings := config.GetSettings()
	mergedEnv := make(map[string]string)
	for k, v := range settings.Env {
		mergedEnv[k] = v
	}
	for k, v := range agent.Env {
		mergedEnv[k] = v
	}

	// 根据 provider 调用不同的 CLI 执行 soul 更新
	provider := agent.Provider
	if provider == "" {
		provider = "anthropic"
	}

	var err error
	switch provider {
	case "anthropic", "claude", "":
		err = soulUpdateClaude(agent, agentID, agentDir, mergedEnv)
	case "openai", "codex":
		err = soulUpdateCodex(agent, agentID, agentDir, mergedEnv)
	case "opencode":
		err = soulUpdateOpenCode(agent, agentID, agentDir, mergedEnv)
	case "gemini", "google":
		err = soulUpdateGemini(agent, agentID, agentDir, mergedEnv)
	default:
		err = soulUpdateClaude(agent, agentID, agentDir, mergedEnv)
	}

	if err != nil {
		logging.Log("WARN", fmt.Sprintf("🧠 soul 更新失败 (agent: %s): %v", agentID, err))
	} else {
		logging.Log("INFO", fmt.Sprintf("🧠 soul 更新完成 (agent: %s)", agentID))
	}
}

// soulUpdateClaude 使用 Claude CLI 执行 soul 更新
func soulUpdateClaude(agent *types.AgentConfig, agentID, agentDir string, env map[string]string) error {
	modelID := config.ResolveClaudeModel(agent.Model)
	args := []string{
		"--dangerously-skip-permissions",
		"--output-format", "stream-json", "--verbose",
		"-c", // 继续上次对话，这样 agent 知道刚做了什么
		"-p", "-",
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}

	// 使用空 agentID 不广播活跃度事件（这是后台静默操作）
	_, err := RunCommand("claude", args, agentDir, env, soulUpdateTimeoutMin, "", soulUpdatePrompt)
	return err
}

// soulUpdateCodex 使用 Codex CLI 执行 soul 更新
func soulUpdateCodex(agent *types.AgentConfig, agentID, agentDir string, env map[string]string) error {
	modelID := config.ResolveCodexModel(agent.Model)
	args := []string{
		"exec",
		"resume", "--last", // 继续上次对话
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		"--json",
		"-",
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}

	_, err := RunCommand("codex", args, agentDir, env, soulUpdateTimeoutMin, "", soulUpdatePrompt)
	return err
}

// soulUpdateGemini 使用 Gemini CLI 执行 soul 更新
func soulUpdateGemini(agent *types.AgentConfig, agentID, agentDir string, env map[string]string) error {
	modelID := config.ResolveGeminiModel(agent.Model)
	args := []string{
		"--yolo",
		"--output-format", "stream-json",
		"-p", soulUpdatePrompt,
	}
	if modelID != "" {
		args = append(args, "-m", modelID)
	}

	_, err := RunCommand("gemini", args, agentDir, env, soulUpdateTimeoutMin, "")
	return err
}

// soulUpdateOpenCode 使用 OpenCode CLI 执行 soul 更新
func soulUpdateOpenCode(agent *types.AgentConfig, agentID, agentDir string, env map[string]string) error {
	modelID := config.ResolveOpenCodeModel(agent.Model)
	args := []string{
		"run",
		"--format", "json",
		"-",
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}

	_, err := RunCommand("opencode", args, agentDir, env, soulUpdateTimeoutMin, "", soulUpdatePrompt)
	return err
}
