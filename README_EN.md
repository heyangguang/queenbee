<div align="center">

<img src="docs/assets/hero-banner.png" alt="QueenBee Banner" width="100%" />

# 🐝 QueenBee

### Message-Queue-Driven Multi-Agent Orchestration Engine

[![Go Version](https://img.shields.io/badge/Go-1.25-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-yellow?style=for-the-badge)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-Welcome-brightgreen?style=for-the-badge)](CONTRIBUTING.md)
[![GitHub Stars](https://img.shields.io/github/stars/heyangguang/queenbee?style=for-the-badge&logo=github)](https://github.com/heyangguang/queenbee/stargazers)

**[中文](README.md) | English | [日本語](README_JA.md)**

**QueenBee is an open-source, local-first multi-AI-agent collaboration engine.** It orchestrates multiple AI CLIs (Claude, Gemini, Codex, OpenCode) through an SQLite message queue, enabling agents to collaborate via `@mention` conversations, automatic routing, and parallel task execution — with persistent memory, a skill system, and soul self-reflection capabilities.

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

QueenBee uses a **message-queue-driven** architecture where all agent interactions are scheduled through an SQLite message queue for serial/parallel dispatch:

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
                          │  │  └──────┴────────┘ │  │   │  Reflect  │
                          │  └───────────────────┘  │   └──────────┘
                          └─────────────────────────┘
```

### Core Processing Pipeline

```
Message Enqueue → ClaimNextMessage (atomic claim)
               → ParseAgentRouting (@mention routing)
               → FindTeamForAgent (team lookup)
               → InvokeAgent (CLI call + Fallback + cooldown)
               → ExtractTeammateMentions ([@teammate: msg] extraction)
               → EnqueueInternalMessage (teammate message enqueue)
               → CompleteConversation (aggregate response + history save)
```

---

## 🚀 Key Features

### 📬 SQLite Message Queue Engine
The message-driven core scheduler. Messages follow a `pending → processing → completed / dead` lifecycle with support for:
- **Atomic Claim** — `ClaimNextMessage` prevents duplicate consumption
- **Auto-Retry** — Messages enter the dead-letter queue after 5 failures
- **Cross-Agent Parallelism** — Different agents run fully in parallel; same-agent messages are serialized
- **Session Recovery** — `RestoreConversations` restores active sessions from the database on startup
- **Stale Recovery** — `RecoverStaleMessages` automatically recovers timed-out messages

### 🏷 @mention Routing System
Agents collaborate through natural language `@mention`:
```
User sends: @coder implement login feature
Routes to: coder agent

coder replies: [@reviewer: please review this code] [@tester: please write tests]
System auto: extract teammate mentions → enqueue to corresponding agents
```
- **Two-Round Matching** — First exact-match Agent ID, then fallback to Team Leader
- **Team Routing** — `@team-name` automatically routes to the Team Leader Agent
- **Message Sanitization** — Automatically strips `@mention` prefix, keeping clean message body

### 🔌 Four CLI Providers + Fallback
Invokes local AI CLIs via `os/exec`, with a unified abstraction over provider differences:

| Provider | CLI Binary | Features |
|:---------|:-----------|:---------|
| **Anthropic** | `claude` | stream-json output, `-c` resume conversation, `--dangerously-skip-permissions` |
| **Google** | `gemini` | `--yolo` mode, sandbox support |
| **OpenAI** | `codex` | `exec resume --last` resume conversation, JSON output |
| **OpenCode** | `opencode` | `run` mode, JSON format |

- **Auto-Fallback** — Automatically switches to backup provider on primary failure
- **Cooldown Mechanism** — Failed providers are cooled down for 5 minutes
- **Activity Watchdog** — Detects stale processes via stdout timeout, auto-kills
- **Model Resolution** — `ResolveClaudeModel` / `ResolveGeminiModel` smart model mapping

### 🧠 Three-Tier Degrading Persistent Memory
Multi-layered memory system where each agent has independent long-term memory:

```
Search Priority: Ollama Embedding vector search
                   ↓ (if unavailable)
                FTS5 full-text search
                   ↓ (if unavailable)
                LIKE fuzzy matching
```

- **Three Scopes** — Agent-private / Team-shared / User-global
- **Auto-Extraction** — Automatically extracts valuable memory entries from conversations
- **Context Injection** — `FormatMemoriesForContext` injects relevant memories into Agent Prompt

### 🧩 Skill System
Dynamic skill mounting via SKILL.md files to inject specialized capabilities into agents:
- **Multi-CLI Sync** — Writes to `.agents/skills/`, `.claude/skills/`, `.gemini/skills/` simultaneously
- **Built-in Skill Discovery** — Auto-scans `templates/` and `QUEENBEE_HOME` directories
- **CLI Global Skill Scanning** — Discovers globally installed skills for each CLI
- **YAML Frontmatter** — Standardized metadata (name, description, allowed-tools)

### 👻 Soul Self-Reflection System
Agents automatically reflect after each task, updating the persistent `SOUL.md` identity file:
- **Incremental Updates** — Appends new experiences and insights without rewriting
- **Asynchronous Execution** — Runs in background goroutine, non-blocking
- **Full Provider Support** — Each CLI has its own Soul update path

### 📦 Context Compaction
Automatic AI summarization for long messages, reducing token consumption:
- **Smart Threshold** — Triggers compaction above 8000 characters (configurable)
- **AI Summary** — Preserves code, errors, decisions; removes redundancy
- **Fallback Truncation** — Keeps first and last 40% when AI compaction fails

### 🔧 Plugin Engine
Extensible hook system supporting multiple scripting languages:
- **Bidirectional Hooks** — `TransformIncoming` / `TransformOutgoing` message interception
- **Multi-Language Support** — Shell, Python, Node.js, Go native plugins
- **Event Broadcasting** — `BroadcastEvent` distributes system events to all plugins
- **Auto-Discovery** — Scans `plugins/` directory for automatic loading

### 👥 Team Collaboration
Agents organized into Teams with Leader dispatch and direct teammate conversations:
- **AGENTS.md Sync** — Auto-generates teammate info file injected into each agent's working directory
- **Project Directory Injection** — `injectProjectDirectory` injects project path info into agent context
- **Git Repo Auto-Init** — Ensures Claude CLI can discover `.claude/` skill directories

---

## 📦 Getting Started

### Prerequisites

- **Go** 1.25+
- At least one AI CLI tool:
  - [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`)
  - [Gemini CLI](https://github.com/google-gemini/gemini-cli) (`gemini`)
  - [Codex CLI](https://github.com/openai/codex) (`codex`)
  - [OpenCode](https://github.com/opencode-ai/opencode) (`opencode`)

### Installation

```bash
# Clone the repository
git clone https://github.com/heyangguang/queenbee.git
cd queenbee

# Build
go build -o queenbee .

# Or install directly
go install github.com/heyangguang/queenbee@latest
```

### macOS Quick Install

```bash
curl -fsSL https://raw.githubusercontent.com/heyangguang/queenbee/main/darwin-install.sh | bash
```

### Launch

```bash
# Start server (default port 9876)
queenbee serve

# Specify port
queenbee serve --port 8080

# Health check
curl http://localhost:9876/health
```

---

## 📁 Project Structure

```
queenbee/
├── cmd/
│   ├── root.go              # CLI entry (Cobra) — serve / setup commands
│   ├── extras.go             # Utility commands
│   └── visualize.go          # Visualization tools
├── internal/
│   ├── config/               # Configuration management (pure database-driven, no YAML files)
│   │   └── config.go         #   Init, GetSettings, SaveSettings, Resolve*Model
│   ├── db/                   # SQLite persistence layer
│   │   └── db.go             #   Message queue, response queue, Agent/Team/Task/Skill CRUD
│   ├── engine/               # 🧠 Core orchestration engine
│   │   ├── processor.go      #   ProcessQueue — message dispatch (cross-agent parallel, same-agent serial)
│   │   ├── invoke.go         #   InvokeAgent — 4 CLI providers + Fallback + cooldown + Watchdog
│   │   ├── conversation.go   #   ConversationMap — thread-safe session management + persistence recovery
│   │   ├── routing.go        #   @mention routing — ParseAgentRouting + ExtractTeammateMentions
│   │   ├── agent.go          #   Agent directory management — template copy, AGENTS.md sync, Git init
│   │   ├── skills.go         #   Skill system — SKILL.md sync, built-in skill discovery
│   │   ├── soul.go           #   Soul self-reflection — SOUL.md async update
│   │   ├── compaction.go     #   Context compaction — AI summary + fallback truncation
│   │   ├── activity.go       #   Agent activity tracking
│   │   └── response.go       #   Response handling
│   ├── memory/               # 🧠 Persistent memory system
│   │   ├── memory.go         #   Three-tier search (Embedding → FTS5 → LIKE) + multi-scope
│   │   ├── memory_extract.go #   Conversation memory auto-extraction
│   │   └── embedding.go      #   Ollama vector generation
│   ├── plugins/              # 🔌 Plugin engine
│   │   └── plugins.go        #   Script hooks + Go native plugins + event broadcasting
│   ├── server/               # 🌐 HTTP service
│   │   ├── server.go         #   Gin routes — Agent/Team/Task/Queue/Session/Log API
│   │   ├── api_v2.go         #   V2 API — Health/Provider/Skill/Soul/Memory/Project
│   │   ├── sse.go            #   Server-Sent Events real-time push
│   │   └── helpers.go        #   Utility functions
│   ├── event/                # Internal event bus
│   ├── logging/              # Structured logging
│   └── pairing/              # Agent pairing
├── templates/                # Agent template files
│   ├── AGENTS.md             #   Teammate info template
│   ├── SOUL.md               #   Agent identity template
│   └── heartbeat.md          #   Heartbeat template
├── types/                    # Shared type definitions
├── main.go                   # Application entry point
├── go.mod
└── go.sum
```

---

## 🌐 API Overview

### Core API

| Category | Method | Endpoint | Description |
|:---------|:-------|:---------|:------------|
| **Message** | `POST` | `/message` | Send message to Agent (supports @mention routing) |
| **Agent** | `GET` | `/agents` | List all Agents |
| | `POST` | `/agents` | Create Agent |
| | `PUT` | `/agents/:id` | Update Agent configuration |
| | `DELETE` | `/agents/:id` | Delete Agent |
| **Team** | `GET` | `/teams` | List all Teams |
| | `POST` | `/teams` | Create Team |
| **Queue** | `GET` | `/queue/status` | Queue status (pending/processing/completed/dead) |
| | `GET` | `/queue/dead` | Dead-letter message list |
| | `POST` | `/queue/dead/:id/retry` | Retry dead-letter message |
| | `POST` | `/queue/recover` | Recover stuck processing messages |
| **Task** | `GET` | `/tasks` | List tasks |
| | `POST` | `/tasks` | Create task |
| **Memory** | `GET` | `/agents/:id/memories` | List Agent memories |
| | `POST` | `/agents/:id/memories/search` | Search related memories |
| | `POST` | `/agents/:id/memories` | Manually store memory |
| **Skill** | `GET` | `/agents/:id/skills` | Agent's loaded skills |
| | `POST` | `/agents/:id/skills` | Load skill for Agent |
| | `GET` | `/skills` | All skill definitions |
| **Provider** | `GET` | `/providers` | Available AI Provider list |
| | `GET` | `/providers/:id/models` | Provider's available models |
| **Soul** | `GET` | `/agents/:id/soul` | Read Agent's SOUL.md |
| **System** | `GET` | `/health` | Health check |
| | `GET` | `/system/status` | System status (OS/Memory/Goroutine) |
| **SSE** | `GET` | `/events` | Real-time event stream |
| **Response** | `GET` | `/responses/recent` | Recent responses |
| | `POST` | `/responses/:id/ack` | Acknowledge response delivered |

---

## 🧪 Testing

```bash
# Run all tests
go test ./...

# With verbose output
go test -v ./internal/engine/...

# Race condition detection
go test -race ./...
```

---

## 🗺 Roadmap

- [x] SQLite message queue engine + dead-letter + auto-retry
- [x] Four CLI Providers (Claude / Gemini / Codex / OpenCode) + Fallback
- [x] @mention routing + Team collaboration + teammate message extraction
- [x] Three-tier degrading persistent memory (Embedding → FTS5 → LIKE)
- [x] Skill system + multi-CLI directory sync
- [x] Soul self-reflection + SOUL.md persistent identity
- [x] Context compaction + fallback truncation
- [x] Plugin engine (script hooks + Go native + event broadcasting)
- [x] Session persistence + startup recovery
- [ ] pgvector semantic memory (replacing SQLite Embedding)
- [ ] WebSocket bidirectional communication
- [ ] Agent sandbox isolated execution
- [ ] Community plugin marketplace

---

## 🤝 Contributing

We welcome all forms of contributions!

### Contribution Workflow

1. **Fork** the repository
2. **Create a branch** (`git checkout -b feat/amazing-feature`)
3. **Commit** (`git commit -m 'feat: add amazing feature'`)
4. **Push** (`git push origin feat/amazing-feature`)
5. **Create a Pull Request**

### Local Development

```bash
git clone https://github.com/YOUR_USERNAME/queenbee.git
cd queenbee
go mod download
go run . serve --port 9876
```

### Commit Convention

Use conventional commits: `feat:` / `fix:` / `docs:` / `refactor:` / `test:`

---

## 📄 License

This project is licensed under **Apache License 2.0** — see the [LICENSE](LICENSE) file for details.

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

| Project | Description |
|:--------|:------------|
| [queenbee](https://github.com/heyangguang/queenbee) | 🐝 This repo — Go backend engine |
| [queenbee-ui](https://github.com/heyangguang/queenbee-ui) | 🖥 Web management interface (Next.js) |

---

## 🌟 Star History

If QueenBee helps you, please give it a ⭐!

[![Star History Chart](https://api.star-history.com/svg?repos=heyangguang/queenbee&type=Date)](https://star-history.com/#heyangguang/queenbee&Date)

---

<div align="center">

**Built with 🐝 by the QueenBee Community**

*Let AI Agents collaborate like a swarm of bees.*

</div>
