package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"html"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func init() {
	// Android/Termux does not provide /etc/resolv.conf.
	// Providing a fallback DNS resolver ensures cross-compiled Go binaries
	// can resolve domain names out-of-the-box on any Android device.
	if _, err := os.Stat("/etc/resolv.conf"); os.IsNotExist(err) {
		net.DefaultResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				for _, dns := range []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"} {
					if conn, err := d.DialContext(ctx, "udp", dns); err == nil {
						return conn, nil
					}
				}
				return d.DialContext(ctx, network, address)
			},
		}
	}
}

const (
	Version   = "1.1.0"
	RPCPort   = 6800
	RPCSecret = "torq_secret_session"
)

// ANSI Styling
const (
	Bold      = "\033[1m"
	Green     = "\033[32m"
	Cyan      = "\033[36m"
	Yellow    = "\033[33m"
	Red       = "\033[31m"
	Magenta   = "\033[35m"
	Blue      = "\033[34m"
	White     = "\033[37m"
	Dim       = "\033[2m"
	Reset     = "\033[0m"
	ClearLine = "\033[K"
	ClearScrn = "\033[2J\033[H"
	MoveTop   = "\033[H"
	HlBg      = "\033[44;97;1m"
	HlArrow   = "\033[1m\033[36m▶\033[0m"
)

var topTrackers = []string{
	"http://tracker.opentrackr.org:1337/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://open.demonii.com:1337/announce",
	"http://tracker.qu.ax:6969/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://explodie.org:6969/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
	"udp://tracker.bittor.pw:1337/announce",
	"udp://public.popcorn-tracker.org:6969/announce",
}

var qualityOrder = []string{
	"Highest Quality (4K / UHD / Remux)",
	"High Quality (1080p / FHD / BD)",
	"Medium Quality (720p / HD)",
	"Low Quality (480p / SD / CAM)",
	"Standard / General Media",
}

var qualityShort = map[string]string{
	"Highest Quality (4K / UHD / Remux)": "4K / Remux",
	"High Quality (1080p / FHD / BD)":   "1080p FHD",
	"Medium Quality (720p / HD)":        "720p HD",
	"Low Quality (480p / SD / CAM)":     "480p SD",
	"Standard / General Media":          "General",
}

var qualityColors = map[string]string{
	"Highest Quality (4K / UHD / Remux)": "\033[1;35m",
	"High Quality (1080p / FHD / BD)":   "\033[1;32m",
	"Medium Quality (720p / HD)":        "\033[1;33m",
	"Low Quality (480p / SD / CAM)":     "\033[1;31m",
	"Standard / General Media":          "\033[1;36m",
}

type TorrentItem struct {
	Title    string `json:"title"`
	Source   string `json:"source"`
	Seeders  int    `json:"seeders"`
	Leechers int    `json:"leechers"`
	Size     string `json:"size"`
	RawSize  int64  `json:"raw_size"`
	Category string `json:"category"`
	Tier     string `json:"tier"`
	Magnet   string `json:"magnet"`
}

func formatBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func formatSpeed(bps int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
	)
	switch {
	case bps >= mb:
		return fmt.Sprintf("%.2f MiB/s", float64(bps)/float64(mb))
	case bps >= kb:
		return fmt.Sprintf("%.1f KiB/s", float64(bps)/float64(kb))
	default:
		return fmt.Sprintf("%d B/s", bps)
	}
}

func formatTime(seconds int64) string {
	if seconds <= 0 || seconds > 86400*30 {
		return "--:--"
	}
	m := seconds / 60
	s := seconds % 60
	h := m / 60
	m = m % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%02dm %02ds", m, s)
}

var (
	reLow    = regexp.MustCompile(`(?i)\b(480p|360p|240p|dvdrip|camrip|hdcam|telesync|telecine|vcd|cam|ts|tc)\b`)
	reHigh4k = regexp.MustCompile(`(?i)\b(2160p|4k|uhd|remux|bdremux)\b`)
	reHigh1k = regexp.MustCompile(`(?i)\b(1080p|fhd|1080i)\b`)
	reMed720 = regexp.MustCompile(`(?i)\b(720p|hdrip|hdtv|720i)\b`)
	reBDSub  = regexp.MustCompile(`(?i)\b(720p|480p)\b`)
)

func detectQualityTier(title string) string {
	t := strings.ToLower(title)
	if reLow.MatchString(t) {
		return "Low Quality (480p / SD / CAM)"
	}
	if reHigh4k.MatchString(t) {
		return "Highest Quality (4K / UHD / Remux)"
	}
	if reHigh1k.MatchString(t) {
		return "High Quality (1080p / FHD / BD)"
	}
	if (strings.Contains(t, "bluray") || strings.Contains(t, "bdrip") || strings.Contains(t, "blu-ray")) && !reBDSub.MatchString(t) {
		return "High Quality (1080p / FHD / BD)"
	}
	if reMed720.MatchString(t) {
		return "Medium Quality (720p / HD)"
	}
	return "Standard / General Media"
}

func injectTrackers(magnetURI, title string) string {
	if !strings.HasPrefix(magnetURI, "magnet:?") {
		return magnetURI
	}
	var trParts []string
	for _, tr := range topTrackers {
		trParts = append(trParts, "tr="+url.QueryEscape(tr))
	}
	trParams := strings.Join(trParts, "&")
	dn := ""
	if title != "" && !strings.Contains(magnetURI, "&dn=") {
		dn = "&dn=" + url.QueryEscape(title)
	}
	return magnetURI + dn + "&" + trParams
}

func searchTPB(ctx context.Context, query string) []TorrentItem {
	endpoint := "https://apibay.org/q.php?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) Chrome/144.0.0.0 Mobile Safari/537.36")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var rawList []map[string]interface{}
	if err := json.Unmarshal(body, &rawList); err != nil {
		return nil
	}

	var results []TorrentItem
	for _, item := range rawList {
		name, _ := item["name"].(string)
		if name == "" || name == "No results returned" {
			continue
		}
		name = html.UnescapeString(name)
		infoHash, _ := item["info_hash"].(string)
		catID, _ := item["category"].(string)

		var rawSize int64
		switch v := item["size"].(type) {
		case string:
			rawSize, _ = strconv.ParseInt(v, 10, 64)
		case float64:
			rawSize = int64(v)
		}

		var seeders int
		switch v := item["seeders"].(type) {
		case string:
			s, _ := strconv.Atoi(v)
			seeders = s
		case float64:
			seeders = int(v)
		}

		var leechers int
		switch v := item["leechers"].(type) {
		case string:
			l, _ := strconv.Atoi(v)
			leechers = l
		case float64:
			leechers = int(v)
		}

		tier := detectQualityTier(name)
		baseMagnet := fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", infoHash, url.QueryEscape(name))
		magnet := injectTrackers(baseMagnet, name)

		results = append(results, TorrentItem{
			Title:    name,
			Source:   "ThePirateBay",
			Seeders:  seeders,
			Leechers: leechers,
			Size:     formatBytes(rawSize),
			RawSize:  rawSize,
			Category: fmt.Sprintf("TPB (%s)", catID),
			Tier:     tier,
			Magnet:   magnet,
		})
	}
	return results
}

type NyaaRSS struct {
	Channel struct {
		Items []struct {
			Title    string `xml:"title"`
			Seeders  string `xml:"seeders"`
			Leechers string `xml:"leechers"`
			InfoHash string `xml:"infoHash"`
			Size     string `xml:"size"`
			Category string `xml:"category"`
		} `xml:"item"`
	} `xml:"channel"`
}

func searchNyaa(ctx context.Context, query string) []TorrentItem {
	endpoint := fmt.Sprintf("https://nyaa.si/?page=rss&q=%s&s=seeders&o=desc", url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) Chrome/144.0.0.0 Mobile Safari/537.36")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var rss NyaaRSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil
	}

	var results []TorrentItem
	for _, it := range rss.Channel.Items {
		title := html.UnescapeString(it.Title)
		if title == "" {
			title = "Untitled"
		}
		sVal, _ := strconv.Atoi(it.Seeders)
		lVal, _ := strconv.Atoi(it.Leechers)
		sizeStr := it.Size
		if sizeStr == "" {
			sizeStr = "N/A"
		}
		catStr := it.Category
		if catStr == "" {
			catStr = "Anime"
		}
		tier := detectQualityTier(title)

		var magnet string
		if it.InfoHash != "" {
			baseMagnet := fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", it.InfoHash, url.QueryEscape(title))
			magnet = injectTrackers(baseMagnet, title)
		}

		results = append(results, TorrentItem{
			Title:    title,
			Source:   "Nyaa",
			Seeders:  sVal,
			Leechers: lVal,
			Size:     sizeStr,
			RawSize:  0,
			Category: catStr,
			Tier:     tier,
			Magnet:   magnet,
		})
	}
	return results
}

func getDownloadsDir() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/sdcard/Download",
		filepath.Join(home, "storage/downloads"),
		filepath.Join(home, "Downloads"),
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			if unix.Access(p, unix.W_OK) == nil {
				return p
			}
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

func checkDiskSpace(destDir string, requiredBytes int64) (bool, int64) {
	var stat unix.Statfs_t
	if err := unix.Statfs(destDir, &stat); err != nil {
		return true, 0
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if requiredBytes > 0 && free < requiredBytes {
		return false, free
	}
	return true, free
}

func copyToClipboard(text string) bool {
	if _, err := exec.LookPath("termux-clipboard-set"); err == nil {
		cmd := exec.Command("termux-clipboard-set")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run() == nil
	}
	if _, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.Command("wl-copy")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run() == nil
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run() == nil
	}
	if _, err := exec.LookPath("pbcopy"); err == nil {
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run() == nil
	}
	return false
}

func sendNotification(title, message string) {
	if _, err := exec.LookPath("termux-notification"); err == nil {
		_ = exec.Command("termux-notification", "--title", title, "--content", message, "--priority", "high").Run()
		return
	}
	if _, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command("notify-send", title, message).Run()
		return
	}
}

func openPath(target string) {
	if _, err := exec.LookPath("termux-open"); err == nil {
		_ = exec.Command("termux-open", target).Start()
		return
	}
	if _, err := exec.LookPath("xdg-open"); err == nil {
		_ = exec.Command("xdg-open", target).Start()
		return
	}
	if _, err := exec.LookPath("open"); err == nil {
		_ = exec.Command("open", target).Start()
		return
	}
}

// Aria2 RPC Client
type AriaTask struct {
	GID             string   `json:"gid"`
	Status          string   `json:"status"`
	TotalLength     string   `json:"totalLength"`
	CompletedLength string   `json:"completedLength"`
	DownloadSpeed   string   `json:"downloadSpeed"`
	NumSeeders      string   `json:"numSeeders"`
	Connections     string   `json:"connections"`
	ErrorMessage    string   `json:"errorMessage"`
	ErrorCode       string   `json:"errorCode"`
	FollowedBy      []string `json:"followedBy"`
	Following       string   `json:"following"`
	Bittorrent      *struct {
		Info *struct {
			Name string `json:"name"`
		} `json:"info"`
	} `json:"bittorrent"`
	Files []struct {
		Path   string `json:"path"`
		Length string `json:"length"`
	} `json:"files"`
}

func ariaRPC(method string, params []interface{}) (json.RawMessage, error) {
	allParams := append([]interface{}{"token:" + RPCSecret}, params...)
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "torq",
		"method":  method,
		"params":  allParams,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", RPCPort), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("%s", rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func ensureAriaDaemon(destDir string) {
	if _, err := exec.LookPath("aria2c"); err != nil {
		fmt.Printf("\r\n%s[!] 'aria2c' is not installed.%s\r\nPlease install it using: pkg install aria2 (or sudo apt install aria2)\r\n", Red, Reset)
		return
	}

	trackersArg := strings.Join(topTrackers, ",")

	// 1. Check if an aria2 daemon is already running and has DHT enabled
	if raw, err := ariaRPC("aria2.getGlobalOption", nil); err == nil {
		var opts map[string]string
		if json.Unmarshal(raw, &opts) == nil {
			if opts["enable-dht"] == "true" {
				// Daemon is alive and healthy, dynamically update directory and trackers
				_, _ = ariaRPC("aria2.changeGlobalOption", []interface{}{
					map[string]string{
						"dir":        destDir,
						"bt-tracker": trackersArg,
					},
				})
				return
			}
		}
	}

	// 2. Kill stale or flag-less aria2c daemon
	_ = exec.Command("pkill", "-9", "aria2c").Run()
	time.Sleep(200 * time.Millisecond)

	// 3. Launch aria2c with full BitTorrent, DHT, PEX, and network optimizations
	cmd := exec.Command("aria2c",
		"--enable-rpc=true",
		fmt.Sprintf("--rpc-listen-port=%d", RPCPort),
		fmt.Sprintf("--rpc-secret=%s", RPCSecret),
		fmt.Sprintf("--dir=%s", destDir),
		"--seed-time=0",
		"--file-allocation=none",
		"--bt-max-peers=120",
		"--bt-request-peer-speed-limit=0",
		"--max-connection-per-server=16",
		"--split=16",
		"--min-split-size=1M",
		"--piece-length=1M",
		"--enable-dht=true",
		"--enable-dht6=true",
		"--dht-listen-port=6881-6999",
		"--listen-port=6881-6999",
		"--dht-entry-point=dht.transmissionbt.com:6881",
		"--bt-enable-lpd=true",
		"--enable-peer-exchange=true",
		"--follow-torrent=mem",
		fmt.Sprintf("--bt-tracker=%s", trackersArg),
		"--async-dns=false",
		"--summary-interval=0",
		"--quiet=true",
		"--daemon=true",
	)
	_ = cmd.Run()
	time.Sleep(400 * time.Millisecond)
}

func addMagnet(magnet, destDir string) (string, error) {
	opts := map[string]string{
		"dir":       destDir,
		"seed-time": "0",
	}
	raw, err := ariaRPC("aria2.addUri", []interface{}{[]string{magnet}, opts})
	if err != nil {
		return "", err
	}
	var gid string
	_ = json.Unmarshal(raw, &gid)
	return gid, nil
}

func getTask(gid string) *AriaTask {
	raw, err := ariaRPC("aria2.tellStatus", []interface{}{gid})
	if err != nil {
		return nil
	}
	var t AriaTask
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil
	}
	return &t
}

func pauseDownload(gid string) {
	_, _ = ariaRPC("aria2.pause", []interface{}{gid})
}

func unpauseDownload(gid string) {
	_, _ = ariaRPC("aria2.unpause", []interface{}{gid})
}

func removeDownload(gid string) {
	_, _ = ariaRPC("aria2.remove", []interface{}{gid})
}

func getActiveTasks() []AriaTask {
	var results []AriaTask
	parseTasks := func(raw json.RawMessage) []AriaTask {
		var list []AriaTask
		_ = json.Unmarshal(raw, &list)
		return list
	}
	if raw, err := ariaRPC("aria2.tellActive", nil); err == nil {
		results = append(results, parseTasks(raw)...)
	}
	if raw, err := ariaRPC("aria2.tellWaiting", []interface{}{0, 10}); err == nil {
		results = append(results, parseTasks(raw)...)
	}
	return results
}

func hasKeyInput(stdinFd int, timeout time.Duration) bool {
	var readFds unix.FdSet
	readFds.Set(stdinFd)
	tv := unix.NsecToTimeval(timeout.Nanoseconds())
	n, err := unix.Select(stdinFd+1, &readFds, nil, nil, &tv)
	return err == nil && n > 0 && readFds.IsSet(stdinFd)
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > maxRunes {
		return string(r[:maxRunes])
	}
	return s
}

func readKey(stdinFd int) string {
	buf := make([]byte, 1)
	n, err := os.Stdin.Read(buf)
	if err != nil || n == 0 {
		return "QUIT"
	}
	b := buf[0]
	if b == 0x1b { // ESC or escape sequence
		if hasKeyInput(stdinFd, 50*time.Millisecond) {
			seq := make([]byte, 16)
			nSeq, err := os.Stdin.Read(seq)
			if err == nil && nSeq > 0 {
				s := string(seq[:nSeq])
				if strings.HasPrefix(s, "[A") || strings.HasPrefix(s, "OA") {
					return "UP"
				}
				if strings.HasPrefix(s, "[B") || strings.HasPrefix(s, "OB") {
					return "DOWN"
				}
				if strings.HasPrefix(s, "[C") || strings.HasPrefix(s, "OC") {
					return "RIGHT"
				}
				if strings.HasPrefix(s, "[D") || strings.HasPrefix(s, "OD") {
					return "LEFT"
				}
				if strings.HasPrefix(s, "[5~") {
					return "PAGE_UP"
				}
				if strings.HasPrefix(s, "[6~") {
					return "PAGE_DOWN"
				}
				if strings.HasPrefix(s, "[H") || strings.HasPrefix(s, "[1~") {
					return "HOME"
				}
				if strings.HasPrefix(s, "[F") || strings.HasPrefix(s, "[4~") {
					return "END"
				}
			}
		}
		return "ESC"
	}
	switch b {
	case '\r', '\n':
		return "ENTER"
	case '\t':
		return "TAB"
	case 0x03, 0x04:
		return "QUIT"
	case ' ':
		return "SPACE"
	case 0x7f, 0x08:
		return "BACKSPACE"
	default:
		return string(b)
	}
}

func downloadDashboard(item TorrentItem, destDir string) {
	ensureAriaDaemon(destDir)

	fd := int(os.Stdin.Fd())
	fmt.Print("\033[?1049h\033[?25l" + ClearScrn)
	defer fmt.Print("\033[?25h\033[?1049l\r\n")

	activeGID, err := addMagnet(item.Magnet, destDir)
	if err != nil {
		fmt.Printf("\r\n%s%s✖ Failed to start download: %v%s\r\n", Bold, Red, err, Reset)
		fmt.Printf("\r\n%sPress any key to return to search...%s", Dim, Reset)
		_ = readKey(fd)
		return
	}

	statusMsg := ""
	isPaused := false

	for {
		cols, _, err := term.GetSize(fd)
		if err != nil || cols <= 0 {
			cols = 80
		}

		task := getTask(activeGID)
		// Auto-transition when metadata fetch finishes and spawns the content download
		if task != nil && len(task.FollowedBy) > 0 {
			activeGID = task.FollowedBy[0]
			task = getTask(activeGID)
		}

		statusText := "connecting"
		var total, completed, speed int64
		var seeders, conns int

		if task != nil {
			statusText = task.Status
			total, _ = strconv.ParseInt(task.TotalLength, 10, 64)
			completed, _ = strconv.ParseInt(task.CompletedLength, 10, 64)
			speed, _ = strconv.ParseInt(task.DownloadSpeed, 10, 64)
			seeders, _ = strconv.Atoi(task.NumSeeders)
			conns, _ = strconv.Atoi(task.Connections)

			// If paused unexpectedly, unpause automatically
			if statusText == "paused" && !isPaused {
				unpauseDownload(activeGID)
			}
		}

		fileName := item.Title
		var filePath string
		isMetadataPhase := true

		if task != nil {
			if task.Bittorrent != nil && task.Bittorrent.Info != nil && task.Bittorrent.Info.Name != "" {
				fileName = task.Bittorrent.Info.Name
			}
			if len(task.Files) > 0 {
				p := task.Files[0].Path
				if p != "" && !strings.HasPrefix(p, "[METADATA]") && !strings.HasPrefix(p, "[MEMORY]") {
					isMetadataPhase = false
					filePath = p
					if fileName == item.Title {
						fileName = filepath.Base(p)
					}
				}
			}
		}

		pct := 0.0
		if total > 0 && !isMetadataPhase {
			pct = (float64(completed) / float64(total)) * 100.0
		}
		var etaSec int64
		if speed > 0 && total > completed {
			etaSec = (total - completed) / speed
		}

		var frame []string
		divLen := cols - 2
		if divLen > 78 {
			divLen = 78
		}
		if divLen < 1 {
			divLen = 1
		}

		srcLabel := item.Source
		if srcLabel == "ThePirateBay" {
			srcLabel = "TPB"
		}
		hdr := fmt.Sprintf("%s%s⚡ TORQ DOWNLOAD MANAGER%s %s───%s %s%s[%s]%s", Bold, Cyan, Reset, Dim, Reset, Bold, Magenta, srcLabel, Reset)
		frame = append(frame, "\r"+hdr+ClearLine)
		frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)

		fnDisp := truncateRunes(fileName, cols-8)
		frame = append(frame, "\r"+fmt.Sprintf("%sFile:%s %s%s%s", Bold, Reset, White, fnDisp, Reset)+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf("%sDest:%s %s%s%s", Bold, Reset, Dim, truncateRunes(destDir, cols-8), Reset)+ClearLine)

		badge := ""
		switch {
		case isMetadataPhase:
			badge = fmt.Sprintf("%s◐ CONNECTING & FETCHING METADATA...%s", Magenta, Reset)
		case statusText == "active":
			badge = fmt.Sprintf("%s● DOWNLOADING%s", Green, Reset)
		case statusText == "paused":
			badge = fmt.Sprintf("%s❚❚ PAUSED%s", Yellow, Reset)
		case statusText == "complete":
			badge = fmt.Sprintf("%s%s✔ COMPLETED%s", Bold, Cyan, Reset)
		case statusText == "removed":
			badge = fmt.Sprintf("%s✖ CANCELLED%s", Red, Reset)
		case statusText == "error":
			badge = fmt.Sprintf("%s✖ ERROR%s", Red, Reset)
		default:
			badge = fmt.Sprintf("%s● %s%s", Yellow, strings.ToUpper(statusText), Reset)
		}

		frame = append(frame, "\r"+fmt.Sprintf("%sStatus:%s %s   %sPeers:%s %d (%d seeds)", Bold, Reset, badge, Bold, Reset, conns, seeders)+ClearLine)

		barWidth := cols - 18
		if barWidth < 12 {
			barWidth = 12
		}
		if barWidth > 36 {
			barWidth = 36
		}

		filled := int(float64(barWidth) * pct / 100.0)
		if filled > barWidth {
			filled = barWidth
		}
		empty := barWidth - filled
		if empty < 0 {
			empty = 0
		}

		fillStr := fmt.Sprintf("%s%s%s%s", Bold, Cyan, strings.Repeat("█", filled), Reset)
		emptyStr := fmt.Sprintf("%s%s%s", Dim, strings.Repeat("░", empty), Reset)
		barDisplay := fmt.Sprintf("[%s%s] %s%s%5.1f%%%s", fillStr, emptyStr, Bold, White, pct, Reset)
		frame = append(frame, "\r"+barDisplay+ClearLine)

		if isMetadataPhase {
			frame = append(frame, "\r"+fmt.Sprintf("%sFinding swarm seeds & resolving pieces...%s", Dim, Reset)+ClearLine)
		} else {
			metrics := fmt.Sprintf("%sSpeed:%s %s%-9s%s %sData:%s %s/%s  %sETA:%s %s%s%s",
				Bold, Reset, Green, formatSpeed(speed), Reset,
				Bold, Reset, formatBytes(completed), formatBytes(total),
				Bold, Reset, Yellow, formatTime(etaSec), Reset)
			frame = append(frame, "\r"+metrics+ClearLine)
		}

		if statusMsg != "" {
			frame = append(frame, "\r"+fmt.Sprintf("%sℹ %s%s", Cyan, statusMsg, Reset)+ClearLine)
			statusMsg = ""
		} else {
			frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		}

		var controls string
		if statusText == "complete" && !isMetadataPhase {
			controls = fmt.Sprintf("%s[Enter/q]%s Return   %s[o]%s Open in Player", Bold, Reset, Bold, Reset)
		} else if statusText == "removed" || statusText == "error" {
			controls = fmt.Sprintf("%s[Enter/q]%s Return to Search", Bold, Reset)
		} else {
			pLabel := "Pause"
			if isPaused {
				pLabel = "Resume"
			}
			controls = fmt.Sprintf("%s[p]%s %s  %s[b]%s Background  %s[c]%s Cancel  %s[q]%s Return", Bold, Reset, pLabel, Bold, Reset, Bold, Reset, Bold, Reset)
		}
		frame = append(frame, "\r"+controls+ClearLine)

		fmt.Print(MoveTop + strings.Join(frame, "\r\n") + "\r")

		if statusText == "complete" && !isMetadataPhase {
			sendNotification("Torq Download Complete!", fileName+" finished downloading.")
			k := readKey(fd)
			if strings.ToLower(k) == "o" && filePath != "" {
				openPath(filePath)
			}
			return
		}

		if statusText == "removed" || statusText == "error" {
			_ = readKey(fd)
			return
		}

		if hasKeyInput(fd, 400*time.Millisecond) {
			k := readKey(fd)
			switch strings.ToLower(k) {
			case "p", "space":
				if activeGID != "" {
					if isPaused {
						unpauseDownload(activeGID)
						isPaused = false
						statusMsg = "Download Resumed"
					} else {
						pauseDownload(activeGID)
						isPaused = true
						statusMsg = "Download Paused"
					}
				}
			case "c", "x":
				if activeGID != "" {
					removeDownload(activeGID)
					statusMsg = "Download Cancelled"
				}
			case "b", "q", "quit", "esc":
				return
			}
		}
	}
}

func queueManager(destDir string) {
	ensureAriaDaemon(destDir)
	fmt.Print(ClearScrn)

	activeTasks := getActiveTasks()

	type fileEntry struct {
		name  string
		size  int64
		mtime time.Time
	}

	validExts := map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".webm": true,
		".zip": true, ".apk": true, ".iso": true, ".torrent": true,
	}

	var files []fileEntry
	if entries, err := os.ReadDir(destDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if validExts[ext] && !strings.HasSuffix(e.Name(), ".aria2") {
				if fi, err := e.Info(); err == nil {
					files = append(files, fileEntry{
						name:  e.Name(),
						size:  fi.Size(),
						mtime: fi.ModTime(),
					})
				}
			}
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.After(files[j].mtime)
	})

	fmt.Printf("%s%s📋 TORQ DOWNLOAD QUEUE & MEDIA REPOSITORY%s\n", Bold, Cyan, Reset)
	fmt.Printf("Location: %s%s%s\n", Yellow, destDir, Reset)
	fmt.Printf("%s%s%s\n\n", Dim, strings.Repeat("━", 65), Reset)

	if len(activeTasks) > 0 {
		fmt.Printf("%s%s⚡ ACTIVE DOWNLOADS%s\n", Bold, Green, Reset)
		for idx, t := range activeTasks {
			tName := "BitTorrent Task"
			if t.Bittorrent != nil && t.Bittorrent.Info != nil && t.Bittorrent.Info.Name != "" {
				tName = t.Bittorrent.Info.Name
			} else if len(t.Files) > 0 && t.Files[0].Path != "" {
				tName = filepath.Base(t.Files[0].Path)
			}
			if len(tName) > 42 {
				tName = tName[:42]
			}
			tot, _ := strconv.ParseInt(t.TotalLength, 10, 64)
			done, _ := strconv.ParseInt(t.CompletedLength, 10, 64)
			spd, _ := strconv.ParseInt(t.DownloadSpeed, 10, 64)
			pct := 0.0
			if tot > 0 {
				pct = (float64(done) / float64(tot)) * 100.0
			}
			fmt.Printf(" [A%d] %s%-42s%s  %s%s%s  %s%4.1f%%%s  %s%s%s\n",
				idx+1, White, tName, Reset,
				Green, formatSpeed(spd), Reset,
				Cyan, pct, Reset,
				Yellow, t.Status, Reset)
		}
		fmt.Printf("\n")
	}

	fmt.Printf("%s%s📁 COMPLETED MEDIA (%s)%s\n", Bold, Yellow, destDir, Reset)
	if len(files) == 0 {
		fmt.Printf(" %sNo downloaded media files found in destination folder.%s\n\n", Dim, Reset)
	} else {
		limit := len(files)
		if limit > 12 {
			limit = 12
		}
		for idx := 0; idx < limit; idx++ {
			f := files[idx]
			dispName := f.name
			if len(dispName) > 48 {
				dispName = dispName[:48]
			}
			fmt.Printf(" %s%s[%2d]%s %s%-48s%s %s%s%s\n", Bold, Cyan, idx+1, Reset, White, dispName, Reset, Yellow, formatBytes(f.size), Reset)
		}
		fmt.Println()
	}

	fmt.Printf("%s%s%s\n", Dim, strings.Repeat("━", 65), Reset)
	maxNum := len(files)
	if maxNum > 12 {
		maxNum = 12
	}
	fmt.Printf("%s[1-%d]%s Open File  %s[o]%s Open Folder  %s[q]%s Return\n\n", Bold, maxNum, Reset, Bold, Reset, Bold, Reset)

	fmt.Printf("%sAction > %s", Bold, Reset)
	reader := bufio.NewReader(os.Stdin)
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(strings.ToLower(choice))

	if choice == "o" {
		openPath(destDir)
	} else if num, err := strconv.Atoi(choice); err == nil && num >= 1 && num <= len(files) {
		chosenFile := filepath.Join(destDir, files[num-1].name)
		openPath(chosenFile)
	}
}

func fetchBinary(targetURL string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) torq-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, targetURL)
	}
	return io.ReadAll(resp.Body)
}

func selfUpdate() {
	fmt.Printf("%sChecking for updates from GitHub...%s\n", Cyan, Reset)
	targetPath, err := os.Executable()
	if err != nil {
		targetPath, err = exec.LookPath("torq")
		if err != nil {
			targetPath = "/data/data/com.termux/files/usr/bin/torq"
		}
	}

	arch := runtime.GOARCH
	goos := runtime.GOOS
	if goos == "android" {
		goos = "linux"
	}

	downloadURL := fmt.Sprintf("https://github.com/novachrono09/torq/releases/latest/download/torq-%s-%s", goos, arch)
	newCode, err := fetchBinary(downloadURL)
	if err != nil {
		downloadURL = fmt.Sprintf("https://github.com/novachrono09/torq/releases/download/v%s/torq-%s-%s", Version, goos, arch)
		newCode, err = fetchBinary(downloadURL)
	}
	if err != nil || len(newCode) == 0 {
		fmt.Printf("%sUpdate failed: %v%s\n", Red, err, Reset)
		return
	}

	tmpFile := targetPath + ".tmp"
	if err := os.WriteFile(tmpFile, newCode, 0755); err != nil {
		fmt.Printf("%sUpdate failed writing temp file: %v%s\n", Red, err, Reset)
		return
	}
	if err := os.Rename(tmpFile, targetPath); err != nil {
		fmt.Printf("%sUpdate failed replacing binary: %v%s\n", Red, err, Reset)
		return
	}

	fmt.Printf("%s✔ Successfully updated torq to the latest version!%s\n", Green, Reset)
}

func runTUI(allItems []TorrentItem, initialQuery string) {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		defer term.Restore(fd, oldState)
	}
	fmt.Print("\033[?1049h\033[?25l" + ClearScrn)
	defer fmt.Print("\033[?25h\033[?1049l\r\n")

	tierFilters := append([]string{"ALL"}, qualityOrder...)
	currentTierIdx := 0
	sourceFilters := []string{"ALL", "ThePirateBay", "Nyaa"}
	currentSourceIdx := 0
	searchFilter := ""

	selectedIdx := 0
	statusMsg := ""
	destDir := getDownloadsDir()

	for {
		cols, lines, err := term.GetSize(fd)
		if err != nil || cols <= 0 {
			cols, lines = 80, 24
		}

		var filtered []TorrentItem
		targetTier := tierFilters[currentTierIdx]
		targetSource := sourceFilters[currentSourceIdx]

		for _, item := range allItems {
			if targetTier != "ALL" && item.Tier != targetTier {
				continue
			}
			if targetSource != "ALL" && item.Source != targetSource {
				continue
			}
			if searchFilter != "" && !strings.Contains(strings.ToLower(item.Title), strings.ToLower(searchFilter)) {
				continue
			}
			filtered = append(filtered, item)
		}

		totalItems := len(filtered)
		if totalItems == 0 {
			selectedIdx = 0
		} else {
			if selectedIdx >= totalItems {
				selectedIdx = totalItems - 1
			}
			if selectedIdx < 0 {
				selectedIdx = 0
			}
		}

		divLen := cols - 2
		if divLen > 78 {
			divLen = 78
		}
		if divLen < 1 {
			divLen = 1
		}

		fixedLines := 4
		if searchFilter != "" {
			fixedLines++
		}
		availLines := lines - fixedLines - 2
		if availLines < 4 {
			availLines = 4
		}
		itemsPerPage := availLines / 2
		if itemsPerPage < 2 {
			itemsPerPage = 2
		}

		totalPages := int(math.Ceil(float64(totalItems) / float64(itemsPerPage)))
		if totalPages < 1 {
			totalPages = 1
		}
		currentPage := (selectedIdx / itemsPerPage) + 1
		if totalItems == 0 {
			currentPage = 1
		}

		startIdx := (currentPage - 1) * itemsPerPage
		endIdx := startIdx + itemsPerPage
		if endIdx > totalItems {
			endIdx = totalItems
		}

		var frame []string

		tierLabel := targetTier
		if s, ok := qualityShort[targetTier]; ok {
			tierLabel = s
		}
		qDisp := initialQuery
		if len(qDisp) > 16 {
			qDisp = qDisp[:16]
		}
		srcLabel := targetSource
		if srcLabel == "ThePirateBay" {
			srcLabel = "TPB"
		}
		hdr := fmt.Sprintf("%s%s⚡ TORQ%s | %s%s%s%s | %s%s%s | %s%s%s | Pg: %s%d/%d%s",
			Bold, Cyan, Reset,
			Bold, Yellow, qDisp, Reset,
			Magenta, tierLabel, Reset,
			Blue, srcLabel, Reset,
			Bold, currentPage, totalPages, Reset)
		frame = append(frame, "\r"+hdr+ClearLine)
		frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)

		if searchFilter != "" {
			frame = append(frame, "\r"+fmt.Sprintf("%sFilter (/): %s%s", Yellow, searchFilter, Reset)+ClearLine)
		}

		if totalItems == 0 {
			frame = append(frame, "\r"+fmt.Sprintf("   %sNo torrents found matching active filters.%s", Yellow, Reset)+ClearLine)
			frame = append(frame, "\r"+fmt.Sprintf("   %sPress 't' to change tier or 's' to change source.%s", Dim, Reset)+ClearLine)
		} else {
			for idx := startIdx; idx < endIdx; idx++ {
				it := filtered[idx]
				isActive := (idx == selectedIdx)

				badge := ""
				switch it.Tier {
				case "Highest Quality (4K / UHD / Remux)":
					badge = Magenta + "[4K]" + Reset
				case "High Quality (1080p / FHD / BD)":
					badge = Green + "[1080p]" + Reset
				case "Medium Quality (720p / HD)":
					badge = Yellow + "[720p]" + Reset
				case "Low Quality (480p / SD / CAM)":
					badge = Red + "[480p]" + Reset
				default:
					badge = Cyan + "[General]" + Reset
				}

				maxTitle := cols - 18
				if maxTitle < 12 {
					maxTitle = 12
				}
				titleDisp := truncateRunes(it.Title, maxTitle)

				var line1 string
				if isActive {
					line1 = fmt.Sprintf(" %s %s[%2d]%s %s %s%s%s", HlArrow, Cyan, idx+1, Reset, badge, HlBg, titleDisp, Reset)
				} else {
					line1 = fmt.Sprintf("   %s[%2d]%s %s %s", Cyan, idx+1, Reset, badge, titleDisp)
				}

				sColor := Red
				if it.Seeders > 5 {
					sColor = Green
				} else if it.Seeders > 0 {
					sColor = Yellow
				}

				itemSrc := it.Source
				if itemSrc == "ThePirateBay" {
					itemSrc = "TPB"
				}

				line2 := fmt.Sprintf("       %s[%s]%s  %s%s%s  ▲ %s%d%s  ▼ %s%d%s",
					Magenta, itemSrc, Reset,
					Yellow, it.Size, Reset,
					sColor, it.Seeders, Reset,
					Dim, it.Leechers, Reset)

				frame = append(frame, "\r"+line1+ClearLine)
				frame = append(frame, "\r"+line2+ClearLine)
			}
		}

		for len(frame) < lines-2 {
			frame = append(frame, "\r"+ClearLine)
		}

		if statusMsg != "" {
			frame = append(frame, "\r"+fmt.Sprintf("%s✔ %s%s", Green, statusMsg, Reset)+ClearLine)
			statusMsg = ""
		} else {
			frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		}

		footer := fmt.Sprintf("%s[▲/▼]%s Move  %s[Enter]%s Download  %s[t]%s Tier  %s[s]%s Src  %s[/]%s Filter  %s[q]%s Quit",
			Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset)
		frame = append(frame, "\r"+footer+ClearLine)

		fmt.Print(MoveTop + strings.Join(frame, "\r\n") + "\r")

		k := readKey(fd)
		switch strings.ToLower(k) {
		case "q", "quit", "esc":
			return
		case "up", "k":
			if selectedIdx > 0 {
				selectedIdx--
			}
		case "down", "j":
			if selectedIdx < totalItems-1 {
				selectedIdx++
			}
		case "right", "l", "page_down":
			if currentPage < totalPages {
				selectedIdx = startIdx + itemsPerPage
				if selectedIdx >= totalItems {
					selectedIdx = totalItems - 1
				}
			}
		case "left", "h", "page_up":
			if currentPage > 1 {
				selectedIdx = startIdx - itemsPerPage
				if selectedIdx < 0 {
					selectedIdx = 0
				}
			}
		case "home":
			selectedIdx = 0
		case "end":
			if totalItems > 0 {
				selectedIdx = totalItems - 1
			}
		case "t", "tab":
			currentTierIdx = (currentTierIdx + 1) % len(tierFilters)
			selectedIdx = 0
		case "s":
			currentSourceIdx = (currentSourceIdx + 1) % len(sourceFilters)
			selectedIdx = 0
		case "/":
			term.Restore(fd, oldState)
			fmt.Print("\r\n\033[KEnter filter text (Enter to apply, empty to clear): ")
			reader := bufio.NewReader(os.Stdin)
			lineInput, _ := reader.ReadString('\n')
			searchFilter = strings.TrimSpace(lineInput)
			oldState, _ = term.MakeRaw(fd)
			fmt.Print("\033[?25l" + ClearScrn)
			selectedIdx = 0
		case "m":
			if totalItems > 0 {
				item := filtered[selectedIdx]
				if copyToClipboard(item.Magnet) {
					shortTitle := item.Title
					if len(shortTitle) > 30 {
						shortTitle = shortTitle[:30]
					}
					statusMsg = fmt.Sprintf("Magnet copied! (%s...)", shortTitle)
				} else {
					shortMag := item.Magnet
					if len(shortMag) > 40 {
						shortMag = shortMag[:40]
					}
					statusMsg = fmt.Sprintf("Magnet: %s...", shortMag)
				}
			}
		case "enter", "d":
			if totalItems > 0 {
				item := filtered[selectedIdx]
				hasSpace, freeB := checkDiskSpace(destDir, item.RawSize)
				if !hasSpace {
					statusMsg = fmt.Sprintf("⚠️ Low disk space! Need %s, only %s free.", item.Size, formatBytes(freeB))
					continue
				}
				downloadDashboard(item, destDir)
				fmt.Print("\033[?1049h\033[?25l" + ClearScrn)
			}
		}
	}
}

func main() {
	sourceFlag := flag.String("s", "all", "Tracker source to query (all, tpb, nyaa)")
	qualityFlag := flag.String("q", "all", "Filter by quality section (highest, high, medium, low)")
	listFlag := flag.Bool("l", false, "List search results with sections and exit")
	magnetFlag := flag.Bool("m", false, "Print magnet link of top result and exit")
	downloadFlag := flag.Bool("d", false, "Instantly download top result via loading dashboard")
	updateFlag := flag.Bool("u", false, "Check for and install updates from GitHub")
	versionFlag := flag.Bool("v", false, "Show program version and exit")

	flag.Usage = func() {
		fmt.Printf("%s⚡ torq %s - Lightweight, keyboard-driven multi-tracker media engine for your terminal%s\n\n", Bold, Version, Reset)
		fmt.Println("Usage: torq [flags] [query | queue]")
		fmt.Println("\nFlags:")
		flag.PrintDefaults()
		fmt.Println("\nExamples:")
		fmt.Println("  torq \"Doraemon\"")
		fmt.Println("  torq queue")
		fmt.Println("  torq -q highest \"Oppenheimer\"")
		fmt.Println("  torq --update")
	}

	flag.Parse()

	if *versionFlag {
		fmt.Printf("torq %s\n", Version)
		return
	}

	if *updateFlag {
		selfUpdate()
		return
	}

	args := flag.Args()
	queryStr := strings.TrimSpace(strings.Join(args, " "))

	if strings.ToLower(queryStr) == "queue" || strings.ToLower(queryStr) == "q" {
		destDir := getDownloadsDir()
		queueManager(destDir)
		return
	}

	if queryStr == "" {
		fmt.Printf("%sSearch torrents for (or 'queue'): %s", Bold, Reset)
		reader := bufio.NewReader(os.Stdin)
		inputQuery, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(inputQuery) == "" {
			fmt.Printf("%sNo search query provided.%s\n", Red, Reset)
			os.Exit(1)
		}
		queryStr = strings.TrimSpace(inputQuery)
	}

	if strings.ToLower(queryStr) == "queue" || strings.ToLower(queryStr) == "q" {
		destDir := getDownloadsDir()
		queueManager(destDir)
		return
	}

	if !*magnetFlag {
		fmt.Fprintf(os.Stderr, "%sSearching multi-trackers for '%s'...%s\n", Cyan, queryStr, Reset)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var rawResults []TorrentItem
	var mu sync.Mutex

	src := strings.ToLower(*sourceFlag)
	if src == "all" || src == "tpb" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := searchTPB(ctx, queryStr)
			mu.Lock()
			rawResults = append(rawResults, items...)
			mu.Unlock()
		}()
	}

	if src == "all" || src == "nyaa" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := searchNyaa(ctx, queryStr)
			mu.Lock()
			rawResults = append(rawResults, items...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	if len(rawResults) == 0 {
		fmt.Printf("%sNo results found for '%s'.%s\n", Yellow, queryStr, Reset)
		return
	}

	qMap := map[string]string{
		"highest": "Highest Quality (4K / UHD / Remux)",
		"high":    "High Quality (1080p / FHD / BD)",
		"medium":  "Medium Quality (720p / HD)",
		"low":     "Low Quality (480p / SD / CAM)",
	}

	targetQuality := strings.ToLower(*qualityFlag)
	if targetQuality != "all" {
		targetTier, ok := qMap[targetQuality]
		if ok {
			var filtered []TorrentItem
			for _, r := range rawResults {
				if r.Tier == targetTier {
					filtered = append(filtered, r)
				}
			}
			rawResults = filtered
			if len(rawResults) == 0 {
				fmt.Printf("%sNo results found in %s section for '%s'.%s\n", Yellow, targetTier, queryStr, Reset)
				return
			}
		}
	}

	var sortedResults []TorrentItem
	tierMap := make(map[string][]TorrentItem)
	for _, it := range rawResults {
		tierMap[it.Tier] = append(tierMap[it.Tier], it)
	}

	for _, tier := range qualityOrder {
		items := tierMap[tier]
		sort.Slice(items, func(i, j int) bool {
			return items[i].Seeders > items[j].Seeders
		})
		sortedResults = append(sortedResults, items...)
		delete(tierMap, tier)
	}

	for _, items := range tierMap {
		sort.Slice(items, func(i, j int) bool {
			return items[i].Seeders > items[j].Seeders
		})
		sortedResults = append(sortedResults, items...)
	}

	if *magnetFlag {
		fmt.Println(sortedResults[0].Magnet)
		return
	}

	if *downloadFlag {
		dest := getDownloadsDir()
		downloadDashboard(sortedResults[0], dest)
		return
	}

	if *listFlag || !term.IsTerminal(int(os.Stdin.Fd())) {
		currentSection := ""
		limit := len(sortedResults)
		if limit > 30 {
			limit = 30
		}
		for idx := 0; idx < limit; idx++ {
			it := sortedResults[idx]
			if it.Tier != currentSection {
				currentSection = it.Tier
				color := qualityColors[it.Tier]
				fmt.Printf("\n%s━━━ %s ━━━%s\n", color, it.Tier, Reset)
			}
			sColor := Red
			if it.Seeders > 5 {
				sColor = Green
			} else if it.Seeders > 0 {
				sColor = Yellow
			}
			srcTag := fmt.Sprintf("%s[%s]%s", Magenta, it.Source, Reset)
			fmt.Printf(" [%2d] %s %s%s%s\n", idx+1, srcTag, Bold, it.Title, Reset)
			fmt.Printf("      Size: %s%s%s | Seeders: %s%d%s | Leechers: %s%d%s\n",
				Yellow, it.Size, Reset, sColor, it.Seeders, Reset, Dim, it.Leechers, Reset)
		}
		fmt.Println()
		return
	}

	runTUI(sortedResults, queryStr)
}
