package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	logFile    string
	logMu      sync.Mutex
	listeners  []EventListener
	listenerMu sync.RWMutex
)

// EventListener 事件监听函数
type EventListener func(eventType string, data map[string]interface{})

// Init 初始化日志路径
func Init(logFilePath string) {
	logFile = logFilePath
	dir := filepath.Dir(logFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "无法创建日志目录: %v\n", err)
	}
}

// Log 写日志到 stdout 和文件
func Log(level, message string) {
	timestamp := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("[%s] [%s] %s\n", timestamp, level, message)
	fmt.Print(line)

	if logFile == "" {
		return
	}

	logMu.Lock()
	defer logMu.Unlock()
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line)
}

// OnEvent 注册事件监听器
func OnEvent(listener EventListener) {
	listenerMu.Lock()
	defer listenerMu.Unlock()
	listeners = append(listeners, listener)
}

// EmitEvent 广播事件到所有监听器
func EmitEvent(eventType string, data map[string]interface{}) {
	listenerMu.RLock()
	defer listenerMu.RUnlock()
	for _, l := range listeners {
		func() {
			defer func() { recover() }() // 防止崩溃
			l(eventType, data)
		}()
	}
}
