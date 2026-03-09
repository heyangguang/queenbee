# 🐝 QueenBee 使用说明

本文档详细介绍 QueenBee 的使用方法，涵盖 CLI 命令、API 调用、Agent 配置、Team 协作等核心功能。

---

## 📋 目录

- [快速开始](#快速开始)
- [CLI 命令参考](#cli-命令参考)
- [Agent 管理](#agent-管理)
- [Team 协作](#team-协作)
- [消息队列](#消息队列)
- [记忆系统](#记忆系统)
- [Skill 系统](#skill-系统)
- [插件系统](#插件系统)
- [配置管理](#配置管理)
- [故障排查](#故障排查)

---

## 快速开始

### 1. 初始化配置

```bash
# 交互式配置向导
queenbee setup
```

配置向导将引导你设置：
- **工作区路径** — Agent 文件存储目录（默认 `~/queenbee-workspace`）
- **AI Provider** — 默认 AI 提供商（anthropic / google / openai / opencode）
- **默认 Agent** — 初始 Agent 名称
- **Heartbeat 间隔** — 心跳检测周期

### 2. 启动服务

```bash
# 启动 QueenBee（默认端口 3777）
queenbee start

# 验证运行状态
queenbee status

# 健康检查
curl http://localhost:3777/api/health
```

### 3. 发送消息

```bash
# 通过 CLI 发送消息
queenbee send "@default 你好，请介绍自己"

# 指定 Agent
queenbee send "@coder 请实现一个快速排序"
```

---

## CLI 命令参考

### 基础命令

| 命令 | 说明 | 示例 |
|:-----|:-----|:-----|
| `queenbee start` | 启动 QueenBee 守护进程 | `queenbee start` |
| `queenbee stop` | 停止 QueenBee | `queenbee stop` |
| `queenbee status` | 查看运行状态和队列信息 | `queenbee status` |
| `queenbee setup` | 交互式配置向导 | `queenbee setup` |

### Agent 管理

| 命令 | 说明 | 示例 |
|:-----|:-----|:-----|
| `queenbee agent list` | 列出所有 Agent | `queenbee agent list` |
| `queenbee agent create` | 创建新 Agent | `queenbee agent create --name coder --provider anthropic` |

### Team 管理

| 命令 | 说明 | 示例 |
|:-----|:-----|:-----|
| `queenbee team list` | 列出所有 Team | `queenbee team list` |
| `queenbee team create` | 创建新 Team | `queenbee team create --name dev-team --leader coder` |

### 消息发送

| 命令 | 说明 | 示例 |
|:-----|:-----|:-----|
| `queenbee send` | 发送消息给 Agent | `queenbee send "@coder 请实现登录功能"` |
| `queenbee reset` | 重置 Agent 对话 | `queenbee reset coder` |

### 其它命令

| 命令 | 说明 |
|:-----|:-----|
| `queenbee logs` | 查看实时日志 |
| `queenbee queue` | 查看队列状态 |

---

## Agent 管理

### 通过 API 创建 Agent

```bash
curl -X POST http://localhost:3777/api/agents \
  -H "Content-Type: application/json" \
  -d '{
    "name": "coder",
    "description": "专注编码的 Agent",
    "provider": "anthropic",
    "model": "claude-sonnet-4-20250514",
    "system_prompt": "你是一个专业的程序员..."
  }'
```

### Agent 配置选项

| 字段 | 类型 | 说明 |
|:-----|:-----|:-----|
| `name` | string | Agent 名称（用于 @mention 路由） |
| `description` | string | Agent 描述 |
| `provider` | string | AI Provider（anthropic / google / openai / opencode） |
| `model` | string | 模型名称（留空使用默认） |
| `fallback_provider` | string | 备用 Provider |
| `system_prompt` | string | 系统提示词 |

### @mention 路由

QueenBee 通过 `@mention` 将消息路由到指定 Agent：

```
# 直接指定 Agent
@coder 请实现用户登录功能

# Agent 可以 @mention 队友
# coder 的回复中包含：
[@reviewer: 请审查这段代码]
[@tester: 请编写单元测试]

# 系统自动将消息路由给 reviewer 和 tester
```

### Agent 目录结构

每个 Agent 在工作区有独立的工作目录：

```
~/queenbee-workspace/
├── agents/
│   ├── coder/
│   │   ├── .claude/
│   │   │   └── skills/     # Claude CLI 技能目录
│   │   ├── .agents/
│   │   │   └── skills/     # 通用技能目录
│   │   ├── .gemini/
│   │   │   └── skills/     # Gemini CLI 技能目录
│   │   ├── AGENTS.md        # 队友信息
│   │   ├── SOUL.md          # Agent 身份与自省
│   │   └── repo/            # 项目代码目录
│   └── reviewer/
│       └── ...
└── queenbee.db              # SQLite 数据库
```

---

## Team 协作

### 创建 Team

```bash
curl -X POST http://localhost:3777/api/teams \
  -H "Content-Type: application/json" \
  -d '{
    "name": "dev-team",
    "description": "开发团队",
    "leader_agent_id": "coder-uuid",
    "member_ids": ["reviewer-uuid", "tester-uuid"]
  }'
```

### Team 工作流程

```
用户 → @dev-team 实现用户注册功能
     → 路由到 Team Leader (coder)
     → coder 分解任务
     → [@reviewer: 请审查代码]
     → [@tester: 请编写测试]
     → 各 Agent 并行执行
     → 结果汇总返回用户
```

---

## 消息队列

### 队列生命周期

```
pending → processing → completed
                    ↓
                   dead (5次失败后)
```

### 队列操作

```bash
# 查看队列状态
curl http://localhost:3777/api/queue/status

# 查看死信消息
curl http://localhost:3777/api/queue/dead

# 重试死信消息
curl -X POST http://localhost:3777/api/queue/dead/{id}/retry

# 恢复卡住的消息
curl -X POST http://localhost:3777/api/queue/recover
```

---

## 记忆系统

QueenBee 为每个 Agent 提供独立的持久记忆，支持三级搜索降级：

### 搜索记忆

```bash
curl -X POST http://localhost:3777/api/agents/{id}/memories/search \
  -H "Content-Type: application/json" \
  -d '{"query": "用户认证方案"}'
```

### 手动存储记忆

```bash
curl -X POST http://localhost:3777/api/agents/{id}/memories \
  -H "Content-Type: application/json" \
  -d '{
    "content": "项目使用 JWT 进行用户认证",
    "scope": "agent"
  }'
```

### 记忆 Scope

| Scope | 说明 |
|:------|:-----|
| `agent` | Agent 私有记忆，仅自身可见 |
| `team` | Team 共享记忆，团队成员可见 |
| `global` | 全局记忆，所有 Agent 可见 |

---

## Skill 系统

### 创建技能

```bash
curl -X POST http://localhost:3777/api/skills \
  -H "Content-Type: application/json" \
  -d '{
    "name": "code-review",
    "description": "代码审查技能",
    "content": "# Code Review Skill\n\n你是一个代码审查专家...",
    "allowed_tools": ["read_file", "write_file"]
  }'
```

### 为 Agent 装载技能

```bash
curl -X POST http://localhost:3777/api/agents/{id}/skills \
  -H "Content-Type: application/json" \
  -d '{"skill_id": "skill-uuid"}'
```

### 导入内置技能

```bash
# 从文件系统导入
curl -X POST http://localhost:3777/api/skills/import-builtin
```

---

## 插件系统

### 插件目录

将插件脚本放置在 `plugins/` 目录下：

```
plugins/
├── pre_process.sh       # Shell 前处理钩子
├── post_process.py      # Python 后处理钩子
└── custom_handler.js    # Node.js 自定义处理
```

### 钩子类型

| 钩子 | 说明 |
|:-----|:-----|
| `TransformIncoming` | 消息进入时拦截和转换 |
| `TransformOutgoing` | 消息输出时拦截和转换 |

---

## 配置管理

### 全局配置

QueenBee 配置存储在 SQLite 数据库中（纯数据库驱动，无 YAML 文件）。

通过 API 查看和修改配置：

```bash
# 获取当前设置
curl http://localhost:3777/api/settings

# 更新设置
curl -X PUT http://localhost:3777/api/settings \
  -H "Content-Type: application/json" \
  -d '{
    "models": {
      "provider": "anthropic"
    },
    "compaction": {
      "threshold": 8000
    }
  }'
```

### 环境变量

| 变量 | 说明 | 默认值 |
|:-----|:-----|:------|
| `QUEENBEE_HOME` | 数据目录 | `~/.queenbee` |
| `QUEENBEE_PORT` | API 端口 | `3777` |
| `ANTHROPIC_API_KEY` | Anthropic API Key | — |
| `GOOGLE_API_KEY` | Google API Key | — |
| `OPENAI_API_KEY` | OpenAI API Key | — |

---

## 故障排查

### 常见问题

#### Agent 无响应

1. 检查 AI CLI 是否已安装：`which claude` / `which gemini`
2. 检查 API Key 是否已设置
3. 查看日志：`queenbee logs`
4. 检查队列状态：`curl http://localhost:3777/api/queue/status`

#### 消息卡在 Processing

```bash
# 恢复卡住的消息
curl -X POST http://localhost:3777/api/queue/recover
```

#### 数据库错误

```bash
# 数据库文件位于
ls -la ~/.queenbee/queenbee.db

# 如需重置，备份后删除
cp ~/.queenbee/queenbee.db ~/.queenbee/queenbee.db.bak
```

#### Provider Fallback 不工作

1. 确认 Agent 配置了 `fallback_provider`
2. 确认备用 Provider 的 CLI 已安装
3. 检查冷却状态（Provider 连续失败后冷却 5 分钟）

---

## 更多资源

- [README — 项目概述](../README.md)
- [BUILD — 构建手册](BUILD.md)
- [CONTRIBUTING — 贡献指南](../CONTRIBUTING.md)
- [API 文档](https://github.com/heyangguang/queenbee/wiki)
