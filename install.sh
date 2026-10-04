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
echo "${BOLD}⚡ Installing torq - Lightning Fast Terminal Media Engine${NC}\n"

# 1. Detect environment
IS_TERMUX=false
if [ -d "/data/data/com.termux" ] || [ -n "$PREFIX" ]; then
    IS_TERMUX=true
fi

# 2. Check and install dependencies
echo "${CYAN}[1/4] Checking system dependencies...${NC}"

if [ "$IS_TERMUX" = true ]; then
    INSTALL_CMD=""
    if ! command -v python3 >/dev/null 2>&1; then
        INSTALL_CMD="$INSTALL_CMD python"
    fi
    if ! command -v aria2c >/dev/null 2>&1; then
        INSTALL_CMD="$INSTALL_CMD aria2"
    fi
    if [ -n "$INSTALL_CMD" ]; then
        echo "${YELLOW}Installing missing packages ($INSTALL_CMD)...${NC}"
        pkg install -y $INSTALL_CMD
    fi
else
    # Linux / macOS
    if ! command -v python3 >/dev/null 2>&1; then
        echo "${RED}Error: python3 is not installed. Please install python3 first.${NC}"
        exit 1
    fi
    if ! command -v aria2c >/dev/null 2>&1; then
        echo "${YELLOW}aria2c is missing. Attempting to install...${NC}"
        if command -v apt-get >/dev/null 2>&1; then
            sudo apt-get update && sudo apt-get install -y aria2
        elif command -v pacman >/dev/null 2>&1; then
            sudo pacman -Sy --noconfirm aria2
        elif command -v dnf >/dev/null 2>&1; then
            sudo dnf install -y aria2
        elif command -v brew >/dev/null 2>&1; then
            brew install aria2
        else
            echo "${RED}Could not auto-install aria2. Please install 'aria2' using your package manager.${NC}"
            exit 1
        fi
    fi
fi

# 3. Determine target install directory
echo "${CYAN}[2/4] Determining installation directory...${NC}"

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
echo "Target path: ${BOLD}$TARGET${NC}"

# 4. Fetch or copy executable
echo "${CYAN}[3/4] Downloading torq...${NC}"

# If running from inside a cloned repository
SCRIPT_DIR="$(cd "$(dirname "$0")" 2>/dev/null && pwd || echo "")"
if [ -f "$SCRIPT_DIR/torq" ] && [ "$SCRIPT_DIR/torq" != "$TARGET" ]; then
    cp "$SCRIPT_DIR/torq" "$TARGET"
else
    curl -fsSL "https://raw.githubusercontent.com/novachrono09/torq/main/torq" -o "$TARGET"
fi

chmod +x "$TARGET"

if [ "$IS_TERMUX" = true ] && command -v termux-fix-shebang >/dev/null 2>&1; then
    termux-fix-shebang "$TARGET"
fi

# 5. Verification
echo "${CYAN}[4/4] Verifying installation...${NC}"
if command -v torq >/dev/null 2>&1 || [ -x "$TARGET" ]; then
    VERSION=$("$TARGET" --version 2>/dev/null || echo "v1.0.0")
    echo ""
    echo "${GREEN}${BOLD}✔ Successfully installed $VERSION!${NC}"
    echo ""
    echo "Usage Examples:"
    echo "  ${CYAN}torq \"Interstellar\"${NC}      # Interactive search & download"
    echo "  ${CYAN}torq queue${NC}               # View downloads & active files"
    echo "  ${CYAN}torq -q highest \"Avatar\"${NC}  # Filter 4K / Remux"
    echo "  ${CYAN}torq --update${NC}            # Auto-update to latest release"
    echo ""
else
    echo "${RED}Installation failed. Please check permissions.${NC}"
    exit 1
fi
