package agyp

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func encodeTestVarint(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			out = append(out, b)
			return out
		}
		out = append(out, b|0x80)
	}
}

func pbVarint(field uint32, v uint64) []byte {
	out := encodeTestVarint(uint64(field)<<3 | 0)
	out = append(out, encodeTestVarint(v)...)
	return out
}

func pbBytes(field uint32, raw []byte) []byte {
	out := encodeTestVarint(uint64(field)<<3 | 2)
	out = append(out, encodeTestVarint(uint64(len(raw)))...)
	out = append(out, raw...)
	return out
}

func pbString(field uint32, s string) []byte {
	return pbBytes(field, []byte(s))
}

func pbTimestampMs(ms int64) []byte {
	sec := uint64(ms / 1000)
	nanos := uint64((ms % 1000) * 1_000_000)
	var out []byte
	out = append(out, pbVarint(1, sec)...)
	out = append(out, pbVarint(2, nanos)...)
	return out
}

func TestProtoWireAndStepIndices(t *testing.T) {
	var payload []byte
	// Unpacked field 2 + packed field 2
	payload = append(payload, pbVarint(2, 1)...)
	packed := append(encodeTestVarint(2), encodeTestVarint(5)...)
	payload = append(payload, pbBytes(2, packed)...)
	payload = append(payload, pbString(4, "exec-123")...)
	// Fixed64 wire type 1 on field 99
	payload = append(payload, encodeTestVarint(uint64(99)<<3|1)...)
	var f64 [8]byte
	binary.LittleEndian.PutUint64(f64[:], 42)
	payload = append(payload, f64[:]...)
	// Truncated field at end should not panic and should keep prior fields
	payload = append(payload, encodeTestVarint(uint64(77)<<3|2)...)
	payload = append(payload, encodeTestVarint(999)...)

	m := parseProtoFields(payload)
	if got := protoFirstString(m, 4); got != "exec-123" {
		t.Fatalf("expected exec-123, got %q", got)
	}
	indices := protoStepIndices(m[2])
	if len(indices) != 3 || indices[0] != 1 || indices[1] != 2 || indices[2] != 5 {
		t.Fatalf("unexpected step indices: %v", indices)
	}
}

func TestBuildContextWindowLargestRemainderAndPostCompact(t *testing.T) {
	gen := &rawGeneration{
		idx:                 0,
		stepIndices:         []int{1, 2},
		modelName:           "Gemini 4 Neon (XHigh)",
		hasProviderInput:    true,
		providerInputTokens: 100000,
		inputTokens:         5000,
		cacheReadTokens:     95000,
		outputTokens:        1200,
		context: &rawContextMeta{
			usedTokens: 80000,
			maxTokens:  512000,
			groups: []rawCtxGroup{
				{
					label:  "System Prompt",
					tokens: 10000,
					children: []rawCtxChild{
						{label: "identity", tokens: 100},
						{label: "skills", tokens: 4000},
					},
				},
				{label: "Tools", tokens: 10000},
				{label: "Chat Messages", tokens: 60000},
			},
		},
	}

	ctx := buildContextWindow(gen, nil)
	if ctx == nil {
		t.Fatalf("expected non-nil context window")
	}
	if ctx.UsedTokens != 100000 || ctx.MaxTokens != 512000 {
		t.Fatalf("unexpected used/max: %d / %d", ctx.UsedTokens, ctx.MaxTokens)
	}
	var sumActive int64
	hasSkills := false
	hasSys := false
	for _, cat := range ctx.Categories {
		if cat.Tone == "free" {
			continue
		}
		sumActive += cat.Tokens
		if cat.Key == "skills" {
			hasSkills = true
		}
		if cat.Key == "system_prompt" {
			hasSys = true
		}
	}
	if sumActive != ctx.UsedTokens {
		t.Fatalf("category sum %d != usedTokens %d", sumActive, ctx.UsedTokens)
	}
	if !hasSkills || !hasSys {
		t.Fatalf("expected skills split out of system prompt: %+v", ctx.Categories)
	}
	if ctx.CacheReusedTokens != 95000 || ctx.NewInputTokens != 5000 {
		t.Fatalf("unexpected cache/new split: %d / %d", ctx.CacheReusedTokens, ctx.NewInputTokens)
	}

	// Post-compact check when checkpoint step > gen max step
	postCtx := buildContextWindow(gen, &checkpointMeta{stepIndex: 5, summaryTokens: 3500})
	if postCtx == nil || !postCtx.CompactedPending {
		t.Fatalf("expected post-compact estimate, got %+v", postCtx)
	}
	if postCtx.UsedTokens >= 100000 {
		t.Fatalf("expected post-compact usedTokens < 100000, got %d", postCtx.UsedTokens)
	}
}

func TestMergeToolIntervals(t *testing.T) {
	intervals := [][2]float64{
		{1000, 3000},
		{2000, 4500}, // overlaps first -> merged [1000, 4500] = 3500
		{5000, 6200}, // disjoint -> 1200
	}
	got := mergeToolIntervals(intervals)
	if math.Abs(got-4700) > 0.01 {
		t.Fatalf("expected 4700ms merged tool time, got %v", got)
	}
}

func TestAnnotateRequestCacheAnomalies(t *testing.T) {
	// Rows in newest-first order (matching GetRecentLogs)
	rows := []RequestLogRow{
		{
			Time: "10:03:20.000", TimestampMs: 200_000, Status: 200,
			Session: "agent/sess-a", Model: "gemini-4-neon", Profile: "backup",
			InTok: 120000, CacheTok: 0, Retry: 1,
		},
		{
			Time: "10:02:50.000", TimestampMs: 170_000, Status: 200,
			Session: "agent/sess-a", Model: "gemini-4-neon", Profile: "primary",
			InTok: 118000, CacheTok: 110000,
		},
		// Compaction shrink (>40% drop in input) should NOT trigger false positive
		{
			Time: "10:02:00.000", TimestampMs: 120_000, Status: 200,
			Session: "agent/sess-b", Model: "gemini-4-neon", Profile: "primary",
			InTok: 20000, CacheTok: 0,
		},
		{
			Time: "10:01:40.000", TimestampMs: 100_000, Status: 200,
			Session: "agent/sess-b", Model: "gemini-4-neon", Profile: "primary",
			InTok: 120000, CacheTok: 110000,
		},
		// Steep cliff drop within 30s on same input size -> "dropped"
		{
			Time: "10:01:00.000", TimestampMs: 60_000, Status: 200,
			Session: "agent/sess-c", Model: "gemini-4-hydrogen", Profile: "primary",
			InTok: 100000, CacheTok: 10000,
		},
		{
			Time: "10:00:30.000", TimestampMs: 30_000, Status: 200,
			Session: "agent/sess-c", Model: "gemini-4-hydrogen", Profile: "primary",
			InTok: 98000, CacheTok: 92000,
		},
	}

	annotateRequestCacheAnomalies(rows)

	if rows[0].CacheAnomaly != "cleared" {
		t.Fatalf("expected cleared anomaly on rows[0], got %q (%s)", rows[0].CacheAnomaly, rows[0].CacheAnomalyTip)
	}
	if rows[2].CacheAnomaly != "" {
		t.Fatalf("did not expect false positive on post-compact shrink, got %q", rows[2].CacheAnomaly)
	}
	if rows[4].CacheAnomaly != "dropped" {
		t.Fatalf("expected dropped anomaly on rows[4], got %q (%s)", rows[4].CacheAnomaly, rows[4].CacheAnomalyTip)
	}
}

func buildTestGenBlob(model string, modelEnum, execEnum uint64, inTok, cacheTok, outTok, thinkTok int64, ttftMs, streamMs int64, ctxUsed, ctxMax int64, stepIdx int) []byte {
	var usage []byte
	usage = append(usage, pbVarint(2, uint64(inTok))...)
	usage = append(usage, pbVarint(5, uint64(cacheTok))...)
	usage = append(usage, pbVarint(3, uint64(outTok))...)
	usage = append(usage, pbVarint(9, uint64(thinkTok))...)
	usage = append(usage, pbVarint(10, uint64(outTok-thinkTok))...)

	var breakdown []byte
	sysChild := append(pbString(1, "skills"), pbVarint(3, 4000)...)
	sysGrp := append(pbString(1, "System Prompt"), pbVarint(4, 9000)...)
	sysGrp = append(sysGrp, pbBytes(5, sysChild)...)
	msgGrp := append(pbString(1, "Chat Messages"), pbVarint(4, uint64(ctxUsed-9000))...)
	breakdown = append(breakdown, pbBytes(1, sysGrp)...)
	breakdown = append(breakdown, pbBytes(1, msgGrp)...)

	var ctxMeta []byte
	ctxMeta = append(ctxMeta, pbVarint(1, uint64(ctxUsed))...)
	ctxMeta = append(ctxMeta, pbBytes(3, breakdown)...)
	ctxMeta = append(ctxMeta, pbVarint(4, uint64(ctxMax))...)

	startMeta := pbBytes(10, ctxMeta)

	var chat []byte
	chat = append(chat, pbVarint(3, modelEnum)...)
	chat = append(chat, pbBytes(4, usage)...)
	chat = append(chat, pbBytes(9, startMeta)...)
	chat = append(chat, pbBytes(11, pbTimestampMs(ttftMs))...)
	chat = append(chat, pbBytes(12, pbTimestampMs(streamMs))...)
	chat = append(chat, pbString(21, model)...)

	var gen []byte
	gen = append(gen, pbBytes(1, chat)...)
	gen = append(gen, pbVarint(2, uint64(stepIdx))...)
	if execEnum > 0 {
		planExec := pbVarint(1, execEnum)
		plannerCfg := pbBytes(60, planExec)
		gen = append(gen, pbBytes(3, plannerCfg)...)
	}
	return gen
}

func TestGetSessionDetailEndToEndSQLite(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not installed")
	}

	tmpHome := t.TempDir()
	agypDir := filepath.Join(tmpHome, ".agyp")
	appDir := filepath.Join(tmpHome, "antigravity-data")
	convDir := filepath.Join(appDir, "conversations")
	if err := os.MkdirAll(agypDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", appDir)

	parentUUID := "11111111-1111-4111-8111-111111111111"
	subUUID := "22222222-2222-4222-8222-222222222222"

	// Create conversation_summaries.db
	sumDB := filepath.Join(appDir, "conversation_summaries.db")
	sumSQL := fmt.Sprintf(`
CREATE TABLE conversation_summaries (
  conversation_id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT "",
  status TEXT NOT NULL DEFAULT "",
  step_count INTEGER NOT NULL DEFAULT 0,
  last_modified_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  parent_conversation_id TEXT NOT NULL DEFAULT "",
  agent_name TEXT NOT NULL DEFAULT ""
);
INSERT INTO conversation_summaries (conversation_id, title, status, step_count, parent_conversation_id, agent_name) VALUES
  ('%s', 'Parent Feature Session', 'idle', 4, '', ''),
  ('%s', '', 'completed', 2, '%s', 'Codebase Researcher');
`, parentUUID, subUUID, parentUUID)
	if out, err := exec.Command("sqlite3", sumDB, sumSQL).CombinedOutput(); err != nil {
		t.Fatalf("create summary db failed: %v: %s", err, string(out))
	}

	// Create parent conversation DB
	parentGen := buildTestGenBlob("Gemini 4 Neon (XHigh)", 10, 10, 2000, 98000, 1000, 200, 1500, 4000, 100000, 512000, 1)
	stepMeta0 := pbBytes(1, pbTimestampMs(1700000000000))
	stepMeta1 := pbBytes(1, pbTimestampMs(1700000001000))
	stepMeta2 := append(pbBytes(32, pbTimestampMs(1700000006000)), pbBytes(8, pbTimestampMs(1700000008500))...)

	parentSQL := fmt.Sprintf(`
CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 0, metadata BLOB, step_payload BLOB);
CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB);
INSERT INTO steps (idx, step_type, status, metadata) VALUES
  (0, 14, 3, X'%s'),
  (1, 15, 3, X'%s'),
  (2, 132, 3, X'%s'),
  (3, 23, 3, X'%s');
INSERT INTO gen_metadata (idx, data) VALUES (0, X'%s');
`, hex.EncodeToString(stepMeta0), hex.EncodeToString(stepMeta1), hex.EncodeToString(stepMeta2), hex.EncodeToString(stepMeta0), hex.EncodeToString(parentGen))
	// Note: step 3 is checkpoint at idx 3 > gen step 1 -> keep gen at step 3 to test exact context without post-compact override:
	parentGenAt3 := buildTestGenBlob("Gemini 4 Neon (XHigh)", 10, 10, 2000, 98000, 1000, 200, 1500, 4000, 100000, 512000, 3)
	parentSQL = fmt.Sprintf(`
CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 0, metadata BLOB, step_payload BLOB);
CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB);
INSERT INTO steps (idx, step_type, status, metadata) VALUES
  (0, 14, 3, X'%s'),
  (1, 23, 3, X'%s'),
  (2, 132, 3, X'%s'),
  (3, 15, 3, X'%s');
INSERT INTO gen_metadata (idx, data) VALUES (0, X'%s');
`, hex.EncodeToString(stepMeta0), hex.EncodeToString(stepMeta0), hex.EncodeToString(stepMeta2), hex.EncodeToString(stepMeta1), hex.EncodeToString(parentGenAt3))
	if out, err := exec.Command("sqlite3", filepath.Join(convDir, parentUUID+".db"), parentSQL).CombinedOutput(); err != nil {
		t.Fatalf("create parent db failed: %v: %s", err, string(out))
	}

	// Create subagent conversation DB
	subGen := buildTestGenBlob("Gemini 4 Hydrogen (High)", 20, 0, 1000, 24000, 500, 50, 1000, 2000, 25000, 512000, 1)
	subSQL := fmt.Sprintf(`
CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 0, metadata BLOB, step_payload BLOB);
CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB);
INSERT INTO steps (idx, step_type, status, metadata) VALUES
  (0, 14, 3, X'%s'),
  (1, 15, 3, X'%s'),
  (2, 132, 3, X'%s');
INSERT INTO gen_metadata (idx, data) VALUES (0, X'%s');
`, hex.EncodeToString(stepMeta0), hex.EncodeToString(stepMeta1), hex.EncodeToString(stepMeta2), hex.EncodeToString(subGen))
	if out, err := exec.Command("sqlite3", filepath.Join(convDir, subUUID+".db"), subSQL).CombinedOutput(); err != nil {
		t.Fatalf("create sub db failed: %v: %s", err, string(out))
	}

	// Gateway latency row for profile overlay
	latLine := fmt.Sprintf(`{"ts":"2026-11-26 08:00:00.000","profile":"primary","session":"agent/%s","path":"/v1internal:streamGenerateContent","status":200,"head_ms":1500,"stream_ms":4000,"total_ms":5500,"model":"gemini-4-neon","p_tok":100000,"cache_tok":98000,"c_tok":800,"t_tok":200,"retry":1}`+"\n", parentUUID)
	if err := os.WriteFile(filepath.Join(agypDir, "latency.jsonl"), []byte(latLine), 0644); err != nil {
		t.Fatal(err)
	}

	client := NewClient(agypDir)
	detail, err := client.GetSessionDetail("agent/" + parentUUID)
	if err != nil {
		t.Fatalf("GetSessionDetail failed: %v", err)
	}
	if detail.Source != "local-db" {
		t.Fatalf("expected source local-db, got %s", detail.Source)
	}
	if detail.Title != "Parent Feature Session" {
		t.Fatalf("unexpected title: %q", detail.Title)
	}
	if detail.Turns != 1 || detail.Steps != 4 || detail.ToolCalls != 1 {
		t.Fatalf("unexpected step counts: turns=%d steps=%d tools=%d", detail.Turns, detail.Steps, detail.ToolCalls)
	}
	if detail.Context == nil || detail.Context.UsedTokens != 100000 || detail.Context.Compactions != 1 {
		t.Fatalf("unexpected context window: %+v", detail.Context)
	}
	if math.Abs(detail.ToolTimeMs-2500) > 1 {
		t.Fatalf("expected toolTimeMs=2500, got %v", detail.ToolTimeMs)
	}
	if detail.GatewayRetries != 1 || len(detail.Profiles) != 1 || detail.Profiles[0] != "primary" {
		t.Fatalf("unexpected gateway overlay: retries=%d profiles=%v", detail.GatewayRetries, detail.Profiles)
	}
	if detail.SubagentRollup == nil || detail.SubagentRollup.Count != 1 {
		t.Fatalf("expected 1 subagent rollup, got %+v", detail.SubagentRollup)
	}
	if detail.Subagents[0].Name != "Codebase Researcher" || detail.Subagents[0].ShortModel != "Hydrogen" {
		t.Fatalf("unexpected subagent metadata: %+v", detail.Subagents[0])
	}
	if detail.SubagentRollup.CombinedTotalTokens != detail.TotalTokens+detail.Subagents[0].TotalTokens {
		t.Fatalf("unexpected combined tokens: %d", detail.SubagentRollup.CombinedTotalTokens)
	}
}
