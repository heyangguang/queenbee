package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/queenbee-ai/queenbee/internal/engine"
	"github.com/queenbee-ai/queenbee/internal/logging"
)

var (
	sseClients   = make(map[http.ResponseWriter]chan string)
	sseClientsMu sync.RWMutex
)

// AddSSEClient 注册 SSE 客户端
func AddSSEClient(w http.ResponseWriter, ch chan string) {
	sseClientsMu.Lock()
	defer sseClientsMu.Unlock()
	sseClients[w] = ch
}

// RemoveSSEClient 移除 SSE 客户端
func RemoveSSEClient(w http.ResponseWriter) {
	sseClientsMu.Lock()
	defer sseClientsMu.Unlock()
	if ch, ok := sseClients[w]; ok {
		close(ch)
		delete(sseClients, w)
	}
}

// BroadcastSSE 广播 SSE 事件
func BroadcastSSE(eventType string, data string) {
	sseClientsMu.RLock()
	defer sseClientsMu.RUnlock()
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data)
	for _, ch := range sseClients {
		select {
		case ch <- msg:
		default:
			// OPT-5：满时记日志而非静默丢弃
			logging.Log("WARN", fmt.Sprintf("SSE 事件丢弃 (channel 已满): %s", eventType))
		}
	}
}

// --- 活跃度 SSE（独立通道） ---

var (
	activityClients   = make(map[http.ResponseWriter]chan string)
	activityClientsMu sync.RWMutex
)

// AddActivityClient 注册活跃度 SSE 客户端
func AddActivityClient(w http.ResponseWriter, ch chan string) {
	activityClientsMu.Lock()
	defer activityClientsMu.Unlock()
	activityClients[w] = ch
}

// RemoveActivityClient 移除活跃度 SSE 客户端
func RemoveActivityClient(w http.ResponseWriter) {
	activityClientsMu.Lock()
	defer activityClientsMu.Unlock()
	if ch, ok := activityClients[w]; ok {
		close(ch)
		delete(activityClients, w)
	}
}

// BroadcastActivity 向活跃度 SSE 通道广播事件
func BroadcastActivity(data string) {
	activityClientsMu.RLock()
	defer activityClientsMu.RUnlock()
	msg := fmt.Sprintf("event: agent_activity\ndata: %s\n\n", data)
	for _, ch := range activityClients {
		select {
		case ch <- msg:
		default:
			// 满了就丢弃，活跃度事件不关键
		}
	}
}

func init() {
	// 团队协作事件 → 现有 SSE 通道
	logging.OnEvent(func(eventType string, data map[string]interface{}) {
		// 将事件数据平铺到顶层（而非嵌套在 data 子对象中）
		flat := make(map[string]interface{}, len(data)+2)
		flat["type"] = eventType
		flat["timestamp"] = time.Now().UnixMilli()
		for k, v := range data {
			flat[k] = v
		}
		jsonData, _ := json.Marshal(flat)
		BroadcastSSE(eventType, string(jsonData))
	})

	// Agent 活跃度事件 → 独立活跃度 SSE 通道
	engine.OnActivity(func(activity *engine.AgentActivity) {
		BroadcastActivity(engine.ActivityToJSON(activity))
	})
}
