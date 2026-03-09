#!/bin/sh
# QueenBee 一键安装脚本
# 用法: curl -fsSL https://raw.githubusercontent.com/heyangguang/queenbee/main/install.sh | sh
#
# 支持平台: macOS (ARM64/x86_64), Linux (x86_64/ARM64/x86), FreeBSD (x86_64)
# 支持 Windows 请手动下载: https://github.com/heyangguang/queenbee/releases

set -e

REPO="heyangguang/queenbee"
INSTALL_DIR="/usr/local/bin"
BINARY="queenbee"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { printf "${CYAN}[INFO]${NC}  %s\n" "$1"; }
ok()    { printf "${GREEN}[OK]${NC}    %s\n" "$1"; }
warn()  { printf "${YELLOW}[WARN]${NC}  %s\n" "$1"; }
fail()  { printf "${RED}[ERROR]${NC} %s\n" "$1"; exit 1; }

# ── 检测最新版本 ──
get_latest_version() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/'
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/'
    else
        fail "需要 curl 或 wget"
    fi
}

# ── 检测平台 ──
detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$OS" in
        darwin)  OS="darwin" ;;
        linux)   OS="linux" ;;
        freebsd) OS="freebsd" ;;
        *)       fail "不支持的操作系统: $OS" ;;
    esac

    case "$ARCH" in
        x86_64|amd64)  ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        i386|i686)     ARCH="386" ;;
        *)             fail "不支持的架构: $ARCH" ;;
    esac

    # macOS 不支持 386
    if [ "$OS" = "darwin" ] && [ "$ARCH" = "386" ]; then
        fail "macOS 不支持 32 位架构"
    fi

    # FreeBSD 只支持 amd64
    if [ "$OS" = "freebsd" ] && [ "$ARCH" != "amd64" ]; then
        fail "FreeBSD 只支持 x86_64 架构"
    fi
}

# ── 下载并安装 ──
install() {
    VERSION=$(get_latest_version)
    if [ -z "$VERSION" ]; then
        fail "无法获取最新版本号"
    fi

    info "最新版本: v${VERSION}"
    detect_platform
    info "检测到平台: ${OS}/${ARCH}"

    FILENAME="${BINARY}_${VERSION}_${OS}_${ARCH}"
    if [ "$OS" = "windows" ]; then
        FILENAME="${FILENAME}.zip"
    else
        FILENAME="${FILENAME}.tar.gz"
    fi

    URL="https://github.com/${REPO}/releases/download/v${VERSION}/${FILENAME}"
    info "下载 ${URL}"

    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$URL" -o "${TMPDIR}/${FILENAME}"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$URL" -O "${TMPDIR}/${FILENAME}"
    fi

    info "解压中..."
    cd "$TMPDIR"
    tar xzf "$FILENAME"

    # 安装二进制
    if [ -w "$INSTALL_DIR" ]; then
        mv "$BINARY" "$INSTALL_DIR/"
    else
        info "需要 root 权限安装到 ${INSTALL_DIR}"
        sudo mv "$BINARY" "$INSTALL_DIR/"
    fi
    chmod +x "${INSTALL_DIR}/${BINARY}"

    ok "QueenBee v${VERSION} 已安装到 ${INSTALL_DIR}/${BINARY}"

    # 验证
    if command -v "$BINARY" >/dev/null 2>&1; then
        ok "验证通过: $($BINARY --version 2>/dev/null || echo '已安装')"
    fi

    # Ollama 检测与安装提示
    echo ""
    if command -v ollama >/dev/null 2>&1; then
        ok "Ollama 已安装"
        # 检查嵌入模型
        if ollama list 2>/dev/null | grep -q "nomic-embed-text"; then
            ok "嵌入模型 nomic-embed-text 已就绪"
        else
            warn "未检测到嵌入模型，正在下载..."
            ollama pull nomic-embed-text
            ok "嵌入模型已下载"
        fi
    else
        warn "未检测到 Ollama — 记忆搜索将使用关键词匹配（效果较弱）"
        echo ""
        echo "  推荐安装 Ollama 以启用语义记忆搜索:"
        echo "    curl -fsSL https://ollama.com/install.sh | sh"
        echo "    ollama pull nomic-embed-text"
        echo ""
    fi

    echo ""
    echo "🐝 快速开始:"
    echo "   queenbee start          # 启动服务"
    echo "   queenbee setup          # 初始化配置"
    echo "   queenbee --help         # 查看帮助"
    echo ""
}

# ── 主入口 ──
echo ""
echo "  🐝 QueenBee 安装程序"
echo "  ────────────────────"
echo ""

install
