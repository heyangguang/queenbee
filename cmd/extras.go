package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/types"
	"github.com/spf13/cobra"
)

// ==================== restart ====================
// 对齐原版 tinyclaw.sh restart（stop + start）

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "重启 QueenBee（stop + start）",
	Run: func(cmd *cobra.Command, args []string) {
		pidFile := filepath.Join(config.QueenBeeHome, "queenbee.pid")
		pid := readPID(pidFile)
		if pid > 0 && processExists(pid) {
			proc, _ := os.FindProcess(pid)
			proc.Signal(syscall.SIGTERM)
			fmt.Printf("🛑 已停止 (PID: %d)\n", pid)
			os.Remove(pidFile)
			time.Sleep(2 * time.Second)
		}
		exe, _ := os.Executable()
		child := exec.Command(exe, "start")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		child.Stdin = os.Stdin
		child.Run()
	},
}

// ==================== attach ====================
// 对齐原版 tinyclaw.sh attach（实时日志跟踪）

var attachCmd = &cobra.Command{
	Use:   "attach",
	Short: "实时跟踪日志输出（tail -f）",
	Run: func(cmd *cobra.Command, args []string) {
		logFile := filepath.Join(config.QueenBeeHome, "logs", "queue.log")
		if _, err := os.Stat(logFile); os.IsNotExist(err) {
			fmt.Printf("⚠️  日志文件不存在: %s\n", logFile)
			return
		}
		tail := exec.Command("tail", "-f", "-n", "50", logFile)
		tail.Stdout = os.Stdout
		tail.Stderr = os.Stderr
		tail.Run()
	},
}

// ==================== provider ====================
// 对齐原版 tinyclaw.sh provider

var providerCmd = &cobra.Command{
	Use:   "provider [name]",
	Short: "查看或切换 AI Provider",
	Long:  "不带参数查看当前 provider，带参数切换为指定 provider（anthropic/openai/opencode）",
	Run: func(cmd *cobra.Command, args []string) {
		settings := config.GetSettings()

		if len(args) == 0 {
			provider, model := getProviderAndModel(settings)
			fmt.Printf("当前 Provider: %s\n", provider)
			if model != "" {
				fmt.Printf("当前 Model: %s\n", model)
			}
			return
		}

		newProvider := args[0]
		if newProvider != "anthropic" && newProvider != "openai" && newProvider != "opencode" {
			fmt.Printf("❌ 不支持的 provider: %s（可选: anthropic/openai/opencode）\n", newProvider)
			return
		}

		if settings.Models == nil {
			settings.Models = &types.ModelsConfig{}
		}
		settings.Models.Provider = newProvider

		modelFlag, _ := cmd.Flags().GetString("model")
		if modelFlag != "" {
			setProviderModel(settings, newProvider, modelFlag)
		}

		config.SaveSettings(settings)
		fmt.Printf("✅ Provider 已切换为: %s\n", newProvider)
		if modelFlag != "" {
			fmt.Printf("✅ Model 已设置为: %s\n", modelFlag)
		}
	},
}

func init() {
	providerCmd.Flags().String("model", "", "同时设置模型")
}

// ==================== model ====================
// 对齐原版 tinyclaw.sh model

var modelCmd = &cobra.Command{
	Use:   "model [name]",
	Short: "查看或切换 AI Model",
	Run: func(cmd *cobra.Command, args []string) {
		settings := config.GetSettings()
		provider, currentModel := getProviderAndModel(settings)

		if len(args) == 0 {
			if currentModel == "" {
				fmt.Println("⚠️  未配置模型")
			} else {
				fmt.Printf("当前 Provider: %s\n", provider)
				fmt.Printf("当前 Model: %s\n", currentModel)
			}
			return
		}

		if settings.Models == nil {
			settings.Models = &types.ModelsConfig{Provider: provider}
		}
		setProviderModel(settings, provider, args[0])
		config.SaveSettings(settings)
		fmt.Printf("✅ Model 已切换为: %s（Provider: %s）\n", args[0], provider)
	},
}

// ─── 辅助函数 ────────────────────────────────────────────────────────────────

func getProviderAndModel(settings *types.Settings) (string, string) {
	provider := "anthropic"
	model := ""
	if settings.Models != nil {
		if settings.Models.Provider != "" {
			provider = settings.Models.Provider
		}
		switch provider {
		case "openai":
			if settings.Models.OpenAI != nil {
				model = settings.Models.OpenAI.Model
			}
		case "opencode":
			if settings.Models.OpenCode != nil {
				model = settings.Models.OpenCode.Model
			}
		default:
			if settings.Models.Anthropic != nil {
				model = settings.Models.Anthropic.Model
			}
		}
	}
	return provider, model
}

func setProviderModel(settings *types.Settings, provider, model string) {
	if settings.Models == nil {
		settings.Models = &types.ModelsConfig{}
	}
	switch provider {
	case "openai":
		settings.Models.OpenAI = &types.ProviderModel{Model: model}
	case "opencode":
		settings.Models.OpenCode = &types.ProviderModel{Model: model}
	default:
		settings.Models.Anthropic = &types.ProviderModel{Model: model}
	}
}
