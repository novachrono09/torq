# ⚡ torq

> High-performance, keyboard-driven multi-tracker media engine & downloader for your terminal.
> **Written in Go — 100% Standalone Native Binary. Zero Python required.**

```
  _______ ____  _____   ____ 
 |__   __/ __ \|  __ \ / __ \
    | | | |  | | |__) | |  | |
    | | | |  | |  _  /| |  | |
    | | | |__| | | \ \| |__| |
    |_|  \____/|_|  \_\\___\_\
```

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Platform: Linux / Termux / macOS](https://img.shields.io/badge/Platform-Linux%20%7C%20Termux%20%7C%20macOS-green.svg)]()
[![Language: Go](https://img.shields.io/badge/Language-Go%201.23%2B-00ADD8.svg)]()
[![Engine: aria2](https://img.shields.io/badge/Engine-aria2-red.svg)]()

---

## ⚡ Why torq?

| Feature | Old Scripts / Python CLIs | ⚡ `torq` (Go) |
| :--- | :--- | :--- |
| **Dependencies** | Requires ~220MB Python runtime | **ZERO (0 MB) — Pure Native Executable** |
| **Startup Time** | ~120 ms | **~2 ms (Instant)** |
| **Scraping Concurrency** | Sequential / Thread GIL | **Parallel Goroutines (Scrapes 5+ trackers simultaneously)** |
| **Installation** | Virtualenv / pip errors (PEP 668) | **1 single file dropped directly to `$PREFIX/bin`** |

---

## ✨ Features

- 🚀 **Zero Python / Zero Pip** – Standalone compiled executable. No virtualenvs, no wheel compilation, no Python dependencies.
- 🎯 **Interactive Terminal UI** – Smooth arrow-key navigation (`↑`/`↓`), real-time search filtering (`/`), and quality tier toggling (`TAB`).
- 💎 **Intelligent Quality Classification**:
  - 🟣 **4K / 2160p / UHD / Remux**
  - 🔵 **1080p FHD**
  - 🟢 **720p HD**
  - ⚪ **480p / Standard Media**
- ⚡ **Multi-Tracker Speed Injection** – Automatically injects Tier-1 public UDP trackers into every magnet link for maximum peer discovery and saturation.
- 📊 **Visual Progress Dashboard** – Graphic block progress bar (`[████████░░░░]`), real-time download speed, ETA, seeds/peers, and file size.
- 🎮 **In-Flight Controls**:
  - `p` or `Space` – Pause / Resume download
  - `b` – Send download to background (non-blocking daemon)
  - `c` – Cancel download cleanly
- 💾 **Pre-Flight Disk Space Check** – Verifies available storage on your device before downloading begins.
- 📂 **Queue & File Manager** – `torq queue` lists downloaded media and lets you launch files directly in your preferred player (VLC, MPV, etc.).
- 🔄 **One-Command Auto-Updater** – `torq -u` updates the binary directly from GitHub Releases.

---

## 🚀 Quick Install (1-Line)

Run this in your **Termux**, **Linux**, or **macOS** terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/novachrono09/torq/main/install.sh | bash
```

### Manual Installation (From Source)

```bash
# Clone the repository
git clone https://github.com/novachrono09/torq.git
cd torq

# Build native binary
go build -ldflags="-s -w" -o torq main.go
chmod +x torq

# Move to your bin directory
mv torq $PREFIX/bin/   # On Termux
# or: sudo mv torq /usr/local/bin/   # On Linux / macOS
```

---

## 🎮 Usage

### 1. Interactive Search & Download
```bash
torq "Interstellar"
```
Use `↑` / `↓` to navigate, `Enter` to download, `TAB` to switch quality tiers, or `/` to filter.

### 2. View Completed Files & Downloads
```bash
torq queue
```

### 3. Quick Quality Filters
```bash
# Filter only 4K / Remux releases
torq -q highest "Oppenheimer"

# Filter 1080p FHD releases
torq -q high "Cyberpunk Edgerunners"
```

### 4. Non-Interactive / Scripting
```bash
# List search results formatted by quality
torq -l "Doraemon"

# Grab the top magnet link directly (pure output, pipeline friendly)
torq -m "Ubuntu 24.04"

# Directly start downloading the top result
torq -d "Debian"
```

### 5. Self Update
```bash
torq -u
```

---

## ⌨️ Keyboard Controls

### Search & TUI View
| Key | Action |
| :--- | :--- |
| `↑` / `k` | Move cursor up |
| `↓` / `j` | Move cursor down |
| `TAB` / `t` | Switch quality tier filter (ALL → 4K → 1080p → 720p → 480p) |
| `s` | Switch tracker source (All → TPB → Nyaa) |
| `/` | Instant text filter on results |
| `Enter` / `d` | Select and start download |
| `m` | Copy magnet link to clipboard |
| `Q` | Open Queue Manager |
| `q` / `Ctrl+C` | Exit |

### Download Dashboard View
| Key | Action |
| :--- | :--- |
| `p` / `Space` | **Pause** / **Resume** current download |
| `b` | Send download to **background** |
| `c` | **Cancel** download |
| `o` | **Open** finished file in media player |

---

## 📄 License

This project is licensed under the [MIT License](LICENSE).
