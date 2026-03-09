package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/queenbee-ai/queenbee/internal/config"
	"github.com/queenbee-ai/queenbee/internal/logging"
)

const LongResponseThreshold = 4000

// HandleLongResponse 如果响应过长，保存到文件并截断
func HandleLongResponse(response string, existingFiles []string) (message string, files []string) {
	if len(response) <= LongResponseThreshold {
		return response, existingFiles
	}

	// 保存完整响应到文件
	filename := fmt.Sprintf("response_%d.md", time.Now().UnixMilli())
	filePath := filepath.Join(config.FilesDir, filename)
	os.WriteFile(filePath, []byte(response), 0o644)
	logging.Log("INFO", fmt.Sprintf("长响应 (%d 字符) 已保存到 %s", len(response), filename))

	// 截断预览
	preview := response[:LongResponseThreshold] + "\n\n_(Full response attached as file)_"
	allFiles := make([]string, len(existingFiles))
	copy(allFiles, existingFiles)
	allFiles = append(allFiles, filePath)

	return preview, allFiles
}

// CollectFiles 从响应文本中收集 [send_file: path] 引用
func CollectFiles(response string, fileSet map[string]struct{}) {
	re := regexp.MustCompile(`\[send_file:\s*([^\]]+)\]`)
	matches := re.FindAllStringSubmatch(response, -1)
	for _, match := range matches {
		filePath := strings.TrimSpace(match[1])
		if _, err := os.Stat(filePath); err == nil {
			fileSet[filePath] = struct{}{}
		}
	}
}
