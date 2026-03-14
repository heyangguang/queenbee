package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/db"
	"github.com/queenbee-ai/queenbee/internal/engine"
	"github.com/queenbee-ai/queenbee/internal/logging"
	"github.com/queenbee-ai/queenbee/internal/pairing"
	"github.com/queenbee-ai/queenbee/internal/plugins"
	"github.com/queenbee-ai/queenbee/internal/server"
	"github.com/queenbee-ai/queenbee/types"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "queenbee",
	Short: "QueenBee — 多 Agent 多团队 AI 助手",
	Long:  "QueenBee 🐝 — Multi-agent, multi-team AI assistant framework written in Go.",
}

// Execute 入口
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(func() {
		config.Init()
	})

	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(teamCmd)
	rootCmd.AddCommand(sendCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(attachCmd)
	rootCmd.AddCommand(providerCmd)
	rootCmd.AddCommand(modelCmd)
	rootCmd.AddCommand(pairingCmd)
}

// ==================== start ====================

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "启动 QueenBee 守护进程",
	Run: func(cmd *cobra.Command, args []string) {
		// 检查是否已在运行
		pidFile := filepath.Join(config.QueenBeeHome, "queenbee.pid")
		if pid := readPID(pidFile); pid > 0 {
			if processExists(pid) {
				fmt.Printf("✅ QueenBee 已在运行 (PID: %d)\n", pid)
				return
			}
		}

		daemon, _ := cmd.Flags().GetBool("daemon")
		if daemon {
			// 后台运行：重新启动自己
			exe, _ := os.Executable()
			attr := &os.ProcAttr{
				Dir:   filepath.Dir(exe),
				Env:   os.Environ(),
				Files: []*os.File{nil, nil, nil},
			}
			proc, err := os.StartProcess(exe, []string{exe, "start", "--foreground"}, attr)
			if err != nil {
				fmt.Printf("❌ 启动失败: %v\n", err)
				return
			}
			proc.Release()
			fmt.Printf("✅ QueenBee 已在后台启动 (PID: %d)\n", proc.Pid)
			return
		}

		// 前台运行
		fmt.Println("🐝 QueenBee 正在启动...")

		// 写 PID
		os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
		defer os.Remove(pidFile)

		// 启动队列处理器（内部会初始化数据库）
		engine.StartProcessor()

		// 数据库初始化后再检查是否为空
		if db.AgentCount() == 0 {
			fmt.Println("⚠️  数据库为空，请通过前端或 API 进行初始化")
			fmt.Println("   POST http://localhost:3777/api/setup")
		}

		// 加载插件
		plugins.LoadPlugins()

		// 启动 API Server
		apiServer := server.StartAPIServer()

		// 启动 Heartbeat（如果配置了）
		settings := config.GetSettings()
		if settings.Monitoring != nil && settings.Monitoring.HeartbeatInterval > 0 {
			go startHeartbeat(settings)
		}

		fmt.Println("✅ QueenBee 已启动")
		fmt.Printf("   API: http://localhost:3777\n")
		fmt.Printf("   PID: %d\n", os.Getpid())

		// 等待信号
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig

		fmt.Println("\n🛑 正在关闭...")
		apiServer.Close()
		db.CloseQueueDb()
		fmt.Println("👋 已关闭")
	},
}

func init() {
	startCmd.Flags().Bool("daemon", false, "后台运行")
	startCmd.Flags().Bool("foreground", false, "前台运行（内部使用）")
}

// ==================== stop ====================

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "停止 QueenBee",
	Run: func(cmd *cobra.Command, args []string) {
		pidFile := filepath.Join(config.QueenBeeHome, "queenbee.pid")
		pid := readPID(pidFile)
		if pid <= 0 {
			fmt.Println("⚠️  QueenBee 未在运行")
			return
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			fmt.Printf("❌ 找不到进程 %d\n", pid)
			return
		}
		proc.Signal(os.Interrupt)
		os.Remove(pidFile)
		fmt.Printf("✅ 已停止 QueenBee (PID: %d)\n", pid)
	},
}

// ==================== status ====================

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看 QueenBee 状态",
	Run: func(cmd *cobra.Command, args []string) {
		pidFile := filepath.Join(config.QueenBeeHome, "queenbee.pid")
		pid := readPID(pidFile)
		if pid > 0 && processExists(pid) {
			fmt.Printf("✅ QueenBee 正在运行 (PID: %d)\n", pid)

			// 尝试调用 API
			resp, err := http.Get("http://localhost:3777/api/queue/status")
			if err == nil {
				defer resp.Body.Close()
				var status map[string]interface{}
				json.NewDecoder(resp.Body).Decode(&status)
				fmt.Printf("   待处理: %.0f | 处理中: %.0f | 已完成: %.0f | 死信: %.0f\n",
					status["pending"], status["processing"], status["completed"], status["dead"])
			}
		} else {
			fmt.Println("⚠️  QueenBee 未在运行")
		}
	},
}

// ==================== setup ====================

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "交互式配置向导",
	Run: func(cmd *cobra.Command, args []string) {
		reader := bufio.NewReader(os.Stdin)
		fmt.Println("\n🐝 QueenBee 配置向导")

		// Workspace
		home, _ := os.UserHomeDir()
		defaultWorkspace := filepath.Join(home, "queenbee-workspace")
		fmt.Printf("工作区路径 [%s]: ", defaultWorkspace)
		workspace := readLine(reader)
		if workspace == "" {
			workspace = defaultWorkspace
		}

		// Provider
		fmt.Print("AI Provider (anthropic/openai/gemini) [anthropic]: ")
		provider := readLine(reader)
		if provider == "" {
			provider = "anthropic"
		}

		// Model
		defaultModel := "sonnet"
		if provider == "openai" {
			defaultModel = "gpt-5.3-codex"
		}
		fmt.Printf("模型 [%s]: ", defaultModel)
		model := readLine(reader)
		if model == "" {
			model = defaultModel
		}

		// Agent name
		fmt.Print("默认 Agent 名称 [Assistant]: ")
		agentName := readLine(reader)
		if agentName == "" {
			agentName = "Assistant"
		}

		// Heartbeat
		fmt.Print("Heartbeat 间隔（秒，0=禁用）[3600]: ")
		hbStr := readLine(reader)
		hbInterval := 3600
		if hbStr != "" {
			hbInterval, _ = strconv.Atoi(hbStr)
		}

		// 创建配置
		settings := &types.Settings{
			Workspace: &types.WorkspaceConfig{
				Path: workspace,
				Name: filepath.Base(workspace),
			},
			Models: &types.ModelsConfig{
				Provider: provider,
			},
			Agents: map[string]*types.AgentConfig{
				"default": {
					Name:             agentName,
					Provider:         provider,
					Model:            model,
					WorkingDirectory: filepath.Join(workspace, "default"),
				},
			},
			Monitoring: &types.MonitoringConfig{
				HeartbeatInterval: hbInterval,
			},
		}

		// 设置 provider model 配置
		switch provider {
		case "openai":
			settings.Models.OpenAI = &types.ProviderModel{Model: model}

		default:
			settings.Models.Anthropic = &types.ProviderModel{Model: model}
		}

		if err := config.SaveSettings(settings); err != nil {
			fmt.Printf("❌ 保存配置失败: %v\n", err)
			return
		}

		// 创建工作区目录
		os.MkdirAll(workspace, 0o755)

		fmt.Println("\n✅ 配置已保存到数据库")
		fmt.Println("运行 queenbee start 启动")
	},
}

// ==================== agent ====================

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Agent 管理",
}

func init() {
	agentCmd.AddCommand(&cobra.Command{
		Use: "list", Short: "列出所有 Agent",
		Run: func(cmd *cobra.Command, args []string) {
			settings := config.GetSettings()
			agents := config.GetAgents(settings)
			fmt.Printf("📋 %d 个 Agent:\n\n", len(agents))
			for id, a := range agents {
				fmt.Printf("  %s: %s [%s/%s] → %s\n", id, a.Name, a.Provider, a.Model, a.WorkingDirectory)
			}
		},
	})

	agentCmd.AddCommand(&cobra.Command{
		Use: "add", Short: "添加 Agent（交互式）",
		Run: func(cmd *cobra.Command, args []string) {
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Agent ID: ")
			id := readLine(reader)
			if id == "" {
				fmt.Println("❌ ID 不能为空")
				return
			}
			fmt.Print("名称: ")
			name := readLine(reader)
			fmt.Print("Provider (anthropic/openai/gemini) [anthropic]: ")
			provider := readLine(reader)
			if provider == "" {
				provider = "anthropic"
			}
			fmt.Print("模型 [sonnet]: ")
			model := readLine(reader)
			if model == "" {
				model = "sonnet"
			}

			settings := config.GetSettings()
			if settings.Agents == nil {
				settings.Agents = make(map[string]*types.AgentConfig)
			}
			home, _ := os.UserHomeDir()
			workspacePath := filepath.Join(home, "queenbee-workspace")
			if settings.Workspace != nil && settings.Workspace.Path != "" {
				workspacePath = settings.Workspace.Path
			}

			settings.Agents[id] = &types.AgentConfig{
				Name:             name,
				Provider:         provider,
				Model:            model,
				WorkingDirectory: filepath.Join(workspacePath, id),
			}
			config.SaveSettings(settings)
			fmt.Printf("✅ Agent '%s' 已添加\n", id)
		},
	})

	agentCmd.AddCommand(&cobra.Command{
		Use: "show", Short: "查看 Agent 配置",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			settings := config.GetSettings()
			agents := config.GetAgents(settings)
			if a, ok := agents[args[0]]; ok {
				data, _ := json.MarshalIndent(a, "", "  ")
				fmt.Println(string(data))
			} else {
				fmt.Printf("❌ Agent '%s' 不存在\n", args[0])
			}
		},
	})

	agentCmd.AddCommand(&cobra.Command{
		Use: "remove", Short: "删除 Agent",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			settings := config.GetSettings()
			if settings.Agents == nil || settings.Agents[args[0]] == nil {
				fmt.Printf("❌ Agent '%s' 不存在\n", args[0])
				return
			}
			delete(settings.Agents, args[0])
			config.SaveSettings(settings)
			fmt.Printf("✅ Agent '%s' 已删除\n", args[0])
		},
	})
}

// ==================== team ====================

var teamCmd = &cobra.Command{
	Use:   "team",
	Short: "Team 管理",
}

func init() {
	teamCmd.AddCommand(&cobra.Command{
		Use: "list", Short: "列出所有 Team",
		Run: func(cmd *cobra.Command, args []string) {
			settings := config.GetSettings()
			teams := config.GetTeams(settings)
			if len(teams) == 0 {
				fmt.Println("暂无团队")
				return
			}
			for id, t := range teams {
				fmt.Printf("  %s: %s [agents: %s] leader=%s\n", id, t.Name, strings.Join(t.Agents, ","), t.LeaderAgent)
			}
		},
	})

	teamCmd.AddCommand(&cobra.Command{
		Use: "add", Short: "添加 Team（交互式）",
		Run: func(cmd *cobra.Command, args []string) {
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Team ID: ")
			id := readLine(reader)
			fmt.Print("名称: ")
			name := readLine(reader)
			fmt.Print("Agent IDs (逗号分隔): ")
			agentsStr := readLine(reader)
			agents := strings.Split(agentsStr, ",")
			for i := range agents {
				agents[i] = strings.TrimSpace(agents[i])
			}
			fmt.Printf("Leader Agent [%s]: ", agents[0])
			leader := readLine(reader)
			if leader == "" {
				leader = agents[0]
			}

			settings := config.GetSettings()
			if settings.Teams == nil {
				settings.Teams = make(map[string]*types.TeamConfig)
			}
			settings.Teams[id] = &types.TeamConfig{Name: name, Agents: agents, LeaderAgent: leader}
			config.SaveSettings(settings)
			fmt.Printf("✅ Team '%s' 已添加\n", id)
		},
	})

	teamCmd.AddCommand(&cobra.Command{
		Use: "show", Short: "查看 Team",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			settings := config.GetSettings()
			teams := config.GetTeams(settings)
			if t, ok := teams[args[0]]; ok {
				data, _ := json.MarshalIndent(t, "", "  ")
				fmt.Println(string(data))
			} else {
				fmt.Printf("❌ Team '%s' 不存在\n", args[0])
			}
		},
	})

	teamCmd.AddCommand(&cobra.Command{
		Use: "remove", Short: "删除 Team",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			settings := config.GetSettings()
			if settings.Teams == nil || settings.Teams[args[0]] == nil {
				fmt.Printf("❌ Team '%s' 不存在\n", args[0])
				return
			}
			delete(settings.Teams, args[0])
			config.SaveSettings(settings)
			fmt.Printf("✅ Team '%s' 已删除\n", args[0])
		},
	})
}

// ==================== send ====================

var sendCmd = &cobra.Command{
	Use:   "send [message]",
	Short: "发送消息给 Agent",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		message := strings.Join(args, " ")
		body := fmt.Sprintf(`{"channel":"cli","sender":"cli_user","message":%s}`, mustJSON(message))

		resp, err := http.Post("http://localhost:3777/api/messages", "application/json", strings.NewReader(body))
		if err != nil {
			fmt.Printf("❌ 发送失败（QueenBee 是否在运行？）: %v\n", err)
			return
		}
		defer resp.Body.Close()

		var result map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&result)
		fmt.Printf("✅ 消息已发送 (ID: %v)\n", result["message_id"])

		// 轮询等待响应
		fmt.Println("⏳ 等待响应...")
		for i := 0; i < 120; i++ { // 最多等 2 分钟
			time.Sleep(1 * time.Second)
			resp, err := http.Get("http://localhost:3777/api/responses/pending?channel=cli")
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			var responses []map[string]interface{}
			json.Unmarshal(body, &responses)

			for _, r := range responses {
				fmt.Printf("\n🤖 %s\n", r["message"])
				// ACK
				if id, ok := r["id"]; ok {
					http.Post(fmt.Sprintf("http://localhost:3777/api/responses/%.0f/ack", id.(float64)), "", nil)
				}
				return
			}
		}
		fmt.Println("⏰ 超时，请检查日志")
	},
}

// ==================== reset ====================

var resetCmd = &cobra.Command{
	Use:   "reset [agent_id]",
	Short: "重置 Agent 对话",
	Run: func(cmd *cobra.Command, args []string) {
		settings := config.GetSettings()
		home, _ := os.UserHomeDir()
		workspacePath := filepath.Join(home, "queenbee-workspace")
		if settings.Workspace != nil && settings.Workspace.Path != "" {
			workspacePath = settings.Workspace.Path
		}

		agents := config.GetAgents(settings)
		var targets []string
		if len(args) > 0 {
			targets = args
		} else {
			for id := range agents {
				targets = append(targets, id)
			}
		}

		for _, id := range targets {
			resetFlag := filepath.Join(workspacePath, id, "reset_flag")
			os.MkdirAll(filepath.Dir(resetFlag), 0o755)
			os.WriteFile(resetFlag, []byte("reset"), 0o644)
			fmt.Printf("✅ Agent '%s' 对话将在下次消息时重置\n", id)
		}
	},
}

// ==================== logs ====================

var logsCmd = &cobra.Command{
	Use:   "logs [type]",
	Short: "查看日志",
	Run: func(cmd *cobra.Command, args []string) {
		logType := "queue"
		if len(args) > 0 {
			logType = args[0]
		}
		logFile := filepath.Join(config.QueenBeeHome, "logs", logType+".log")
		data, err := os.ReadFile(logFile)
		if err != nil {
			fmt.Printf("❌ 日志不存在: %s\n", logFile)
			return
		}
		lines := strings.Split(string(data), "\n")
		start := 0
		if len(lines) > 50 {
			start = len(lines) - 50
		}
		for _, line := range lines[start:] {
			fmt.Println(line)
		}
	},
}

// ==================== pairing ====================

var pairingCmd = &cobra.Command{
	Use:   "pairing",
	Short: "配对管理",
}

func init() {
	pairingCmd.AddCommand(&cobra.Command{
		Use: "list", Short: "显示所有配对",
		Run: func(cmd *cobra.Command, args []string) {
			pairingFile := filepath.Join(config.QueenBeeHome, "pairing.json")
			state := pairing.LoadState(pairingFile)
			fmt.Printf("📋 待审批: %d | 已批准: %d\n\n", len(state.Pending), len(state.Approved))
			if len(state.Pending) > 0 {
				fmt.Println("待审批:")
				for _, p := range state.Pending {
					fmt.Printf("  [%s] %s (%s) — 配对码: %s\n", p.Channel, p.Sender, p.SenderID, p.Code)
				}
			}
			if len(state.Approved) > 0 {
				fmt.Println("已批准:")
				for _, a := range state.Approved {
					fmt.Printf("  [%s] %s (%s)\n", a.Channel, a.Sender, a.SenderID)
				}
			}
		},
	})

	pairingCmd.AddCommand(&cobra.Command{
		Use: "approve [code]", Short: "批准配对码",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			pairingFile := filepath.Join(config.QueenBeeHome, "pairing.json")
			result := pairing.ApprovePairingCode(pairingFile, args[0])
			if result.OK {
				fmt.Printf("✅ 已批准: %s (%s/%s)\n", result.Entry.Sender, result.Entry.Channel, result.Entry.SenderID)
			} else {
				fmt.Printf("❌ %s\n", result.Reason)
			}
		},
	})
}

// ==================== Heartbeat ====================

func startHeartbeat(settings *types.Settings) {
	interval := time.Duration(settings.Monitoring.HeartbeatInterval) * time.Second
	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logging.Log("INFO", fmt.Sprintf("Heartbeat 已启动 (间隔: %v)", interval))
	for range ticker.C {
		// 每次 tick 重新读取 settings，确保新增 agent 也能收到心跳
		currentSettings := config.GetSettings()
		agents := config.GetAgents(currentSettings)

		workspacePath := ""
		if currentSettings.Workspace != nil && currentSettings.Workspace.Path != "" {
			workspacePath = currentSettings.Workspace.Path
		} else {
			home, _ := os.UserHomeDir()
			workspacePath = filepath.Join(home, "queenbee-workspace")
		}

		for id := range agents {
			heartbeatFile := filepath.Join(workspacePath, id, "heartbeat.md")
			data, err := os.ReadFile(heartbeatFile)
			if err != nil {
				continue
			}

			message := strings.TrimSpace(string(data))
			if message == "" {
				continue
			}

			db.EnqueueMessage(&db.EnqueueMessageData{
				Channel:   "heartbeat",
				Sender:    "system",
				Message:   message,
				MessageID: fmt.Sprintf("heartbeat_%s_%d", id, time.Now().UnixMilli()),
				Agent:     id,
			})
			logging.Log("INFO", fmt.Sprintf("Heartbeat → @%s", id))
		}
	}
}

// --- 辅助 ---

func readLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func readPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func processExists(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// 发送 nil 信号检测进程是否存在（Unix: kill(pid, 0)），Windows 上 FindProcess 总成功
	err = proc.Signal(nil)
	return err == nil
}

func mustJSON(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
