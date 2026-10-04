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
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.bittor.pw:1337/announce",
	"udp://public.popcorn-tracker.org:6969/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://tracker.coppersurfer.tk:6969/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
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
	var trParts []string
	for _, tr := range topTrackers {
		trParts = append(trParts, "tr="+url.QueryEscape(tr))
	}
	trParams := strings.Join(trParts, "&")
	if !strings.HasPrefix(magnetURI, "magnet:?") {
		return magnetURI
	}
	if strings.Contains(magnetURI, "&tr=") {
		return magnetURI + "&" + trParams
	}
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
	GID             string `json:"gid"`
	Status          string `json:"status"`
	TotalLength     string `json:"totalLength"`
	CompletedLength string `json:"completedLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
	NumSeeders      string `json:"numSeeders"`
	Connections     string `json:"connections"`
	Files           []struct {
		Path   string `json:"path"`
		Length string `json:"length"`
	} `json:"files"`
}

func ensureAriaDaemon(destDir string) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	pingPayload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "p",
		"method":  "aria2.getVersion",
		"params":  []interface{}{"token:" + RPCSecret},
	}
	pingBytes, _ := json.Marshal(pingPayload)
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", RPCPort), bytes.NewReader(pingBytes))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		return
	}

	trackersArg := strings.Join(topTrackers, ",")
	cmd := exec.Command("aria2c",
		"--enable-rpc=true",
		fmt.Sprintf("--rpc-listen-port=%d", RPCPort),
		fmt.Sprintf("--rpc-secret=%s", RPCSecret),
		fmt.Sprintf("--dir=%s", destDir),
		"--seed-time=0",
		"--file-allocation=none",
		"--bt-max-peers=140",
		"--bt-request-peer-speed-limit=0",
		"--max-connection-per-server=16",
		"--split=16",
		"--min-split-size=1M",
		"--piece-length=1M",
		"--enable-dht=true",
		"--enable-dht6=true",
		"--bt-enable-lpd=true",
		"--enable-peer-exchange=true",
		fmt.Sprintf("--bt-tracker=%s", trackersArg),
		"--async-dns=true",
		"--summary-interval=0",
		"--quiet=true",
		"--daemon=true",
	)
	_ = cmd.Run()
	time.Sleep(500 * time.Millisecond)
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

func addMagnet(magnet string) (string, error) {
	raw, err := ariaRPC("aria2.addUri", []interface{}{[]string{magnet}})
	if err != nil {
		return "", err
	}
	var gid string
	_ = json.Unmarshal(raw, &gid)
	return gid, nil
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

func getCurrentTask() *AriaTask {
	parseTasks := func(raw json.RawMessage) []AriaTask {
		var list []AriaTask
		_ = json.Unmarshal(raw, &list)
		return list
	}

	if raw, err := ariaRPC("aria2.tellActive", nil); err == nil {
		tasks := parseTasks(raw)
		for i := range tasks {
			if len(tasks[i].Files) > 0 && !strings.HasPrefix(tasks[i].Files[0].Path, "[METADATA]") {
				return &tasks[i]
			}
		}
		if len(tasks) > 0 {
			return &tasks[0]
		}
	}

	if raw, err := ariaRPC("aria2.tellWaiting", []interface{}{0, 5}); err == nil {
		tasks := parseTasks(raw)
		for i := range tasks {
			if len(tasks[i].Files) > 0 && !strings.HasPrefix(tasks[i].Files[0].Path, "[METADATA]") {
				return &tasks[i]
			}
		}
	}

	if raw, err := ariaRPC("aria2.tellStopped", []interface{}{0, 5}); err == nil {
		tasks := parseTasks(raw)
		for i := range tasks {
			if len(tasks[i].Files) > 0 && !strings.HasPrefix(tasks[i].Files[0].Path, "[METADATA]") {
				return &tasks[i]
			}
		}
	}

	return nil
}

func hasKeyInput(stdinFd int, timeout time.Duration) bool {
	var readFds unix.FdSet
	readFds.Set(stdinFd)
	tv := unix.NsecToTimeval(timeout.Nanoseconds())
	n, err := unix.Select(stdinFd+1, &readFds, nil, nil, &tv)
	return err == nil && n > 0 && readFds.IsSet(stdinFd)
}

func readKey(stdinFd int) string {
	buf := make([]byte, 1)
	n, err := os.Stdin.Read(buf)
	if err != nil || n == 0 {
		return "QUIT"
	}
	b := buf[0]
	if b == 0x1b { // ESC
		time.Sleep(50 * time.Millisecond)
		seq := make([]byte, 8)
		_ = unix.SetNonblock(stdinFd, true)
		nSeq, _ := os.Stdin.Read(seq)
		_ = unix.SetNonblock(stdinFd, false)
		if nSeq == 0 {
			return "ESC"
		}
		s := string(seq[:nSeq])
		switch s {
		case "[A", "OA":
			return "UP"
		case "[B", "OB":
			return "DOWN"
		case "[C", "OC":
			return "RIGHT"
		case "[D", "OD":
			return "LEFT"
		default:
			return "ESC_" + s
		}
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
	_, _ = addMagnet(item.Magnet)

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		defer term.Restore(fd, oldState)
	}
	fmt.Print("\033[?25l" + ClearScrn)
	defer fmt.Print("\033[?25h\n")

	statusMsg := ""
	isPaused := false

	for {
		cols, lines, err := term.GetSize(fd)
		if err != nil || cols <= 0 {
			cols, lines = 80, 24
		}

		task := getCurrentTask()

		statusText := "connecting"
		var total, completed, speed int64
		var seeders, conns int
		var gid string

		if task != nil {
			statusText = task.Status
			total, _ = strconv.ParseInt(task.TotalLength, 10, 64)
			completed, _ = strconv.ParseInt(task.CompletedLength, 10, 64)
			speed, _ = strconv.ParseInt(task.DownloadSpeed, 10, 64)
			seeders, _ = strconv.Atoi(task.NumSeeders)
			conns, _ = strconv.Atoi(task.Connections)
			gid = task.GID
		}

		fileName := item.Title
		var filePath string
		isMetadataPhase := true

		if task != nil && len(task.Files) > 0 {
			p := task.Files[0].Path
			if p != "" && !strings.HasPrefix(p, "[METADATA]") && !strings.HasPrefix(p, "[MEMORY]") {
				isMetadataPhase = false
				filePath = p
				fileName = filepath.Base(p)
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
		frame = append(frame, MoveTop)

		hdr := fmt.Sprintf("%s%s⚡ TORQ DOWNLOAD MANAGER%s %s───%s %s%s[%s]%s", Bold, Cyan, Reset, Dim, Reset, Bold, Magenta, item.Source, Reset)
		if len(hdr) > cols+40 {
			hdr = hdr[:cols+40]
		}
		frame = append(frame, hdr+ClearLine)

		divLen := cols - 1
		if divLen > 78 {
			divLen = 78
		}
		if divLen < 1 {
			divLen = 1
		}
		frame = append(frame, Dim+strings.Repeat("━", divLen)+Reset+ClearLine)

		fnDisp := fileName
		if len(fnDisp) > cols-8 && cols > 8 {
			fnDisp = fnDisp[:cols-8]
		}
		frame = append(frame, fmt.Sprintf("%sFile:%s %s%s%s", Bold, Reset, White, fnDisp, Reset)+ClearLine)
		frame = append(frame, fmt.Sprintf("%sPath:%s %s%s%s", Bold, Reset, Dim, destDir, Reset)+ClearLine)
		frame = append(frame, ClearLine)

		badge := ""
		switch {
		case isMetadataPhase:
			badge = fmt.Sprintf("%s◐ CONNECTING TO PEERS & FETCHING METADATA...%s", Magenta, Reset)
		case statusText == "active":
			badge = fmt.Sprintf("%s● DOWNLOADING%s", Green, Reset)
		case statusText == "paused":
			badge = fmt.Sprintf("%s❚❚ PAUSED%s", Yellow, Reset)
		case statusText == "complete":
			badge = fmt.Sprintf("%s%s✔ COMPLETED%s", Bold, Cyan, Reset)
		case statusText == "removed":
			badge = fmt.Sprintf("%s✖ CANCELLED%s", Red, Reset)
		default:
			badge = fmt.Sprintf("%s● %s%s", Yellow, strings.ToUpper(statusText), Reset)
		}

		frame = append(frame, fmt.Sprintf("%sStatus:%s %s   %sPeers:%s %d connected (%d seeds)", Bold, Reset, badge, Bold, Reset, conns, seeders)+ClearLine)
		frame = append(frame, ClearLine)

		barWidth := cols - 30
		if barWidth < 15 {
			barWidth = 15
		}
		if barWidth > 48 {
			barWidth = 48
		}

		filled := int(float64(barWidth) * pct / 100.0)
		if filled > barWidth {
			filled = barWidth
		}
		empty := barWidth - filled

		barDisplay := fmt.Sprintf("[%s%s%s%s%s%s] %s%s%5.1f%%%s", Bold, Cyan, strings.Repeat("█", filled), Reset, Dim, strings.Repeat("░", empty), Reset, Bold, White, pct, Reset)
		frame = append(frame, barDisplay+ClearLine)
		frame = append(frame, ClearLine)

		if isMetadataPhase {
			frame = append(frame, fmt.Sprintf("%sFinding best swarm seeds and resolving file pieces...%s", Dim, Reset)+ClearLine)
		} else {
			metrics := fmt.Sprintf("%sSpeed:%s %s%-12s%s %sData:%s %s / %s   %sETA:%s %s%s%s",
				Bold, Reset, Green, formatSpeed(speed), Reset,
				Bold, Reset, formatBytes(completed), formatBytes(total),
				Bold, Reset, Yellow, formatTime(etaSec), Reset)
			frame = append(frame, metrics+ClearLine)
		}
		frame = append(frame, ClearLine)

		used := len(frame)
		for i := 0; i < lines-used-4; i++ {
			frame = append(frame, ClearLine)
		}

		if statusMsg != "" {
			frame = append(frame, fmt.Sprintf("%sℹ %s%s", Cyan, statusMsg, Reset)+ClearLine)
			statusMsg = ""
		} else {
			frame = append(frame, Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		}

		var controls string
		if statusText == "complete" {
			controls = fmt.Sprintf("%s[Enter/q]%s Return to Search   %s[o]%s Open File in Player", Bold, Reset, Bold, Reset)
		} else if statusText == "removed" || statusText == "error" {
			controls = fmt.Sprintf("%s[Enter/q]%s Return to Search", Bold, Reset)
		} else {
			pLabel := "Pause"
			if isPaused {
				pLabel = "Resume"
			}
			controls = fmt.Sprintf("%s[p/Space]%s %s   %s[c]%s Cancel   %s[b]%s Run in Background   %s[q]%s Return", Bold, Reset, pLabel, Bold, Reset, Bold, Reset, Bold, Reset)
		}
		frame = append(frame, controls+ClearLine)

		fmt.Print(strings.Join(frame, "\n"))

		if statusText == "complete" {
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
				if gid != "" {
					if isPaused {
						unpauseDownload(gid)
						isPaused = false
						statusMsg = "Download Resumed"
					} else {
						pauseDownload(gid)
						isPaused = true
						statusMsg = "Download Paused"
					}
				}
			case "c", "x":
				if gid != "" {
					removeDownload(gid)
					statusMsg = "Download Cancelled"
				}
			case "b":
				fmt.Print("\033[?25h\n")
				fmt.Printf("\n%s✔ Download running in background!%s\n", Green, Reset)
				fmt.Printf("%sSaved to: %s%s\n\n", Dim, destDir, Reset)
				return
			case "q", "quit":
				if gid != "" {
					removeDownload(gid)
				}
				return
			}
		}
	}
}

func queueManager(destDir string) {
	fmt.Print(ClearScrn)
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

	fmt.Printf("%s%s📋 TORQ DOWNLOAD MANAGER & RECENT DOWNLOADS%s\n", Bold, Cyan, Reset)
	fmt.Printf("Location: %s%s%s\n", Yellow, destDir, Reset)
	fmt.Printf("%s%s%s\n\n", Dim, strings.Repeat("━", 65), Reset)

	if len(files) == 0 {
		fmt.Printf("%sNo recent completed media found in %s.%s\n\n", Yellow, destDir, Reset)
	} else {
		limit := len(files)
		if limit > 15 {
			limit = 15
		}
		for idx := 0; idx < limit; idx++ {
			f := files[idx]
			dispName := f.name
			if len(dispName) > 50 {
				dispName = dispName[:50]
			}
			fmt.Printf(" %s%s[%2d]%s %s%-52s%s %s%s%s\n", Bold, Cyan, idx+1, Reset, White, dispName, Reset, Yellow, formatBytes(f.size), Reset)
		}
	}

	fmt.Printf("\n%s%s%s\n", Dim, strings.Repeat("━", 65), Reset)
	maxNum := len(files)
	if maxNum > 15 {
		maxNum = 15
	}
	fmt.Printf("%s[1-%d]%s Open File in Player  %s[o]%s Open Folder  %s[q]%s Return\n\n", Bold, maxNum, Reset, Bold, Reset, Bold, Reset)

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
		// Fallback to current tag
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
	fmt.Print("\033[?25l" + ClearScrn)
	defer fmt.Print("\033[?25h\n")

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

		itemsPerPage := (lines - 7) / 2
		if itemsPerPage < 3 {
			itemsPerPage = 3
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
		frame = append(frame, MoveTop)

		tierLabel := targetTier
		if s, ok := qualityShort[targetTier]; ok {
			tierLabel = s
		}
		qDisp := initialQuery
		if len(qDisp) > 18 {
			qDisp = qDisp[:18]
		}
		hdr := fmt.Sprintf("%s%s⚡ TORQ TUI%s | %s%s%s%s | Tier: %s[%s]%s | Src: %s[%s]%s | Pg: %s%d/%d%s",
			Bold, Cyan, Reset, Bold, Yellow, qDisp, Reset, Magenta, tierLabel, Reset, Blue, targetSource, Reset, Bold, currentPage, totalPages, Reset)
		frame = append(frame, hdr+ClearLine)

		divLen := cols - 1
		if divLen > 78 {
			divLen = 78
		}
		if divLen < 1 {
			divLen = 1
		}

		if searchFilter != "" {
			frame = append(frame, fmt.Sprintf("%sFilter (/): %s%s", Yellow, searchFilter, Reset)+ClearLine)
		} else {
			frame = append(frame, Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		}

		if totalItems == 0 {
			frame = append(frame, fmt.Sprintf("\n   %sNo torrents found matching active filters.%s", Yellow, Reset)+ClearLine)
			frame = append(frame, fmt.Sprintf("   %sPress 't' to reset quality tier or 's' to reset source.%s", Dim, Reset)+ClearLine)
		} else {
			currentSection := ""
			for idx := startIdx; idx < endIdx; idx++ {
				it := filtered[idx]
				isActive := (idx == selectedIdx)

				if targetTier == "ALL" && it.Tier != currentSection {
					currentSection = it.Tier
					color := qualityColors[it.Tier]
					frame = append(frame, fmt.Sprintf("%s── %s ──%s", color, it.Tier, Reset)+ClearLine)
				}

				sColor := Red
				if it.Seeders > 5 {
					sColor = Green
				} else if it.Seeders > 0 {
					sColor = Yellow
				}

				titleDisp := it.Title
				if len(titleDisp) > cols-14 && cols > 14 {
					titleDisp = titleDisp[:cols-14]
				}

				srcTag := fmt.Sprintf("%s[%s]%s", Magenta, it.Source, Reset)
				if isActive {
					line1 := fmt.Sprintf("%s %s[%2d] %s%s", HlArrow, HlBg, idx+1, titleDisp, Reset)
					line2 := fmt.Sprintf("   %s Size: %s%s%s | Seeders: %s%d%s | Leechers: %s%d%s | %s%s%s",
						srcTag, Yellow, it.Size, Reset, sColor, it.Seeders, Reset, Dim, it.Leechers, Reset, Dim, it.Category, Reset)
					frame = append(frame, line1+ClearLine, line2+ClearLine)
				} else {
					line1 := fmt.Sprintf("   %s[%2d]%s %s", Cyan, idx+1, Reset, titleDisp)
					line2 := fmt.Sprintf("   %s Size: %s%s%s | Seeders: %s%d%s | Leechers: %s%d%s | %s%s%s",
						srcTag, Yellow, it.Size, Reset, sColor, it.Seeders, Reset, Dim, it.Leechers, Reset, Dim, it.Category, Reset)
					frame = append(frame, line1+ClearLine, line2+ClearLine)
				}
			}
		}

		used := len(frame)
		for i := 0; i < lines-used-2; i++ {
			frame = append(frame, ClearLine)
		}

		if statusMsg != "" {
			frame = append(frame, fmt.Sprintf("%s✔ %s%s", Green, statusMsg, Reset)+ClearLine)
			statusMsg = ""
		} else {
			frame = append(frame, Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		}

		footer := fmt.Sprintf("%s[▲/▼]%s Nav  %s[Enter/d]%s Download  %s[Q]%s Queue  %s[m]%s Magnet  %s[t]%s Tier  %s[s]%s Source  %s[q]%s Quit",
			Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset)
		frame = append(frame, footer+ClearLine)

		fmt.Print(strings.Join(frame, "\n"))

		k := readKey(fd)
		switch k {
		case "q", "QUIT":
			return
		case "UP", "k":
			if selectedIdx > 0 {
				selectedIdx--
			}
		case "DOWN", "j":
			if selectedIdx < totalItems-1 {
				selectedIdx++
			}
		case "RIGHT", "l", "n":
			if currentPage < totalPages {
				selectedIdx = startIdx + itemsPerPage
				if selectedIdx >= totalItems {
					selectedIdx = totalItems - 1
				}
			}
		case "LEFT", "h", "p":
			if currentPage > 1 {
				selectedIdx = startIdx - itemsPerPage
				if selectedIdx < 0 {
					selectedIdx = 0
				}
			}
		case "t", "TAB":
			currentTierIdx = (currentTierIdx + 1) % len(tierFilters)
			selectedIdx = 0
		case "s":
			currentSourceIdx = (currentSourceIdx + 1) % len(sourceFilters)
			selectedIdx = 0
		case "Q":
			term.Restore(fd, oldState)
			queueManager(destDir)
			oldState, _ = term.MakeRaw(fd)
			fmt.Print("\033[?25l" + ClearScrn)
		case "/":
			term.Restore(fd, oldState)
			fmt.Print("\033[?25h\n\033[KEnter filter text (Enter to apply, empty to clear): ")
			reader := bufio.NewReader(os.Stdin)
			lineInput, _ := reader.ReadString('\n')
			searchFilter = strings.TrimSpace(lineInput)
			oldState, _ = term.MakeRaw(fd)
			fmt.Print("\033[?25l")
			selectedIdx = 0
		case "m", "M":
			if totalItems > 0 {
				item := filtered[selectedIdx]
				if copyToClipboard(item.Magnet) {
					shortTitle := item.Title
					if len(shortTitle) > 32 {
						shortTitle = shortTitle[:32]
					}
					statusMsg = fmt.Sprintf("Magnet copied to clipboard! (%s...)", shortTitle)
				} else {
					shortMag := item.Magnet
					if len(shortMag) > 50 {
						shortMag = shortMag[:50]
					}
					statusMsg = fmt.Sprintf("Magnet: %s...", shortMag)
				}
			}
		case "ENTER", "d", "D":
			if totalItems > 0 {
				item := filtered[selectedIdx]
				hasSpace, freeB := checkDiskSpace(destDir, item.RawSize)
				if !hasSpace {
					term.Restore(fd, oldState)
					fmt.Printf("\n%s%s⚠️  DISK SPACE WARNING%s\n", Bold, Red, Reset)
					fmt.Printf("Torrent Size: %s%s%s | Free on storage: %s%s%s\n", Yellow, item.Size, Reset, Red, formatBytes(freeB), Reset)
					fmt.Printf("%sProceed anyway? [y/N]: %s", Bold, Reset)
					reader := bufio.NewReader(os.Stdin)
					ans, _ := reader.ReadString('\n')
					ans = strings.TrimSpace(strings.ToLower(ans))
					if ans != "y" && ans != "yes" {
						oldState, _ = term.MakeRaw(fd)
						fmt.Print("\033[?25l" + ClearScrn)
						continue
					}
					oldState, _ = term.MakeRaw(fd)
				}
				term.Restore(fd, oldState)
				downloadDashboard(item, destDir)
				oldState, _ = term.MakeRaw(fd)
				fmt.Print(ClearScrn)
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
