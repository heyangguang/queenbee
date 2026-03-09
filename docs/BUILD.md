# 🔨 QueenBee 构建手册

本文档详细介绍 QueenBee 的构建、编译、测试和发布流程。

---

## 📋 目录

- [环境要求](#环境要求)
- [本地构建](#本地构建)
- [交叉编译](#交叉编译)
- [Docker 构建](#docker-构建)
- [测试](#测试)
- [CI/CD](#cicd)
- [发布流程](#发布流程)
- [常见问题](#常见问题)

---

## 环境要求

### 必需工具

| 工具 | 最低版本 | 安装方式 | 用途 |
|:-----|:--------|:--------|:-----|
| **Go** | 1.25+ | [go.dev/dl](https://go.dev/dl/) | 编译运行 |
| **Git** | 2.x | `brew install git` | 版本控制 |
| **GCC/Clang** | — | Xcode Command Line Tools | CGO (SQLite) |

### 可选工具

| 工具 | 用途 | 安装方式 |
|:-----|:-----|:--------|
| **GoReleaser** | 自动化构建 + 发布 | `brew install goreleaser` |
| **golangci-lint** | 代码质量检查 | `brew install golangci-lint` |
| **Docker** | 容器化构建 | [docker.com](https://www.docker.com/) |

### Go 环境验证

```bash
# 检查 Go 版本
go version
# 期望输出: go version go1.25.x darwin/arm64

# 检查 CGO 支持
go env CGO_ENABLED
# 期望输出: 1 (SQLite 需要 CGO)
```

---

## 本地构建

### 快速构建

```bash
# 克隆仓库
git clone https://github.com/heyangguang/queenbee.git
cd queenbee

# 下载依赖
go mod download

# 构建二进制
go build -o queenbee .

# 验证
./queenbee --help
```

### 带版本信息构建

```bash
VERSION=$(git describe --tags --always --dirty)
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
COMMIT=$(git rev-parse --short HEAD)

go build -ldflags "\
  -X main.version=${VERSION} \
  -X main.buildTime=${BUILD_TIME} \
  -X main.commit=${COMMIT}" \
  -o queenbee .
```

### 优化构建（生产用）

```bash
# 去除调试信息，减小二进制体积
go build -ldflags="-s -w" -o queenbee .

# 使用 UPX 进一步压缩（可选）
upx --best queenbee
```

### 安装到 $GOPATH/bin

```bash
go install .
# 或者指定远程版本
go install github.com/heyangguang/queenbee@latest
```

---

## 交叉编译

### 支持的平台

| OS | Architecture | 二进制名称 |
|:---|:-------------|:----------|
| macOS | amd64 (Intel) | `queenbee-darwin-amd64` |
| macOS | arm64 (Apple Silicon) | `queenbee-darwin-arm64` |
| Linux | amd64 | `queenbee-linux-amd64` |
| Linux | arm64 | `queenbee-linux-arm64` |
| Windows | amd64 | `queenbee-windows-amd64.exe` |
| FreeBSD | amd64 | `queenbee-freebsd-amd64` |

### 手动交叉编译

> ⚠️ **注意**：QueenBee 使用 CGO（因为 SQLite 驱动），交叉编译需要对应平台的 C 编译器。

```bash
# macOS Intel
GOOS=darwin GOARCH=amd64 go build -o queenbee-darwin-amd64 .

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o queenbee-darwin-arm64 .

# Linux amd64（需要 Linux 交叉编译器）
CGO_ENABLED=1 CC=x86_64-linux-musl-gcc \
  GOOS=linux GOARCH=amd64 \
  go build -o queenbee-linux-amd64 .
```

### 使用 GoReleaser 构建全平台

```bash
# 仅构建（不发布）
goreleaser build --snapshot --clean

# 输出在 dist/ 目录
ls dist/
```

---

## Docker 构建

### Dockerfile

```dockerfile
# 多阶段构建
FROM golang:1.25-alpine AS builder

# 安装 CGO 依赖
RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /queenbee .

# 运行阶段
FROM alpine:3.21
RUN apk add --no-cache ca-certificates sqlite-libs

COPY --from=builder /queenbee /usr/local/bin/queenbee

EXPOSE 3777
VOLUME /data

ENV QUEENBEE_HOME=/data

ENTRYPOINT ["queenbee"]
CMD ["start"]
```

### Docker 命令

```bash
# 构建镜像
docker build -t queenbee:latest .

# 运行
docker run -d \
  --name queenbee \
  -p 3777:3777 \
  -v queenbee-data:/data \
  queenbee:latest

# 查看日志
docker logs -f queenbee
```

---

## 测试

### 运行全部测试

```bash
go test ./...
```

### 详细输出

```bash
go test -v ./...
```

### 指定包测试

```bash
# 测试 engine 包
go test -v ./internal/engine/...

# 测试 memory 包
go test -v ./internal/memory/...

# 测试 server 包
go test -v ./internal/server/...
```

### 竞态检测

```bash
go test -race ./...
```

### 测试覆盖率

```bash
# 生成覆盖率报告
go test -coverprofile=coverage.out ./...

# 查看覆盖率摘要
go tool cover -func=coverage.out

# 生成 HTML 覆盖率报告
go tool cover -html=coverage.out -o coverage.html
open coverage.html
```

### 基准测试

```bash
go test -bench=. -benchmem ./internal/engine/...
```

---

## CI/CD

### GitHub Actions

项目使用 GitHub Actions 进行持续集成。工作流配置在 `.github/workflows/` 目录下。

#### CI 工作流（`.github/workflows/ci.yml`）

每次 push 和 PR 自动执行：
- `go vet ./...` — 静态分析
- `go test ./...` — 全量测试
- `golangci-lint run` — 代码质量检查

#### Release 工作流（`.github/workflows/release.yml`）

创建新 tag 时自动触发：
1. 运行测试
2. 使用 GoReleaser 构建全平台二进制
3. 生成 Changelog
4. 创建 GitHub Release 并上传 Artifacts

---

## 发布流程

### 版本号规范

遵循 [Semantic Versioning](https://semver.org/)：`MAJOR.MINOR.PATCH`

- **MAJOR** — 不兼容的 API 变更
- **MINOR** — 向后兼容的功能添加
- **PATCH** — 向后兼容的 Bug 修复

### 发布步骤

```bash
# 1. 确保所有测试通过
go test ./...

# 2. 更新 CHANGELOG（如有）
# 编辑 CHANGELOG.md

# 3. 创建并推送 tag
git tag -a v0.2.0 -m "release: v0.2.0 — 新增消息优先级队列"
git push origin v0.2.0

# 4. GitHub Actions 自动构建和发布
# 检查 Release 页面：https://github.com/heyangguang/queenbee/releases
```

### GoReleaser 配置

项目使用 `.goreleaser.yml` 配置构建矩阵。详细配置参考仓库中的配置文件。

---

## 常见问题

### CGO 编译错误

```
# cgo: C compiler "gcc" not found
```

**解决方法**：
```bash
# macOS: 安装 Xcode Command Line Tools
xcode-select --install

# Linux: 安装 GCC
sudo apt-get install build-essential  # Debian/Ubuntu
sudo yum install gcc                   # CentOS/RHEL
```

### SQLite 链接错误

```
# undefined reference to `sqlite3_xxx`
```

**解决方法**：
```bash
# 确保 CGO 已启用
export CGO_ENABLED=1

# macOS 通常自带 SQLite
# Linux 需要安装开发包
sudo apt-get install libsqlite3-dev  # Debian/Ubuntu
```

### Go Module 问题

```bash
# 清理模块缓存
go clean -modcache

# 重新下载依赖
go mod download

# 整理依赖
go mod tidy
```

### 二进制体积过大

```bash
# 使用 -ldflags 去除调试信息
go build -ldflags="-s -w" -o queenbee .

# 查看体积
ls -lh queenbee

# 使用 UPX 压缩（可选）
brew install upx
upx --best queenbee
```

---

## 更多资源

- [README — 项目概述](../README.md)
- [USAGE — 使用说明](USAGE.md)
- [CONTRIBUTING — 贡献指南](../CONTRIBUTING.md)
