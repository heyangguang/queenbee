# 贡献指南

感谢你对 QueenBee 项目的关注！我们欢迎所有形式的贡献。

## 📋 目录

- [行为准则](#行为准则)
- [如何贡献](#如何贡献)
- [开发环境搭建](#开发环境搭建)
- [代码规范](#代码规范)
- [提交规范](#提交规范)
- [Pull Request 流程](#pull-request-流程)
- [Issue 指南](#issue-指南)

---

## 行为准则

本项目遵循 [Contributor Covenant](https://www.contributor-covenant.org/) 行为准则。参与本项目即表示同意遵守此准则。

---

## 如何贡献

### 🐛 报告 Bug

1. 在 [Issues](https://github.com/heyangguang/queenbee/issues) 中搜索是否已有相同问题
2. 如果没有，创建一个新的 Issue，包含：
   - 清晰的标题
   - 复现步骤
   - 预期行为 vs 实际行为
   - 环境信息（OS、Go 版本、CLI 版本）
   - 相关日志或截图

### 💡 功能建议

1. 在 Issues 中搜索是否已有类似建议
2. 创建 Feature Request，描述：
   - 功能概述
   - 使用场景
   - 建议的实现方式（可选）

### 🔧 代码贡献

1. Fork 本仓库
2. 创建功能分支
3. 编写代码和测试
4. 提交 Pull Request

---

## 开发环境搭建

### 前置要求

| 工具 | 最低版本 | 用途 |
|:-----|:--------|:-----|
| **Go** | 1.25+ | 编译运行 |
| **Git** | 2.x | 版本控制 |
| **SQLite** | 3.x | 数据库（CGO 编译） |
| **AI CLI** | 任一 | 运行时依赖（claude / gemini / codex / opencode） |

### 快速开始

```bash
# 1. Fork 并克隆
git clone https://github.com/YOUR_USERNAME/queenbee.git
cd queenbee

# 2. 安装依赖
go mod download

# 3. 运行测试
go test ./...

# 4. 启动开发服务器
go run . start

# 5. 验证
curl http://localhost:3777/api/health
```

### 目录结构

详细的目录结构请参考 [README.md](README.md)。

---

## 代码规范

### Go 代码风格

- 遵循 [Effective Go](https://go.dev/doc/effective_go) 标准
- 使用 `gofmt` / `goimports` 格式化代码
- 使用 `golangci-lint` 进行静态分析
- 代码注释使用**中文**
- 导出函数和类型必须有 GoDoc 注释

### 命名约定

```go
// ✅ 好的命名
func ClaimNextMessage(ctx context.Context) (*Message, error)
func (e *Engine) InvokeAgent(agent *Agent) error

// ❌ 避免的命名
func cnm(ctx context.Context) (*Message, error) // 缩写不明确
func (e *Engine) Do(a *Agent) error             // 语义不清
```

### 错误处理

```go
// ✅ 使用 fmt.Errorf 包装上下文
if err != nil {
    return fmt.Errorf("invoke agent %s: %w", agent.ID, err)
}

// ❌ 避免忽略错误
result, _ := db.Query(...)  // 不要这样
```

### 测试要求

- 新功能必须包含单元测试
- 核心逻辑的测试覆盖率应 > 80%
- 使用 `testify` 进行断言（如果已有依赖）
- 测试文件命名：`xxx_test.go`

---

## 提交规范

使用 [Conventional Commits](https://www.conventionalcommits.org/) 规范：

```
<类型>(<作用域>): <描述>

[可选的正文]

[可选的脚注]
```

### 类型

| 类型 | 说明 | 示例 |
|:-----|:-----|:-----|
| `feat` | 新功能 | `feat(engine): 添加消息优先级队列` |
| `fix` | Bug 修复 | `fix(routing): 修复 @mention 匹配失败` |
| `docs` | 文档变更 | `docs: 更新 API 文档` |
| `style` | 代码格式（不影响功能） | `style: 修复缩进` |
| `refactor` | 重构（既不修复 bug 也不添加功能） | `refactor(memory): 重构搜索逻辑` |
| `perf` | 性能优化 | `perf(queue): 优化批量入队性能` |
| `test` | 添加或修改测试 | `test(invoke): 添加 Fallback 测试` |
| `chore` | 构建过程或辅助工具变动 | `chore: 更新 CI 配置` |

---

## Pull Request 流程

### 1. 分支命名

```
feat/消息优先级
fix/mention-路由失败
docs/api-文档更新
refactor/memory-搜索重构
```

### 2. PR 描述模板

```markdown
## 变更说明

简要描述你的改动。

## 变更类型

- [ ] 🐛 Bug 修复
- [ ] ✨ 新功能
- [ ] 📝 文档更新
- [ ] ♻️ 代码重构
- [ ] ⚡ 性能优化
- [ ] 🧪 测试相关

## 测试覆盖

- [ ] 已添加/更新测试
- [ ] 通过 `go test ./...`
- [ ] 通过 `go test -race ./...`

## 相关 Issue

Closes #(issue number)
```

### 3. 审查清单

- [ ] 代码通过 `go vet ./...`
- [ ] 代码通过 `golangci-lint run`
- [ ] 所有测试通过
- [ ] 新功能包含文档更新
- [ ] 没有引入破坏性变更（或在 PR 中明确说明）

---

## Issue 指南

### Bug Report 模板

```markdown
### 问题描述
[简要描述 bug]

### 复现步骤
1. 启动 queenbee serve
2. 发送消息 ...
3. 观察到 ...

### 预期行为
[应该发生什么]

### 实际行为
[实际发生了什么]

### 环境
- OS: macOS 15.x / Ubuntu 24.04
- Go: 1.25.x
- QueenBee: v0.1.0
- AI CLI: claude 1.x / gemini 0.x

### 日志/截图
[如有，请附上]
```

---

## 💬 联系方式

- **GitHub Issues** — Bug 报告和功能请求
- **Email** — [heyangev@gmail.com](mailto:heyangev@gmail.com)

---

感谢你的贡献！🐝
