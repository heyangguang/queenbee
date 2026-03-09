package templates

import "embed"

// FS 包含所有模板文件，编译时嵌入到二进制中
// 确保无论在哪里部署二进制，模板都随时可用
//
//go:embed AGENTS.md SOUL.md heartbeat.md
var FS embed.FS
