#!/usr/bin/env bash
# ==============================================================================
# M365-Copilot2API - WSL Build Script
# Automatically builds Windows (.exe) and Linux binaries
# ==============================================================================

set -euo pipefail

# ANSI Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT_DIR}"

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 1. Detect Go Compiler
GO_BIN=""
USE_DOCKER=false

find_go() {
    if command -v go >/dev/null 2>&1; then
        GO_BIN="go"
        return 0
    fi

    # Check common non-standard installations in WSL
    local candidates=(
        "${HOME}/go123/bin/go"
        "${HOME}/go/bin/go"
        "/usr/local/go/bin/go"
        "/opt/go/bin/go"
        "/snap/bin/go"
    )

    for c in "${candidates[@]}"; do
        if [[ -x "$c" ]]; then
            GO_BIN="$c"
            export PATH="$(dirname "$c"):$PATH"
            return 0
        fi
    done

    # Fallback to Docker if Go is not installed on host
    if command -v docker >/dev/null 2>&1; then
        USE_DOCKER=true
        return 0
    fi

    return 1
}

if ! find_go; then
    log_error "未检测到 Go 编译器或 Docker 环境。请安装 Go (>=1.22) 或启动 Docker。"
    exit 1
fi

if [[ "$USE_DOCKER" == true ]]; then
    log_info "未检测到本地 Go 环境，将使用 Docker (golang:1.23-alpine) 进行编译..."
else
    GO_VER="$(${GO_BIN} version)"
    log_info "检测到本地 Go 环境: ${CYAN}${GO_VER}${NC}"
fi

# 2. Synchronize Web Assets (Ensure embedded files match latest web/ changes)
sync_web_assets() {
    log_info "同步前端静态资源到 internal/web/web/ 嵌入目录..."
    mkdir -p "${ROOT_DIR}/internal/web/web"
    if [[ -f "${ROOT_DIR}/web/index.html" ]]; then
        cp -f "${ROOT_DIR}/web/index.html" "${ROOT_DIR}/internal/web/web/index.html"
    fi
    if [[ -f "${ROOT_DIR}/web/conversation.html" ]]; then
        cp -f "${ROOT_DIR}/web/conversation.html" "${ROOT_DIR}/internal/web/web/conversation.html"
    fi
    if [[ -f "${ROOT_DIR}/web/login.html" ]]; then
        cp -f "${ROOT_DIR}/web/login.html" "${ROOT_DIR}/internal/web/web/login.html"
    fi
    if [[ -f "${ROOT_DIR}/web/debug.html" ]]; then
        cp -f "${ROOT_DIR}/web/debug.html" "${ROOT_DIR}/internal/web/web/debug.html"
    fi
}

# 3. Parse Command-line Options
TARGET="all"
RUN_TESTS=false

for arg in "$@"; do
    case "$arg" in
        win|windows)
            TARGET="windows"
            ;;
        linux)
            TARGET="linux"
            ;;
        all)
            TARGET="all"
            ;;
        --test|-t)
            RUN_TESTS=true
            ;;
        --help|-h)
            echo "用法: ./build.sh [win|linux|all] [--test]"
            echo ""
            echo "参数说明:"
            echo "  win / windows : 仅编译 Windows 版本 (m365-copilot2api.exe)"
            echo "  linux         : 仅编译 Linux 版本 (m365-copilot2api)"
            echo "  all           : 同时编译 Windows 与 Linux 版本 (默认)"
            echo "  --test, -t    : 编译前执行单元测试"
            echo "  --help, -h    : 显示本帮助信息"
            exit 0
            ;;
        *)
            log_warn "未知参数: $arg (将被忽略)"
            ;;
    esac
done

sync_web_assets

# 4. Optional Unit Tests
if [[ "$RUN_TESTS" == true ]]; then
    log_info "执行单元测试..."
    if [[ "$USE_DOCKER" == true ]]; then
        docker run --rm -v "${ROOT_DIR}:/src" -w /src golang:1.23-alpine go test ./internal/web/...
    else
        ${GO_BIN} test ./internal/web/...
    fi
    log_success "单元测试全部通过！"
fi

# 5. Build Functions
BUILD_LDFLAGS="-s -w"

build_windows() {
    log_info "正在编译 Windows (amd64) 产物: ${CYAN}m365-copilot2api.exe${NC}..."
    local start_time=$(date +%s)
    if [[ "$USE_DOCKER" == true ]]; then
        docker run --rm -v "${ROOT_DIR}:/src" -w /src golang:1.23-alpine \
            sh -c "CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='${BUILD_LDFLAGS}' -o m365-copilot2api.exe ./cmd/server"
    else
        CGO_ENABLED=0 GOOS=windows GOARCH=amd64 ${GO_BIN} build -trimpath -ldflags="${BUILD_LDFLAGS}" -o m365-copilot2api.exe ./cmd/server
    fi
    local duration=$(( $(date +%s) - start_time ))
    log_success "Windows 版本编译完成 (耗时: ${duration}s)"
}

build_linux() {
    log_info "正在编译 Linux (amd64) 产物: ${CYAN}m365-copilot2api${NC}..."
    local start_time=$(date +%s)
    if [[ "$USE_DOCKER" == true ]]; then
        docker run --rm -v "${ROOT_DIR}:/src" -w /src golang:1.23-alpine \
            sh -c "CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='${BUILD_LDFLAGS}' -o m365-copilot2api ./cmd/server"
    else
        CGO_ENABLED=0 GOOS=linux GOARCH=amd64 ${GO_BIN} build -trimpath -ldflags="${BUILD_LDFLAGS}" -o m365-copilot2api ./cmd/server
    fi
    chmod +x "${ROOT_DIR}/m365-copilot2api" 2>/dev/null || true
    local duration=$(( $(date +%s) - start_time ))
    log_success "Linux 版本编译完成 (耗时: ${duration}s)"
}

# 6. Execute Build
case "$TARGET" in
    windows)
        build_windows
        ;;
    linux)
        build_linux
        ;;
    all)
        build_windows
        build_linux
        ;;
esac

# 7. Print Result Summary
echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}            编译产物清单               ${NC}"
echo -e "${GREEN}========================================${NC}"
if [[ -f "${ROOT_DIR}/m365-copilot2api.exe" ]]; then
    size=$(ls -lh "${ROOT_DIR}/m365-copilot2api.exe" | awk '{print $5}')
    echo -e "  Windows: ${CYAN}m365-copilot2api.exe${NC} (${size})"
fi
if [[ -f "${ROOT_DIR}/m365-copilot2api" ]]; then
    size=$(ls -lh "${ROOT_DIR}/m365-copilot2api" | awk '{print $5}')
    echo -e "  Linux:   ${CYAN}m365-copilot2api${NC} (${size})"
fi
echo -e "${GREEN}========================================${NC}"
echo -e "${BLUE}构建成功！可以直接运行或部署对应产物。${NC}"
