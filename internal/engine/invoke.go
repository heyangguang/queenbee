package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/db"
	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/types"
)

// processStore 全局进程追踪：agentID → *os.Process
// 用于手动恢复时强杀卡死进程
var processStore sync.Map

// killedAgentStore 标记被手动强杀的 agent
// ProcessMessage 检测到此标记后会重置消息为 pending 而非 complete
var killedAgentStore sync.Map

// providerCooldown 记录 provider 冷却时间：provider → 冷却截止时间
// 当某个 provider 连续失败时，冷却 5 分钟内不再尝试该 provider
var providerCooldown sync.Map

const providerCooldownDuration = 5 * time.Minute

// isProviderCoolingDown 检查 provider 是否在冷却期
func isProviderCoolingDown(provider string) bool {
	val, ok := providerCooldown.Load(provider)
	if !ok {
		return false
	}
	cooldownUntil, ok := val.(time.Time)
	if !ok {
		return false
	}
	if time.Now().After(cooldownUntil) {
		providerCooldown.Delete(provider)
		return false
	}
	return true
}

// markProviderCooldown 标记 provider 进入冷却期
func markProviderCooldown(provider string) {
	providerCooldown.Store(provider, time.Now().Add(providerCooldownDuration))
	logging.Log("WARN", fmt.Sprintf("🧊 Provider '%s' 进入冷却期 (%v)，将临时跳过", provider, providerCooldownDuration))
}

// activityTrackingWriter 包装 io.Writer，每次 Write 时更新活跃时间戳
type activityTrackingWriter struct {
	inner        io.Writer
	lastActivity *atomic.Int64
}

func (w *activityTrackingWriter) Write(p []byte) (n int, err error) {
	w.lastActivity.Store(time.Now().UnixMilli())
	return w.inner.Write(p)
}

// RunCommand 执行外部命令并返回 stdout
// extraEnv 为附加环境变量（来自 settings.json 配置）
// timeoutMin 绝对超时时间（分钟），0 表示使用默认值 60 分钟
// agentID 用于活跃度 SSE 事件广播，传空字符串则不广播
// stdinData 可选的 stdin 输入内容（用于传递消息，避免命令行参数解析问题）
// 使用 stdout 活跃度检测：无输出超过 idleTimeout 判定为卡死
func RunCommand(command string, args []string, cwd string, extraEnv map[string]string, timeoutMin int, agentID string, stdinData ...string) (string, error) {
	if timeoutMin <= 0 {
		timeoutMin = 60
	}

	// 读取空闲超时配置
	settings := config.GetSettings()
	idleTimeoutMin := settings.AgentIdleTimeout
	if idleTimeoutMin <= 0 {
		idleTimeoutMin = 5 // 默认 5 分钟无输出判定卡死
	}

	// 绝对超时上下文（兜底）
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMin)*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}

	// 设置进程组，确保超时可以杀掉所有子进程（包括 Codex 启动的 Node.js 等）
	setProcGroup(cmd)

	// 基于当前进程环境变量，过滤 CLAUDECODE
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			filtered = append(filtered, e)
		}
	}

	// 注入用户配置的额外环境变量（覆盖同名变量）
	for k, v := range extraEnv {
		key := k + "="
		newFiltered := make([]string, 0, len(filtered))
		for _, e := range filtered {
			if !strings.HasPrefix(e, key) {
				newFiltered = append(newFiltered, e)
			}
		}
		filtered = newFiltered
		filtered = append(filtered, k+"="+v)
	}

	cmd.Env = filtered

	// 如果有 stdin 数据，通过管道传入（避免消息内容被 CLI 参数解析器误判为选项）
	if len(stdinData) > 0 && stdinData[0] != "" {
		cmd.Stdin = strings.NewReader(stdinData[0])
	}

	// 流式 stdout + 活跃度追踪
	var stdout, stderr bytes.Buffer
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixMilli())

	cmd.Stdout = &activityTrackingWriter{inner: &stdout, lastActivity: &lastActivity}
	cmd.Stderr = &stderr

	// 非阻塞启动进程
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动命令 %s 失败: %w", command, err)
	}

	// 注册进程到全局 store（用于手动强杀）
	if agentID != "" && cmd.Process != nil {
		processStore.Store(agentID, cmd.Process)
	}

	startTime := time.Now().UnixMilli()

	// 广播初始活跃状态
	if agentID != "" {
		UpdateActivity(agentID, "active", startTime, 0, startTime)
	}

	// cmd.Wait 在 goroutine 中等待
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// watchdog：每 30 秒检查活跃度
	idleTimeout := time.Duration(idleTimeoutMin) * time.Minute
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	idleKilled := false
	var cmdErr error

loop:
	for {
		select {
		case cmdErr = <-done:
			// 进程结束（正常或被 context 杀掉）
			break loop

		case <-ticker.C:
			// 定期检查活跃度
			lastAct := time.UnixMilli(lastActivity.Load())
			idle := time.Since(lastAct)

			// 判断状态
			status := "active"
			if idle > 1*time.Minute {
				status = "idle_warning"
			}

			// 广播活跃度
			if agentID != "" {
				UpdateActivity(agentID, status, lastActivity.Load(), stdout.Len(), startTime)
			}

			if idle > idleTimeout {
				idleKilled = true
				if cmd.Process != nil {
					killProcessGroup(cmd.Process.Pid)
				}
				logging.Log("ERROR", fmt.Sprintf(
					"命令 %s 空闲超时（%v 无输出），判定为卡死并终止",
					command, idle.Round(time.Second)))
				if agentID != "" {
					UpdateActivity(agentID, "idle_killed", lastActivity.Load(), stdout.Len(), startTime)
				}
				// 等待进程退出
				cmdErr = <-done
				break loop
			}
			// 日志记录活跃状态（DEBUG 级别，不刷屏）
			agentLabel := command
			if agentID != "" {
				agentLabel = fmt.Sprintf("%s(%s)", command, agentID)
			}
			logging.Log("DEBUG", fmt.Sprintf(
				"[watchdog] %s 活跃检查: 最后输出 %v 前, stdout %d 字节",
				agentLabel, idle.Round(time.Second), stdout.Len()))
		}
	}

	// 进程结束，清理活跃度 + 写入执行记录
	execStatus := "completed"
	if idleKilled {
		execStatus = "idle_killed"
	} else if ctx.Err() == context.DeadlineExceeded {
		execStatus = "timeout_killed"
	} else if cmdErr != nil {
		execStatus = "error"
	}
	defer func() {
		if agentID != "" {
			processStore.Delete(agentID) // 从进程追踪中移除
			RemoveActivity(agentID)
			// 写入执行历史
			now := time.Now().UnixMilli()
			db.SaveAgentExecution(&db.AgentExecution{
				AgentID:     agentID,
				DurationSec: int((now - startTime) / 1000),
				StdoutBytes: stdout.Len(),
				Status:      execStatus,
				StartedAt:   startTime,
				EndedAt:     now,
			})
		}
	}()

	// 处理结果
	if idleKilled {
		return "", fmt.Errorf("命令空闲超时（%d 分钟无输出，判定卡死）", idleTimeoutMin)
	}

	if ctx.Err() == context.DeadlineExceeded {
		if cmd.Process != nil {
			killProcessGroup(cmd.Process.Pid)
		}
		logging.Log("ERROR", fmt.Sprintf("命令 %s 绝对超时并被强杀（%d 分钟限制）", command, timeoutMin))
		if agentID != "" {
			UpdateActivity(agentID, "timeout_killed", lastActivity.Load(), stdout.Len(), startTime)
		}
		return "", fmt.Errorf("命令执行超时（%d 分钟限制）", timeoutMin)
	}

	// 记录 stderr 到日志
	if stderr.Len() > 0 {
		logging.Log("WARN", fmt.Sprintf("[%s stderr] %s", command, strings.TrimSpace(stderr.String())))
	}

	if cmdErr != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = cmdErr.Error()
			// stderr 为空时，保存完整 stdout 到文件帮助诊断
			outStr := strings.TrimSpace(stdout.String())
			if outStr != "" {
				errLogDir := filepath.Join(config.QueenBeeHome, "logs")
				os.MkdirAll(errLogDir, 0o755)
				errLogFile := filepath.Join(errLogDir, fmt.Sprintf("error_%s_%d.log", command, time.Now().UnixMilli()))
				if writeErr := os.WriteFile(errLogFile, []byte(outStr), 0o644); writeErr == nil {
					logging.Log("ERROR", fmt.Sprintf("[%s 错误输出已保存] %s", command, errLogFile))
				}
			}
		}
		return "", fmt.Errorf("%s", errMsg)
	}

	return stdout.String(), nil
}

// invokeByProvider 根据 provider 调用对应的 CLI
func invokeByProvider(provider, model string, agent *types.AgentConfig, agentID, message, workingDir string, shouldReset bool, env map[string]string, timeoutMin int) (string, error) {
	// 复制 agent 配置并覆盖 provider 和 model
	effectiveAgent := *agent
	effectiveAgent.Provider = provider
	effectiveAgent.Model = model

	switch provider {
	case "openai", "codex":
		return invokeCodex(&effectiveAgent, agentID, message, workingDir, shouldReset, env, timeoutMin)
	case "opencode":
		return invokeOpenCode(&effectiveAgent, agentID, message, workingDir, shouldReset, env, timeoutMin)
	case "gemini", "google":
		return invokeGemini(&effectiveAgent, agentID, message, workingDir, shouldReset, env, timeoutMin)
	default:
		return invokeClaude(&effectiveAgent, agentID, message, workingDir, shouldReset, env, timeoutMin)
	}
}

// InvokeAgent 调用 AI Agent，根据 provider 使用不同的 CLI
// 如果主 provider 失败且配置了 FallbackProvider，会自动切换到备用 provider 重试
func InvokeAgent(
	agent *types.AgentConfig,
	agentID string,
	message string,
	workspacePath string,
	shouldReset bool,
	agents map[string]*types.AgentConfig,
	teams map[string]*types.TeamConfig,
) (string, error) {
	// 确保 agent 目录存在
	agentDir := filepath.Join(workspacePath, agentID)
	isNew := false
	if _, err := os.Stat(agentDir); os.IsNotExist(err) {
		isNew = true
	}
	EnsureAgentDirectory(agentDir)
	if isNew {
		logging.Log("INFO", fmt.Sprintf("初始化 agent 目录: %s", agentDir))
		// 首次创建时自动注册内置技能
		AutoRegisterBuiltinSkills(agentID)
	}

	// 同步数据库中配置的技能到 Agent 工作目录
	if err := SyncAgentSkills(agentID, agentDir); err != nil {
		logging.Log("WARN", fmt.Sprintf("同步 agent %s 的技能失败: %v", agentID, err))
	}

	// 更新 AGENTS.md 队友信息
	UpdateAgentTeammates(agentDir, agentID, agents, teams)

	// 解析工作目录
	workingDir := agentDir
	if agent.WorkingDirectory != "" {
		if filepath.IsAbs(agent.WorkingDirectory) {
			workingDir = agent.WorkingDirectory
		} else {
			workingDir = filepath.Join(workspacePath, agent.WorkingDirectory)
		}
	}

	// 合并环境变量：全局 env + agent 级 env（agent 级覆盖全局）
	settings := config.GetSettings()
	mergedEnv := make(map[string]string)
	for k, v := range settings.Env {
		mergedEnv[k] = v
	}
	for k, v := range agent.Env {
		mergedEnv[k] = v
	}

	provider := agent.Provider
	if provider == "" {
		provider = "anthropic"
	}
	model := agent.Model

	timeoutMin := settings.AgentTimeout

	// ── Failover 逻辑 ──
	hasFallback := agent.FallbackProvider != ""

	// 步骤 1：尝试主 provider（如果没在冷却中）
	if !isProviderCoolingDown(provider) {
		result, err := invokeByProvider(provider, model, agent, agentID, message, workingDir, shouldReset, mergedEnv, timeoutMin)
		if err == nil {
			return result, nil
		}

		// 主 provider 失败
		logging.Log("WARN", fmt.Sprintf("⚠️ 主 provider '%s' 调用失败 (agent: %s): %v", provider, agentID, err))

		if hasFallback {
			// 标记主 provider 冷却
			markProviderCooldown(provider)
		} else {
			// 没有 fallback，直接返回错误
			return "", err
		}
	} else {
		logging.Log("INFO", fmt.Sprintf("⏭️ 主 provider '%s' 冷却中，跳过 (agent: %s)", provider, agentID))
		if !hasFallback {
			return "", fmt.Errorf("主 provider '%s' 冷却中，且未配置备用 provider", provider)
		}
	}

	// 步骤 2：尝试 fallback provider
	fallbackProvider := agent.FallbackProvider
	fallbackModel := agent.FallbackModel
	if fallbackModel == "" {
		fallbackModel = model // 使用相同 model 名
	}

	if isProviderCoolingDown(fallbackProvider) {
		return "", fmt.Errorf("主 provider '%s' 和备用 provider '%s' 均在冷却中", provider, fallbackProvider)
	}

	logging.Log("INFO", fmt.Sprintf("🔄 切换到备用 provider '%s/%s' (agent: %s)", fallbackProvider, fallbackModel, agentID))
	result, err := invokeByProvider(fallbackProvider, fallbackModel, agent, agentID, message, workingDir, shouldReset, mergedEnv, timeoutMin)
	if err != nil {
		logging.Log("ERROR", fmt.Sprintf("❌ 备用 provider '%s' 也失败 (agent: %s): %v", fallbackProvider, agentID, err))
		markProviderCooldown(fallbackProvider)
		return "", fmt.Errorf("主 provider '%s' 和备用 provider '%s' 均失败: %w", provider, fallbackProvider, err)
	}

	logging.Log("INFO", fmt.Sprintf("✅ 备用 provider '%s' 成功 (agent: %s)", fallbackProvider, agentID))
	return result, nil
}

// invokeClaude 调用 Anthropic Claude CLI
// 使用 --output-format stream-json --verbose --include-partial-messages 实现流式输出
// 这样 RunCommand 的 watchdog 可以通过 stdout 活跃度判断进程是否卡死
func invokeClaude(agent *types.AgentConfig, agentID, message, workingDir string, shouldReset bool, env map[string]string, timeoutMin int) (string, error) {
	logging.Log("INFO", fmt.Sprintf("使用 Claude provider (agent: %s)", agentID))

	continueConversation := !shouldReset
	if shouldReset {
		logging.Log("INFO", fmt.Sprintf("🔄 重置对话 (agent: %s)", agentID))
	}

	modelID := config.ResolveClaudeModel(agent.Model)
	args := []string{"--dangerously-skip-permissions"}
	// stream-json 模式：每个动作都输出 JSONL 事件，用于活跃度检测
	args = append(args, "--output-format", "stream-json", "--verbose", "--include-partial-messages")
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	if agent.SystemPrompt != "" {
		args = append(args, "--system-prompt", agent.SystemPrompt)
	}
	// 不使用 -c：每条消息独立会话，避免旧上下文污染
	// Agent 仍然知道自己的文件（CLAUDE.md、工作区、技能），因为这些每次都会加载
	// 上下文靠记忆注入 + 对话历史替代 -c 的功能
	if continueConversation {
		logging.Log("DEBUG", fmt.Sprintf("跳过 -c 标志，使用记忆注入替代会话续写 (agent: %s)", agentID))
	}
	// 通过 stdin 传递消息内容，避免消息中的 - 等字符被 CLI 当作选项解析
	args = append(args, "-p", "-")

	output, err := RunCommand("claude", args, workingDir, env, timeoutMin, agentID, message)
	if err != nil {
		return "", err
	}

	// 从 stream-json JSONL 输出中提取最终回复
	return parseClaudeStreamJSON(output, agentID), nil
}

// parseClaudeStreamJSON 从 Claude CLI stream-json 输出中提取最终回复
// 最后一行 type=result 包含完整的 result 字段
func parseClaudeStreamJSON(output string, agentID string) string {
	var lastResult string
	var lastAssistantText string

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 512*1024), 512*1024) // 512KB buffer 防截断
	for scanner.Scan() {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(scanner.Text()), &obj); err != nil {
			continue
		}

		switch obj["type"] {
		case "result":
			// type=result 的 result 字段是最终回复文本
			if result, ok := obj["result"].(string); ok && result != "" {
				lastResult = result
			}
		case "assistant":
			// 备选: 从 assistant 消息的 text content 提取
			if msg, ok := obj["message"].(map[string]interface{}); ok {
				if content, ok := msg["content"].([]interface{}); ok {
					for _, c := range content {
						if block, ok := c.(map[string]interface{}); ok {
							if block["type"] == "text" {
								if text, ok := block["text"].(string); ok && text != "" {
									lastAssistantText = text
								}
							}
						}
					}
				}
			}
		}
	}

	// 优先用 result 字段，备选 assistant text
	if lastResult != "" {
		return lastResult
	}
	if lastAssistantText != "" {
		logging.Log("WARN", fmt.Sprintf("Claude agent %s 无 result 行，使用最后 assistant text", agentID))
		return lastAssistantText
	}

	// 终极 fallback：返回原始输出（可能是纯文本模式）
	trimmed := strings.TrimSpace(output)
	if trimmed != "" {
		logging.Log("WARN", fmt.Sprintf("Claude agent %s stream-json 解析失败，fallback 原始输出 (%d 字节)", agentID, len(trimmed)))
		return trimmed
	}

	logging.Log("WARN", fmt.Sprintf("Claude agent %s 返回空回复", agentID))
	return "Sorry, I could not generate a response from Claude."
}

// RunUtilityPrompt 通用辅助 AI 调用——用于压缩、记忆提取等内部任务
// 根据 provider 自动选择正确的 CLI 和参数格式
// systemPrompt 可为空（Codex/OpenCode/Gemini 不支持 --system-prompt 时会忽略）
// 返回 CLI 的原始输出文本
func RunUtilityPrompt(provider, prompt, systemPrompt string, timeoutMin int) (string, error) {
	if provider == "" {
		provider = "anthropic"
	}

	settings := config.GetSettings()
	env := make(map[string]string)
	for k, v := range settings.Env {
		env[k] = v
	}

	switch provider {
	case "openai", "codex":
		// Codex CLI: codex exec --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox --json -
		args := []string{"exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--json", "-"}
		output, err := RunCommand("codex", args, "", env, timeoutMin, "", prompt)
		if err != nil {
			return "", err
		}
		// 解析 JSONL 提取 agent_message
		var lastMsg string
		scanner := bufio.NewScanner(strings.NewReader(output))
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
		for scanner.Scan() {
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(scanner.Text()), &obj); err != nil {
				continue
			}
			if obj["type"] == "item.completed" {
				if item, ok := obj["item"].(map[string]interface{}); ok {
					if item["type"] == "agent_message" {
						if text, ok := item["text"].(string); ok && text != "" {
							lastMsg = text
						}
					}
				}
			}
		}
		if lastMsg != "" {
			return lastMsg, nil
		}
		return strings.TrimSpace(output), nil

	case "gemini", "google":
		// Gemini CLI: gemini --yolo --output-format stream-json -p <prompt>
		fullPrompt := prompt
		if systemPrompt != "" {
			fullPrompt = systemPrompt + "\n\n" + prompt
		}
		args := []string{"--yolo", "--output-format", "stream-json", "-p", fullPrompt}
		output, err := RunCommand("gemini", args, "", env, timeoutMin, "")
		if err != nil {
			return "", err
		}
		// 解析 stream-json 提取 assistant 消息
		var textParts []string
		scanner := bufio.NewScanner(strings.NewReader(output))
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
		for scanner.Scan() {
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(scanner.Text()), &obj); err != nil {
				continue
			}
			if obj["type"] == "message" {
				if role, _ := obj["role"].(string); role == "assistant" {
					if content, ok := obj["content"].(string); ok && content != "" {
						textParts = append(textParts, content)
					}
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
		return strings.TrimSpace(output), nil

	case "opencode":
		// OpenCode CLI: opencode run --format json -
		fullPrompt := prompt
		if systemPrompt != "" {
			fullPrompt = systemPrompt + "\n\n" + prompt
		}
		args := []string{"run", "--format", "json", "-"}
		output, err := RunCommand("opencode", args, "", env, timeoutMin, "", fullPrompt)
		if err != nil {
			return "", err
		}
		// 解析 JSONL 提取 text 类型
		var lastText string
		scanner := bufio.NewScanner(strings.NewReader(output))
		for scanner.Scan() {
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(scanner.Text()), &obj); err != nil {
				continue
			}
			if obj["type"] == "text" {
				if part, ok := obj["part"].(map[string]interface{}); ok {
					if text, ok := part["text"].(string); ok {
						lastText = text
					}
				}
			}
		}
		if lastText != "" {
			return lastText, nil
		}
		return strings.TrimSpace(output), nil

	default:
		// Claude CLI (anthropic): claude --dangerously-skip-permissions -p -
		args := []string{"--dangerously-skip-permissions"}
		if systemPrompt != "" {
			args = append(args, "--system-prompt", systemPrompt)
		}
		args = append(args, "-p", "-")
		output, err := RunCommand("claude", args, "", env, timeoutMin, "", prompt)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(output), nil
	}
}

// invokeCodex 调用 OpenAI Codex CLI
func invokeCodex(agent *types.AgentConfig, agentID, message, workingDir string, shouldReset bool, env map[string]string, timeoutMin int) (string, error) {
	logging.Log("INFO", fmt.Sprintf("使用 Codex CLI (agent: %s)", agentID))

	shouldResume := !shouldReset
	if shouldReset {
		logging.Log("INFO", fmt.Sprintf("🔄 重置 Codex 对话 (agent: %s)", agentID))
	}

	// 检查是否有历史会话可以 resume（没有 .codex 目录说明是首次）
	codexStateDir := filepath.Join(workingDir, ".codex")
	if _, err := os.Stat(codexStateDir); os.IsNotExist(err) {
		shouldResume = false
	}

	// 对齐 tinyclaw invoke.ts 第 92-100 行
	modelID := config.ResolveCodexModel(agent.Model)
	args := []string{"exec"}
	if shouldResume {
		args = append(args, "resume", "--last")
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	args = append(args, "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--json", "-")

	output, err := RunCommand("codex", args, workingDir, env, timeoutMin, agentID, message)
	if err != nil {
		return "", err
	}

	// 解析 JSONL 输出——收集所有 agent_message 文本
	var agentMessages []string
	lastReasoning := ""
	scanner := bufio.NewScanner(strings.NewReader(output))
	// 增大 scanner buffer 防止长行被截断
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	for scanner.Scan() {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(scanner.Text()), &obj); err != nil {
			continue
		}
		if obj["type"] == "item.completed" {
			if item, ok := obj["item"].(map[string]interface{}); ok {
				itemType, _ := item["type"].(string)
				text, _ := item["text"].(string)
				switch itemType {
				case "agent_message":
					if text != "" {
						agentMessages = append(agentMessages, text)
					}
				case "reasoning":
					if text != "" {
						lastReasoning = text
					}
				}
			}
		}
	}

	// 优先用最后一个 agent_message，否则 fallback 到 reasoning
	response := ""
	if len(agentMessages) > 0 {
		response = agentMessages[len(agentMessages)-1]
	} else if lastReasoning != "" {
		response = lastReasoning
		logging.Log("WARN", fmt.Sprintf("Codex agent %s 无 agent_message，使用 reasoning 作为回复", agentID))
	}

	if response == "" {
		logging.Log("WARN", fmt.Sprintf("Codex agent %s 返回空回复，原始输出长度: %d", agentID, len(output)))
		return "Sorry, I could not generate a response from Codex.", nil
	}
	return response, nil
}

// invokeOpenCode 调用 OpenCode CLI
func invokeOpenCode(agent *types.AgentConfig, agentID, message, workingDir string, shouldReset bool, env map[string]string, timeoutMin int) (string, error) {
	modelID := config.ResolveOpenCodeModel(agent.Model)
	logging.Log("INFO", fmt.Sprintf("使用 OpenCode CLI (agent: %s, model: %s)", agentID, modelID))

	continueConversation := !shouldReset
	if shouldReset {
		logging.Log("INFO", fmt.Sprintf("🔄 重置 OpenCode 对话 (agent: %s)", agentID))
	}

	args := []string{"run", "--format", "json"}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	// 不使用 -c：每条消息独立会话，避免旧上下文污染（与 Claude provider 一致）
	if continueConversation {
		logging.Log("DEBUG", fmt.Sprintf("跳过 -c 标志，使用记忆注入替代会话续写 (agent: %s)", agentID))
	}
	// 通过 stdin 传递消息
	args = append(args, "-")

	output, err := RunCommand("opencode", args, workingDir, env, timeoutMin, agentID, message)
	if err != nil {
		return "", err
	}

	// 解析 JSONL 输出
	response := ""
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(scanner.Text()), &obj); err != nil {
			continue
		}
		if obj["type"] == "text" {
			if part, ok := obj["part"].(map[string]interface{}); ok {
				if text, ok := part["text"].(string); ok {
					response = text
				}
			}
		}
	}

	if response == "" {
		return "Sorry, I could not generate a response from OpenCode.", nil
	}
	return response, nil
}

// invokeGemini 调用 Google Gemini CLI
func invokeGemini(agent *types.AgentConfig, agentID, message, workingDir string, shouldReset bool, env map[string]string, timeoutMin int) (string, error) {
	modelID := config.ResolveGeminiModel(agent.Model)
	logging.Log("INFO", fmt.Sprintf("使用 Gemini CLI (agent: %s, model: %s)", agentID, modelID))

	if shouldReset {
		logging.Log("INFO", fmt.Sprintf("🔄 重置 Gemini 对话 (agent: %s)", agentID))
	}

	// 构建参数：非交互模式 + stream-json 输出 + 自动批准
	// stream-json 模式每个动作都输出 JSONL 事件，用于 watchdog 活跃度检测
	args := []string{
		"--yolo",                         // 自动批准所有工具调用
		"--output-format", "stream-json", // 流式 JSON 格式输出（与 Claude 对齐）
	}
	if modelID != "" {
		args = append(args, "-m", modelID)
	}
	// Gemini CLI 不使用 -r resume：
	// 1. 会话数据存在 ~/.gemini/tmp/ 全局目录，无法可靠检测
	// 2. 失败会话 resume 后会引入脏数据（重复 user message → API 400）
	// 3. Gemini CLI 每次启动自动读 AGENTS.md/GEMINI.md，项目上下文不丢
	// 4. QueenBee 的任务模型天然适合每次新会话
	// Gemini CLI 使用 yargs 解析参数，能正确处理 -p 后带 - 前缀的消息值
	// 注意：不能用 stdin + -p "" 方式，会导致两条连续 user message 触发 INVALID_ARGUMENT
	args = append(args, "-p", message)

	output, err := RunCommand("gemini", args, workingDir, env, timeoutMin, agentID)
	if err != nil {
		return "", err
	}

	// 解析 stream-json 输出
	// Gemini CLI --output-format stream-json 输出格式：
	// {"type":"init","session_id":"...","model":"..."}
	// {"type":"message","role":"user","content":"..."}
	// {"type":"message","role":"assistant","content":"...","delta":true}
	// {"type":"result","status":"success","stats":{...}}
	var textParts []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}

		turnType, _ := obj["type"].(string)

		// stream-json 格式：type=message + role=assistant 包含模型回复
		if turnType == "message" {
			role, _ := obj["role"].(string)
			if role == "assistant" {
				if content, ok := obj["content"].(string); ok && content != "" {
					textParts = append(textParts, content)
				}
			}
		}

		// 兼容旧 json 格式的 modelTurn 类型
		if turnType == "modelTurn" {
			if parts, ok := obj["parts"].([]interface{}); ok {
				for _, p := range parts {
					if part, ok := p.(map[string]interface{}); ok {
						if text, ok := part["text"].(string); ok && text != "" {
							textParts = append(textParts, text)
						}
					}
				}
			}
		}
	}

	// 合并所有文本部分
	response := strings.Join(textParts, "\n")

	// 如果 JSON 解析不出内容，fallback 到原始输出（可能是纯文本模式）
	if response == "" {
		response = strings.TrimSpace(output)
	}

	if response == "" {
		logging.Log("WARN", fmt.Sprintf("Gemini agent %s 返回空回复，原始输出长度: %d", agentID, len(output)))
		return "Sorry, I could not generate a response from Gemini.", nil
	}
	return response, nil
}

// KillAgentProcess 强杀指定 agent 的运行进程
// 返回 true 表示找到并杀掉了进程
func KillAgentProcess(agentID string) bool {
	val, ok := processStore.Load(agentID)
	if !ok {
		return false
	}
	proc, ok := val.(*os.Process)
	if !ok || proc == nil {
		processStore.Delete(agentID)
		return false
	}

	// 杀掉整个进程组（包括子进程如 Node.js 等）
	// 先设标记，让 ProcessMessage 知道这是手动强杀
	killedAgentStore.Store(agentID, true)
	if err := killProcessGroup(proc.Pid); err != nil {
		logging.Log("WARN", fmt.Sprintf("强杀 agent %s 进程组 (pid=%d) 失败: %v", agentID, proc.Pid, err))
		// 尝试直接杀进程
		proc.Kill()
	} else {
		logging.Log("INFO", fmt.Sprintf("✅ 强杀 agent %s 进程组 (pid=%d) 成功", agentID, proc.Pid))
	}

	processStore.Delete(agentID)
	RemoveActivity(agentID)
	return true
}

// KillAllAgentProcesses 强杀所有正在运行的 agent 进程
// 返回被杀掉的 agent ID 列表
func KillAllAgentProcesses() []string {
	var killed []string
	processStore.Range(func(key, value interface{}) bool {
		agentID, ok := key.(string)
		if !ok {
			return true
		}
		if KillAgentProcess(agentID) {
			killed = append(killed, agentID)
		}
		return true
	})
	return killed
}

// IsAgentManualKilled 检查 agent 是否被手动强杀
func IsAgentManualKilled(agentID string) bool {
	_, ok := killedAgentStore.Load(agentID)
	return ok
}

// ClearAgentManualKilled 清除手动强杀标记
func ClearAgentManualKilled(agentID string) {
	killedAgentStore.Delete(agentID)
}
