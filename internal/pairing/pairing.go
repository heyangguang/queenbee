package pairing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PendingEntry 待审批条目
type PendingEntry struct {
	Channel    string `json:"channel"`
	SenderID   string `json:"sender_id"`
	Sender     string `json:"sender"`
	Code       string `json:"code"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
}

// ApprovedEntry 已批准条目
type ApprovedEntry struct {
	Channel      string `json:"channel"`
	SenderID     string `json:"sender_id"`
	Sender       string `json:"sender"`
	ApprovedAt   int64  `json:"approved_at"`
	ApprovedCode string `json:"approved_code,omitempty"`
}

// State 配对状态
type State struct {
	Pending  []PendingEntry  `json:"pending"`
	Approved []ApprovedEntry `json:"approved"`
}

// CheckResult 配对检查结果
type CheckResult struct {
	Approved     bool
	Code         string
	IsNewPending bool
}

// ApproveResult 审批结果
type ApproveResult struct {
	OK     bool
	Reason string
	Entry  *ApprovedEntry
}

func loadState(pairingFile string) *State {
	data, err := os.ReadFile(pairingFile)
	if err != nil {
		return &State{}
	}
	var state State
	json.Unmarshal(data, &state)
	return &state
}

func saveState(pairingFile string, state *State) {
	os.MkdirAll(filepath.Dir(pairingFile), 0o755)
	data, _ := json.MarshalIndent(state, "", "  ")
	tmp := pairingFile + ".tmp"
	os.WriteFile(tmp, data, 0o644)
	os.Rename(tmp, pairingFile)
}

func randomCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	rand.Read(b)
	code := make([]byte, 8)
	for i := range b {
		code[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(code)
}

func createUniqueCode(state *State) string {
	existing := make(map[string]bool)
	for _, p := range state.Pending {
		existing[strings.ToUpper(p.Code)] = true
	}
	for _, a := range state.Approved {
		if a.ApprovedCode != "" {
			existing[strings.ToUpper(a.ApprovedCode)] = true
		}
	}
	for i := 0; i < 20; i++ {
		code := randomCode()
		if !existing[code] {
			return code
		}
	}
	return strings.ToUpper(hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))[:8])
}

// EnsureSenderPaired 确保发送者已配对
func EnsureSenderPaired(pairingFile, channel, senderID, sender string) *CheckResult {
	state := loadState(pairingFile)
	senderKey := channel + "::" + senderID

	// 检查已批准
	for i, a := range state.Approved {
		if a.Channel+"::"+a.SenderID == senderKey {
			if a.Sender != sender {
				state.Approved[i].Sender = sender
				saveState(pairingFile, state)
			}
			return &CheckResult{Approved: true}
		}
	}

	// 检查已 pending
	for i, p := range state.Pending {
		if p.Channel == channel && p.SenderID == senderID {
			state.Pending[i].LastSeenAt = time.Now().UnixMilli()
			state.Pending[i].Sender = sender
			saveState(pairingFile, state)
			return &CheckResult{Approved: false, Code: p.Code, IsNewPending: false}
		}
	}

	// 新 pending
	code := createUniqueCode(state)
	now := time.Now().UnixMilli()
	state.Pending = append(state.Pending, PendingEntry{
		Channel: channel, SenderID: senderID, Sender: sender,
		Code: code, CreatedAt: now, LastSeenAt: now,
	})
	saveState(pairingFile, state)
	return &CheckResult{Approved: false, Code: code, IsNewPending: true}
}

// ApprovePairingCode 审批配对码
func ApprovePairingCode(pairingFile, code string) *ApproveResult {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if normalized == "" {
		return &ApproveResult{OK: false, Reason: "配对码不能为空"}
	}

	state := loadState(pairingFile)
	for i, p := range state.Pending {
		if strings.ToUpper(p.Code) == normalized {
			state.Pending = append(state.Pending[:i], state.Pending[i+1:]...)
			entry := ApprovedEntry{
				Channel: p.Channel, SenderID: p.SenderID, Sender: p.Sender,
				ApprovedAt: time.Now().UnixMilli(), ApprovedCode: normalized,
			}
			// 检查是否已存在相同 sender 的 approved 条目，避免重复
			existingIdx := -1
			for j, a := range state.Approved {
				if a.Channel == p.Channel && a.SenderID == p.SenderID {
					existingIdx = j
					break
				}
			}
			if existingIdx >= 0 {
				state.Approved[existingIdx] = entry
			} else {
				state.Approved = append(state.Approved, entry)
			}
			saveState(pairingFile, state)
			return &ApproveResult{OK: true, Entry: &entry}
		}
	}
	return &ApproveResult{OK: false, Reason: fmt.Sprintf("配对码不存在: %s", normalized)}
}

// LoadState 加载配对状态（供 CLI 使用）
func LoadState(pairingFile string) *State {
	return loadState(pairingFile)
}
