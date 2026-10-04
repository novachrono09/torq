#!/bin/sh
# torq installer - https://github.com/novachrono09/torq
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

echo "${CYAN}${BOLD}"
cat << "EOF"
  _______ ____  _____   ____ 
 |__   __/ __ \|  __ \ / __ \
    | | | |  | | |__) | |  | |
    | | | |  | |  _  /| |  | |
    | | | |__| | | \ \| |__| |
    |_|  \____/|_|  \_\\___\_\
EOF
echo "${NC}"
echo "${BOLD}⚡ Installing torq - Ultra-Fast Standalone Media Engine (No Python Required)${NC}\n"

# 1. Detect OS & Architecture
IS_TERMUX=false
if [ -d "/data/data/com.termux" ] || [ -n "$PREFIX" ]; then
    IS_TERMUX=true
fi

RAW_OS="$(uname -s)"
case "$RAW_OS" in
    Linux*)  OS="linux" ;;
    Darwin*) OS="darwin" ;;
    *)       OS="linux" ;;
esac

RAW_ARCH="$(uname -m)"
case "$RAW_ARCH" in
    x86_64|amd64)   ARCH="amd64" ;;
    aarch64|arm64)  ARCH="arm64" ;;
    armv7l|armv7)   ARCH="armv7" ;;
    *)              ARCH="arm64" ;;
esac

echo "${CYAN}[1/4] Detected Platform: ${BOLD}${OS}/${ARCH}${NC}"
if [ "$IS_TERMUX" = true ]; then
    echo "       Environment: ${GREEN}Termux (Android)${NC}"
fi

# 2. Check and install aria2 engine (C++ native engine, ~4MB)
echo "${CYAN}[2/4] Checking download engine (aria2c)...${NC}"

if ! command -v aria2c >/dev/null 2>&1; then
    echo "${YELLOW}Installing aria2 engine...${NC}"
    if [ "$IS_TERMUX" = true ]; then
        pkg install -y aria2
    elif command -v apt-get >/dev/null 2>&1; then
        sudo apt-get update && sudo apt-get install -y aria2
    elif command -v pacman >/dev/null 2>&1; then
        sudo pacman -Sy --noconfirm aria2
    elif command -v dnf >/dev/null 2>&1; then
        sudo dnf install -y aria2
    elif command -v brew >/dev/null 2>&1; then
        brew install aria2
    else
        echo "${YELLOW}Notice: aria2c was not found. Please install aria2 using your package manager.${NC}"
    fi
else
    echo "       ${GREEN}✔ aria2c is ready.${NC}"
fi

# 3. Determine target directory
echo "${CYAN}[3/4] Locating installation path...${NC}"

if [ "$IS_TERMUX" = true ]; then
    BIN_DIR="${PREFIX:-/data/data/com.termux/files/usr}/bin"
elif [ -w "/usr/local/bin" ]; then
    BIN_DIR="/usr/local/bin"
elif [ "$(id -u)" -eq 0 ]; then
    BIN_DIR="/usr/local/bin"
else
    BIN_DIR="$HOME/.local/bin"
    mkdir -p "$BIN_DIR"
    case ":$PATH:" in
        *":$BIN_DIR:"*) ;;
        *) echo "${YELLOW}Warning: $BIN_DIR is not in your PATH. Add 'export PATH=\"\$HOME/.local/bin:\$PATH\"' to your ~/.bashrc or ~/.zshrc${NC}" ;;
    esac
fi

TARGET="$BIN_DIR/torq"
echo "       Destination: ${BOLD}$TARGET${NC}"

# 4. Install binary
echo "${CYAN}[4/4] Installing standalone binary...${NC}"

SCRIPT_DIR="$(cd "$(dirname "$0")" 2>/dev/null && pwd || echo "")"
if [ -f "$SCRIPT_DIR/torq" ] && [ "$SCRIPT_DIR/torq" != "$TARGET" ]; then
    cp "$SCRIPT_DIR/torq" "$TARGET"
else
    DOWNLOAD_URL="https://github.com/novachrono09/torq/releases/latest/download/torq-${OS}-${ARCH}"
    echo "       Downloading from: $DOWNLOAD_URL"
    if ! curl -fsSL "$DOWNLOAD_URL" -o "$TARGET" 2>/dev/null; then
        # Fallback to main binary or local build
        curl -fsSL "https://raw.githubusercontent.com/novachrono09/torq/main/torq" -o "$TARGET" || true
    fi
fi

chmod +x "$TARGET"

# 5. Verification
if [ -x "$TARGET" ]; then
    VER=$("$TARGET" -v 2>/dev/null || echo "1.1.0")
    echo ""
    echo "${GREEN}${BOLD}✔ Successfully installed $VER (Native Go Standalone Binary)!${NC}"
    echo "${BOLD}Zero Python. Zero Pip. Instant Startup.${NC}"
    echo ""
    echo "Examples:"
    echo "  ${CYAN}torq \"Interstellar\"${NC}        # Interactive multi-tracker TUI"
    echo "  ${CYAN}torq queue${NC}                 # View downloads & active files"
    echo "  ${CYAN}torq -q highest \"Oppenheimer\"${NC} # Filter 4K / Remux"
    echo "  ${CYAN}torq --update${NC}              # Auto-update binary"
    echo ""
else
    echo "${RED}Installation failed. Please check permissions.${NC}"
    exit 1
fi
