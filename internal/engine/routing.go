package engine

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/types"
)

// ParseAgentRouting 解析 @agent_id 或 @team_id 前缀
// 如果消息中有多个 @mention，优先匹配具体 agent，避免用户 @了具体 agent 时还路由到 team leader
func ParseAgentRouting(rawMessage string, agents map[string]*types.AgentConfig, teams map[string]*types.TeamConfig) (agentID, message string, isTeam bool) {
	// 提取所有 @mention（支持消息任意位置）
	mentionRe := regexp.MustCompile(`@([\w][\w-]*)`)
	allMentions := mentionRe.FindAllStringSubmatch(rawMessage, -1)

	// 第一轮：在所有 @mention 中优先找具体 agent
	for _, m := range allMentions {
		candidateID := strings.ToLower(m[1])
		if _, ok := agents[candidateID]; ok {
			// 找到具体 agent，移除消息中的 @agent_id 和 @team_id 前缀
			cleaned := cleanMentionPrefixes(rawMessage, agents, teams)
			return candidateID, cleaned, false
		}
		// 按 agent name 匹配
		for id, cfg := range agents {
			if strings.ToLower(cfg.Name) == candidateID {
				cleaned := cleanMentionPrefixes(rawMessage, agents, teams)
				return id, cleaned, false
			}
		}
	}

	// 第二轮：没有具体 agent，fallback 到 team → leader
	for _, m := range allMentions {
		candidateID := strings.ToLower(m[1])
		if team, ok := teams[candidateID]; ok {
			cleaned := cleanMentionPrefixes(rawMessage, agents, teams)
			return team.LeaderAgent, cleaned, true
		}
		// 按 team name 匹配
		for _, cfg := range teams {
			if strings.ToLower(cfg.Name) == candidateID {
				cleaned := cleanMentionPrefixes(rawMessage, agents, teams)
				return cfg.LeaderAgent, cleaned, true
			}
		}
	}

	// 兼容旧格式：[channel/sender]: @agent_id message
	re := regexp.MustCompile(`^(\[[^\]]*\]:\s*)?@(\S+)\s+([\s\S]*)$`)
	match := re.FindStringSubmatch(rawMessage)
	if match != nil {
		prefix := match[1]
		candidateID := strings.ToLower(match[2])
		msg := prefix + match[3]
		if _, ok := agents[candidateID]; ok {
			return candidateID, msg, false
		}
		if team, ok := teams[candidateID]; ok {
			return team.LeaderAgent, msg, true
		}
	}

	return "default", rawMessage, false
}

// cleanMentionPrefixes 移除消息开头的 @agent/@team 前缀，保留消息正文
func cleanMentionPrefixes(msg string, agents map[string]*types.AgentConfig, teams map[string]*types.TeamConfig) string {
	// 匹配消息开头的 [channel/sender]: 前缀
	prefixRe := regexp.MustCompile(`^(\[[^\]]*\]:\s*)`)
	prefix := ""
	if m := prefixRe.FindString(msg); m != "" {
		prefix = m
		msg = msg[len(m):]
	}
	// 移除开头连续的 @mention（agent/team ID）
	mentionRe := regexp.MustCompile(`^@[\w][\w-]*\s*`)
	for {
		loc := mentionRe.FindStringIndex(msg)
		if loc == nil || loc[0] != 0 {
			break
		}
		msg = msg[loc[1]:]
	}
	return prefix + strings.TrimSpace(msg)
}

// FindTeamForAgent 查找 agent 所属的第一个团队
func FindTeamForAgent(agentID string, teams map[string]*types.TeamConfig) *types.TeamContext {
	for teamID, team := range teams {
		for _, a := range team.Agents {
			if a == agentID {
				return &types.TeamContext{TeamID: teamID, Team: team}
			}
		}
	}
	return nil
}

// IsTeammate 检查 mentioned 是否是 currentAgent 的队友
func IsTeammate(mentionedID, currentAgentID, teamID string, teams map[string]*types.TeamConfig, agents map[string]*types.AgentConfig) bool {
	team, ok := teams[teamID]
	if !ok {
		logging.Log("WARN", fmt.Sprintf("isTeammate: Team '%s' 不存在", teamID))
		return false
	}

	if mentionedID == currentAgentID {
		return false
	}

	found := false
	for _, a := range team.Agents {
		if a == mentionedID {
			found = true
			break
		}
	}
	if !found {
		logging.Log("WARN", fmt.Sprintf("isTeammate: Agent '%s' 不在 team '%s' (team.Agents=%v)", mentionedID, teamID, team.Agents))
		return false
	}

	if _, ok := agents[mentionedID]; !ok {
		logging.Log("WARN", fmt.Sprintf("isTeammate: Agent '%s' 不在 agents 配置中", mentionedID))
		return false
	}

	return true
}

// TeammateMessage 队友消息提取结果
type TeammateMessage struct {
	TeammateID string
	Message    string
}

// ExtractTeammateMentions 提取 [@teammate: message] 标签
func ExtractTeammateMentions(response, currentAgentID, teamID string, teams map[string]*types.TeamConfig, agents map[string]*types.AgentConfig) []TeammateMessage {
	var results []TeammateMessage
	seen := make(map[string]bool)

	// [@agent_id: message] 或 [@agent1,agent2: message]
	tagRegex := regexp.MustCompile(`\[@([^\]]+?):\s*([\s\S]*?)\]`)

	// 共享上下文 = 去掉所有 tag 后的文本
	sharedContext := strings.TrimSpace(tagRegex.ReplaceAllString(response, ""))

	matches := tagRegex.FindAllStringSubmatch(response, -1)
	for _, match := range matches {
		directMessage := strings.TrimSpace(match[2])
		fullMessage := directMessage
		if sharedContext != "" {
			fullMessage = sharedContext + "\n\n------\n\nDirected to you:\n" + directMessage
		}

		// 支持逗号分隔 [@coder,reviewer: message]
		candidateIDs := strings.Split(strings.ToLower(match[1]), ",")
		for _, candidateID := range candidateIDs {
			candidateID = strings.TrimSpace(candidateID)
			if candidateID == "" || seen[candidateID] {
				continue
			}
			if IsTeammate(candidateID, currentAgentID, teamID, teams, agents) {
				results = append(results, TeammateMessage{TeammateID: candidateID, Message: fullMessage})
				seen[candidateID] = true
			}
		}
	}

	return results
}

// GetAgentResetFlag 获取 agent 重置标志路径
func GetAgentResetFlag(agentID, workspacePath string) string {
	return filepath.Join(workspacePath, agentID, "reset_flag")
}
