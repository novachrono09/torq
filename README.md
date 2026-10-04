# ⚡ torq

> A high-performance, keyboard-driven multi-tracker media engine & downloader for your terminal.

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
[![Python: 3.8+](https://img.shields.io/badge/Python-3.8%2B-yellow.svg)]()
[![Engine: aria2](https://img.shields.io/badge/Engine-aria2-red.svg)]()

---

## ✨ Features

- 🚀 **Zero Python Dependencies** – Built entirely on Python 3 standard libraries (`urllib`, `argparse`, `termios`). No `pip install` headaches or bloated virtualenvs.
- 🎯 **Interactive TUI** – Full arrow-key navigation (`↑`/`↓`), real-time search filtering (`/`), and tracker/quality switching (`TAB`).
- 💎 **Intelligent Quality Sections** – Categorizes releases automatically:
  - 🟣 **4K / 2160p / UHD / Remux**
  - 🔵 **1080p FHD**
  - 🟢 **720p HD**
  - ⚪ **480p / Standard Media**
- ⚡ **Multi-Tracker Speed Injection** – Automatically embeds high-performance public tier-1 trackers directly into magnet links for maximum seed discovery and blistering speeds.
- 📊 **Visual Progress Dashboard** – Graphic block progress bar (`[████████░░░░]`), real-time speed calculation, ETA, peer/seed stats, and size indicators.
- 🎮 **In-Flight Controls**:
  - `p` – Pause / Resume download
  - `b` – Send download to background (non-blocking daemon)
  - `c` – Cancel download cleanly
- 💾 **Pre-Flight Disk Space Check** – Prevents partial or failed downloads by verifying free space before starting.
- 📂 **Queue & File Manager** – `torq queue` lists active downloads, finished files, and lets you open media directly with your default player.
- 🔄 **One-Command Updater** – `torq --update` updates the binary directly from GitHub.

---

## 🚀 Quick Install (1-Line)

Run this in your **Termux** or **Linux** terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/novachrono09/torq/main/install.sh | bash
```

### Manual Installation

```bash
# Clone the repository
git clone https://github.com/novachrono09/torq.git
cd torq

# Run installer or copy binary
chmod +x install.sh
./install.sh
```

---

## 🎮 Usage

### 1. Interactive Search & Download
```bash
torq "Interstellar"
```
Use `↑` / `↓` to navigate, `Enter` to download, `TAB` to switch quality tiers, or `/` to filter.

### 2. View Downloads & Completed Files
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

# Grab the top magnet link directly
torq -m "Ubuntu 24.04"

# Directly start downloading the top result
torq -d "Debian"
```

### 5. Self Update
```bash
torq --update
```

---

## ⌨️ Keyboard Controls

### Search & TUI View
| Key | Action |
| :--- | :--- |
| `↑` / `k` | Move cursor up |
| `↓` / `j` | Move cursor down |
| `TAB` | Switch quality tier filter (ALL → 4K → 1080p → 720p → 480p) |
| `s` | Switch tracker source (All → TPB → Nyaa) |
| `/` | Instant text filter on results |
| `Enter` | Select and start download |
| `m` | Copy magnet link to clipboard / print |
| `q` / `Ctrl+C` | Exit |

### Download Dashboard View
| Key | Action |
| :--- | :--- |
| `p` | **Pause** / **Resume** current download |
| `b` | Send download to **background** |
| `c` | **Cancel** download and clean temporary files |

---

## 📋 Requirements

- **Python**: `python3` (3.8 or newer)
- **Engine**: `aria2` (`aria2c`)
- **Supported OS**: Android (Termux), Linux (Ubuntu, Debian, Arch, Fedora), macOS.

---

## 📄 License

This project is licensed under the [MIT License](LICENSE).
