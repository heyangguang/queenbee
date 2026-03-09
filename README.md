<div align="center">

<img src="docs/assets/hero-banner.png" alt="QueenBee Banner" width="100%" />

# 🐝 QueenBee

### Message-Queue-Driven Multi-Agent Orchestration Engine

[![Go Version](https://img.shields.io/badge/Go-1.25-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-yellow?style=for-the-badge)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-Welcome-brightgreen?style=for-the-badge)](CONTRIBUTING.md)
[![GitHub Stars](https://img.shields.io/github/stars/heyangguang/queenbee?style=for-the-badge&logo=github)](https://github.com/heyangguang/queenbee/stargazers)

**中文 | [English](README_EN.md) | [日本語](README_JA.md)**

**QueenBee 是一个开源的、本地优先的多 AI Agent 协作引擎。** 它通过 SQLite 消息队列调度多个 AI CLI（Claude、Gemini、Codex、OpenCode），让不同 Agent 以 `@mention` 方式协作对话、自动路由、并行执行任务——并在此基础上提供持久记忆、技能系统和灵魂自省能力。

[Getting Started](#-getting-started) · [Architecture](#-architecture) · [Documentation](#-documentation) · [Contributing](#-contributing)

</div>

---

## ✨ What Makes QueenBee Different?

<table>
<tr>
<td width="50%">

### 🤖 Traditional AI Tool
```
You → AI → Code → You Review → Repeat
```
- Single agent, single context
- Manual coordination
- No memory across sessions
- No team collaboration

</td>
<td width="50%">

### 🐝 QueenBee
```
You → @agent Message → Queue → Agent → Response
    → @teammate → Route → Parallel Execution
```
- Multi-agent team with `@mention` routing
- SQLite queue with dead-letter + auto-retry
- Persistent memory (FTS5 + Embedding)
- Plugin hooks + Soul self-reflection

</td>
</tr>
</table>

---

## 🏗 Architecture

QueenBee 采用**消息队列驱动**的架构，所有 Agent 交互通过 SQLite 消息队列串行/并行调度：

<div align="center">
<img src="docs/assets/architecture.png" alt="QueenBee Architecture" width="700" />
</div>

```
                          ┌─────────────────────────┐
                          │      Clients            │
                          │  Web UI / Slack / API    │
                          └───────────┬─────────────┘
                                      │ REST + SSE
                          ┌───────────▼─────────────┐
                          │    Server Layer (Gin)    │
                          │  REST API  │  SSE Events │
                          ├─────────────────────────┤
                          │    Engine Layer          │
                          │  ┌───────────────────┐  │
                          │  │  Message Queue     │  │   ┌──────────┐
                          │  │  (SQLite)          │  │   │  Memory   │
                          │  │  pending → process │  │   │  FTS5 +   │
                          │  │  → complete / dead  │  │   │  Embedding│
                          │  └────────┬──────────┘  │   └──────────┘
                          │           │              │
                          │  ┌────────▼──────────┐  │   ┌──────────┐
                          │  │  Process Queue     │  │   │  Plugins  │
                          │  │  @mention Router   │  │   │  Hooks +  │
                          │  │  Team Routing      │  │   │  EventBus │
                          │  │  Conversation Mgr  │  │   └──────────┘
                          │  └────────┬──────────┘  │
                          │           │              │   ┌──────────┐
                          │  ┌────────▼──────────┐  │   │  Skills   │
                          │  │   InvokeAgent      │  │   │  SKILL.md │
                          │  │  ┌──────┬────────┐ │  │   │  Sync     │
                          │  │  │Claude│ Gemini │ │  │   └──────────┘
                          │  │  │ CLI  │  CLI   │ │  │
                          │  │  ├──────┼────────┤ │  │   ┌──────────┐
                          │  │  │Codex │OpenCode│ │  │   │  Soul     │
                          │  │  │ CLI  │  CLI   │ │  │   │  SOUL.md  │
                          │  │  └──────┴────────┘ │  │   │  自省      │
                          │  └───────────────────┘  │   └──────────┘
                          └─────────────────────────┘
```

### 核心处理流水线

```
消息入队 → ClaimNextMessage (原子领取)
         → ParseAgentRouting (@mention 路由)
         → FindTeamForAgent (团队查找)
         → InvokeAgent (CLI 调用 + Fallback + 冷却)
         → ExtractTeammateMentions ([@teammate: msg] 提取)
         → EnqueueInternalMessage (队友消息入队)
         → CompleteConversation (聚合响应 + 历史保存)
```

---

## 🚀 Key Features

### 📬 SQLite 消息队列引擎
消息驱动的核心调度器。消息经过 `pending → processing → completed / dead` 生命周期，支持：
- **原子领取** — `ClaimNextMessage` 防止重复消费
- **自动重试** — 失败 5 次后进入死信队列
- **跨 Agent 并行** — 不同 Agent 完全并行，同 Agent 串行锁定
- **会话恢复** — `RestoreConversations` 启动时从数据库恢复活跃会话
- **卡死恢复** — `RecoverStaleMessages` 自动恢复超时消息

### 🏷 @mention 路由系统
Agent 间通过自然语言 `@mention` 协作：
```
用户发: @coder 实现用户登录功能
路由到: coder agent

coder 回复: [@reviewer: 请审查这段代码] [@tester: 请编写测试]
系统自动: 提取 teammate mentions → 入队到对应 agent
```
- **双轮匹配** — 先精确匹配 Agent ID，再 fallback 到 Team Leader
- **Team 路由** — `@team-name` 自动路由到 Team Leader Agent
- **消息清洗** — 自动移除 `@mention` 前缀，保留干净消息正文

### 🔌 四大 CLI Provider + Fallback
通过 `os/exec` 调用本地 AI CLI，统一抽象不同 Provider 差异：

| Provider | CLI Binary | 特性 |
|:---------|:-----------|:-----|
| **Anthropic** | `claude` | stream-json 输出、`-c` 续对话、`--dangerously-skip-permissions` |
| **Google** | `gemini` | `--yolo` 模式、sandbox 支持 |
| **OpenAI** | `codex` | `exec resume --last` 续对话、JSON 输出 |
| **OpenCode** | `opencode` | `run` 模式、JSON 格式 |

- **自动 Fallback** — 主 Provider 失败自动切换备用 Provider
- **冷却机制** — 连续失败的 Provider 冷却 5 分钟
- **活跃度 Watchdog** — stdout 无输出超时判定为卡死，自动终止进程
- **模型解析** — `ResolveClaudeModel` / `ResolveGeminiModel` 等智能模型映射

### 🧠 三级降级持久记忆
多层记忆系统，每个 Agent 拥有独立的长期记忆：

```
搜索优先级: Ollama Embedding 向量搜索
              ↓ (不可用时)
           FTS5 全文搜索
              ↓ (不可用时)
           LIKE 模糊匹配
```

- **三层 Scope** — Agent 私有 / Team 共享 / User 全局
- **自动提取** — 从对话中自动提取有价值的记忆条目
- **上下文注入** — `FormatMemoriesForContext` 将相关记忆注入 Agent Prompt

### 🧩 Skill 系统
动态技能挂载，通过 SKILL.md 文件为 Agent 注入专业能力：
- **多 CLI 同步** — 同步写入 `.agents/skills/`、`.claude/skills/`、`.gemini/skills/`
- **内置技能发现** — 自动扫描 `templates/` 和 `QUEENBEE_HOME` 目录
- **CLI 全局技能扫描** — 发现各 CLI 已安装的全局技能
- **YAML Frontmatter** — 标准化元数据（name、description、allowed-tools）

### 👻 Soul 自省系统
Agent 在每次任务完成后自动反思，更新 `SOUL.md` 持久身份文件：
- **增量更新** — 不重写，只添加新的经验和观点
- **异步执行** — goroutine 后台运行，不阻塞主流程
- **全 Provider 支持** — 每种 CLI 有独立的 Soul 更新路径

### 📦 上下文压缩
长消息自动 AI 摘要压缩，减少 token 消耗：
- **智能阈值** — 超过 8000 字符触发压缩（可配置）
- **AI 摘要** — 保留代码、错误信息、决策，删除冗余
- **Fallback 截断** — AI 压缩失败时保留首尾 40%

### 🔧 插件引擎
可扩展的钩子系统，支持多种脚本语言：
- **双向钩子** — `TransformIncoming` / `TransformOutgoing` 消息拦截
- **多语言支持** — Shell、Python、Node.js、Go 原生插件
- **事件广播** — `BroadcastEvent` 向所有插件分发系统事件
- **自动发现** — 扫描 `plugins/` 目录自动加载

### 👥 Team 协作
Agent 组织为 Team，支持 Leader 分发和队友间直接对话：
- **AGENTS.md 同步** — 自动生成队友信息文件注入每个 Agent 工作目录
- **项目目录注入** — `injectProjectDirectory` 将项目路径信息注入 Agent 上下文
- **Git 仓库自动初始化** — 确保 Claude CLI 能发现 `.claude/` 技能目录

---

## 📦 Getting Started

### 前置要求

- **Go** 1.25+
- 至少一个 AI CLI 工具：
  - [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`)
  - [Gemini CLI](https://github.com/google-gemini/gemini-cli) (`gemini`)
  - [Codex CLI](https://github.com/openai/codex) (`codex`)
  - [OpenCode](https://github.com/opencode-ai/opencode) (`opencode`)

### 安装

```bash
# 克隆仓库
git clone https://github.com/heyangguang/queenbee.git
cd queenbee

# 构建
go build -o queenbee .

# 或直接安装
go install github.com/heyangguang/queenbee@latest
```

### 启动

```bash
# 启动服务器（默认端口 3777）
queenbee start

# 后台运行
queenbee start --daemon

# 健康检查
curl http://localhost:3777/api/health
```

### 🐳 Docker 部署

```bash
# 拉取镜像
docker pull ghcr.io/heyangguang/queenbee:latest

# 运行后端
docker run -d --name queenbee \
  -p 3777:3777 \
  -v queenbee-data:/data \
  ghcr.io/heyangguang/queenbee:latest

# 前后端一起启动
docker run -d --name queenbee -p 3777:3777 -v queenbee-data:/data ghcr.io/heyangguang/queenbee:latest
docker run -d --name queenbee-ui -p 3000:3000 -e NEXT_PUBLIC_API_URL=http://localhost:3777 ghcr.io/heyangguang/queenbee-ui:latest
```

**Docker Compose：**

```yaml
# docker-compose.yml
services:
  queenbee:
    image: ghcr.io/heyangguang/queenbee:latest
    ports:
      - "3777:3777"
    volumes:
      - queenbee-data:/data
    restart: unless-stopped

  queenbee-ui:
    image: ghcr.io/heyangguang/queenbee-ui:latest
    ports:
      - "3000:3000"
    environment:
      - NEXT_PUBLIC_API_URL=http://queenbee:3777
    depends_on:
      - queenbee
    restart: unless-stopped

volumes:
  queenbee-data:
```

```bash
docker compose up -d
# 打开 http://localhost:3000 即可使用
```

---

## ⚙️ 模型配置

### 前提：安装 AI CLI 工具

QueenBee **不直接调用 AI API**，而是通过 `os/exec` 调用本地安装的 CLI 工具。你只需要在本地安装对应的 CLI 并完成认证（登录 / 设置 API Key），QueenBee 就能直接使用。

| Provider | CLI 工具 | 安装方式 | 认证方式 |
|:---------|:---------|:---------|:---------|
| **Anthropic** | `claude` | `npm install -g @anthropic-ai/claude-code` | `claude` 首次运行会引导登录 |
| **Google** | `gemini` | `npm install -g @anthropic-ai/gemini-cli` | `gemini` 首次运行会引导登录 |
| **OpenAI** | `codex` | `npm install -g @openai/codex` | `export OPENAI_API_KEY=sk-xxx` |
| **OpenCode** | `opencode` | `go install github.com/opencode-ai/opencode@latest` | 配置文件中设置 API Key |

> **💡 只需安装一个 CLI 即可使用。** QueenBee 会自动检测本地已安装的 CLI。

### 验证 CLI 可用

```bash
# 检查 CLI 是否安装成功（任意一个即可）
claude --version
gemini --version
codex --version
opencode --version
```

只要对应的 CLI 能正常响应，QueenBee 就能调用它。

### 模型别名

QueenBee 支持简短别名，自动映射为完整模型 ID：

<details>
<summary><b>📋 Anthropic (Claude CLI) 模型别名</b></summary>

| 别名 | 完整模型 ID |
|:-----|:-----------|
| `sonnet` | `claude-sonnet-4-6` |
| `opus` | `claude-opus-4-6` |
| `haiku` | `claude-haiku-4-5` |

</details>

<details>
<summary><b>📋 Google (Gemini CLI) 模型别名</b></summary>

| 别名 | 完整模型 ID |
|:-----|:-----------|
| `flash` | `gemini-2.5-flash` |
| `pro` | `gemini-2.5-pro` |

</details>

<details>
<summary><b>📋 OpenAI (Codex CLI) 模型别名</b></summary>

| 别名 | 完整模型 ID |
|:-----|:-----------|
| `gpt-5.3-codex` | `gpt-5.3-codex` |
| `codex-mini` | `codex-mini-latest` |
| `o4-mini` | `o4-mini` |

</details>

<details>
<summary><b>📋 OpenCode 模型别名</b></summary>

| 别名 | 完整模型 ID |
|:-----|:-----------|
| `sonnet` | `opencode/claude-sonnet-4-5` |
| `opus` | `opencode/claude-opus-4-6` |

支持多个底层 Provider：Claude、Gemini、GLM、Kimi、MiniMax 等。

</details>

### 通过 API 配置 Agent 的 Provider 和模型

```bash
# 创建一个使用 Claude 的 Agent
curl -X POST http://localhost:3777/api/agents \
  -H "Content-Type: application/json" \
  -d '{
    "id": "coder",
    "name": "Coder",
    "provider": "anthropic",
    "model": "sonnet"
  }'

# 创建一个带 Fallback 的 Agent（Claude 失败自动切换 Gemini）
curl -X POST http://localhost:3777/api/agents \
  -H "Content-Type: application/json" \
  -d '{
    "id": "reviewer",
    "name": "Reviewer",
    "provider": "anthropic",
    "model": "sonnet",
    "fallback_provider": "gemini",
    "fallback_model": "pro"
  }'
```

### 环境变量

如果 CLI 需要 API Key，可通过全局环境变量配置：

```bash
# 通过 Settings API 设置全局环境变量
curl -X PUT http://localhost:3777/api/settings \
  -H "Content-Type: application/json" \
  -d '{
    "env": {
      "OPENAI_API_KEY": "sk-xxx",
      "ANTHROPIC_API_KEY": "sk-ant-xxx"
    }
  }'
```

也可直接在前端的 **Settings → 环境变量** 页面中配置。

---

## 🔗 前后端连接

### 默认配置（零配置即可连通）

| 服务 | 默认端口 | 说明 |
|:-----|:---------|:-----|
| **QueenBee 后端** | `3777` | REST API + SSE 事件流 |
| **QueenBee Workstation (前端)** | `3000` | Next.js Web 界面 |

> **默认即可连通**：前端默认连接 `http://localhost:3777`，后端默认监听 `3777` 端口，两者都在本地启动时无需任何配置。

### 修改后端端口

后端端口目前硬编码为 `3777`，如需修改请编辑 `internal/server/server.go` 中的端口值后重新编译。

### 修改前端连接地址

如果后端不在默认地址，有两种方式修改：

**方式一：环境变量（推荐）**

```bash
# 开发模式
NEXT_PUBLIC_API_URL=http://192.168.1.100:3777 npm run dev

# 或创建 .env.local 文件
echo "NEXT_PUBLIC_API_URL=http://your-server:3777" > .env.local
npm run dev
```

**方式二：Docker 部署时指定**

```bash
docker run -d -p 3000:3000 \
  -e NEXT_PUBLIC_API_URL=http://your-server:3777 \
  ghcr.io/heyangguang/queenbee-ui:latest
```

### 跨机器部署示例

```
服务器 A (192.168.1.10) — 后端
  queenbee start → 监听 3777

服务器 B (192.168.1.20) — 前端
  NEXT_PUBLIC_API_URL=http://192.168.1.10:3777 npm run dev → 连接服务器 A
```

---

## 📁 Project Structure

```
queenbee/
├── cmd/
│   ├── root.go              # CLI 入口 (Cobra) — serve / setup 命令
│   ├── extras.go             # 辅助命令
│   └── visualize.go          # 可视化工具
├── internal/
│   ├── config/               # 配置管理（纯数据库驱动，无 YAML 文件）
│   │   └── config.go         #   Init、GetSettings、SaveSettings、Resolve*Model
│   ├── db/                   # SQLite 持久层
│   │   └── db.go             #   消息队列、响应队列、Agent/Team/Task/Skill CRUD
│   ├── engine/               # 🧠 核心编排引擎
│   │   ├── processor.go      #   ProcessQueue — 消息调度（跨 Agent 并行，同 Agent 串行）
│   │   ├── invoke.go         #   InvokeAgent — 4 种 CLI 调用 + Fallback + 冷却 + Watchdog
│   │   ├── conversation.go   #   ConversationMap — 线程安全会话管理 + 持久化恢复
│   │   ├── routing.go        #   @mention 路由 — ParseAgentRouting + ExtractTeammateMentions
│   │   ├── agent.go          #   Agent 目录管理 — 模板复制、AGENTS.md 同步、Git 初始化
│   │   ├── skills.go         #   Skill 系统 — SKILL.md 同步、内置技能发现
│   │   ├── soul.go           #   Soul 自省 — SOUL.md 异步更新
│   │   ├── compaction.go     #   上下文压缩 — AI 摘要 + Fallback 截断
│   │   ├── activity.go       #   Agent 活跃度追踪
│   │   └── response.go       #   响应处理
│   ├── memory/               # 🧠 持久记忆系统
│   │   ├── memory.go         #   三级搜索 (Embedding → FTS5 → LIKE) + 多层 Scope
│   │   ├── memory_extract.go #   对话记忆自动提取
│   │   └── embedding.go      #   Ollama 向量生成
│   ├── plugins/              # 🔌 插件引擎
│   │   └── plugins.go        #   脚本钩子 + Go 原生插件 + 事件广播
│   ├── server/               # 🌐 HTTP 服务
│   │   ├── server.go         #   Gin 路由 — Agent/Team/Task/Queue/Session/Log API
│   │   ├── api_v2.go         #   V2 API — Health/Provider/Skill/Soul/Memory/Project
│   │   ├── sse.go            #   Server-Sent Events 实时推送
│   │   └── helpers.go        #   工具函数
│   ├── event/                # 内部事件总线
│   ├── logging/              # 结构化日志
│   └── pairing/              # Agent 配对
├── templates/                # Agent 模板文件
│   ├── AGENTS.md             #   队友信息模板
│   ├── SOUL.md               #   Agent 身份模板
│   └── heartbeat.md          #   心跳模板
├── types/                    # 共享类型定义
├── main.go                   # 应用入口
├── go.mod
└── go.sum
```

---

## 🌐 API Overview

### 核心 API

| Category | Method | Endpoint | Description |
|:---------|:-------|:---------|:------------|
| **消息** | `POST` | `/message` | 发送消息到 Agent（支持 @mention 路由） |
| **Agent** | `GET` | `/agents` | 列出所有 Agent |
| | `POST` | `/agents` | 创建 Agent |
| | `PUT` | `/agents/:id` | 更新 Agent 配置 |
| | `DELETE` | `/agents/:id` | 删除 Agent |
| **Team** | `GET` | `/teams` | 列出所有 Team |
| | `POST` | `/teams` | 创建 Team |
| **Queue** | `GET` | `/queue/status` | 队列状态（pending/processing/completed/dead） |
| | `GET` | `/queue/dead` | 死信消息列表 |
| | `POST` | `/queue/dead/:id/retry` | 重试死信消息 |
| | `POST` | `/queue/recover` | 恢复卡住的 processing 消息 |
| **Task** | `GET` | `/tasks` | 列出任务 |
| | `POST` | `/tasks` | 创建任务 |
| **Memory** | `GET` | `/agents/:id/memories` | 列出 Agent 记忆 |
| | `POST` | `/agents/:id/memories/search` | 搜索相关记忆 |
| | `POST` | `/agents/:id/memories` | 手动存储记忆 |
| **Skill** | `GET` | `/agents/:id/skills` | Agent 已装载技能 |
| | `POST` | `/agents/:id/skills` | 给 Agent 装载技能 |
| | `GET` | `/skills` | 所有技能定义 |
| **Provider** | `GET` | `/providers` | 可用 AI Provider 列表 |
| | `GET` | `/providers/:id/models` | Provider 的可用模型 |
| **Soul** | `GET` | `/agents/:id/soul` | 读取 Agent 的 SOUL.md |
| **System** | `GET` | `/api/health` | 健康检查 |
| | `GET` | `/system/status` | 系统状态（OS/内存/Goroutine） |
| **SSE** | `GET` | `/events` | 实时事件流 |
| **Response** | `GET` | `/responses/recent` | 最近响应 |
| | `POST` | `/responses/:id/ack` | 确认响应已送达 |

---

## 🧪 Testing

```bash
# 运行全部测试
go test ./...

# 带详细输出
go test -v ./internal/engine/...

# 竞态检测
go test -race ./...
```

---

## 🗺 Roadmap

- [x] SQLite 消息队列引擎 + 死信 + 自动重试
- [x] 四大 CLI Provider (Claude / Gemini / Codex / OpenCode) + Fallback
- [x] @mention 路由 + Team 协作 + 队友消息提取
- [x] 三级降级持久记忆 (Embedding → FTS5 → LIKE)
- [x] Skill 系统 + 多 CLI 目录同步
- [x] Soul 自省 + SOUL.md 持久身份
- [x] 上下文压缩 + Fallback 截断
- [x] 插件引擎（脚本钩子 + Go 原生 + 事件广播）
- [x] 会话持久化 + 启动恢复
- [ ] pgvector 语义记忆（替代 SQLite Embedding）
- [ ] WebSocket 双向通信
- [ ] Agent 沙箱隔离执行
- [ ] 社区插件市场

---

## 🤝 Contributing

我们欢迎所有形式的贡献！

### 贡献流程

1. **Fork** 本仓库
2. **创建分支** (`git checkout -b feat/amazing-feature`)
3. **提交代码** (`git commit -m 'feat: 添加新功能'`)
4. **推送** (`git push origin feat/amazing-feature`)
5. **创建 Pull Request**

### 本地开发

```bash
git clone https://github.com/YOUR_USERNAME/queenbee.git
cd queenbee
go mod download
go run . serve --port 9876
```

### 提交规范

使用 conventional commits：`feat:` / `fix:` / `docs:` / `refactor:` / `test:`

---

## 📄 License

本项目使用 **Apache License 2.0** — 详见 [LICENSE](LICENSE) 文件。

---

## 👤 Author

<table>
<tr>
<td align="center">
<a href="https://github.com/heyangguang">
<img src="https://github.com/heyangguang.png" width="100px;" alt="Kuber" /><br />
<sub><b>Kuber</b></sub>
</a><br />
<a href="mailto:heyangev@gmail.com">📧 heyangev@gmail.com</a>
</td>
</tr>
</table>

---

## 🔗 Related

| 项目 | 说明 |
|:-----|:-----|
| [queenbee](https://github.com/heyangguang/queenbee) | 🐝 本仓库 — Go 后端引擎 |
| [queenbee-ui](https://github.com/heyangguang/queenbee-ui) | 🖥 Web 管理界面 (Next.js) |

---

## 🌟 Star History

如果 QueenBee 对你有帮助，请给个 ⭐！

[![Star History Chart](https://api.star-history.com/svg?repos=heyangguang/queenbee&type=Date)](https://star-history.com/#heyangguang/queenbee&Date)

---

<div align="center">

**Built with 🐝 by the QueenBee Community**

*让 AI Agent 像蜂群一样协作。*

</div>
