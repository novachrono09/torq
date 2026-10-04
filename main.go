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
	Version           = "1.3.0"
	RPCPort           = 6800
	RPCSecret         = "torq_secret_session"
	StreamPort        = 3030
	StreamGatewayPort = 8888
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

type YTSResponse struct {
	Status string `json:"status"`
	Data   struct {
		MovieCount int `json:"movie_count"`
		Movies     []struct {
			Title    string `json:"title"`
			Year     int    `json:"year"`
			Torrents []struct {
				Hash      string `json:"hash"`
				Quality   string `json:"quality"`
				Type      string `json:"type"`
				Seeds     int    `json:"seeds"`
				Peers     int    `json:"peers"`
				Size      string `json:"size"`
				SizeBytes int64  `json:"size_bytes"`
			} `json:"torrents"`
		} `json:"movies"`
	} `json:"data"`
}

func searchYTS(ctx context.Context, query string) []TorrentItem {
	mirrors := []string{
		"https://yts.lt/api/v2/list_movies.json?query_term=",
		"https://yts.bz/api/v2/list_movies.json?query_term=",
	}

	client := &http.Client{Timeout: 8 * time.Second}

	for _, endpoint := range mirrors {
		target := endpoint + url.QueryEscape(query)
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0")

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}

		var yr YTSResponse
		err = json.NewDecoder(resp.Body).Decode(&yr)
		resp.Body.Close()
		if err != nil || len(yr.Data.Movies) == 0 {
			continue
		}

		var results []TorrentItem
		for _, m := range yr.Data.Movies {
			for _, t := range m.Torrents {
				title := fmt.Sprintf("%s (%d) [%s] [%s]", m.Title, m.Year, t.Quality, strings.ToUpper(t.Type))
				tier := detectQualityTier(title)
				if strings.Contains(strings.ToLower(t.Quality), "2160p") || strings.Contains(strings.ToLower(t.Quality), "4k") {
					tier = "Highest Quality (4K / UHD / Remux)"
				} else if strings.Contains(strings.ToLower(t.Quality), "1080p") {
					tier = "High Quality (1080p / FHD / BD)"
				} else if strings.Contains(strings.ToLower(t.Quality), "720p") {
					tier = "Medium Quality (720p / HD)"
				}

				baseMag := fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", t.Hash, url.QueryEscape(title))
				mag := injectTrackers(baseMag, title)

				results = append(results, TorrentItem{
					Title:    title,
					Source:   "YTS",
					Seeders:  t.Seeds,
					Leechers: t.Peers,
					Size:     t.Size,
					RawSize:  t.SizeBytes,
					Category: "Movies",
					Tier:     tier,
					Magnet:   mag,
				})
			}
		}
		if len(results) > 0 {
			return results
		}
	}
	return nil
}

type AnimeToshoItem struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	MagnetURI string `json:"magnet_uri"`
	Seeders   int    `json:"seeders"`
	Leechers  int    `json:"leechers"`
	TotalSize int64  `json:"total_size"`
}

func searchAnimeTosho(ctx context.Context, query string) []TorrentItem {
	target := "https://feed.animetosho.org/json?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var items []AnimeToshoItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil
	}

	var results []TorrentItem
	for _, it := range items {
		if it.Title == "" || it.MagnetURI == "" {
			continue
		}
		title := html.UnescapeString(it.Title)
		tier := detectQualityTier(title)
		mag := injectTrackers(it.MagnetURI, title)

		results = append(results, TorrentItem{
			Title:    title,
			Source:   "AnimeTosho",
			Seeders:  it.Seeders,
			Leechers: it.Leechers,
			Size:     formatBytes(it.TotalSize),
			RawSize:  it.TotalSize,
			Category: "Anime",
			Tier:     tier,
			Magnet:   mag,
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
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if _, err := exec.LookPath("termux-clipboard-set"); err == nil {
		cmd := exec.CommandContext(ctx, "termux-clipboard-set")
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return true
		}
	}
	if _, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.CommandContext(ctx, "wl-copy")
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return true
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return true
		}
	}
	if _, err := exec.LookPath("pbcopy"); err == nil {
		cmd := exec.CommandContext(ctx, "pbcopy")
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return true
		}
	}
	return false
}

func sendNotification(title, message string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		if _, err := exec.LookPath("termux-notification"); err == nil {
			_ = exec.CommandContext(ctx, "termux-notification", "--title", title, "--content", message, "--priority", "high").Run()
			return
		}
		if _, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.CommandContext(ctx, "notify-send", title, message).Run()
			return
		}
	}()
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

type TorqConfig struct {
	MaxDownloadLimit string `json:"max_download_limit"`
	MaxUploadLimit   string `json:"max_upload_limit"`
}

func getTorqConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "torq", "config.json")
}

func loadTorqConfig() TorqConfig {
	cfg := TorqConfig{
		MaxDownloadLimit: "0",
		MaxUploadLimit:   "0",
	}
	p := getTorqConfigPath()
	data, err := os.ReadFile(p)
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.MaxDownloadLimit == "" {
		cfg.MaxDownloadLimit = "0"
	}
	if cfg.MaxUploadLimit == "" {
		cfg.MaxUploadLimit = "0"
	}
	return cfg
}

func saveTorqConfig(cfg TorqConfig) error {
	p := getTorqConfigPath()
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0644)
}

func ensureAriaDaemon(destDir string) {
	if _, err := exec.LookPath("aria2c"); err != nil {
		fmt.Printf("\r\n%s[!] 'aria2c' is not installed.%s\r\nPlease install it using: pkg install aria2 (or sudo apt install aria2)\r\n", Red, Reset)
		return
	}

	cfg := loadTorqConfig()
	trackersArg := strings.Join(topTrackers, ",")

	// 1. Check if an aria2 daemon is already running and has DHT enabled
	if raw, err := ariaRPC("aria2.getGlobalOption", nil); err == nil {
		var opts map[string]string
		if json.Unmarshal(raw, &opts) == nil {
			if opts["enable-dht"] == "true" {
				// Daemon is alive and healthy, dynamically update directory, trackers, and limits
				_, _ = ariaRPC("aria2.changeGlobalOption", []interface{}{
					map[string]string{
						"dir":                        destDir,
						"bt-tracker":                 trackersArg,
						"max-overall-download-limit": cfg.MaxDownloadLimit,
						"max-overall-upload-limit":   cfg.MaxUploadLimit,
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
		fmt.Sprintf("--max-overall-download-limit=%s", cfg.MaxDownloadLimit),
		fmt.Sprintf("--max-overall-upload-limit=%s", cfg.MaxUploadLimit),
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

func runDownloadLoop(initialGID, defaultTitle, srcLabel, destDir string, fd int) {
	activeGID := initialGID
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

		fileName := defaultTitle
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
					if fileName == defaultTitle {
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
			cfg := loadTorqConfig()
			capStr := ""
			if cfg.MaxDownloadLimit != "0" {
				capStr = fmt.Sprintf(" %s[Cap: %s]%s", Yellow, cfg.MaxDownloadLimit, Reset)
			}
			metrics := fmt.Sprintf("%sSpeed:%s %s%-9s%s%s %sData:%s %s/%s  %sETA:%s %s%s%s",
				Bold, Reset, Green, formatSpeed(speed), Reset, capStr,
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
			controls = fmt.Sprintf("%s[Enter/q]%s Return", Bold, Reset)
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

	runDownloadLoop(activeGID, item.Title, item.Source, destDir, fd)
}

var videoExts = map[string]bool{
	".mp4":  true,
	".mkv":  true,
	".avi":  true,
	".mov":  true,
	".wmv":  true,
	".webm": true,
	".flv":  true,
	".m4v":  true,
	".ts":   true,
	".m2ts": true,
}

type RqbitStreamStats struct {
	State         string `json:"state"`
	ProgressBytes int64  `json:"progress_bytes"`
	TotalBytes    int64  `json:"total_bytes"`
	Live          *struct {
		Snapshot struct {
			DownloadedBytes int64 `json:"downloaded_and_checked_bytes"`
			PeerStats       struct {
				Live       int `json:"live"`
				Connecting int `json:"connecting"`
			} `json:"peer_stats"`
		} `json:"snapshot"`
		DownloadSpeed struct {
			HumanReadable string `json:"human_readable"`
		} `json:"download_speed"`
		UploadSpeed struct {
			HumanReadable string `json:"human_readable"`
		} `json:"upload_speed"`
	} `json:"live"`
}

type StreamVideoFile struct {
	Index  int
	Name   string
	Length int64
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func getTerminalSize(fd int) (int, int) {
	cols, lines, err := term.GetSize(fd)
	if err != nil || cols <= 0 {
		return 80, 24
	}
	return cols, lines
}

func cleanURLFileName(name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(filepath.Base(name), ext)
	reg := regexp.MustCompile(`[^a-zA-Z0-9_\-\.]+`)
	safeBase := reg.ReplaceAllString(base, "_")
	safeBase = strings.Trim(safeBase, "_")
	if safeBase == "" {
		safeBase = "stream"
	}
	if ext == "" {
		ext = ".mp4"
	}
	return safeBase + ext
}

func detectCodec(title string) string {
	low := strings.ToLower(title)
	if strings.Contains(low, "av1") {
		return "AV1"
	}
	if strings.Contains(low, "hevc") || strings.Contains(low, "x265") || strings.Contains(low, "h265") || strings.Contains(low, "h.265") {
		return "HEVC (x265)"
	}
	if strings.Contains(low, "x264") || strings.Contains(low, "h264") || strings.Contains(low, "h.264") || strings.Contains(low, "avc") {
		return "AVC (x264)"
	}
	return "Standard"
}

func drawConnectingFrame(fd int, title, message string, spinIdx int) {
	spinChars := []string{"◐", "◓", "◑", "◒"}
	spinner := spinChars[spinIdx%len(spinChars)]
	cols, lines := getTerminalSize(fd)
	divLen := cols - 4
	if divLen < 40 {
		divLen = 40
	}

	var frame []string
	hdr := fmt.Sprintf("%s%s⚡ TORQ STREAM ENGINE%s | %s%sZERO-DISK MODE%s", Bold, Cyan, Reset, Bold, Green, Reset)
	frame = append(frame, "\r"+hdr+ClearLine)
	frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)

	dispTitle := title
	if len(dispTitle) > cols-12 {
		dispTitle = dispTitle[:cols-15] + "..."
	}
	frame = append(frame, "\r"+fmt.Sprintf(" %sTitle:%s  %s%s%s", Bold, Reset, Bold, dispTitle, Reset)+ClearLine)
	frame = append(frame, "\r"+fmt.Sprintf(" %sStatus:%s %s%s %s%s", Bold, Reset, Cyan, spinner, message, Reset)+ClearLine)
	frame = append(frame, "\r"+ClearLine)

	frame = append(frame, "\r"+fmt.Sprintf(" %s🔒 ZERO-DISK GUARANTEE:%s", Yellow, Reset)+ClearLine)
	frame = append(frame, "\r"+fmt.Sprintf(" %sEphemeral cache active. No file is saved to Downloads.%s", Dim, Reset)+ClearLine)
	frame = append(frame, "\r"+fmt.Sprintf(" %sBandwidth is isolated exclusively to your selected video.%s", Dim, Reset)+ClearLine)

	for len(frame) < lines-2 {
		frame = append(frame, "\r"+ClearLine)
	}

	frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
	footer := fmt.Sprintf("%s[q]%s Cancel", Bold, Reset)
	frame = append(frame, "\r"+footer+ClearLine)

	fmt.Print(MoveTop + strings.Join(frame, "\r\n") + "\r")
}

func startRqbitServer(cacheDir string, title string, fd int) (*exec.Cmd, error) {
	if _, err := exec.LookPath("rqbit"); err != nil {
		return nil, fmt.Errorf("'rqbit' streaming engine is not installed. Run: pkg install rqbit")
	}

	drawConnectingFrame(fd, title, "Initializing stream engine...", 0)

	_ = exec.Command("pkill", "-9", "rqbit").Run()
	time.Sleep(150 * time.Millisecond)

	cmd := exec.Command("rqbit",
		"--http-api-listen-addr", fmt.Sprintf("0.0.0.0:%d", StreamPort),
		"server", "start", cacheDir,
		"--disable-persistence",
	)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 300 * time.Millisecond}
	testURL := fmt.Sprintf("http://127.0.0.1:%d/", StreamPort)
	for i := 0; i < 35; i++ {
		drawConnectingFrame(fd, title, "Starting local stream server...", i)
		time.Sleep(80 * time.Millisecond)
		resp, err := client.Get(testURL)
		if err == nil && resp.StatusCode == 200 {
			_ = resp.Body.Close()
			return cmd, nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
	}

	return cmd, nil
}

func pauseRqbitTorrent(torrentID int) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/torrents/%d/pause", StreamPort, torrentID), nil)
	if resp, err := client.Do(req); err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

func resumeRqbitTorrent(torrentID int) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/torrents/%d/start", StreamPort, torrentID), nil)
	if resp, err := client.Do(req); err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

func setRqbitOnlyFile(torrentID, fileIdx int) {
	body := fmt.Sprintf(`{"only_files": [%d]}`, fileIdx)
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/torrents/%d/update_only_files", StreamPort, torrentID), strings.NewReader(body))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil && resp != nil {
			_ = resp.Body.Close()
		}
	}
}

func deleteRqbitTorrent(torrentID int) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/torrents/%d/delete", StreamPort, torrentID), nil)
	if err == nil {
		resp, err := client.Do(req)
		if err == nil && resp != nil {
			_ = resp.Body.Close()
		}
	}
}

func startStreamGateway(torrentID, fileIdx int, cleanFileName string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/stream/"+cleanFileName, func(w http.ResponseWriter, r *http.Request) {
		backendURL := fmt.Sprintf("http://127.0.0.1:%d/torrents/%d/stream/%d", StreamPort, torrentID, fileIdx)
		req, err := http.NewRequestWithContext(r.Context(), r.Method, backendURL, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for k, v := range r.Header {
			req.Header[k] = v
		}

		client := &http.Client{Timeout: 0}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		ext := strings.ToLower(filepath.Ext(cleanFileName))
		switch ext {
		case ".mp4":
			w.Header().Set("Content-Type", "video/mp4")
		case ".mkv":
			w.Header().Set("Content-Type", "video/x-matroska")
		case ".webm":
			w.Header().Set("Content-Type", "video/webm")
		default:
			w.Header().Set("Content-Type", "video/mp4")
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", cleanFileName))

		for k, v := range resp.Header {
			if strings.EqualFold(k, "Content-Type") {
				continue
			}
			w.Header()[k] = v
		}

		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", StreamGatewayPort),
		Handler: mux,
	}

	go func() {
		_ = server.ListenAndServe()
	}()

	return server
}

func openVideoPlayer(streamURL string) {
	if _, err := exec.LookPath("am"); err == nil {
		_ = exec.Command("am", "start", "-a", "android.intent.action.VIEW", "-d", streamURL, "-t", "video/*").Start()
		return
	}
	if _, err := exec.LookPath("termux-open-url"); err == nil {
		_ = exec.Command("termux-open-url", streamURL).Start()
		return
	}
	if _, err := exec.LookPath("xdg-open"); err == nil {
		_ = exec.Command("xdg-open", streamURL).Start()
		return
	}
}

func selectEpisodeInteractive(videoFiles []StreamVideoFile, fd int) (StreamVideoFile, bool) {
	if len(videoFiles) == 1 {
		return videoFiles[0], true
	}

	selectedIdx := 0
	for {
		cols, lines := getTerminalSize(fd)
		divLen := cols - 4
		if divLen < 40 {
			divLen = 40
		}

		var frame []string
		hdr := fmt.Sprintf("%s%s⚡ TORQ STREAM | SELECT EPISODE / FILE%s", Bold, Cyan, Reset)
		frame = append(frame, "\r"+hdr+ClearLine)
		frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" %sMulti-file release (%d videos). Choose which to stream:%s", Bold, len(videoFiles), Reset)+ClearLine)
		frame = append(frame, "\r"+ClearLine)

		maxShow := lines - 8
		if maxShow < 4 {
			maxShow = 4
		}
		if maxShow > len(videoFiles) {
			maxShow = len(videoFiles)
		}

		startIdx := 0
		if selectedIdx >= maxShow {
			startIdx = selectedIdx - maxShow + 1
		}
		endIdx := startIdx + maxShow
		if endIdx > len(videoFiles) {
			endIdx = len(videoFiles)
		}

		for i := startIdx; i < endIdx; i++ {
			f := videoFiles[i]
			fName := f.Name
			if len(fName) > cols-20 {
				fName = fName[:cols-23] + "..."
			}
			if i == selectedIdx {
				frame = append(frame, "\r"+fmt.Sprintf(" %s%s➔ [%2d] %s (%s)%s", Bold, Cyan, i+1, fName, formatBytes(f.Length), Reset)+ClearLine)
			} else {
				frame = append(frame, "\r"+fmt.Sprintf("    [%2d] %s %s(%s)%s", i+1, fName, Dim, formatBytes(f.Length), Reset)+ClearLine)
			}
		}

		for len(frame) < lines-3 {
			frame = append(frame, "\r"+ClearLine)
		}

		frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf("%s[▲/▼]%s Navigate  %s[Enter]%s Stream File  %s[q]%s Cancel", Bold, Reset, Bold, Reset, Bold, Reset)+ClearLine)

		fmt.Print(MoveTop + strings.Join(frame, "\r\n") + "\r")

		k := readKey(fd)
		switch strings.ToLower(k) {
		case "up", "k":
			if selectedIdx > 0 {
				selectedIdx--
			}
		case "down", "j":
			if selectedIdx < len(videoFiles)-1 {
				selectedIdx++
			}
		case "enter", "space":
			return videoFiles[selectedIdx], true
		case "q", "quit", "esc":
			return StreamVideoFile{}, false
		}
	}
}

func addRqbitTorrent(magnetURI, title string, fd int) (int, int, string, int64, error) {
	spinIdx := 0

	type addPostResult struct {
		torrentID int
		files     []struct {
			Name   string `json:"name"`
			Length int64  `json:"length"`
		}
		err error
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	postChan := make(chan addPostResult, 1)
	go func() {
		client := &http.Client{Timeout: 90 * time.Second}
		addURL := fmt.Sprintf("http://127.0.0.1:%d/torrents", StreamPort)
		req, err := http.NewRequestWithContext(ctx, "POST", addURL, strings.NewReader(magnetURI))
		if err != nil {
			postChan <- addPostResult{err: err}
			return
		}
		req.Header.Set("Content-Type", "text/plain")

		resp, err := client.Do(req)
		if err != nil {
			postChan <- addPostResult{err: err}
			return
		}
		defer resp.Body.Close()

		var addResp struct {
			ID      int `json:"id"`
			Details struct {
				Files []struct {
					Name   string `json:"name"`
					Length int64  `json:"length"`
				} `json:"files"`
			} `json:"details"`
			Files []struct {
				Name   string `json:"name"`
				Length int64  `json:"length"`
			} `json:"files"`
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(bodyBytes, &addResp)

		torrentID := addResp.ID
		pauseRqbitTorrent(torrentID)

		fList := addResp.Details.Files
		if len(fList) == 0 {
			fList = addResp.Files
		}

		postChan <- addPostResult{torrentID: torrentID, files: fList}
	}()

	var torrentID int
	var initialFiles []struct {
		Name   string `json:"name"`
		Length int64  `json:"length"`
	}

	startTime := time.Now()
	pollClient := &http.Client{Timeout: 1 * time.Second}
	lastPoll := time.Time{}

	for {
		select {
		case res := <-postChan:
			if res.err != nil {
				if ctx.Err() == context.Canceled {
					return 0, 0, "", 0, fmt.Errorf("stream cancelled by user")
				}
				return 0, 0, "", 0, fmt.Errorf("swarm metadata timed out: %w", res.err)
			}
			torrentID = res.torrentID
			initialFiles = res.files
			goto MetadataLoop

		default:
			if hasKeyInput(fd, 100*time.Millisecond) {
				k := readKey(fd)
				if strings.ToLower(k) == "q" || k == "ESC" || k == "QUIT" {
					cancel()
					return 0, 0, "", 0, fmt.Errorf("stream cancelled by user")
				}
			}

			// Concurrently check if rqbit has already registered the torrent
			if time.Since(lastPoll) >= 500*time.Millisecond {
				lastPoll = time.Now()
				listResp, err := pollClient.Get(fmt.Sprintf("http://127.0.0.1:%d/torrents", StreamPort))
				if err == nil && listResp.StatusCode == 200 {
					var torrentsData struct {
						Torrents []struct {
							ID    int    `json:"id"`
							Name  string `json:"name"`
							Files []struct {
								Name   string `json:"name"`
								Length int64  `json:"length"`
							} `json:"files"`
						} `json:"torrents"`
					}
					if err := json.NewDecoder(listResp.Body).Decode(&torrentsData); err == nil {
						listResp.Body.Close()
						if len(torrentsData.Torrents) > 0 {
							t := torrentsData.Torrents[0]
							torrentID = t.ID
							pauseRqbitTorrent(torrentID)
							initialFiles = t.Files
							cancel()
							goto MetadataLoop
						}
					} else {
						listResp.Body.Close()
					}
				} else if listResp != nil {
					listResp.Body.Close()
				}
			}

			elapsed := int(time.Since(startTime).Seconds())
			if elapsed >= 90 {
				cancel()
				return 0, 0, "", 0, fmt.Errorf("swarm metadata resolution timed out (90s)")
			}

			spinIdx++
			msg := fmt.Sprintf("Contacting tracker swarm... (%ds / 90s)", elapsed)
			drawConnectingFrame(fd, title, msg, spinIdx)
		}
	}

MetadataLoop:
	extractCandidates := func(fList []struct {
		Name   string `json:"name"`
		Length int64  `json:"length"`
	}) []StreamVideoFile {
		var list []StreamVideoFile
		for idx, f := range fList {
			ext := strings.ToLower(filepath.Ext(f.Name))
			if videoExts[ext] {
				list = append(list, StreamVideoFile{Index: idx, Name: f.Name, Length: f.Length})
			}
		}
		if len(list) == 0 {
			for idx, f := range fList {
				list = append(list, StreamVideoFile{Index: idx, Name: f.Name, Length: f.Length})
			}
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Name < list[j].Name
		})
		return list
	}

	var candidateFiles []StreamVideoFile
	if len(initialFiles) > 0 {
		candidateFiles = extractCandidates(initialFiles)
	}

	if len(candidateFiles) == 0 {
		pollClient := &http.Client{Timeout: 2 * time.Second}
		metaStart := time.Now()
		for time.Since(metaStart) < 35*time.Second {
			if hasKeyInput(fd, 150*time.Millisecond) {
				k := readKey(fd)
				if strings.ToLower(k) == "q" || k == "ESC" || k == "QUIT" {
					deleteRqbitTorrent(torrentID)
					return 0, 0, "", 0, fmt.Errorf("stream cancelled by user")
				}
			}
			spinIdx++
			elapsedMeta := int(time.Since(metaStart).Seconds())
			drawConnectingFrame(fd, title, fmt.Sprintf("Resolving video container & tracks... (%ds)", elapsedMeta), spinIdx)

			getResp, err := pollClient.Get(fmt.Sprintf("http://127.0.0.1:%d/torrents/%d", StreamPort, torrentID))
			if err == nil && getResp.StatusCode == 200 {
				var detailResp struct {
					Files []struct {
						Name   string `json:"name"`
						Length int64  `json:"length"`
					} `json:"files"`
					Details struct {
						Files []struct {
							Name   string `json:"name"`
							Length int64  `json:"length"`
						} `json:"files"`
					} `json:"details"`
				}
				_ = json.NewDecoder(getResp.Body).Decode(&detailResp)
				getResp.Body.Close()

				fList := detailResp.Files
				if len(fList) == 0 {
					fList = detailResp.Details.Files
				}

				if len(fList) > 0 {
					candidateFiles = extractCandidates(fList)
					if len(candidateFiles) > 0 {
						break
					}
				}
			} else if getResp != nil {
				getResp.Body.Close()
			}
		}
	}

	if len(candidateFiles) == 0 {
		deleteRqbitTorrent(torrentID)
		return 0, 0, "", 0, fmt.Errorf("swarm metadata timed out (could not read file list)")
	}

	var chosen StreamVideoFile
	if len(candidateFiles) == 1 {
		chosen = candidateFiles[0]
	} else {
		picked, ok := selectEpisodeInteractive(candidateFiles, fd)
		if !ok {
			deleteRqbitTorrent(torrentID)
			return 0, 0, "", 0, fmt.Errorf("stream cancelled by user")
		}
		chosen = picked
	}

	setRqbitOnlyFile(torrentID, chosen.Index)
	resumeRqbitTorrent(torrentID)

	return torrentID, chosen.Index, chosen.Name, chosen.Length, nil
}

func preBufferStream(torrentID int, fileSize int64, title, fileName, cleanName, streamURL string, fd int) bool {
	minBuffer := int64(25 * 1024 * 1024)
	if fileSize > 0 && fileSize < 100*1024*1024 {
		minBuffer = fileSize * 15 / 100
	} else if fileSize > 0 && minBuffer > fileSize/2 {
		minBuffer = fileSize / 2
	}

	statsClient := &http.Client{Timeout: 2 * time.Second}
	spinIdx := 0

	for {
		if hasKeyInput(fd, 250*time.Millisecond) {
			k := readKey(fd)
			switch strings.ToLower(k) {
			case "q", "esc", "quit":
				return false
			case "p", "enter", "o":
				return true
			}
		}

		spinIdx++

		resp, err := statsClient.Get(fmt.Sprintf("http://127.0.0.1:%d/torrents/%d/stats/v1", StreamPort, torrentID))
		var stats RqbitStreamStats
		if err == nil && resp.StatusCode == 200 {
			_ = json.NewDecoder(resp.Body).Decode(&stats)
			resp.Body.Close()
		} else if resp != nil {
			resp.Body.Close()
		}

		var buffered int64 = 0
		speedDown := "0.00 MiB/s"
		livePeers := 0
		if stats.Live != nil {
			buffered = stats.Live.Snapshot.DownloadedBytes
			speedDown = stats.Live.DownloadSpeed.HumanReadable
			livePeers = stats.Live.Snapshot.PeerStats.Live
		}
		if buffered == 0 && stats.ProgressBytes > 0 {
			buffered = stats.ProgressBytes
		}

		if buffered >= minBuffer {
			return true
		}

		pct := float64(buffered) / float64(minBuffer) * 100.0
		if pct > 100 {
			pct = 100
		}

		cols, lines := getTerminalSize(fd)
		divLen := cols - 4
		if divLen < 40 {
			divLen = 40
		}

		barWidth := cols - 32
		if barWidth < 12 {
			barWidth = 12
		}
		if barWidth > 35 {
			barWidth = 35
		}
		filled := int(float64(barWidth) * pct / 100.0)
		empty := barWidth - filled

		var frame []string
		hdr := fmt.Sprintf("%s%s⚡ TORQ STREAM | STALL-FREE PRE-BUFFER%s", Bold, Cyan, Reset)
		frame = append(frame, "\r"+hdr+ClearLine)
		frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		frame = append(frame, "\r"+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" %sPre-buffering initial window for stall-free playback...%s", Bold, Reset)+ClearLine)
		dispFile := fileName
		if len(dispFile) > cols-12 {
			dispFile = dispFile[:cols-15] + "..."
		}
		frame = append(frame, "\r"+fmt.Sprintf(" File: %s%s%s", Yellow, dispFile, Reset)+ClearLine)
		frame = append(frame, "\r"+ClearLine)

		fillStr := fmt.Sprintf("%s%s%s%s", Bold, Green, strings.Repeat("█", filled), Reset)
		emptyStr := fmt.Sprintf("%s%s%s", Dim, strings.Repeat("░", empty), Reset)
		frame = append(frame, "\r"+fmt.Sprintf(" Buffer: [%s%s] %s%5.1f%%%s (%s / %s)", fillStr, emptyStr, Bold, pct, Reset, formatBytes(buffered), formatBytes(minBuffer))+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" Rate:   %s▼ %s%s  |  Swarm: %s%d peers%s", Green, speedDown, Reset, Yellow, livePeers, Reset)+ClearLine)
		frame = append(frame, "\r"+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" %sℹ Rex Player will auto-open once buffer reaches 100%%%s", Dim, Reset)+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" %s[Enter / p]%s Force-play now   %s[q]%s Cancel", Bold, Reset, Bold, Reset)+ClearLine)

		for len(frame) < lines-2 {
			frame = append(frame, "\r"+ClearLine)
		}

		fmt.Print(MoveTop + strings.Join(frame, "\r\n") + "\r")
	}
}

func streamDashboard(item TorrentItem) {
	fd := int(os.Stdin.Fd())

	var oldState *term.State
	if term.IsTerminal(fd) {
		oldState, _ = term.MakeRaw(fd)
	}
	defer func() {
		if oldState != nil {
			_ = term.Restore(fd, oldState)
		}
	}()

	fmt.Print("\033[?25l" + ClearScrn)

	if _, err := exec.LookPath("rqbit"); err != nil {
		fmt.Printf("\r\n%s[!] 'rqbit' streaming engine is not installed.%s\r\nPlease install it using: pkg install rqbit\r\n", Red, Reset)
		fmt.Printf("\r\n%sPress any key to return...%s", Dim, Reset)
		_ = readKey(fd)
		return
	}

	drawConnectingFrame(fd, item.Title, "Preparing ephemeral stream cache...", 0)

	cacheDir, err := os.MkdirTemp("", "torq-stream-*")
	if err != nil {
		cacheDir = filepath.Join(os.TempDir(), fmt.Sprintf("torq-stream-%d", time.Now().UnixNano()))
		_ = os.MkdirAll(cacheDir, 0700)
	}
	defer func() {
		_ = os.RemoveAll(cacheDir)
	}()

	rqCmd, err := startRqbitServer(cacheDir, item.Title, fd)
	if err != nil {
		fmt.Printf("\r\n%s%s✖ Failed to start stream engine: %v%s\r\n", Bold, Red, err, Reset)
		fmt.Printf("\r\n%sPress any key to return...%s", Dim, Reset)
		_ = readKey(fd)
		return
	}
	defer func() {
		if rqCmd != nil && rqCmd.Process != nil {
			_ = rqCmd.Process.Kill()
			_ = rqCmd.Wait()
		}
		_ = exec.Command("pkill", "-9", "rqbit").Run()
	}()

	torrentID, bestFileIdx, fileName, fileSize, err := addRqbitTorrent(item.Magnet, item.Title, fd)
	if err != nil {
		if rqCmd != nil && rqCmd.Process != nil {
			_ = rqCmd.Process.Kill()
			_ = rqCmd.Wait()
		}
		_ = exec.Command("pkill", "-9", "rqbit").Run()
		_ = os.RemoveAll(cacheDir)

		if strings.Contains(strings.ToLower(err.Error()), "cancelled") {
			return
		}

		fmt.Print(ClearScrn + MoveTop)
		cols, _ := getTerminalSize(fd)
		divLen := cols - 4
		if divLen < 40 {
			divLen = 40
		}
		fmt.Printf("\r\n%s%s⚡ TORQ STREAM NOTICE%s\r\n", Bold, Yellow, Reset)
		fmt.Printf("%s%s%s\r\n", Dim, strings.Repeat("━", divLen), Reset)
		fmt.Printf(" %s✖ %v%s\r\n\r\n", Red, err, Reset)
		fmt.Printf(" %s💡 Swarm metadata could not be resolved.%s\r\n", Yellow, Reset)
		fmt.Printf("   Try choosing a release with more active seeders, or\r\n")
		fmt.Printf("   use [Enter] to download via aria2c.\r\n\r\n")
		fmt.Printf("%s%s%s\r\n", Dim, strings.Repeat("━", divLen), Reset)
		fmt.Printf(" %sPress any key to return to search...%s", Dim, Reset)
		_ = readKey(fd)
		return
	}

	defer func() {
		deleteRqbitTorrent(torrentID)
	}()

	cleanName := cleanURLFileName(fileName)
	gateway := startStreamGateway(torrentID, bestFileIdx, cleanName)
	defer func() {
		_ = gateway.Close()
	}()

	streamURL := fmt.Sprintf("http://127.0.0.1:%d/stream/%s", StreamGatewayPort, cleanName)
	lanIP := getOutboundIP()
	lanURL := ""
	if lanIP != "" {
		lanURL = fmt.Sprintf("http://%s:%d/stream/%s", lanIP, StreamGatewayPort, cleanName)
	}

	go copyToClipboard(streamURL)

	if ok := preBufferStream(torrentID, fileSize, item.Title, fileName, cleanName, streamURL, fd); !ok {
		return
	}

	openVideoPlayer(streamURL)

	codec := detectCodec(item.Title + " " + fileName)
	runStreamLoop(torrentID, item.Title, fileName, cleanName, codec, fileSize, streamURL, lanURL, cacheDir, fd)
}

func runStreamLoop(torrentID int, title, fileName, cleanName, codec string, fileSize int64, streamURL, lanURL, cacheDir string, fd int) {
	statusMsg := "Player intent launched! Tap play in Rex Player."
	statsClient := &http.Client{Timeout: 2 * time.Second}

	for {
		statsURL := fmt.Sprintf("http://127.0.0.1:%d/torrents/%d/stats/v1", StreamPort, torrentID)
		resp, err := statsClient.Get(statsURL)

		var stats RqbitStreamStats
		if err == nil && resp.StatusCode == 200 {
			_ = json.NewDecoder(resp.Body).Decode(&stats)
			resp.Body.Close()
		} else if resp != nil {
			resp.Body.Close()
		}

		var bufferedBytes int64 = 0
		speedDown := "0.00 MiB/s"
		speedUp := "0.00 MiB/s"
		livePeers := 0

		if stats.Live != nil {
			bufferedBytes = stats.Live.Snapshot.DownloadedBytes
			speedDown = stats.Live.DownloadSpeed.HumanReadable
			speedUp = stats.Live.UploadSpeed.HumanReadable
			livePeers = stats.Live.Snapshot.PeerStats.Live
		}
		if bufferedBytes == 0 && stats.ProgressBytes > 0 {
			bufferedBytes = stats.ProgressBytes
		}

		totBytes := fileSize
		if totBytes <= 0 {
			totBytes = stats.TotalBytes
		}
		pct := 0.0
		if totBytes > 0 {
			pct = float64(bufferedBytes) / float64(totBytes) * 100
			if pct > 100 {
				pct = 100
			}
		}

		cols, lines := getTerminalSize(fd)
		divLen := cols - 4
		if divLen < 40 {
			divLen = 40
		}

		barWidth := cols - 32
		if barWidth < 12 {
			barWidth = 12
		}
		if barWidth > 40 {
			barWidth = 40
		}
		filled := int(float64(barWidth) * pct / 100.0)
		if filled > barWidth {
			filled = barWidth
		}
		empty := barWidth - filled

		var frame []string
		hdr := fmt.Sprintf("%s%s⚡ TORQ STREAM ENGINE%s | %s%sZERO-DISK MODE%s", Bold, Cyan, Reset, Bold, Green, Reset)
		frame = append(frame, "\r"+hdr+ClearLine)
		frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)

		dispTitle := title
		if len(dispTitle) > cols-12 {
			dispTitle = dispTitle[:cols-15] + "..."
		}
		dispFile := fileName
		if len(dispFile) > cols-12 {
			dispFile = dispFile[:cols-15] + "..."
		}

		frame = append(frame, "\r"+fmt.Sprintf(" %sTitle:%s  %s%s%s", Bold, Reset, Bold, dispTitle, Reset)+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" %sFile:%s   %s%s%s (%s)", Bold, Reset, Yellow, dispFile, Reset, formatBytes(fileSize))+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" %sCodec:%s  %s%s%s", Bold, Reset, Cyan, codec, Reset)+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" %sStatus:%s %s▶ STREAMING LIVE TO PLAYER%s", Bold, Reset, Green, Reset)+ClearLine)
		frame = append(frame, "\r"+ClearLine)

		frame = append(frame, "\r"+fmt.Sprintf(" %sDIRECT VIDEO STREAM URLS:%s", Bold, Reset)+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf("  %s🔗 Rex Player / Local:%s %s%s%s", Bold, Reset, Cyan, streamURL, Reset)+ClearLine)
		if lanURL != "" {
			frame = append(frame, "\r"+fmt.Sprintf("  %s🌐 Wi-Fi / TV LAN:     %s %s%s%s", Bold, Reset, Magenta, lanURL, Reset)+ClearLine)
		}
		frame = append(frame, "\r"+ClearLine)

		fillStr := fmt.Sprintf("%s%s%s%s", Bold, Green, strings.Repeat("█", filled), Reset)
		emptyStr := fmt.Sprintf("%s%s%s", Dim, strings.Repeat("░", empty), Reset)
		barDisplay := fmt.Sprintf(" Buffer: [%s%s] %s%5.1f%%%s (%s / %s)", fillStr, emptyStr, Bold, pct, Reset, formatBytes(bufferedBytes), formatBytes(totBytes))
		frame = append(frame, "\r"+barDisplay+ClearLine)

		metrics := fmt.Sprintf(" Stream Rate: %s▼ %s%s  %s▲ %s%s  |  Swarm: %s%d peers%s",
			Green, speedDown, Reset,
			Dim, speedUp, Reset,
			Yellow, livePeers, Reset)
		frame = append(frame, "\r"+metrics+ClearLine)
		frame = append(frame, "\r"+ClearLine)

		if codec == "AV1" {
			frame = append(frame, "\r"+fmt.Sprintf(" %s⚠️ AV1 Notice: If video is blank, your device lacks AV1 HW decode. Try an x264/x265 release.%s", Yellow, Reset)+ClearLine)
		}

		frame = append(frame, "\r"+fmt.Sprintf(" %s🔒 ZERO-DISK GUARANTEE:%s", Yellow, Reset)+ClearLine)
		frame = append(frame, "\r"+fmt.Sprintf(" %sEphemeral cache active. No file is saved to Downloads. Purged on exit.%s", Dim, Reset)+ClearLine)

		for len(frame) < lines-3 {
			frame = append(frame, "\r"+ClearLine)
		}

		if statusMsg != "" {
			frame = append(frame, "\r"+fmt.Sprintf("%s✔ %s%s", Green, statusMsg, Reset)+ClearLine)
			statusMsg = ""
		} else {
			frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)
		}

		footer := fmt.Sprintf("%s[o]%s Re-open in Player  %s[c]%s Copy URL  %s[q]%s Stop & Purge Cache",
			Bold, Reset, Bold, Reset, Bold, Reset)
		frame = append(frame, "\r"+footer+ClearLine)

		fmt.Print(MoveTop + strings.Join(frame, "\r\n") + "\r")

		if hasKeyInput(fd, 400*time.Millisecond) {
			k := readKey(fd)
			switch strings.ToLower(k) {
			case "o":
				openVideoPlayer(streamURL)
				statusMsg = "Re-sent player intent for Rex Player."
			case "c":
				if copyToClipboard(streamURL) {
					statusMsg = "Stream URL copied to clipboard!"
				} else {
					statusMsg = "Failed to copy URL."
				}
			case "q", "quit", "esc", "x":
				return
			}
		}
	}
}

func handleStreamCommand(target, sourceFlag, qualityFlag string) {
	if strings.HasPrefix(target, "magnet:?") {
		item := TorrentItem{
			Title:  "Direct Magnet Stream",
			Magnet: target,
			Source: "Magnet",
		}
		streamDashboard(item)
		return
	}

	if target == "" {
		fmt.Printf("%sEnter search query to stream (or magnet URI): %s", Bold, Reset)
		reader := bufio.NewReader(os.Stdin)
		inputTarget, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(inputTarget) == "" {
			fmt.Printf("%sNo stream target provided.%s\n", Red, Reset)
			return
		}
		target = strings.TrimSpace(inputTarget)
		if strings.HasPrefix(target, "magnet:?") {
			item := TorrentItem{
				Title:  "Direct Magnet Stream",
				Magnet: target,
				Source: "Magnet",
			}
			streamDashboard(item)
			return
		}
	}

	results := performSearch(target, sourceFlag)
	if len(results) == 0 {
		fmt.Printf("%sNo results found for '%s'.%s\n", Yellow, target, Reset)
		return
	}

	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Seeders > results[j].Seeders
	})

	streamDashboard(results[0])
}

func cancelDownloads(target string) {
	ensureAriaDaemon(getDownloadsDir())
	active := getActiveTasks()
	if len(active) == 0 {
		fmt.Printf("%sNo active or waiting downloads to cancel.%s\n", Yellow, Reset)
		return
	}
	if target == "all" || target == "" {
		for _, t := range active {
			_, _ = ariaRPC("aria2.forceRemove", []interface{}{t.GID})
			_, _ = ariaRPC("aria2.remove", []interface{}{t.GID})
		}
		_, _ = ariaRPC("aria2.purgeDownloadResult", nil)
		fmt.Printf("%s✔ Cancelled %d active download(s).%s\n", Green, len(active), Reset)
		return
	}
	num, err := strconv.Atoi(target)
	if err == nil && num >= 1 && num <= len(active) {
		t := active[num-1]
		_, _ = ariaRPC("aria2.forceRemove", []interface{}{t.GID})
		_, _ = ariaRPC("aria2.remove", []interface{}{t.GID})
		_, _ = ariaRPC("aria2.purgeDownloadResult", nil)
		fmt.Printf("%s✔ Cancelled download #%d.%s\n", Green, num, Reset)
		return
	}
	fmt.Printf("%sInvalid download index: %s%s\n", Red, target, Reset)
}

func pauseDownloads(target string) {
	ensureAriaDaemon(getDownloadsDir())
	active := getActiveTasks()
	if len(active) == 0 {
		fmt.Printf("%sNo active downloads to pause.%s\n", Yellow, Reset)
		return
	}
	if target == "all" || target == "" {
		_, _ = ariaRPC("aria2.pauseAll", nil)
		fmt.Printf("%s✔ Paused all active downloads.%s\n", Green, Reset)
		return
	}
	num, err := strconv.Atoi(target)
	if err == nil && num >= 1 && num <= len(active) {
		t := active[num-1]
		_, _ = ariaRPC("aria2.pause", []interface{}{t.GID})
		fmt.Printf("%s✔ Paused download #%d.%s\n", Green, num, Reset)
		return
	}
	fmt.Printf("%sInvalid download index: %s%s\n", Red, target, Reset)
}

func resumeDownloads(target string) {
	ensureAriaDaemon(getDownloadsDir())
	active := getActiveTasks()
	if len(active) == 0 {
		fmt.Printf("%sNo downloads to resume.%s\n", Yellow, Reset)
		return
	}
	if target == "all" || target == "" {
		_, _ = ariaRPC("aria2.unpauseAll", nil)
		fmt.Printf("%s✔ Resumed all downloads.%s\n", Green, Reset)
		return
	}
	num, err := strconv.Atoi(target)
	if err == nil && num >= 1 && num <= len(active) {
		t := active[num-1]
		_, _ = ariaRPC("aria2.unpause", []interface{}{t.GID})
		fmt.Printf("%s✔ Resumed download #%d.%s\n", Green, num, Reset)
		return
	}
	fmt.Printf("%sInvalid download index: %s%s\n", Red, target, Reset)
}

func parseSpeedLimit(s string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" || s == "none" || s == "off" || s == "unlimited" || s == "max" {
		return "0", nil
	}
	s = strings.TrimSuffix(s, "/s")
	s = strings.TrimSuffix(s, "ps")
	s = strings.TrimSuffix(s, "b")
	s = strings.TrimSuffix(s, "i")

	if strings.HasSuffix(s, "k") {
		val := strings.TrimSuffix(s, "k")
		if _, err := strconv.ParseFloat(val, 64); err == nil {
			return strings.ToUpper(val) + "K", nil
		}
	}
	if strings.HasSuffix(s, "m") {
		val := strings.TrimSuffix(s, "m")
		if _, err := strconv.ParseFloat(val, 64); err == nil {
			return strings.ToUpper(val) + "M", nil
		}
	}
	if strings.HasSuffix(s, "g") {
		val := strings.TrimSuffix(s, "g")
		if _, err := strconv.ParseFloat(val, 64); err == nil {
			return strings.ToUpper(val) + "G", nil
		}
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return s, nil
	}
	return "", fmt.Errorf("invalid speed limit '%s'. Examples: 2M, 500K, 0", s)
}

func formatSpeedBadge(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return fmt.Sprintf("%sUnlimited%s (0)", Green, Reset)
	}
	if b, err := strconv.ParseInt(s, 10, 64); err == nil && b > 0 {
		return fmt.Sprintf("%s%s%s", Yellow, formatSpeed(b), Reset)
	}
	return fmt.Sprintf("%s%s/s%s", Yellow, s, Reset)
}

func handleLimitCommand(downArg, upArg string) {
	ensureAriaDaemon(getDownloadsDir())
	cfg := loadTorqConfig()

	// If no arguments provided, display current limit status
	if downArg == "" && upArg == "" {
		liveDown := cfg.MaxDownloadLimit
		liveUp := cfg.MaxUploadLimit
		if raw, err := ariaRPC("aria2.getGlobalOption", nil); err == nil {
			var opts map[string]string
			if json.Unmarshal(raw, &opts) == nil {
				if d, ok := opts["max-overall-download-limit"]; ok && d != "" {
					liveDown = d
				}
				if u, ok := opts["max-overall-upload-limit"]; ok && u != "" {
					liveUp = u
				}
			}
		}

		fmt.Printf("\r\n%s%s⚡ TORQ BANDWIDTH LIMITER%s\r\n", Bold, Cyan, Reset)
		fmt.Printf("%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\r\n", Dim, Reset)
		fmt.Printf("  %sDownload Cap:%s  %s\r\n", Bold, Reset, formatSpeedBadge(liveDown))
		fmt.Printf("  %sUpload Cap:  %s  %s\r\n", Bold, Reset, formatSpeedBadge(liveUp))
		fmt.Printf("%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\r\n", Dim, Reset)
		fmt.Printf(" %sUsage:%s\r\n", Bold, Reset)
		fmt.Printf("   torq limit <speed>       %s(e.g., torq limit 2M, torq limit 500K)%s\r\n", Dim, Reset)
		fmt.Printf("   torq limit <down> <up>   %s(e.g., torq limit 3M 500K)%s\r\n", Dim, Reset)
		fmt.Printf("   torq limit 0             %s(Remove all caps / Full unlimited speed)%s\r\n\r\n", Dim, Reset)
		return
	}

	parsedDown, err := parseSpeedLimit(downArg)
	if err != nil {
		fmt.Printf("\r\n%s✖ %v%s\r\n\r\n", Red, err, Reset)
		return
	}

	parsedUp := cfg.MaxUploadLimit
	if upArg != "" {
		u, err := parseSpeedLimit(upArg)
		if err != nil {
			fmt.Printf("\r\n%s✖ %v%s\r\n\r\n", Red, err, Reset)
			return
		}
		parsedUp = u
	}

	// Update live running aria2 daemon
	changeOpts := map[string]string{
		"max-overall-download-limit": parsedDown,
		"max-overall-upload-limit":   parsedUp,
	}
	_, err = ariaRPC("aria2.changeGlobalOption", []interface{}{changeOpts})
	if err != nil {
		fmt.Printf("\r\n%s✖ Failed communicating with aria2 daemon: %v%s\r\n\r\n", Red, err, Reset)
		return
	}

	// Persist to configuration
	cfg.MaxDownloadLimit = parsedDown
	cfg.MaxUploadLimit = parsedUp
	_ = saveTorqConfig(cfg)

	fmt.Printf("\r\n%s%s⚡ TORQ BANDWIDTH LIMITER UPDATED%s\r\n", Bold, Green, Reset)
	fmt.Printf("%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\r\n", Dim, Reset)
	fmt.Printf("  %s✔ Download Cap:%s  %s\r\n", Bold, Reset, formatSpeedBadge(parsedDown))
	fmt.Printf("  %s✔ Upload Cap:  %s  %s\r\n", Bold, Reset, formatSpeedBadge(parsedUp))
	fmt.Printf("%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\r\n", Dim, Reset)
	if parsedDown != "0" {
		fmt.Printf(" Active and future background downloads will respect this limit.\r\n")
		fmt.Printf(" %sTo remove limit anytime: torq limit 0%s\r\n\r\n", Dim, Reset)
	} else {
		fmt.Printf(" Speed limits removed. Downloading at maximum connection rate!\r\n\r\n")
	}
}

func getTaskDisplayName(t AriaTask) string {
	if t.Bittorrent != nil && t.Bittorrent.Info != nil && t.Bittorrent.Info.Name != "" {
		return t.Bittorrent.Info.Name
	}
	if len(t.Files) > 0 && t.Files[0].Path != "" {
		p := t.Files[0].Path
		if strings.HasPrefix(p, "[METADATA]") {
			clean := strings.TrimPrefix(p, "[METADATA]")
			if unesc, err := url.QueryUnescape(clean); err == nil {
				return unesc
			}
			return clean
		}
		return filepath.Base(p)
	}
	return "Active Torrent"
}

func queueManager(destDir string) {
	ensureAriaDaemon(destDir)

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		defer term.Restore(fd, oldState)
	}
	fmt.Print("\033[?1049h\033[?25l" + ClearScrn)
	defer fmt.Print("\033[?25h\033[?1049l\r\n")

	selectedIdx := 0
	statusMsg := ""

	for {
		cols, lines, err := term.GetSize(fd)
		if err != nil || cols <= 0 {
			cols, lines = 80, 24
		}

		activeTasks := getActiveTasks()
		totalItems := len(activeTasks)

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

		var frame []string
		cfg := loadTorqConfig()
		capBadge := ""
		if cfg.MaxDownloadLimit != "0" {
			capBadge = fmt.Sprintf(" %s[Cap: %s]%s", Yellow, cfg.MaxDownloadLimit, Reset)
		}
		hdr := fmt.Sprintf("%s%s⚡ TORQ ACTIVE DOWNLOAD QUEUE%s%s %s───%s %s%d Active%s", Bold, Cyan, Reset, capBadge, Dim, Reset, Yellow, totalItems, Reset)
		frame = append(frame, "\r"+hdr+ClearLine)
		frame = append(frame, "\r"+Dim+strings.Repeat("━", divLen)+Reset+ClearLine)

		if totalItems == 0 {
			frame = append(frame, "\r"+ClearLine)
			frame = append(frame, "\r"+fmt.Sprintf("   %sℹ No active or paused downloads in queue.%s", Yellow, Reset)+ClearLine)
			frame = append(frame, "\r"+ClearLine)
			frame = append(frame, "\r"+fmt.Sprintf("   %sSearch & start any download anytime using:%s", Dim, Reset)+ClearLine)
			frame = append(frame, "\r"+fmt.Sprintf("   %s%storq \"<media name>\"%s", Bold, White, Reset)+ClearLine)
			frame = append(frame, "\r"+ClearLine)
		} else {
			for idx, t := range activeTasks {
				isActive := (idx == selectedIdx)
				tName := getTaskDisplayName(t)
				maxTitle := cols - 16
				if maxTitle < 12 {
					maxTitle = 12
				}
				titleDisp := truncateRunes(tName, maxTitle)

				tot, _ := strconv.ParseInt(t.TotalLength, 10, 64)
				done, _ := strconv.ParseInt(t.CompletedLength, 10, 64)
				spd, _ := strconv.ParseInt(t.DownloadSpeed, 10, 64)
				seeders, _ := strconv.Atoi(t.NumSeeders)
				conns, _ := strconv.Atoi(t.Connections)

				isMeta := false
				if len(t.Files) > 0 && (strings.HasPrefix(t.Files[0].Path, "[METADATA]") || strings.HasPrefix(t.Files[0].Path, "[MEMORY]")) {
					isMeta = true
				}

				pct := 0.0
				if tot > 0 && !isMeta {
					pct = (float64(done) / float64(tot)) * 100.0
				}
				var etaSec int64
				if spd > 0 && tot > done {
					etaSec = (tot - done) / spd
				}

				var line1 string
				if isActive {
					line1 = fmt.Sprintf(" %s %s[%2d]%s %s%s%s", HlArrow, Cyan, idx+1, Reset, HlBg, titleDisp, Reset)
				} else {
					line1 = fmt.Sprintf("   %s[%2d]%s %s", Cyan, idx+1, Reset, titleDisp)
				}

				statusBadge := ""
				switch {
				case isMeta:
					statusBadge = fmt.Sprintf("%s◐ METADATA%s", Magenta, Reset)
				case t.Status == "active":
					statusBadge = fmt.Sprintf("%s● DOWNLOADING (%s)%s", Green, formatSpeed(spd), Reset)
				case t.Status == "paused":
					statusBadge = fmt.Sprintf("%s❚❚ PAUSED%s", Yellow, Reset)
				case t.Status == "complete":
					statusBadge = fmt.Sprintf("%s✔ COMPLETED%s", Cyan, Reset)
				default:
					statusBadge = fmt.Sprintf("%s● %s%s", Yellow, strings.ToUpper(t.Status), Reset)
				}

				barW := cols - 35
				if barW < 10 {
					barW = 10
				}
				if barW > 25 {
					barW = 25
				}
				filled := int(float64(barW) * pct / 100.0)
				if filled > barW {
					filled = barW
				}
				empty := barW - filled
				if empty < 0 {
					empty = 0
				}
				fillStr := fmt.Sprintf("%s%s%s%s", Bold, Cyan, strings.Repeat("█", filled), Reset)
				emptyStr := fmt.Sprintf("%s%s%s", Dim, strings.Repeat("░", empty), Reset)
				barDisp := fmt.Sprintf("[%s%s] %5.1f%%", fillStr, emptyStr, pct)

				line2 := fmt.Sprintf("       %s  %s", barDisp, statusBadge)

				line3 := fmt.Sprintf("       %sData:%s %s/%s  %sPeers:%s %d (%d seeds)  %sETA:%s %s",
					Bold, Reset, formatBytes(done), formatBytes(tot),
					Bold, Reset, conns, seeders,
					Bold, Reset, formatTime(etaSec))

				frame = append(frame, "\r"+line1+ClearLine)
				frame = append(frame, "\r"+line2+ClearLine)
				frame = append(frame, "\r"+line3+ClearLine)
				frame = append(frame, "\r"+ClearLine)
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

		var footer string
		if totalItems > 0 {
			footer = fmt.Sprintf("%s[▲/▼]%s Move  %s[Enter]%s Dashboard  %s[p]%s Pause/Resume  %s[c]%s Cancel  %s[q]%s Return",
				Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset)
		} else {
			footer = fmt.Sprintf("%s[q/Esc/Enter]%s Return to Terminal", Bold, Reset)
		}
		frame = append(frame, "\r"+footer+ClearLine)

		fmt.Print(MoveTop + strings.Join(frame, "\r\n") + "\r")

		if hasKeyInput(fd, 500*time.Millisecond) {
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
			case "home":
				selectedIdx = 0
			case "end":
				if totalItems > 0 {
					selectedIdx = totalItems - 1
				}
			case "p", "space":
				if totalItems > 0 {
					t := activeTasks[selectedIdx]
					if t.Status == "paused" {
						unpauseDownload(t.GID)
						statusMsg = "Download resumed!"
					} else {
						pauseDownload(t.GID)
						statusMsg = "Download paused!"
					}
				}
			case "c", "x":
				if totalItems > 0 {
					t := activeTasks[selectedIdx]
					removeDownload(t.GID)
					_, _ = ariaRPC("aria2.purgeDownloadResult", nil)
					statusMsg = "Download cancelled!"
					if selectedIdx >= totalItems-1 && selectedIdx > 0 {
						selectedIdx--
					}
				}
			case "enter", "a":
				if totalItems > 0 {
					t := activeTasks[selectedIdx]
					tName := getTaskDisplayName(t)
					runDownloadLoop(t.GID, tName, "Queue", destDir, fd)
					fmt.Print("\033[?1049h\033[?25l" + ClearScrn)
				} else {
					return
				}
			}
		}
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
	sourceFilters := []string{"ALL", "ThePirateBay", "YTS", "Nyaa", "AnimeTosho"}
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
		switch srcLabel {
		case "ThePirateBay":
			srcLabel = "TPB"
		case "AnimeTosho":
			srcLabel = "Tosho"
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
				switch itemSrc {
				case "ThePirateBay":
					itemSrc = "TPB"
				case "AnimeTosho":
					itemSrc = "Tosho"
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

		footer := fmt.Sprintf("%s[▲/▼]%s Move  %s[Enter]%s DL  %s[p]%s Stream  %s[m]%s Mag  %s[t]%s Tier  %s[s]%s Src  %s[/]%s Filter  %s[q]%s Quit",
			Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset)
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
		case "p":
			if totalItems > 0 {
				item := filtered[selectedIdx]
				streamDashboard(item)
				fmt.Print("\033[?1049h\033[?25l" + ClearScrn)
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

func performSearch(queryStr, sourceFlag string) []TorrentItem {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var rawResults []TorrentItem
	var mu sync.Mutex

	src := strings.ToLower(sourceFlag)
	if src == "all" || src == "tpb" || src == "thepiratebay" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := searchTPB(ctx, queryStr)
			mu.Lock()
			rawResults = append(rawResults, items...)
			mu.Unlock()
		}()
	}

	if src == "all" || src == "yts" || src == "yify" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := searchYTS(ctx, queryStr)
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

	if src == "all" || src == "tosho" || src == "animetosho" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := searchAnimeTosho(ctx, queryStr)
			mu.Lock()
			rawResults = append(rawResults, items...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Deduplicate items with identical info_hashes across multi-trackers
	seenHashes := make(map[string]bool)
	var dedupedResults []TorrentItem
	for _, it := range rawResults {
		key := it.Magnet
		if idx := strings.Index(key, "xt=urn:btih:"); idx != -1 {
			hashPart := key[idx+12:]
			if ampersand := strings.Index(hashPart, "&"); ampersand != -1 {
				hashPart = hashPart[:ampersand]
			}
			key = strings.ToLower(hashPart)
		}
		if key != "" && seenHashes[key] {
			continue
		}
		if key != "" {
			seenHashes[key] = true
		}
		dedupedResults = append(dedupedResults, it)
	}
	return dedupedResults
}

func main() {
	sourceFlag := flag.String("s", "all", "Tracker source to query (all, tpb, yts, nyaa, tosho)")
	qualityFlag := flag.String("q", "all", "Filter by quality section (highest, high, medium, low)")
	listFlag := flag.Bool("l", false, "List search results with sections and exit")
	magnetFlag := flag.Bool("m", false, "Print magnet link of top result and exit")
	downloadFlag := flag.Bool("d", false, "Instantly download top result via loading dashboard")
	streamFlag := flag.Bool("p", false, "Stream top result immediately in video player (Rex Player)")
	flag.BoolVar(streamFlag, "stream", false, "Stream top result immediately in video player (Rex Player)")
	updateFlag := flag.Bool("u", false, "Check for and install updates from GitHub")
	versionFlag := flag.Bool("v", false, "Show program version and exit")

	flag.Usage = func() {
		fmt.Printf("%s⚡ torq %s - Lightweight, keyboard-driven multi-tracker media engine for your terminal%s\n\n", Bold, Version, Reset)
		fmt.Println("Usage: torq [flags] [query | stream | queue | cancel | pause | resume | limit]")
		fmt.Println("\nCommands:")
		fmt.Println("  torq stream <query|magnet>  Stream video directly in Rex Player (Zero-Disk)")
		fmt.Println("  torq queue                  View active downloads and recent completed media")
		fmt.Println("  torq limit [speed]          Set or view download speed limit (e.g. 2M, 500K, 0)")
		fmt.Println("  torq cancel [all | N]       Cancel active background download(s)")
		fmt.Println("  torq pause  [all | N]       Pause active background download(s)")
		fmt.Println("  torq resume [all | N]       Resume paused background download(s)")
		fmt.Println("\nFlags:")
		flag.PrintDefaults()
		fmt.Println("\nExamples:")
		fmt.Println("  torq \"Doraemon\"")
		fmt.Println("  torq stream \"Interstellar\"")
		fmt.Println("  torq stream \"magnet:?xt=...\"")
		fmt.Println("  torq queue")
		fmt.Println("  torq limit 2M")
		fmt.Println("  torq limit 0")
		fmt.Println("  torq cancel")
		fmt.Println("  torq resume")
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
	firstWord := ""
	if len(args) > 0 {
		firstWord = strings.ToLower(args[0])
	}

	if firstWord == "stream" || firstWord == "play" {
		target := strings.TrimSpace(strings.Join(args[1:], " "))
		handleStreamCommand(target, *sourceFlag, *qualityFlag)
		return
	}

	if firstWord == "limit" || firstWord == "throttle" || firstWord == "speed" {
		downArg := ""
		upArg := ""
		if len(args) > 1 {
			downArg = args[1]
		}
		if len(args) > 2 {
			upArg = args[2]
		}
		handleLimitCommand(downArg, upArg)
		return
	}

	if firstWord == "cancel" || firstWord == "stop" {
		target := "all"
		if len(args) > 1 {
			target = strings.ToLower(args[1])
		}
		cancelDownloads(target)
		return
	}

	if firstWord == "pause" {
		target := "all"
		if len(args) > 1 {
			target = strings.ToLower(args[1])
		}
		pauseDownloads(target)
		return
	}

	if firstWord == "resume" || firstWord == "unpause" {
		target := "all"
		if len(args) > 1 {
			target = strings.ToLower(args[1])
		}
		resumeDownloads(target)
		return
	}

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

	rawResults := performSearch(queryStr, *sourceFlag)

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

	if *streamFlag {
		sort.SliceStable(sortedResults, func(i, j int) bool {
			return sortedResults[i].Seeders > sortedResults[j].Seeders
		})
		streamDashboard(sortedResults[0])
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
			srcDisp := it.Source
			switch srcDisp {
			case "ThePirateBay":
				srcDisp = "TPB"
			case "AnimeTosho":
				srcDisp = "Tosho"
			}
			srcTag := fmt.Sprintf("%s[%s]%s", Magenta, srcDisp, Reset)
			fmt.Printf(" [%2d] %s %s%s%s\n", idx+1, srcTag, Bold, it.Title, Reset)
			fmt.Printf("      Size: %s%s%s | Seeders: %s%d%s | Leechers: %s%d%s\n",
				Yellow, it.Size, Reset, sColor, it.Seeders, Reset, Dim, it.Leechers, Reset)
		}
		fmt.Println()
		return
	}

	runTUI(sortedResults, queryStr)
}
