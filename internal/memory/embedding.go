package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/queenbee-ai/queenbee/internal/logging"
)

// ═══════════════════════════════════════════════════════════════════
// Embedding — 向量化模块
//
// 支持 Ollama 本地 embedding（零成本语义搜索）。
// 自动检测 Ollama 服务是否可用，不可用时降级到 FTS5/LIKE。
// ═══════════════════════════════════════════════════════════════════

// 默认 Ollama 配置
const (
	DefaultOllamaURL   = "http://localhost:11434"
	DefaultOllamaModel = "nomic-embed-text" // 轻量级 embedding 模型
)

var (
	ollamaAvailable     bool
	ollamaChecked       bool
	ollamaCheckMu       sync.Mutex
	ollamaLastCheckTime time.Time
	ollamaCheckInterval = 5 * time.Minute // 每 5 分钟重新检测一次
)

// ollamaEmbedRequest Ollama embedding API 请求
type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// ollamaEmbedResponse Ollama embedding API 响应
type ollamaEmbedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

// IsOllamaAvailable 检测 Ollama 是否可用（带缓存）
func IsOllamaAvailable() bool {
	ollamaCheckMu.Lock()
	defer ollamaCheckMu.Unlock()

	// 缓存有效期内直接返回
	if ollamaChecked && time.Since(ollamaLastCheckTime) < ollamaCheckInterval {
		return ollamaAvailable
	}

	// 检测 Ollama 服务
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(DefaultOllamaURL + "/api/tags")
	if err != nil {
		if !ollamaChecked {
			logging.Log("INFO", "🔍 Ollama 未检测到，Memory 搜索将使用 FTS5 关键词匹配")
		}
		ollamaAvailable = false
		ollamaChecked = true
		ollamaLastCheckTime = time.Now()
		return false
	}
	resp.Body.Close()

	// 检查是否有 embedding 模型
	ollamaAvailable = resp.StatusCode == 200
	ollamaChecked = true
	ollamaLastCheckTime = time.Now()

	if ollamaAvailable && !ollamaChecked {
		logging.Log("INFO", "✅ Ollama 已检测到，Memory 搜索将使用语义向量匹配")
	}

	return ollamaAvailable
}

// GetEmbedding 获取文本的 embedding 向量
func GetEmbedding(text string) ([]float64, error) {
	if !IsOllamaAvailable() {
		return nil, fmt.Errorf("Ollama 不可用")
	}

	reqBody := ollamaEmbedRequest{
		Model: DefaultOllamaModel,
		Input: text,
	}
	body, _ := json.Marshal(reqBody)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(DefaultOllamaURL+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("Ollama embedding 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		errMsg := string(respBody)
		// 如果模型不存在，记录提示
		if resp.StatusCode == 404 {
			logging.Log("WARN", fmt.Sprintf("Ollama 模型 %s 不存在，请运行: ollama pull %s", DefaultOllamaModel, DefaultOllamaModel))
		}
		return nil, fmt.Errorf("Ollama embedding 失败 (status %d): %s", resp.StatusCode, errMsg)
	}

	var embedResp ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("解析 Ollama embedding 响应失败: %w", err)
	}

	if len(embedResp.Embeddings) == 0 || len(embedResp.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("Ollama 返回空 embedding")
	}

	return embedResp.Embeddings[0], nil
}

// CosineSimilarity 计算两个向量的余弦相似度
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}
