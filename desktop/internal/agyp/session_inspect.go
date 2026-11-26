package agyp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	stepTypeUserInput  = 14
	stepTypePlanner    = 15
	stepTypeError      = 17
	stepTypeCheckpoint = 23
	stepTypeGeneric    = 132
)

type rawCtxChild struct {
	label  string
	tokens int64
}

type rawCtxGroup struct {
	key      string
	label    string
	tokens   int64
	children []rawCtxChild
}

type rawContextMeta struct {
	usedTokens int64
	maxTokens  int64
	groups     []rawCtxGroup
}

type rawGeneration struct {
	idx                 int
	executionID         string
	errorMsg            string
	stepIndices         []int
	modelName           string
	modelEnum           uint64
	executionModelEnum  uint64
	providerInputTokens int64
	hasProviderInput    bool
	inputTokens         int64 // uncached input (usage field 2)
	cacheReadTokens     int64 // cached input (usage field 5)
	outputTokens        int64 // total output = thinking + response (usage field 3)
	thinkingTokens      int64 // usage field 9
	responseTokens      int64 // usage field 10
	ttftMs              float64
	hasTTFT             bool
	streamingMs         float64
	hasStreaming        bool
	retries             int
	context             *rawContextMeta
}

type stepTiming struct {
	createdMs   float64
	hasCreated  bool
	viewableMs  float64
	hasViewable bool
	finishedMs  float64
	hasFinished bool
	completedMs float64
	hasComplete bool
	startedMs   float64
	hasStarted  bool
}

type checkpointMeta struct {
	stepIndex     int
	summaryTokens int64
}

type localSessionData struct {
	turns            int
	steps            int
	toolCalls        int
	requests         int
	modelName        string
	shortModel       string
	context          *SessionContextWindow
	totalTokens      int64
	inputTokens      int64
	cacheReadTokens  int64
	outputTokens     int64
	thinkingTokens   int64
	costUSD          float64
	costCNY          float64
	avgSpeed         float64
	peakSpeed        float64
	latestSpeed      float64
	cacheHitRate     float64
	lastRequestAtMs  int64
	modelTimeMs      float64
	toolTimeMs       float64
	latestTtftMs     float64
	avgTtftMs        float64
	p50TtftMs        float64
}

type subagentQueryRow struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentId"`
	AgentName string `json:"agentName"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	StepCount int    `json:"stepCount"`
	Depth     int    `json:"depth"`
}

type gatewaySessionStats struct {
	requests         int
	inputTokens      int64
	cacheReadTokens  int64
	outputTokens     int64
	thinkingTokens   int64
	totalTokens      int64
	costUSD          float64
	avgSpeed         float64
	peakSpeed        float64
	latestSpeed      float64
	cacheHitRate     float64
	lastRequestAtMs  int64
	latestInTok      int64
	latestCacheTok   int64
	latestTtftMs     float64
	avgTtftMs        float64
	p50TtftMs        float64
	modelName        string
	profiles         []string
	profileSwitches  int
	retries          int
	errors           int
}

func runSQLiteReadOnly(dbPath, query string, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// mode=ro avoids touching the live DB; when WAL -shm is absent, read-only open
	// can fail (SQLITE_CANTOPEN), so fall back to plain open with query_only pragma.
	out, err := exec.CommandContext(ctx, "sqlite3", "file:"+dbPath+"?mode=ro", query).Output()
	if err == nil {
		return out, nil
	}
	pragmaQuery := "PRAGMA query_only=ON; PRAGMA busy_timeout=1000; " + query
	return exec.CommandContext(ctx, "sqlite3", dbPath, pragmaQuery).Output()
}

func dbStamp(dbPath string) string {
	var parts [2]string
	for idx, p := range []string{dbPath, dbPath + "-wal"} {
		st, err := os.Stat(p)
		if err != nil {
			parts[idx] = ""
			continue
		}
		parts[idx] = fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
	}
	return parts[0] + "|" + parts[1]
}

func dbEffectiveMtime(dbPath string) int64 {
	var maxT int64
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		if st, err := os.Stat(p); err == nil {
			if ns := st.ModTime().UnixNano(); ns > maxT {
				maxT = ns
			}
		}
	}
	return maxT
}

func findConversationDB(uuid string) (dbPath, appDataDir string) {
	if !isUUIDish(uuid) {
		return "", ""
	}
	var bestMtime int64
	for _, dir := range antigravityAppDataDirs() {
		candidate := filepath.Join(dir, "conversations", uuid+".db")
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		mt := dbEffectiveMtime(candidate)
		if dbPath == "" || mt > bestMtime {
			bestMtime = mt
			dbPath = candidate
			appDataDir = dir
		}
	}
	return dbPath, appDataDir
}

func (c *Client) getOrParseLocalSession(dbPath string) (*localSessionData, error) {
	stamp := dbStamp(dbPath)
	c.sessionCacheMu.Lock()
	if c.sessionCache == nil {
		c.sessionCache = make(map[string]*cachedLocalSession)
	}
	if cached, ok := c.sessionCache[dbPath]; ok && cached.stamp == stamp {
		parsed := cached.parsed
		c.sessionCacheMu.Unlock()
		return parsed, nil
	}
	c.sessionCacheMu.Unlock()

	parsed, err := parseLocalSessionDB(dbPath)
	if err != nil {
		return nil, err
	}

	c.sessionCacheMu.Lock()
	if len(c.sessionCache) >= 32 {
		for k := range c.sessionCache {
			delete(c.sessionCache, k)
			break
		}
	}
	c.sessionCache[dbPath] = &cachedLocalSession{stamp: stamp, parsed: parsed}
	c.sessionCacheMu.Unlock()
	return parsed, nil
}

func decodeGenerationBlob(idx int, data []byte) rawGeneration {
	g := parseProtoFields(data)
	chat := protoNested(g, 1)
	plannerCfg := protoNested(g, 3)
	planExec := protoNested(plannerCfg, 60)
	usage := protoNested(chat, 4)
	start := protoNested(chat, 9)
	ctxMeta := protoNested(start, 10)
	breakdown := protoNested(ctxMeta, 3)

	modelName := protoFirstString(chat, 21)
	if modelName == "" {
		modelName = protoFirstString(chat, 19)
	}
	modelEnum, _ := protoFirstVarint(chat, 3)
	execEnum, _ := protoFirstVarint(planExec, 1)

	inTok := protoVarintOrZero(usage, 2)
	cacheTok := protoVarintOrZero(usage, 5)
	hasUsage := len(usage[2]) > 0 || len(usage[5]) > 0
	var providerIn int64
	if hasUsage {
		providerIn = inTok + cacheTok
	}

	ttftMs, hasTTFT := protoTimestampMs(protoFirstBytes(chat, 11))
	streamMs, hasStream := protoTimestampMs(protoFirstBytes(chat, 12))

	var rawCtx *rawContextMeta
	if maxTok := protoVarintOrZero(ctxMeta, 4); maxTok > 0 {
		usedTok := protoVarintOrZero(ctxMeta, 1)
		var groups []rawCtxGroup
		for i, gv := range breakdown[1] {
			if gv.wire != 2 {
				continue
			}
			gm := parseProtoFields(gv.raw)
			var kids []rawCtxChild
			for _, cv := range gm[5] {
				if cv.wire != 2 {
					continue
				}
				cm := parseProtoFields(cv.raw)
				kids = append(kids, rawCtxChild{
					label:  protoFirstString(cm, 1),
					tokens: protoVarintOrZero(cm, 3),
				})
			}
			groups = append(groups, rawCtxGroup{
				key:      fmt.Sprintf("group-%d", i),
				label:    protoFirstString(gm, 1),
				tokens:   protoVarintOrZero(gm, 4),
				children: kids,
			})
		}
		rawCtx = &rawContextMeta{
			usedTokens: usedTok,
			maxTokens:  maxTok,
			groups:     groups,
		}
	}

	return rawGeneration{
		idx:                 idx,
		executionID:         protoFirstString(g, 4),
		errorMsg:            protoFirstString(g, 5),
		stepIndices:         protoStepIndices(g[2]),
		modelName:           modelName,
		modelEnum:           modelEnum,
		executionModelEnum:  execEnum,
		providerInputTokens: providerIn,
		hasProviderInput:    hasUsage,
		inputTokens:         inTok,
		cacheReadTokens:     cacheTok,
		outputTokens:        protoVarintOrZero(usage, 3),
		thinkingTokens:      protoVarintOrZero(usage, 9),
		responseTokens:      protoVarintOrZero(usage, 10),
		ttftMs:              ttftMs,
		hasTTFT:             hasTTFT,
		streamingMs:         streamMs,
		hasStreaming:        hasStream,
		retries:             int(protoVarintOrZero(chat, 14)),
		context:             rawCtx,
	}
}

func estimateTextTokens(text string) int64 {
	if text == "" {
		return 0
	}
	var cjk, other int
	for _, r := range text {
		if (r >= 0x4E00 && r <= 0x9FFF) ||
			(r >= 0x3400 && r <= 0x4DBF) ||
			(r >= 0x3000 && r <= 0x303F) ||
			(r >= 0xFF00 && r <= 0xFFEF) {
			cjk++
		} else {
			other++
		}
	}
	est := math.Ceil(float64(cjk)*1.15 + float64(other)/3.8)
	return int64(est)
}

func decodeCheckpointTokens(stepPayload []byte) int64 {
	if len(stepPayload) == 0 {
		return 0
	}
	payload := parseProtoFields(stepPayload)
	cp := protoNested(payload, 30)
	if len(cp) == 0 {
		return 0
	}
	tokens := estimateTextTokens(protoFirstString(cp, 5))
	for _, reqVal := range cp[30] {
		if reqVal.wire != 2 {
			continue
		}
		reqMap := parseProtoFields(reqVal.raw)
		tokens += estimateTextTokens(protoFirstString(reqMap, 1))
	}
	artifactCount := len(cp[14])
	if artifactCount > 200 {
		artifactCount = 200
	}
	tokens += int64(artifactCount) * 35
	if tokens > 0 && tokens < 500 {
		return 500
	}
	return tokens
}

func parseLocalSessionDB(dbPath string) (*localSessionData, error) {
	genOut, err := runSQLiteReadOnly(dbPath, "SELECT idx || '|' || hex(data) FROM gen_metadata ORDER BY idx;", 4*time.Second)
	if err != nil {
		return nil, fmt.Errorf("read gen_metadata failed: %w", err)
	}
	stepOut, err := runSQLiteReadOnly(dbPath, "SELECT idx || '|' || step_type || '|' || status || '|' || hex(metadata) FROM steps ORDER BY idx;", 4*time.Second)
	if err != nil {
		return nil, fmt.Errorf("read steps failed: %w", err)
	}

	var gens []rawGeneration
	scGen := bufio.NewScanner(bytes.NewReader(genOut))
	scGen.Buffer(make([]byte, 256*1024), 16*1024*1024)
	for scGen.Scan() {
		line := strings.TrimSpace(scGen.Text())
		if line == "" {
			continue
		}
		sep := strings.IndexByte(line, '|')
		if sep <= 0 {
			continue
		}
		idx, err1 := strconv.Atoi(line[:sep])
		blob, err2 := hex.DecodeString(line[sep+1:])
		if err1 != nil || err2 != nil || len(blob) == 0 {
			continue
		}
		gens = append(gens, decodeGenerationBlob(idx, blob))
	}

	turns := 0
	totalSteps := 0
	toolCalls := 0
	var checkpointSteps []int
	latestCompletedCpIdx := -1
	times := make(map[int]stepTiming)
	var toolIntervals [][2]float64

	scSteps := bufio.NewScanner(bytes.NewReader(stepOut))
	scSteps.Buffer(make([]byte, 256*1024), 16*1024*1024)
	for scSteps.Scan() {
		line := strings.TrimSpace(scSteps.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		idx, err1 := strconv.Atoi(parts[0])
		stepType, err2 := strconv.Atoi(parts[1])
		status, err3 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		totalSteps++
		switch stepType {
		case stepTypeUserInput:
			turns++
		case stepTypeCheckpoint:
			checkpointSteps = append(checkpointSteps, idx)
			if status == 3 {
				latestCompletedCpIdx = idx
			}
		case stepTypeGeneric:
			toolCalls++
		}

		if metaBytes, err := hex.DecodeString(parts[3]); err == nil && len(metaBytes) > 0 {
			f := parseProtoFields(metaBytes)
			cr, hasCr := protoTimestampMs(protoFirstBytes(f, 1))
			vw, hasVw := protoTimestampMs(protoFirstBytes(f, 6))
			fn, hasFn := protoTimestampMs(protoFirstBytes(f, 7))
			cp, hasCp := protoTimestampMs(protoFirstBytes(f, 8))
			sa, hasSa := protoTimestampMs(protoFirstBytes(f, 32))
			times[idx] = stepTiming{
				createdMs: cr, hasCreated: hasCr,
				viewableMs: vw, hasViewable: hasVw,
				finishedMs: fn, hasFinished: hasFn,
				completedMs: cp, hasComplete: hasCp,
				startedMs: sa, hasStarted: hasSa,
			}
			if stepType == stepTypeGeneric && hasCp {
				startMs := cr
				hasStart := hasCr
				if hasSa {
					startMs = sa
					hasStart = true
				}
				if hasStart && cp >= startMs {
					toolIntervals = append(toolIntervals, [2]float64{startMs, cp})
				}
			}
		}
	}

	var latestGen *rawGeneration
	var latestContextGen *rawGeneration
	for i := range gens {
		g := &gens[i]
		if g.context != nil || (g.hasProviderInput && g.providerInputTokens > 0) {
			latestContextGen = g
		}
		if g.modelName != "" || g.errorMsg != "" || g.outputTokens > 0 {
			latestGen = g
		}
	}

	var latestCp *checkpointMeta
	if latestCompletedCpIdx >= 0 && latestContextGen != nil {
		genMaxStep := -1
		for _, si := range latestContextGen.stepIndices {
			if si > genMaxStep {
				genMaxStep = si
			}
		}
		if latestCompletedCpIdx > genMaxStep && genMaxStep >= 0 {
			var sumTok int64
			if cpOut, err := runSQLiteReadOnly(dbPath, fmt.Sprintf("SELECT hex(step_payload) FROM steps WHERE idx = %d;", latestCompletedCpIdx), 2*time.Second); err == nil {
				hexStr := strings.TrimSpace(string(cpOut))
				if rawPayload, err := hex.DecodeString(hexStr); err == nil {
					sumTok = decodeCheckpointTokens(rawPayload)
				}
			}
			latestCp = &checkpointMeta{stepIndex: latestCompletedCpIdx, summaryTokens: sumTok}
		}
	}

	ctxWin := buildContextWindow(latestContextGen, latestCp)
	models, compactions := buildModelContexts(gens, checkpointSteps, ctxWin)
	if ctxWin != nil {
		ctxWin.Compactions = compactions
		ctxWin.Models = models
	}

	var (
		totalIn, totalCache, totalOut, totalThink int64
		costUSD                                   float64
		requests                                  int
		modelTimeMs                               float64
		streamToks, streamMsSum                   int64
		e2eToks, e2eMsSum                         int64
		peakSpeed, latestSpeed                    float64
		ttfts                                     []float64
		latestTtftMs                              float64
		lastRequestAtMs                           int64
	)

	for i := range gens {
		g := &gens[i]
		if g.modelName == "" && g.errorMsg == "" && g.outputTokens == 0 {
			continue
		}
		requests++
		totalIn += g.inputTokens
		totalCache += g.cacheReadTokens
		totalOut += g.outputTokens
		totalThink += g.thinkingTokens

		if g.inputTokens > 0 || g.cacheReadTokens > 0 || g.outputTokens > 0 {
			// g.outputTokens (usage field 3) already includes thinking + response tokens.
			costUSD += estimateModelCostUSD(g.modelName, g.inputTokens+g.cacheReadTokens, g.outputTokens, g.cacheReadTokens, 0)
		}

		var genStartMs int64
		for _, si := range g.stepIndices {
			if st, ok := times[si]; ok && st.hasCreated {
				ms := int64(st.createdMs)
				if ms > genStartMs {
					genStartMs = ms
				}
			}
		}
		if g.errorMsg == "" && genStartMs > lastRequestAtMs && (g.inputTokens > 0 || g.cacheReadTokens > 0 || g.outputTokens > 0) {
			lastRequestAtMs = genStartMs
		}

		if g.hasTTFT && g.ttftMs > 0 {
			ttfts = append(ttfts, g.ttftMs)
			latestTtftMs = g.ttftMs
		}
		if g.hasTTFT && g.hasStreaming {
			durMs := g.ttftMs + g.streamingMs
			if g.outputTokens > 0 && durMs > 0 {
				modelTimeMs += durMs
			}
			isStreamed := g.streamingMs >= 500 && g.outputTokens >= 20
			tpsDurMs := durMs
			if isStreamed {
				tpsDurMs = g.streamingMs
				streamToks += g.outputTokens
				streamMsSum += int64(math.Round(g.streamingMs))
			}
			if g.outputTokens > 0 && durMs > 0 {
				e2eToks += g.outputTokens
				e2eMsSum += int64(math.Round(durMs))
			}
			if g.outputTokens > 0 && tpsDurMs > 0 {
				tps := float64(g.outputTokens) * 1000.0 / tpsDurMs
				latestSpeed = math.Round(tps*10) / 10.0
				if isStreamed && tps > peakSpeed {
					peakSpeed = math.Round(tps*10) / 10.0
				}
			}
		}
	}

	if peakSpeed == 0 && latestSpeed > 0 {
		for i := range gens {
			g := &gens[i]
			if g.hasTTFT && g.hasStreaming && g.outputTokens > 0 {
				dur := g.ttftMs + g.streamingMs
				if dur > 0 {
					if tps := float64(g.outputTokens) * 1000.0 / dur; tps > peakSpeed {
						peakSpeed = math.Round(tps*10) / 10.0
					}
				}
			}
		}
	}

	var avgSpeed float64
	if streamMsSum > 0 && streamToks > 0 {
		avgSpeed = math.Round((float64(streamToks)*1000.0/float64(streamMsSum))*10) / 10.0
	} else if e2eMsSum > 0 && e2eToks > 0 {
		avgSpeed = math.Round((float64(e2eToks)*1000.0/float64(e2eMsSum))*10) / 10.0
	}

	if lastRequestAtMs == 0 {
		for _, st := range times {
			if st.hasCreated && int64(st.createdMs) > lastRequestAtMs {
				lastRequestAtMs = int64(st.createdMs)
			}
		}
	}

	var avgTtftMs, p50TtftMs float64
	if len(ttfts) > 0 {
		var sum float64
		for _, v := range ttfts {
			sum += v
		}
		avgTtftMs = math.Round(sum / float64(len(ttfts)))
		sorted := append([]float64(nil), ttfts...)
		sort.Float64s(sorted)
		p50TtftMs = math.Round(sorted[len(sorted)/2])
	}

	totalPrompt := totalIn + totalCache
	cacheHitRate := 0.0
	if totalPrompt > 0 {
		cacheHitRate = math.Round((float64(totalCache)*1000.0/float64(totalPrompt))) / 10.0
	}

	modelName := ""
	if latestContextGen != nil && latestContextGen.modelName != "" {
		modelName = latestContextGen.modelName
	} else if latestGen != nil && latestGen.modelName != "" {
		modelName = latestGen.modelName
	}

	return &localSessionData{
		turns:            turns,
		steps:            totalSteps,
		toolCalls:        toolCalls,
		requests:         requests,
		modelName:        modelName,
		shortModel:       shortSessionModelName(modelName),
		context:          ctxWin,
		totalTokens:      totalIn + totalCache + totalOut,
		inputTokens:      totalIn,
		cacheReadTokens:  totalCache,
		outputTokens:     totalOut,
		thinkingTokens:   totalThink,
		costUSD:          math.Round(costUSD*10000) / 10000.0,
		costCNY:          math.Round(costUSD*7.20*100) / 100.0,
		avgSpeed:         avgSpeed,
		peakSpeed:        peakSpeed,
		latestSpeed:      latestSpeed,
		cacheHitRate:     cacheHitRate,
		lastRequestAtMs:  lastRequestAtMs,
		modelTimeMs:      modelTimeMs,
		toolTimeMs:       mergeToolIntervals(toolIntervals),
		latestTtftMs:     math.Round(latestTtftMs),
		avgTtftMs:        avgTtftMs,
		p50TtftMs:        p50TtftMs,
	}, nil
}

func mergeToolIntervals(intervals [][2]float64) float64 {
	if len(intervals) == 0 {
		return 0
	}
	sorted := append([][2]float64(nil), intervals...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i][0] == sorted[j][0] {
			return sorted[i][1] < sorted[j][1]
		}
		return sorted[i][0] < sorted[j][0]
	})
	var total float64
	curStart, curEnd := sorted[0][0], sorted[0][1]
	for _, iv := range sorted[1:] {
		if iv[0] > curEnd {
			total += curEnd - curStart
			curStart, curEnd = iv[0], iv[1]
		} else if iv[1] > curEnd {
			curEnd = iv[1]
		}
	}
	total += curEnd - curStart
	return total
}

func normalizeCategoryMeta(label string) (key, zhLabel string) {
	low := strings.ToLower(strings.TrimSpace(label))
	switch {
	case low == "chat messages" || low == "messages":
		return "messages", "消息"
	case low == "system prompt":
		return "system_prompt", "系统提示"
	case low == "tools" || low == "system tools":
		return "tools", "系统工具"
	case low == "skills":
		return "skills", "技能"
	case low == "subagents":
		return "subagents", "子智能体"
	case low == "plugins":
		return "plugins", "插件"
	case low == "mcp tools" || low == "mcp_tools" || low == "mcp":
		return "mcp", "MCP Tools"
	case low == "rules" || low == "user_rules":
		return "rules", "规则"
	case low == "unspecified" || low == "other" || low == "":
		return "other", "其他开销"
	default:
		return "other", "其他开销"
	}
}

func buildContextWindow(gen *rawGeneration, latestCp *checkpointMeta) *SessionContextWindow {
	if gen == nil {
		return nil
	}
	var reportedMax int64
	if gen.context != nil && gen.context.maxTokens > 0 {
		reportedMax = gen.context.maxTokens
	}
	baseUsed := gen.providerInputTokens
	if !gen.hasProviderInput || baseUsed <= 0 {
		if gen.context != nil && gen.context.usedTokens > 0 {
			baseUsed = gen.context.usedTokens
		} else {
			baseUsed = 0
		}
	}
	if baseUsed <= 0 && reportedMax <= 0 {
		return nil
	}

	type groupItem struct {
		key    string
		label  string
		tokens int64
	}
	mergedMap := make(map[string]*groupItem)
	var groups []groupItem

	addGroup := func(rawLabel string, tok int64) {
		if tok <= 0 {
			return
		}
		k, zh := normalizeCategoryMeta(rawLabel)
		if ex, ok := mergedMap[k]; ok {
			ex.tokens += tok
			return
		}
		it := &groupItem{key: k, label: zh, tokens: tok}
		mergedMap[k] = it
		groups = append(groups, *it)
	}

	// Re-sync slice from map at the end to preserve merged totals.
	if gen.context != nil {
		for _, g := range gen.context.groups {
			if strings.EqualFold(g.label, "System Prompt") {
				var skillsTok int64
				for _, ch := range g.children {
					if strings.EqualFold(ch.label, "skills") && ch.tokens > 0 {
						skillsTok += ch.tokens
					}
				}
				if skillsTok > 0 {
					rem := g.tokens - skillsTok
					if rem > 0 {
						addGroup(g.label, rem)
					}
					addGroup("Skills", skillsTok)
					continue
				}
			}
			addGroup(g.label, g.tokens)
		}
	}

	groups = groups[:0]
	for _, ptr := range mergedMap {
		groups = append(groups, *ptr)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].tokens == groups[j].tokens {
			return groups[i].key < groups[j].key
		}
		return groups[i].tokens > groups[j].tokens
	})

	usedTokens := baseUsed
	compactedPending := false
	genMaxStep := -1
	for _, si := range gen.stepIndices {
		if si > genMaxStep {
			genMaxStep = si
		}
	}
	if latestCp != nil && latestCp.stepIndex > genMaxStep && genMaxStep >= 0 && baseUsed > 0 {
		summaryEst := latestCp.summaryTokens
		if summaryEst <= 0 {
			summaryEst = int64(math.Round(float64(baseUsed) * 0.12))
			if summaryEst > 12000 {
				summaryEst = 12000
			}
		}
		var fixedTok int64
		var nonMsg []groupItem
		for _, g := range groups {
			if g.key == "messages" {
				continue
			}
			fixedTok += g.tokens
			nonMsg = append(nonMsg, g)
		}
		if len(nonMsg) == 0 {
			fixedTok = int64(math.Round(float64(baseUsed) * 0.12))
			if fixedTok > 18000 {
				fixedTok = 18000
			}
		}
		estTotal := fixedTok + summaryEst
		if estTotal < baseUsed {
			compactedPending = true
			usedTokens = estTotal
			groups = nonMsg
			if len(nonMsg) > 0 {
				groups = append(groups, groupItem{key: "messages", label: "消息", tokens: summaryEst})
			}
		}
	}

	maxTokens := int64(0)
	if reportedMax > 0 && usedTokens <= reportedMax {
		maxTokens = reportedMax
	}
	pctOf := func(tok int64) float64 {
		if maxTokens <= 0 {
			return 0
		}
		return math.Round(float64(tok)*1000.0/float64(maxTokens)) / 10.0
	}

	var totalRaw int64
	for _, g := range groups {
		totalRaw += g.tokens
	}

	var categories []ContextCategory
	if totalRaw > 0 && len(groups) > 0 {
		// Largest-remainder apportionment so category tokens sum exactly to usedTokens.
		type remPair struct {
			idx int
			rem float64
		}
		counts := make([]int64, len(groups))
		rems := make([]remPair, len(groups))
		var allocated int64
		for i, g := range groups {
			exact := float64(usedTokens) * float64(g.tokens) / float64(totalRaw)
			fl := int64(math.Floor(exact))
			counts[i] = fl
			allocated += fl
			rems[i] = remPair{idx: i, rem: exact - float64(fl)}
		}
		sort.Slice(rems, func(i, j int) bool {
			return rems[i].rem > rems[j].rem
		})
		for i := int64(0); i < usedTokens-allocated; i++ {
			counts[rems[int(i)%len(rems)].idx]++
		}
		for i, g := range groups {
			categories = append(categories, ContextCategory{
				Key:       g.key,
				Label:     g.label,
				Tokens:    counts[i],
				Percent:   pctOf(counts[i]),
				Tone:      "active",
				Estimated: true,
			})
		}
		sort.Slice(categories, func(i, j int) bool {
			return categories[i].Tokens > categories[j].Tokens
		})
	} else if usedTokens > 0 {
		categories = append(categories, ContextCategory{
			Key:       "input",
			Label:     "输入上下文",
			Tokens:    usedTokens,
			Percent:   pctOf(usedTokens),
			Tone:      "active",
			Estimated: !gen.hasProviderInput || compactedPending,
		})
	}

	var freeTokens int64
	var freePct float64
	if maxTokens > 0 {
		freeTokens = maxTokens - usedTokens
		if freeTokens < 0 {
			freeTokens = 0
		}
		freePct = pctOf(freeTokens)
		categories = append(categories, ContextCategory{
			Key:     "free",
			Label:   "剩余空间",
			Tokens:  freeTokens,
			Percent: freePct,
			Tone:    "free",
		})
	}

	cacheReused := gen.cacheReadTokens
	newInput := gen.inputTokens
	if gen.hasProviderInput && (cacheReused > 0 || newInput > 0) {
		totStepIn := cacheReused + newInput
		cachePct := math.Round(float64(cacheReused)*1000.0/float64(totStepIn)) / 10.0
		newPct := math.Round((100.0-cachePct)*10) / 10.0
		return &SessionContextWindow{
			UsedTokens:        usedTokens,
			MaxTokens:         maxTokens,
			UsedPercent:       pctOf(usedTokens),
			FreeTokens:        freeTokens,
			FreePercent:       freePct,
			CompactedPending:  compactedPending,
			Categories:        categories,
			CacheReusedTokens: cacheReused,
			CacheReusedPct:    cachePct,
			NewInputTokens:    newInput,
			NewInputPct:       newPct,
			Source:            resolveCtxSource(compactedPending, gen.hasProviderInput),
		}
	}

	return &SessionContextWindow{
		UsedTokens:       usedTokens,
		MaxTokens:        maxTokens,
		UsedPercent:      pctOf(usedTokens),
		FreeTokens:       freeTokens,
		FreePercent:      freePct,
		CompactedPending: compactedPending,
		Categories:       categories,
		Source:           resolveCtxSource(compactedPending, gen.hasProviderInput),
	}
}

func resolveCtxSource(compactedPending, hasActual bool) string {
	if compactedPending {
		return "post-compact-estimate"
	}
	if hasActual {
		return "provider-usage"
	}
	return "client-estimate"
}

var (
	reG4Short   = regexp.MustCompile(`(?i)^gemini[\s-]+4[\s-]+([a-z0-9]+)`)
	reGVerShort = regexp.MustCompile(`(?i)^gemini[\s-]+(\d+(?:\.\d+)?)[\s-]+(pro|flash)`)
	reEndDepth  = regexp.MustCompile(`(?i)-(?:high|xhigh|medium|low|trial|n)$`)
)

func shortSessionModelName(name string) string {
	clean := strings.TrimSpace(reParenEffort.ReplaceAllString(name, " "))
	if clean == "" {
		return "Unknown"
	}
	if m := reG4Short.FindStringSubmatch(clean); len(m) > 1 {
		v := strings.ToLower(m[1])
		return strings.ToUpper(v[:1]) + v[1:]
	}
	if m := reGVerShort.FindStringSubmatch(clean); len(m) > 2 {
		tier := strings.ToLower(m[2])
		return m[1] + " " + strings.ToUpper(tier[:1]) + tier[1:]
	}
	return reEndDepth.ReplaceAllString(clean, "")
}

func buildModelContexts(gens []rawGeneration, checkpointSteps []int, postCompact *SessionContextWindow) ([]ModelContextEntry, int) {
	var valid []rawGeneration
	var knownExecEnum uint64
	hasOrch := false
	for _, g := range gens {
		used := g.providerInputTokens
		if !g.hasProviderInput || used <= 0 {
			if g.context != nil {
				used = g.context.usedTokens
			}
		}
		if g.modelName != "" && used > 0 {
			valid = append(valid, g)
			if knownExecEnum == 0 && g.executionModelEnum != 0 {
				knownExecEnum = g.executionModelEnum
				hasOrch = true
			}
			if strings.Contains(strings.ToLower(g.modelName), "orchestrator") {
				hasOrch = true
			}
		}
	}
	if len(valid) == 0 {
		return nil, len(checkpointSteps)
	}

	entryMap := make(map[string]*ModelContextEntry)
	var order []string
	var lastKey string
	for _, g := range valid {
		used := g.providerInputTokens
		if !g.hasProviderInput || used <= 0 {
			used = g.context.usedTokens
		}
		var rawMax int64
		if g.context != nil && g.context.maxTokens > 0 {
			rawMax = g.context.maxTokens
		}
		var maxTok int64
		if rawMax > 0 && used <= rawMax {
			maxTok = rawMax
		}
		role := ""
		if hasOrch {
			if knownExecEnum != 0 && g.modelEnum != 0 {
				if g.modelEnum == knownExecEnum {
					role = "execution"
				} else {
					role = "planner"
				}
			} else {
				role = "planner"
			}
		}
		shortName := shortSessionModelName(g.modelName)
		key := role + ":" + strings.ToLower(shortName)
		prev, exists := entryMap[key]
		if !exists {
			prev = &ModelContextEntry{
				Role:      role,
				ModelName: g.modelName,
				ShortName: shortName,
			}
			entryMap[key] = prev
			order = append(order, key)
		} else if prev.UsedTokens > 0 && used < int64(float64(prev.UsedTokens)*0.72) && prev.UsedTokens-used > 30000 {
			prev.Compactions++
		}
		if maxTok > 0 {
			prev.MaxTokens = maxTok
		}
		prev.ModelName = g.modelName
		prev.UsedTokens = used
		if prev.MaxTokens > 0 {
			prev.UsedPercent = math.Round(float64(used)*1000.0/float64(prev.MaxTokens)) / 10.0
		}
		lastKey = key
	}

	var entries []ModelContextEntry
	detectedCompactions := 0
	for _, key := range order {
		e := *entryMap[key]
		if key == lastKey {
			e.IsCurrent = true
			if postCompact != nil && postCompact.CompactedPending && postCompact.UsedTokens > 0 {
				e.UsedTokens = postCompact.UsedTokens
				if postCompact.MaxTokens > 0 {
					e.MaxTokens = postCompact.MaxTokens
				}
				if e.MaxTokens > 0 {
					e.UsedPercent = math.Round(float64(e.UsedTokens)*1000.0/float64(e.MaxTokens)) / 10.0
				}
			}
		}
		detectedCompactions += e.Compactions
		entries = append(entries, e)
	}
	compactions := len(checkpointSteps)
	if detectedCompactions > compactions {
		compactions = detectedCompactions
	}
	if len(entries) == 1 && compactions > entries[0].Compactions {
		entries[0].Compactions = compactions
	}
	return entries, compactions
}

func (c *Client) queryDescendantSubagents(rootUUID string, maxParse int) ([]SubagentSummary, *SubagentRollup) {
	if !isUUIDish(rootUUID) {
		return nil, nil
	}
	if maxParse <= 0 {
		maxParse = 20
	}
	query := fmt.Sprintf(`WITH RECURSIVE tree(cid, pid, agent_name, title, status, step_count, depth, last_mod) AS (
		SELECT conversation_id, parent_conversation_id, agent_name, title, status, step_count, 1, last_modified_time
		FROM conversation_summaries WHERE parent_conversation_id = '%s'
		UNION ALL
		SELECT c.conversation_id, c.parent_conversation_id, c.agent_name, c.title, c.status, c.step_count, t.depth + 1, c.last_modified_time
		FROM conversation_summaries c JOIN tree t ON c.parent_conversation_id = t.cid
		WHERE t.depth < 6
	)
	SELECT json_group_array(json_object('id', cid, 'parentId', pid, 'agentName', agent_name, 'title', title, 'status', status, 'stepCount', step_count, 'depth', depth))
	FROM (SELECT * FROM tree ORDER BY last_mod DESC);`, rootUUID)

	seen := make(map[string]bool)
	var rows []subagentQueryRow
	for _, dir := range antigravityAppDataDirs() {
		sumDB := filepath.Join(dir, "conversation_summaries.db")
		if _, err := os.Stat(sumDB); err != nil {
			continue
		}
		out, err := runSQLiteReadOnly(sumDB, query, 2*time.Second)
		if err != nil {
			continue
		}
		trimmed := bytes.TrimSpace(out)
		if len(trimmed) == 0 || string(trimmed) == "null" {
			continue
		}
		var list []subagentQueryRow
		if json.Unmarshal(trimmed, &list) != nil {
			continue
		}
		for _, item := range list {
			if item.ID == "" || seen[item.ID] || !isUUIDish(item.ID) {
				continue
			}
			seen[item.ID] = true
			rows = append(rows, item)
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}

	var list []SubagentSummary
	rollup := &SubagentRollup{TotalCount: len(rows)}
	var subInputSum, subCacheSum int64

	limit := len(rows)
	if limit > maxParse {
		limit = maxParse
	}
	for _, r := range rows[:limit] {
		dbPath, _ := findConversationDB(r.ID)
		if dbPath == "" {
			continue
		}
		parsed, err := c.getOrParseLocalSession(dbPath)
		if err != nil || parsed == nil {
			continue
		}
		if parsed.steps == 0 && parsed.requests == 0 && (parsed.context == nil || parsed.context.UsedTokens == 0) {
			continue
		}
		name := strings.TrimSpace(r.AgentName)
		if name == "" {
			name = strings.TrimSpace(r.Title)
		}
		if name == "" {
			if len(r.ID) >= 8 {
				name = r.ID[:8]
			} else {
				name = r.ID
			}
		}
		var usedTok, maxTok int64
		var usedPct float64
		if parsed.context != nil {
			usedTok = parsed.context.UsedTokens
			maxTok = parsed.context.MaxTokens
			usedPct = parsed.context.UsedPercent
		}
		list = append(list, SubagentSummary{
			ConversationID:  r.ID,
			ParentID:        r.ParentID,
			Name:            name,
			ModelName:       parsed.modelName,
			ShortModel:      parsed.shortModel,
			Depth:           r.Depth,
			Status:          r.Status,
			UsedTokens:      usedTok,
			MaxTokens:       maxTok,
			UsedPercent:     usedPct,
			TotalTokens:     parsed.totalTokens,
			InputTokens:     parsed.inputTokens,
			CacheReadTokens: parsed.cacheReadTokens,
			OutputTokens:    parsed.outputTokens,
			CacheHitRate:    parsed.cacheHitRate,
			AvgSpeed:        parsed.avgSpeed,
			CostUSD:         parsed.costUSD,
			Requests:        parsed.requests,
			ToolCalls:       parsed.toolCalls,
		})
		rollup.Count++
		rollup.TotalTokens += parsed.totalTokens
		rollup.Requests += parsed.requests
		rollup.ToolCalls += parsed.toolCalls
		rollup.CostUSD += parsed.costUSD
		subInputSum += parsed.inputTokens
		subCacheSum += parsed.cacheReadTokens
	}

	if rollup.Count == 0 {
		return nil, nil
	}
	if totalIn := subInputSum + subCacheSum; totalIn > 0 {
		rollup.CacheHitRate = math.Round((float64(subCacheSum)*1000.0/float64(totalIn))) / 10.0
	}
	rollup.CostUSD = math.Round(rollup.CostUSD*10000) / 10000.0
	return list, rollup
}

func (c *Client) scanGatewaySessionSummary(uuid string) *gatewaySessionStats {
	if !isUUIDish(uuid) {
		return nil
	}
	needle := []byte(uuid)
	candidates := []string{
		filepath.Join(c.baseDir, "latency.jsonl.1"),
		filepath.Join(c.baseDir, "latency.jsonl"),
	}

	res := &gatewaySessionStats{}
	profSeen := make(map[string]bool)
	lastProfile := ""
	var streamToks int64
	var streamMsSum float64
	var e2eToks int64
	var e2eMsSum float64
	var ttfts []float64

	for _, path := range candidates {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 256*1024), 10*1024*1024)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 || !bytes.Contains(line, needle) {
				continue
			}
			var row latencyRow
			if json.Unmarshal(line, &row) != nil {
				continue
			}
			if stripSessionPrefix(row.Session) != uuid {
				continue
			}

			res.requests++
			if row.Profile != "" {
				if !profSeen[row.Profile] {
					profSeen[row.Profile] = true
					res.profiles = append(res.profiles, row.Profile)
				}
				if lastProfile != "" && lastProfile != row.Profile {
					res.profileSwitches++
				}
				lastProfile = row.Profile
			}
			res.retries += row.Retry
			if row.Status >= 400 || row.Err != nil {
				res.errors++
			}
			if row.Model != "" {
				res.modelName = row.Model
			}

			uncached := row.PTok - row.CacheTok
			if uncached < 0 {
				uncached = 0
			}
			genTok := row.CTok + row.TTok
			res.inputTokens += uncached
			res.cacheReadTokens += row.CacheTok
			res.outputTokens += genTok
			res.thinkingTokens += row.TTok
			if row.PTok > 0 || genTok > 0 {
				res.costUSD += estimateModelCostUSD(row.Model, row.PTok, row.CTok, row.CacheTok, row.TTok)
			}
			if row.Status == 200 && row.Err == nil && row.PTok > 0 {
				res.latestInTok = row.PTok
				res.latestCacheTok = row.CacheTok
				if tsMs := parseLatencyTimestampMs(row.TS); tsMs > res.lastRequestAtMs {
					res.lastRequestAtMs = tsMs
				}
			}
			if row.HeadMs > 0 {
				ttfts = append(ttfts, row.HeadMs)
				res.latestTtftMs = row.HeadMs
			}
			isStreamed := row.StreamMs >= 500 && genTok >= 20
			if isStreamed {
				streamToks += genTok
				streamMsSum += row.StreamMs
				if tps := float64(genTok) * 1000.0 / row.StreamMs; tps > 0 {
					res.latestSpeed = math.Round(tps*10) / 10.0
					if res.latestSpeed > res.peakSpeed {
						res.peakSpeed = res.latestSpeed
					}
				}
			} else if row.TotalMs > 0 && genTok > 0 {
				if tps := float64(genTok) * 1000.0 / row.TotalMs; tps > 0 {
					res.latestSpeed = math.Round(tps*10) / 10.0
				}
			}
			if row.TotalMs > 0 && genTok > 0 {
				e2eToks += genTok
				e2eMsSum += row.TotalMs
			}
		}
		f.Close()
	}

	if res.requests == 0 {
		return nil
	}
	res.totalTokens = res.inputTokens + res.cacheReadTokens + res.outputTokens
	res.costUSD = math.Round(res.costUSD*10000) / 10000.0
	if streamMsSum > 0 && streamToks > 0 {
		res.avgSpeed = math.Round((float64(streamToks)*1000.0/streamMsSum)*10) / 10.0
	} else if e2eMsSum > 0 && e2eToks > 0 {
		res.avgSpeed = math.Round((float64(e2eToks)*1000.0/e2eMsSum)*10) / 10.0
	}
	if totalPrompt := res.inputTokens + res.cacheReadTokens; totalPrompt > 0 {
		res.cacheHitRate = math.Round((float64(res.cacheReadTokens)*1000.0/float64(totalPrompt))) / 10.0
	}
	if len(ttfts) > 0 {
		var sum float64
		for _, v := range ttfts {
			sum += v
		}
		res.avgTtftMs = math.Round(sum / float64(len(ttfts)))
		sorted := append([]float64(nil), ttfts...)
		sort.Float64s(sorted)
		res.p50TtftMs = math.Round(sorted[len(sorted)/2])
	}
	return res
}

func computeCacheWindow(modelName string, lastRequestAtMs, nowMs int64) (windowMs, remainingMs int64) {
	if lastRequestAtMs <= 0 {
		return 0, 0
	}
	low := strings.ToLower(modelName)
	if !strings.Contains(low, "gemini") && !strings.Contains(low, "claude") &&
		!strings.Contains(low, "neon") && !strings.Contains(low, "hydrogen") &&
		!strings.Contains(low, "carbon") && !strings.Contains(low, "argon") &&
		!strings.Contains(low, "orchestrator") {
		return 0, 0
	}
	windowMs = 300_000
	age := nowMs - lastRequestAtMs
	if age < 0 {
		return windowMs, windowMs
	}
	if age >= windowMs {
		return windowMs, 0
	}
	return windowMs, windowMs - age
}

func (c *Client) GetSessionDetail(sessionID string) (*SessionDetail, error) {
	uuid := stripSessionPrefix(strings.TrimSpace(sessionID))
	if !isUUIDish(uuid) {
		return nil, fmt.Errorf("非法的会话标识: %s", sessionID)
	}

	metas := c.sessionMetas([]string{uuid})
	meta := metas[uuid]
	title := strings.TrimSpace(meta.Title)
	if title == "" {
		if len(uuid) >= 8 {
			title = "会话 " + uuid[:8]
		} else {
			title = uuid
		}
	}
	parentTitle := ""
	if meta.ParentID != "" {
		pMetas := c.sessionMetas([]string{meta.ParentID})
		parentTitle = strings.TrimSpace(pMetas[meta.ParentID].Title)
		if parentTitle == "" && len(meta.ParentID) >= 8 {
			parentTitle = "会话 " + meta.ParentID[:8]
		}
	}

	gw := c.scanGatewaySessionSummary(uuid)
	dbPath, _ := findConversationDB(uuid)
	var local *localSessionData
	if dbPath != "" {
		local, _ = c.getOrParseLocalSession(dbPath)
	}

	detail := &SessionDetail{
		SessionID:       sessionID,
		SessionUUID:     uuid,
		Title:           title,
		AgentName:       meta.AgentName,
		ParentSessionID: meta.ParentID,
		ParentTitle:     parentTitle,
		Source:          "gateway-only",
	}

	nowMs := time.Now().UnixMilli()
	if local != nil {
		detail.Source = "local-db"
		detail.ModelName = local.modelName
		detail.ShortModel = local.shortModel
		detail.Context = local.context
		detail.Turns = local.turns
		detail.Steps = local.steps
		detail.Requests = local.requests
		detail.ToolCalls = local.toolCalls
		detail.TotalTokens = local.totalTokens
		detail.InputTokens = local.inputTokens
		detail.CacheReadTokens = local.cacheReadTokens
		detail.OutputTokens = local.outputTokens
		detail.ThinkingTokens = local.thinkingTokens
		detail.CostUSD = local.costUSD
		detail.CostCNY = local.costCNY
		detail.AvgSpeed = local.avgSpeed
		detail.PeakSpeed = local.peakSpeed
		detail.LatestSpeed = local.latestSpeed
		detail.CacheHitRate = local.cacheHitRate
		detail.LastRequestAtMs = local.lastRequestAtMs
		detail.ModelTimeMs = local.modelTimeMs
		detail.ToolTimeMs = local.toolTimeMs
		detail.LatestTtftMs = local.latestTtftMs
		detail.AvgTtftMs = local.avgTtftMs
		detail.P50TtftMs = local.p50TtftMs
	}

	if gw != nil {
		detail.Profiles = gw.profiles
		detail.ProfileSwitches = gw.profileSwitches
		detail.GatewayRetries = gw.retries
		detail.GatewayErrors = gw.errors
		if detail.ModelName == "" && gw.modelName != "" {
			detail.ModelName = gw.modelName
			detail.ShortModel = shortSessionModelName(gw.modelName)
		}
		if gw.lastRequestAtMs > detail.LastRequestAtMs {
			detail.LastRequestAtMs = gw.lastRequestAtMs
		}
		if local == nil {
			detail.Requests = gw.requests
			detail.TotalTokens = gw.totalTokens
			detail.InputTokens = gw.inputTokens
			detail.CacheReadTokens = gw.cacheReadTokens
			detail.OutputTokens = gw.outputTokens
			detail.ThinkingTokens = gw.thinkingTokens
			detail.CostUSD = gw.costUSD
			detail.CostCNY = math.Round(gw.costUSD*7.20*100) / 100.0
			detail.AvgSpeed = gw.avgSpeed
			detail.PeakSpeed = gw.peakSpeed
			detail.LatestSpeed = gw.latestSpeed
			detail.CacheHitRate = gw.cacheHitRate
			detail.LatestTtftMs = gw.latestTtftMs
			detail.AvgTtftMs = gw.avgTtftMs
			detail.P50TtftMs = gw.p50TtftMs
			if gw.latestInTok > 0 {
				newIn := gw.latestInTok - gw.latestCacheTok
				if newIn < 0 {
					newIn = 0
				}
				cachePct := math.Round(float64(gw.latestCacheTok)*1000.0/float64(gw.latestInTok)) / 10.0
				detail.Context = &SessionContextWindow{
					UsedTokens:        gw.latestInTok,
					Categories:        []ContextCategory{{Key: "input", Label: "输入上下文", Tokens: gw.latestInTok, Tone: "active", Estimated: true}},
					CacheReusedTokens: gw.latestCacheTok,
					CacheReusedPct:    cachePct,
					NewInputTokens:    newIn,
					NewInputPct:       math.Round((100.0-cachePct)*10) / 10.0,
					Source:            "gateway-only",
				}
			}
		} else {
			if detail.Requests == 0 && gw.requests > 0 {
				detail.Requests = gw.requests
			}
			if detail.CostUSD == 0 && gw.costUSD > 0 {
				detail.CostUSD = gw.costUSD
				detail.CostCNY = math.Round(gw.costUSD*7.20*100) / 100.0
			}
		}
	}

	detail.CacheWindowMs, detail.CacheRemainingMs = computeCacheWindow(detail.ModelName, detail.LastRequestAtMs, nowMs)

	subagents, rollup := c.queryDescendantSubagents(uuid, 20)
	if len(subagents) > 0 && rollup != nil {
		rollup.CombinedTotalTokens = detail.TotalTokens + rollup.TotalTokens
		rollup.CombinedCostUSD = math.Round((detail.CostUSD+rollup.CostUSD)*10000) / 10000.0
		detail.Subagents = subagents
		detail.SubagentRollup = rollup
	}

	return detail, nil
}
