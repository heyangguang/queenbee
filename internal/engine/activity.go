package engine

import (
	"encoding/json"
	"sync"
	"time"
)

// AgentActivity 记录单个 agent 的实时执行状态
type AgentActivity struct {
	AgentID        string `json:"agentId"`
	Status         string `json:"status"`         // active, idle_warning, done, idle_killed, timeout_killed
	LastActivityMs int64  `json:"lastActivityMs"` // 最后一次 stdout 输出的时间戳
	IdleSec        int    `json:"idleSec"`        // 距上次输出的秒数
	StdoutBytes    int    `json:"stdoutBytes"`    // 已生成的 stdout 字节数
	StartedAt      int64  `json:"startedAt"`      // 进程启动时间
	ElapsedSec     int    `json:"elapsedSec"`     // 已运行总秒数
}

// 全局活跃度存储
var activityStore sync.Map // map[string]*AgentActivity

// 活跃度事件订阅者
var (
	activityListeners   []func(activity *AgentActivity)
	activityListenersMu sync.RWMutex
)

// OnActivity 注册活跃度事件监听器
func OnActivity(fn func(activity *AgentActivity)) {
	activityListenersMu.Lock()
	defer activityListenersMu.Unlock()
	activityListeners = append(activityListeners, fn)
}

// broadcastActivity 通知所有监听器
func broadcastActivity(activity *AgentActivity) {
	activityListenersMu.RLock()
	defer activityListenersMu.RUnlock()
	for _, fn := range activityListeners {
		fn(activity)
	}
}

// UpdateActivity 更新 agent 活跃状态并广播
func UpdateActivity(agentID, status string, lastActivityMs int64, stdoutBytes int, startedAt int64) {
	now := time.Now().UnixMilli()
	idleSec := int((now - lastActivityMs) / 1000)
	elapsedSec := int((now - startedAt) / 1000)

	activity := &AgentActivity{
		AgentID:        agentID,
		Status:         status,
		LastActivityMs: lastActivityMs,
		IdleSec:        idleSec,
		StdoutBytes:    stdoutBytes,
		StartedAt:      startedAt,
		ElapsedSec:     elapsedSec,
	}

	activityStore.Store(agentID, activity)
	broadcastActivity(activity)
}

// RemoveActivity 移除 agent 活跃状态（进程结束后调用）
func RemoveActivity(agentID string) {
	activityStore.Delete(agentID)
}

// GetAllActivities 获取所有活跃 agent 的状态快照
// 返回时基于当前时间重新计算 ElapsedSec 和 IdleSec，
// 确保客户端连接时获得最新值而非缓存旧值
func GetAllActivities() []*AgentActivity {
	now := time.Now().UnixMilli()
	var result []*AgentActivity
	activityStore.Range(func(key, value interface{}) bool {
		if a, ok := value.(*AgentActivity); ok {
			// 基于当前时间重新计算，避免快照过期
			fresh := *a // 浅拷贝，不影响 store 原值
			fresh.ElapsedSec = int((now - a.StartedAt) / 1000)
			fresh.IdleSec = int((now - a.LastActivityMs) / 1000)
			result = append(result, &fresh)
		}
		return true
	})
	return result
}

// IsAgentActive 检查指定 agent 是否仍在活跃工作
// 判断标准：在 activityStore 中且最近 2 分钟内有 stdout 输出
func IsAgentActive(agentID string) bool {
	val, ok := activityStore.Load(agentID)
	if !ok {
		return false
	}
	a, ok := val.(*AgentActivity)
	if !ok {
		return false
	}
	idleSec := int((time.Now().UnixMilli() - a.LastActivityMs) / 1000)
	return idleSec < 120 // 2 分钟内有输出视为活跃
}

// ActivityToJSON 将活跃度数据序列化为 JSON
func ActivityToJSON(a *AgentActivity) string {
	data, _ := json.Marshal(a)
	return string(data)
}
