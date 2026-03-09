package engine

import (
	"fmt"
	"strings"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/logging"
)

// ═══════════════════════════════════════════════════════════════════
// Context Compaction — 上下文压缩
//
// 当团队会话中的 handoff 消息过长时，使用 AI 摘要自动压缩，
// 减少传递给下游 agent 的 token 消耗，同时保留关键信息。
// ═══════════════════════════════════════════════════════════════════

// 默认压缩阈值（字符数）：超过此长度的消息将被压缩
const DefaultCompactionThreshold = 8000

// compactionSystemPrompt 压缩用的系统提示词
const compactionSystemPrompt = `You are a context compaction assistant. Your job is to compress a long message into a concise summary while preserving ALL critical information.

Rules:
1. Keep all code snippets, file paths, error messages, and technical details
2. Keep all action items, decisions, and conclusions
3. Remove redundant explanations, pleasantries, and verbose formatting
4. Keep @mentions and team member references intact
5. Output should be 30-50% of the original length
6. Use bullet points for clarity
7. Start with "[Compacted Context]" header`

// ShouldCompactMessage 判断消息是否需要压缩
func ShouldCompactMessage(message string) bool {
	threshold := getCompactionThreshold()
	return len([]rune(message)) > threshold
}

// CompactMessage 使用 AI 摘要压缩过长的消息
// 返回压缩后的消息和是否成功压缩的标志
func CompactMessage(message string, agentID string, provider string) (string, bool) {
	if !ShouldCompactMessage(message) {
		return message, false
	}

	originalLen := len([]rune(message))
	logging.Log("INFO", fmt.Sprintf("📦 消息压缩: agent %s 的输入消息 %d 字符，超过阈值 %d，开始压缩",
		agentID, originalLen, getCompactionThreshold()))

	// 使用对应 provider 的 CLI 进行压缩（轻量级调用，不影响 agent 的主会话）
	compactPrompt := fmt.Sprintf("Compress the following message while preserving all critical information:\n\n---\n%s\n---", message)

	output, err := RunUtilityPrompt(provider, compactPrompt, compactionSystemPrompt, 5)
	if err != nil {
		logging.Log("WARN", fmt.Sprintf("📦 消息压缩失败 (agent: %s): %v，使用原始消息", agentID, err))
		// 压缩失败时的 fallback：简单截断保留首尾
		return fallbackTruncate(message), true
	}

	compacted := strings.TrimSpace(output)
	if compacted == "" {
		logging.Log("WARN", fmt.Sprintf("📦 AI 压缩返回空结果 (agent: %s)，使用 fallback 截断", agentID))
		return fallbackTruncate(message), true
	}

	compactedLen := len([]rune(compacted))
	ratio := float64(compactedLen) / float64(originalLen) * 100
	logging.Log("INFO", fmt.Sprintf("📦 消息压缩完成: %d → %d 字符 (%.0f%%)", originalLen, compactedLen, ratio))

	return compacted, true
}

// fallbackTruncate 压缩失败时的简单截断策略
// 保留消息的首部和尾部，中间替换为摘要标记
func fallbackTruncate(message string) string {
	runes := []rune(message)
	threshold := getCompactionThreshold()

	if len(runes) <= threshold {
		return message
	}

	// 保留首部 40% 和尾部 40%，中间截断
	keepChars := threshold * 4 / 10
	head := string(runes[:keepChars])
	tail := string(runes[len(runes)-keepChars:])
	skipped := len(runes) - 2*keepChars

	return fmt.Sprintf("%s\n\n[... 中间 %d 字符已省略，请基于首尾上下文继续工作 ...]\n\n%s", head, skipped, tail)
}

// getCompactionThreshold 获取压缩阈值
func getCompactionThreshold() int {
	settings := config.GetSettings()
	if settings.CompactionThreshold > 0 {
		return settings.CompactionThreshold
	}
	return DefaultCompactionThreshold
}
