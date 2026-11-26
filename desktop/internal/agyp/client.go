package agyp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseDir    string
	gatewayURL string
	httpClient *http.Client

	titleMu    sync.Mutex
	titleCache map[string]sessionMeta
	titleAt    time.Time

	sessionCacheMu sync.Mutex
	sessionCache   map[string]*cachedLocalSession
}

type sessionMeta struct {
	Title     string `json:"title"`
	ParentID  string `json:"parent"`
	AgentName string `json:"agent"`
}

type cachedLocalSession struct {
	stamp  string
	parsed *localSessionData
}

func NewClient(baseDir string) *Client {
	if baseDir == "" {
		if envHome := strings.TrimSpace(os.Getenv("AGYP_HOME")); envHome != "" {
			baseDir = envHome
		} else {
			home, _ := os.UserHomeDir()
			baseDir = filepath.Join(home, ".agyp")
		}
	}
	return &Client{
		baseDir:    baseDir,
		gatewayURL: "http://127.0.0.1:8783",
		httpClient: &http.Client{Timeout: 1500 * time.Millisecond},
	}
}

func resolveActiveProfileName(profilesRaw map[string]any) string {
	if profilesRaw == nil {
		return ""
	}
	profsMap, _ := profilesRaw["profiles"].(map[string]any)
	if ap, ok := profilesRaw["active_profile"].(string); ok && ap != "" {
		if len(profsMap) == 0 {
			return ap
		}
		if _, exists := profsMap[ap]; exists {
			return ap
		}
	}
	if len(profsMap) == 0 {
		return ""
	}
	keys := make([]string, 0, len(profsMap))
	for k := range profsMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}

func (c *Client) FetchDiag() (*LiveDiag, error) {
	diag := &LiveDiag{
		Online:   false,
		PoolIdle: make(map[string]int),
	}

	resp, err := c.httpClient.Get(c.gatewayURL + "/_agyp/diag")
	if err == nil && resp.StatusCode == 200 {
		diag.Online = true
		defer resp.Body.Close()
		var raw struct {
			PID             int      `json:"pid"`
			ActiveProfile   string   `json:"active_profile"`
			PausedProfiles  []string `json:"paused_profiles"`
			EnabledProfiles []string `json:"enabled_profiles"`
			Pool            struct {
				HitRate float64        `json:"hit_rate"`
				Hit     int            `json:"hit"`
				Miss    int            `json:"miss"`
				Idle    map[string]int `json:"idle_by_profile"`
			} `json:"pool"`
			Cache struct {
				Hit        int     `json:"hit"`
				Miss       int     `json:"miss"`
				BytesSaved float64 `json:"bytes_saved"`
			} `json:"cache"`
			Inflight struct {
				Now  int `json:"now"`
				Peak int `json:"peak"`
			} `json:"inflight"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&raw); err == nil {
			diag.Pid = raw.PID
			diag.ActiveProfile = raw.ActiveProfile
			diag.PausedProfiles = raw.PausedProfiles
			diag.EnabledProfiles = raw.EnabledProfiles
			diag.PoolHitRate = raw.Pool.HitRate * 100.0
			diag.PoolHits = raw.Pool.Hit
			diag.PoolMiss = raw.Pool.Miss
			if raw.Pool.Idle != nil {
				diag.PoolIdle = raw.Pool.Idle
			}
			diag.CacheHits = raw.Cache.Hit
			diag.CacheMiss = raw.Cache.Miss
			diag.CacheSavedMB = raw.Cache.BytesSaved / (1024 * 1024)
			diag.InflightNow = raw.Inflight.Now
			diag.InflightPeak = raw.Inflight.Peak
		}
	}

	// Calculate recent window stats from latency.jsonl tail (last 300 seconds)
	rows, _ := c.ReadLatencyTail(250 * 1024)
	cutoff := time.Now().Add(-5 * time.Minute)

	var okCount, badCount int
	var heads []float64
	var sumDecodeTok, sumStreamMs float64
	var sumE2ETok, sumTotalMs float64

	for _, r := range rows {
		tsStr, _ := r["ts"].(string)
		if len(tsStr) >= 19 {
			t, err := time.ParseInLocation("2006-01-02 15:04:05", tsStr[:19], time.Local)
			if err == nil && t.After(cutoff) {
				st, _ := r["status"].(float64)
				hasErr := r["err"] != nil
				if int(st) == 200 && !hasErr {
					okCount++
				} else if int(st) >= 400 || hasErr {
					badCount++
				}
				if h, ok := r["head_ms"].(float64); ok && h > 0 {
					heads = append(heads, h)
				}
				cTok, _ := r["c_tok"].(float64)
				tTok, _ := r["t_tok"].(float64)
				genTok := cTok + tTok
				streamMs, _ := r["stream_ms"].(float64)
				totMs, _ := r["total_ms"].(float64)
				if genTok > 0 && streamMs >= 50 {
					sumDecodeTok += genTok
					sumStreamMs += streamMs
				}
				if genTok > 0 && totMs > 0 {
					sumE2ETok += genTok
					sumTotalMs += totMs
				}
			}
		}
	}

	totalRecent := okCount + badCount
	diag.RecentSuccess = okCount
	diag.RecentFail = badCount
	if totalRecent > 0 {
		diag.RecentFailRate = float64(badCount) * 100.0 / float64(totalRecent)
	}

	if len(heads) > 0 {
		sort.Float64s(heads)
		diag.P50HeadMs = heads[len(heads)/2]
	}
	if sumStreamMs > 0 && sumDecodeTok > 0 {
		diag.RecentDecodeSpeed = sumDecodeTok / (sumStreamMs / 1000.0)
	}
	if sumTotalMs > 0 && sumE2ETok > 0 {
		diag.RecentE2ESpeed = sumE2ETok / (sumTotalMs / 1000.0)
	}
	if diag.RecentDecodeSpeed > 0 {
		diag.RecentSpeed = diag.RecentDecodeSpeed
	} else {
		diag.RecentSpeed = diag.RecentE2ESpeed
	}

	return diag, nil
}

type latencyRow struct {
	TS       string  `json:"ts"`
	Path     string  `json:"path"`
	Status   int     `json:"status"`
	Model    string  `json:"model"`
	Profile  string  `json:"profile"`
	Session  string  `json:"session"`
	HeadMs   float64 `json:"head_ms"`
	FirstMs  float64 `json:"first_ms"`
	StreamMs float64 `json:"stream_ms"`
	TotalMs  float64 `json:"total_ms"`
	PTok     int64   `json:"p_tok"`
	CTok     int64   `json:"c_tok"`
	TTok     int64   `json:"t_tok"`
	CacheTok int64   `json:"cache_tok"`
	Pool     string  `json:"pool"`
	Cache    string  `json:"cache"`
	Retry    int     `json:"retry"`
	Err      any     `json:"err"`
}

func extractTSDateFast(line []byte) string {
	idx := bytes.Index(line, []byte(`"ts":"`))
	if idx == -1 {
		idx = bytes.Index(line, []byte(`"ts": "`))
		if idx != -1 {
			idx += 7
		}
	} else {
		idx += 6
	}
	if idx != -1 && idx+10 <= len(line) {
		d := line[idx : idx+10]
		if len(d) == 10 && d[4] == '-' && d[7] == '-' {
			return string(d)
		}
	}
	return ""
}

func (c *Client) scanLatencyForPeriod(cutoff, end string, fn func(*latencyRow)) {
	candidates := []string{
		filepath.Join(c.baseDir, "latency.jsonl.3"),
		filepath.Join(c.baseDir, "latency.jsonl.2"),
		filepath.Join(c.baseDir, "latency.jsonl.1"),
		filepath.Join(c.baseDir, "latency.jsonl"),
	}
	for idx, path := range candidates {
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		// Rotated files older than cutoff cannot contain records >= cutoff
		if idx < len(candidates)-1 && stat.ModTime().Local().Format("2006-01-02") < cutoff {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			if fastDate := extractTSDateFast(line); fastDate != "" {
				if fastDate < cutoff || fastDate > end {
					continue
				}
			}
			var rec latencyRow
			if json.Unmarshal(line, &rec) != nil || len(rec.TS) < 10 {
				continue
			}
			day := rec.TS[:10]
			if day < cutoff || day > end {
				continue
			}
			fn(&rec)
		}
		f.Close()
	}
}

func readTailOfFile(filePath string, tailBytes int64) []map[string]any {
	file, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil
	}

	offset := int64(0)
	if stat.Size() > tailBytes {
		offset = stat.Size() - tailBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil
	}

	reader := bufio.NewReader(file)
	if offset > 0 {
		// discard partial first line
		_, _ = reader.ReadBytes('\n')
	}

	var results []map[string]any
	for {
		line, err := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var m map[string]any
			if json.Unmarshal(line, &m) == nil {
				results = append(results, m)
			}
		}
		if err != nil {
			break
		}
	}
	return results
}

func (c *Client) ReadLatencyTail(tailBytes int64) ([]map[string]any, error) {
	currPath := filepath.Join(c.baseDir, "latency.jsonl")
	stat, err := os.Stat(currPath)
	if err != nil {
		return nil, err
	}

	var results []map[string]any
	if stat.Size() < tailBytes {
		prevPath := filepath.Join(c.baseDir, "latency.jsonl.1")
		needed := tailBytes - stat.Size()
		prevRows := readTailOfFile(prevPath, needed)
		results = append(results, prevRows...)
	}

	currRows := readTailOfFile(currPath, tailBytes)
	results = append(results, currRows...)
	return results, nil
}

func (c *Client) ReadMetrics() (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(c.baseDir, "metrics.json"))
	if err != nil {
		return nil, err
	}
	var res map[string]any
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) ReadProfiles() (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(c.baseDir, "profiles.json"))
	if err != nil {
		return nil, err
	}
	var res map[string]any
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) callGatewayControl(path string, cliFallbackArgs []string) error {
	resp, err := c.httpClient.Get(c.gatewayURL + path)
	if err == nil {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			var errObj struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(body, &errObj) == nil && errObj.Message != "" {
				return fmt.Errorf("%s", errObj.Message)
			}
			return fmt.Errorf("gateway returned HTTP %d", resp.StatusCode)
		}
		return nil
	}

	if len(cliFallbackArgs) == 0 {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.getAgypBin(), cliFallbackArgs...)
	outBytes, cliErr := cmd.CombinedOutput()
	if cliErr != nil {
		cleanOut := stripANSI(strings.TrimSpace(string(outBytes)))
		if cleanOut != "" {
			return fmt.Errorf("%s", cleanOut)
		}
		return cliErr
	}
	return nil
}

func (c *Client) SwitchProfile(name string) error {
	return c.callGatewayControl(
		fmt.Sprintf("/_agyp/switch?profile=%s", url.QueryEscape(name)),
		[]string{"switch", name},
	)
}

func (c *Client) SetProfilePaused(name string, paused bool) error {
	flag := "1"
	cliCmd := "pause"
	if !paused {
		flag = "0"
		cliCmd = "resume"
	}
	return c.callGatewayControl(
		fmt.Sprintf("/_agyp/pause?profile=%s&paused=%s", url.QueryEscape(name), flag),
		[]string{cliCmd, name},
	)
}

func (c *Client) SetOnlyProfile(name string) error {
	return c.callGatewayControl(
		fmt.Sprintf("/_agyp/only?profile=%s", url.QueryEscape(name)),
		[]string{"only", name},
	)
}

func (c *Client) ResumeAllProfiles() error {
	return c.callGatewayControl(
		"/_agyp/pause?all=0",
		[]string{"resume", "all"},
	)
}

func (c *Client) callGatewayPost(path string, payload map[string]any, timeout time.Duration) (*CommandResult, bool, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequest("POST", c.gatewayURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := strings.TrimSpace(os.Getenv("AGYP_ADMIN_TOKEN")); tok != "" {
		req.Header.Set("X-Agyp-Token", tok)
	}
	hc := &http.Client{Timeout: timeout}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, false, err // gateway unreachable -> caller may fallback to CLI
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var obj struct {
		Status        string `json:"status"`
		Message       string `json:"message"`
		ActiveProfile string `json:"active_profile"`
	}
	_ = json.Unmarshal(raw, &obj)
	if resp.StatusCode >= 400 || obj.Status == "error" {
		msg := strings.TrimSpace(obj.Message)
		if msg == "" {
			msg = fmt.Sprintf("gateway returned HTTP %d", resp.StatusCode)
		}
		return &CommandResult{Success: false, Message: msg}, true, fmt.Errorf("%s", msg)
	}
	msg := strings.TrimSpace(obj.Message)
	if msg == "" && obj.ActiveProfile != "" {
		msg = fmt.Sprintf("当前活动账号: %s", obj.ActiveProfile)
	}
	return &CommandResult{Success: true, Message: msg}, true, nil
}

func (c *Client) runAgypJSONCommand(timeout time.Duration, args ...string) (*CommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.getAgypBin(), args...)
	if strings.TrimSpace(os.Getenv("AGYP_HOME")) == "" && c.baseDir != "" {
		cmd.Env = append(os.Environ(), "AGYP_HOME="+c.baseDir)
	}
	outBytes, err := cmd.CombinedOutput()
	clean := stripANSI(strings.TrimSpace(string(outBytes)))
	var obj struct {
		Status        string `json:"status"`
		Message       string `json:"message"`
		ActiveProfile string `json:"active_profile"`
		Profile       string `json:"profile"`
	}
	for _, line := range strings.Split(clean, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &obj) == nil {
			break
		}
	}
	if err != nil || obj.Status == "error" {
		msg := strings.TrimSpace(obj.Message)
		if msg == "" {
			msg = clean
		}
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return &CommandResult{Success: false, Message: msg}, fmt.Errorf("%s", msg)
	}
	msg := strings.TrimSpace(obj.Message)
	if msg == "" {
		msg = "操作成功"
	}
	return &CommandResult{Success: true, Message: msg}, nil
}

func (c *Client) AddProfileOAuth(name string) (*CommandResult, error) {
	name = strings.TrimSpace(name)
	return c.runAgypJSONCommand(190*time.Second, "add", name, "--timeout", "180", "--json")
}

func (c *Client) ImportKeychainProfile(name string) (*CommandResult, error) {
	name = strings.TrimSpace(name)
	res, reached, err := c.callGatewayPost("/_agyp/profile/add", map[string]any{
		"mode": "keychain",
		"name": name,
	}, 25*time.Second)
	if reached {
		return res, err
	}
	return c.runAgypJSONCommand(25*time.Second, "add", name, "--from-keychain", "--json")
}

func (c *Client) AddProfileToken(name, refreshToken, email string) (*CommandResult, error) {
	name = strings.TrimSpace(name)
	refreshToken = strings.TrimSpace(refreshToken)
	email = strings.TrimSpace(email)
	res, reached, err := c.callGatewayPost("/_agyp/profile/add", map[string]any{
		"mode":          "refresh_token",
		"name":          name,
		"refresh_token": refreshToken,
		"email":         email,
	}, 25*time.Second)
	if reached {
		return res, err
	}
	args := []string{"add", name, "--refresh-token", refreshToken, "--json"}
	if email != "" {
		args = append(args, "--email", email)
	}
	return c.runAgypJSONCommand(25*time.Second, args...)
}

func (c *Client) RemoveProfile(name string, force bool) (*CommandResult, error) {
	name = strings.TrimSpace(name)
	res, reached, err := c.callGatewayPost("/_agyp/profile/remove", map[string]any{
		"name":  name,
		"force": force,
	}, 10*time.Second)
	if reached {
		return res, err
	}
	args := []string{"remove", name, "-y", "--json"}
	if force {
		args = append(args, "--force")
	}
	return c.runAgypJSONCommand(10*time.Second, args...)
}

func (c *Client) RenameProfile(oldName, newName string) (*CommandResult, error) {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	res, reached, err := c.callGatewayPost("/_agyp/profile/rename", map[string]any{
		"old_name": oldName,
		"new_name": newName,
	}, 10*time.Second)
	if reached {
		return res, err
	}
	return c.runAgypJSONCommand(10*time.Second, "rename", oldName, newName, "--json")
}

func (c *Client) GetRecentLogs(limit int) ([]RequestLogRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}

	// Dynamically scale tail read buffer to easily accommodate 100~1000 logs
	tailBytes := int64(limit) * 800
	if tailBytes < 300*1024 {
		tailBytes = 300 * 1024
	}
	if tailBytes > 4*1024*1024 {
		tailBytes = 4 * 1024 * 1024
	}

	rows, err := c.ReadLatencyTail(tailBytes)
	if err != nil {
		return nil, err
	}

	var list []RequestLogRow
	start := 0
	if len(rows) > limit {
		start = len(rows) - limit
	}

	for i := len(rows) - 1; i >= start; i-- {
		r := rows[i]
		rawTS, _ := r["ts"].(string)
		tsMs := parseLatencyTimestampMs(rawTS)
		ts := rawTS
		if len(ts) > 11 {
			ts = ts[11:] // just HH:mm:ss.sss
		}
		path, _ := r["path"].(string)
		path = strings.TrimPrefix(path, "/v1internal:")
		status := int(getFloat(r, "status"))
		model, _ := r["model"].(string)
		profile, _ := r["profile"].(string)
		session, _ := r["session"].(string)
		headMs := getFloat(r, "head_ms")
		streamMs := getFloat(r, "stream_ms")
		totalMs := getFloat(r, "total_ms")
		pTok := int(getFloat(r, "p_tok"))
		cTok := int(getFloat(r, "c_tok"))
		tTok := int(getFloat(r, "t_tok"))
		cacheTok := int(getFloat(r, "cache_tok"))
		cache, _ := r["cache"].(string)

		genTok := cTok + tTok
		speed := 0.0
		if genTok > 0 && totalMs > 0 {
			speed = float64(genTok) / (totalMs / 1000.0)
		}
		decodeSpeed := 0.0
		if genTok > 0 && streamMs >= 50 {
			decodeSpeed = float64(genTok) / (streamMs / 1000.0)
		}
		pool, _ := r["pool"].(string)
		retry := int(getFloat(r, "retry"))
		errStr := ""
		if r["err"] != nil {
			errStr = fmt.Sprintf("%v", r["err"])
		}

		list = append(list, RequestLogRow{
			Time:        ts,
			TimestampMs: tsMs,
			Path:        path,
			Status:      status,
			Model:       model,
			Profile:     profile,
			Session:     session,
			HeadMs:      headMs,
			StreamMs:    streamMs,
			TotalMs:     totalMs,
			InTok:       pTok,
			OutTok:      cTok,
			ThoughtTok:  tTok,
			CacheTok:    cacheTok,
			Speed:       speed,
			DecodeSpeed: decodeSpeed,
			Pool:        pool,
			Cache:       cache,
			Retry:       retry,
			Error:       errStr,
		})
	}

	uuids := make([]string, 0, len(list))
	for _, r := range list {
		if u := stripSessionPrefix(r.Session); u != "" {
			uuids = append(uuids, u)
		}
	}
	metas := c.sessionMetas(uuids)
	for i := range list {
		if u := stripSessionPrefix(list[i].Session); u != "" {
			if meta, ok := metas[u]; ok {
				list[i].SessionName = meta.Title
				if meta.ParentID != "" {
					list[i].ParentSession = "agent/" + meta.ParentID
				}
				list[i].AgentName = meta.AgentName
			}
		}
	}
	annotateRequestCacheAnomalies(list)
	return list, nil
}

func parseLatencyTimestampMs(rawTS string) int64 {
	s := strings.TrimSpace(rawTS)
	if len(s) < 19 {
		return 0
	}
	if len(s) >= 23 {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05.000", s[:23], time.Local); err == nil {
			return t.UnixMilli()
		}
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s[:19], time.Local); err == nil {
		return t.UnixMilli()
	}
	return 0
}

func stripSessionPrefix(s string) string {
	if strings.HasPrefix(s, "agent/") {
		return s[6:]
	}
	if strings.HasPrefix(s, "checkpoint/") {
		return s[11:]
	}
	return s
}

func isUUIDish(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == '-') {
			return false
		}
	}
	return true
}

func antigravityAppDataDirs() []string {
	var candidates []string
	if envDir := strings.TrimSpace(os.Getenv("ANTIGRAVITY_APP_DATA_DIR")); envDir != "" {
		candidates = append(candidates, envDir)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".gemini", "antigravity-cli"),
			filepath.Join(home, ".gemini", "antigravity"),
		)
	}
	seen := make(map[string]bool)
	var dirs []string
	for _, d := range candidates {
		clean := filepath.Clean(d)
		if seen[clean] {
			continue
		}
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			seen[clean] = true
			dirs = append(dirs, clean)
		}
	}
	return dirs
}

// sessionNames maps session uuids to conversation titles while sharing the
// underlying sessionMetas cache.
func (c *Client) sessionNames(uuids []string) map[string]string {
	metas := c.sessionMetas(uuids)
	out := make(map[string]string, len(metas))
	for k, v := range metas {
		out[k] = v.Title
	}
	return out
}

// sessionMetas maps session uuids to conversation metadata across CLI & Desktop
// Antigravity directories, cached briefly so 2.5s polls avoid redundant sqlite3 calls.
func (c *Client) sessionMetas(uuids []string) map[string]sessionMeta {
	c.titleMu.Lock()
	defer c.titleMu.Unlock()
	if c.titleCache == nil {
		c.titleCache = make(map[string]sessionMeta)
	}
	stale := time.Since(c.titleAt) > 30*time.Second
	var need []string
	seen := make(map[string]bool)
	for _, u := range uuids {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		if _, ok := c.titleCache[u]; !ok || stale {
			need = append(need, u)
		}
	}
	if len(need) > 0 {
		if found := querySessionMetas(need); found != nil {
			for _, u := range need {
				c.titleCache[u] = found[u]
			}
			if stale || c.titleAt.IsZero() {
				c.titleAt = time.Now()
			}
		}
	}
	snapshot := make(map[string]sessionMeta, len(c.titleCache))
	for k, v := range c.titleCache {
		snapshot[k] = v
	}
	return snapshot
}

func querySessionMetas(uuids []string) map[string]sessionMeta {
	dirs := antigravityAppDataDirs()
	if len(dirs) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var safe []string
	for _, u := range uuids {
		if u == "" || seen[u] || !isUUIDish(u) {
			continue
		}
		seen[u] = true
		safe = append(safe, "'"+u+"'")
	}
	if len(safe) == 0 {
		return map[string]sessionMeta{}
	}
	query := "SELECT json_group_object(conversation_id, json_object('title', title, 'parent', parent_conversation_id, 'agent', agent_name)) FROM conversation_summaries WHERE conversation_id IN (" + strings.Join(safe, ",") + ");"
	merged := make(map[string]sessionMeta)
	anyDB := false
	for _, dir := range dirs {
		dbPath := filepath.Join(dir, "conversation_summaries.db")
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}
		anyDB = true
		out, err := runSQLiteReadOnly(dbPath, query, 2*time.Second)
		if err != nil {
			continue
		}
		trimmed := bytes.TrimSpace(out)
		if len(trimmed) == 0 {
			continue
		}
		var m map[string]sessionMeta
		if err := json.Unmarshal(trimmed, &m); err != nil {
			continue
		}
		for k, v := range m {
			cur, exists := merged[k]
			if !exists {
				merged[k] = v
				continue
			}
			if cur.Title == "" && v.Title != "" {
				cur.Title = v.Title
			}
			if cur.ParentID == "" && v.ParentID != "" {
				cur.ParentID = v.ParentID
			}
			if cur.AgentName == "" && v.AgentName != "" {
				cur.AgentName = v.AgentName
			}
			merged[k] = cur
		}
	}
	if !anyDB {
		return nil
	}
	return merged
}

func getFloat(m map[string]any, key string) float64 {
	if val, ok := m[key]; ok && val != nil {
		if f, ok := val.(float64); ok {
			return f
		}
	}
	return 0
}

func (c *Client) GetConnectInfo() (*ConnectInfo, error) {
	lanIP := "127.0.0.1"
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				lanIP = ipnet.IP.String()
				break
			}
		}
	}

	diag, _ := c.FetchDiag()
	activeProf := ""
	if diag != nil && diag.ActiveProfile != "" {
		activeProf = diag.ActiveProfile
	} else if profsRaw, err := c.ReadProfiles(); err == nil {
		activeProf = resolveActiveProfileName(profsRaw)
	}

	clients := []ClientSetup{
		{
			Name:        "Antigravity CLI (agy)",
			Description: "本地网关代理，支持多账号无感热切与 429/503 自动容灾",
			EnvCmd:      "export CLOUD_CODE_URL=http://127.0.0.1:8783",
			ConfigTip:   "终端执行上述命令，或直接运行 agyp 命令自动接管",
		},
		{
			Name:        "agyp 命令行托管",
			Description: "自动选取高配额账号、暂停/独占单号及实时状态观测",
			EnvCmd:      "agyp / agyp switch <name> / agyp only <name>",
			ConfigTip:   "可通过 agyp add <name> 新增更多 Google 账号",
		},
		{
			Name:        "本地诊断与管理接口",
			Description: "查询在途请求、连接池复用率、元数据缓存与会话绑定快照",
			EnvCmd:      "curl http://127.0.0.1:8783/_agyp/status",
			ConfigTip:   "默认仅绑定 127.0.0.1 回环地址，避免暴露于公网或跨站访问",
		},
	}

	proxyAddr := strings.TrimSpace(os.Getenv("AGYP_UPSTREAM_PROXY"))
	if proxyAddr == "" {
		proxyAddr = "自动 (系统代理)"
	}

	return &ConnectInfo{
		LocalURL:      "http://127.0.0.1:8783",
		LanURL:        fmt.Sprintf("http://%s:8783", lanIP),
		Port:          8783,
		Proxy:         proxyAddr,
		ActiveProfile: activeProf,
		Clients:       clients,
	}, nil
}

func (c *Client) TestEndpoint(profile, path string) (*EndpointTestResult, error) {
	if profile == "" {
		if profsRaw, err := c.ReadProfiles(); err == nil {
			profile = resolveActiveProfileName(profsRaw)
		}
	}
	if path == "" {
		path = "loadCodeAssist"
	}
	cleanPath := strings.TrimPrefix(path, "/")
	cleanPath = strings.TrimPrefix(cleanPath, "v1internal:")

	url := fmt.Sprintf("%s/v1internal:%s", c.gatewayURL, cleanPath)
	start := time.Now()
	req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(`{"context": "ping"}`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agyp-desktop-inspector/1.0")

	resp, err := c.httpClient.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &EndpointTestResult{
			Path:      cleanPath,
			Profile:   profile,
			Status:    502,
			LatencyMs: latency,
			Success:   false,
			Detail:    fmt.Sprintf("网络连接失败: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	return &EndpointTestResult{
		Path:      cleanPath,
		Profile:   profile,
		Status:    resp.StatusCode,
		LatencyMs: latency,
		Success:   resp.StatusCode == 200 || resp.StatusCode == 204,
		Detail:    fmt.Sprintf("上游返回 HTTP %d，耗时 %dms，网关转发通道畅通。", resp.StatusCode, latency),
	}, nil
}

func (c *Client) getAgypBin() string {
	home, _ := os.UserHomeDir()
	agypBin := filepath.Join(home, ".local", "bin", "agyp")
	if _, err := os.Stat(agypBin); err == nil {
		return agypBin
	}
	if p, err := exec.LookPath("agyp"); err == nil {
		return p
	}
	return "agyp"
}

func (c *Client) RestartGateway(force bool) (*CommandResult, error) {
	agypBin := c.getAgypBin()
	args := []string{"daemon", "restart"}
	if force {
		args = append(args, "--force")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agypBin, args...)
	outBytes, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(outBytes))
	cleanOut := stripANSI(outStr)

	if err != nil {
		if strings.Contains(cleanOut, "正在处理") || strings.Contains(cleanOut, "打断在途") {
			return &CommandResult{
				Success: false,
				Blocked: true,
				Message: cleanOut,
			}, nil
		}
		return &CommandResult{
			Success: false,
			Blocked: false,
			Message: fmt.Sprintf("重启失败: %v\n%s", err, cleanOut),
		}, nil
	}

	pid := 0
	if pidBytes, err := os.ReadFile(filepath.Join(c.baseDir, "gateway.pid")); err == nil {
		fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &pid)
	}

	return &CommandResult{
		Success: true,
		Blocked: false,
		Message: cleanOut,
		PID:     pid,
	}, nil
}

func (c *Client) StopGateway() (*CommandResult, error) {
	agypBin := c.getAgypBin()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agypBin, "daemon", "stop")
	outBytes, err := cmd.CombinedOutput()
	cleanOut := stripANSI(strings.TrimSpace(string(outBytes)))
	return &CommandResult{
		Success: err == nil,
		Message: cleanOut,
	}, nil
}

func (c *Client) StartGateway() (*CommandResult, error) {
	agypBin := c.getAgypBin()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agypBin, "daemon", "start")
	outBytes, err := cmd.CombinedOutput()
	cleanOut := stripANSI(strings.TrimSpace(string(outBytes)))

	pid := 0
	if pidBytes, err := os.ReadFile(filepath.Join(c.baseDir, "gateway.pid")); err == nil {
		fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &pid)
	}

	return &CommandResult{
		Success: err == nil,
		Message: cleanOut,
		PID:     pid,
	}, nil
}

func stripANSI(str string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(str); i++ {
		if str[i] == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if (str[i] >= 'a' && str[i] <= 'z') || (str[i] >= 'A' && str[i] <= 'Z') {
				inEsc = false
			}
			continue
		}
		b.WriteByte(str[i])
	}
	return b.String()
}

