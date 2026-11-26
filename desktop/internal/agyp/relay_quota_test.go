package agyp

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newHermeticRelayTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	profilesPayload := map[string]any{
		"active_profile": "primary",
		"profiles": map[string]any{
			"primary": map[string]any{
				"email": "primary@example.com",
				"tier":  map[string]any{"id": "g1-ultra-tier", "name": "Google AI Ultra", "label": "Ultra"},
				"last_quota": map[string]any{
					"gemini_5h":     0.92,
					"gemini_weekly": 0.90,
					"claude_weekly": 0.95,
				},
			},
			"backup": map[string]any{
				"email": "backup@example.com",
				"tier":  map[string]any{"id": "g1-pro-tier", "name": "Google AI Pro", "label": "Pro"},
				"last_quota": map[string]any{
					"gemini_5h":     1.0,
					"gemini_weekly": 0.85,
					"claude_weekly": 1.0,
				},
			},
		},
	}
	profBytes, _ := json.Marshal(profilesPayload)
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), profBytes, 0644); err != nil {
		t.Fatal(err)
	}

	metricsPayload := map[string]any{
		"summary": map[string]any{
			"total_requests":      12,
			"total_input_tokens":  12000,
			"total_cached_tokens": 9000,
			"total_output_tokens": 2400,
		},
		"by_date": map[string]any{
			yesterday: map[string]any{
				"requests":      5,
				"input_tokens":  5000,
				"cached_tokens": 3500,
				"output_tokens": 1000,
				"profiles": map[string]any{
					"backup": map[string]any{"requests": 5, "input_tokens": 5000, "cached_tokens": 3500, "output_tokens": 1000, "total_tokens": 6000},
				},
			},
			today: map[string]any{
				"requests":      7,
				"input_tokens":  7000,
				"cached_tokens": 5500,
				"output_tokens": 1400,
				"profiles": map[string]any{
					"primary": map[string]any{"requests": 7, "input_tokens": 7000, "cached_tokens": 5500, "output_tokens": 1400, "total_tokens": 8400},
				},
			},
		},
		"by_profile": map[string]any{
			"primary": map[string]any{"requests": 7, "input_tokens": 7000, "cached_tokens": 5500, "output_tokens": 1400},
			"backup":  map[string]any{"requests": 5, "input_tokens": 5000, "cached_tokens": 3500, "output_tokens": 1000},
		},
	}
	metBytes, _ := json.Marshal(metricsPayload)
	if err := os.WriteFile(filepath.Join(dir, "metrics.json"), metBytes, 0644); err != nil {
		t.Fatal(err)
	}

	ts := now.Format("2006-01-02T15:04:05")
	latLine := `{"ts":"` + ts + `","profile":"primary","session":"agent/demo-session","path":"/v1internal:streamGenerateContent","status":200,"head_ms":420,"stream_ms":1000,"total_ms":1420,"model":"gemini-3.5-pro","p_tok":4000,"cache_tok":3200,"c_tok":600,"t_tok":200,"retry":0}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "latency.jsonl"), []byte(latLine), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDumpRelayJSON(t *testing.T) {
	client := NewClient(newHermeticRelayTestDir(t))
	relay, err := client.GetRelayTimeline()
	if err != nil {
		t.Fatalf("GetRelayTimeline failed: %v", err)
	}
	data, err := json.MarshalIndent(relay, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("expected non-empty relay JSON")
	}
	t.Logf("Serialized %d bytes of hermetic relay JSON", len(data))
}

func TestRelayQuotaData(t *testing.T) {
	client := NewClient(newHermeticRelayTestDir(t))
	weekly, err := client.GetWeeklyQuotaData()
	if err != nil {
		t.Fatalf("GetWeeklyQuotaData failed: %v", err)
	}
	if weekly.CurrentCycleLabel == "" {
		t.Errorf("CurrentCycleLabel is empty")
	}
	if len(weekly.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(weekly.Accounts))
	}

	sessions, err := client.GetDailyTopSessions(7)
	if err != nil {
		t.Fatalf("GetDailyTopSessions failed: %v", err)
	}
	if len(sessions) == 0 {
		t.Errorf("expected non-empty DailyTopSessions")
	}

	relay, err := client.GetRelayTimeline()
	if err != nil {
		t.Fatalf("GetRelayTimeline failed: %v", err)
	}
	if relay.WeeklyQuota == nil || len(relay.RelayOrder) != 2 {
		t.Fatalf("unexpected relay timeline: weekly=%v order=%v", relay.WeeklyQuota != nil, relay.RelayOrder)
	}
	data, err := json.Marshal(relay)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	previewLen := min(len(data), 160)
	t.Logf("JSON length: %d, sample: %s", len(data), string(data[:previewLen]))
}

func TestCalculateCycleStart(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// Wednesday 2026-11-18 10:35:23
	now := time.Date(2026, 11, 18, 10, 35, 23, 0, loc)

	// Upcoming Wednesday reset 2026-11-25 10:35:25 (7 days + 2s later)
	resetTime := time.Date(2026, 11, 25, 10, 35, 25, 0, loc)

	cycleStart := calculateCycleStart(resetTime, now)
	expectedStart := time.Date(2026, 11, 18, 0, 0, 0, 0, loc)
	if !cycleStart.Equal(expectedStart) {
		t.Errorf("Expected cycleStart to be %v, got %v", expectedStart, cycleStart)
	}

	// Zero reset fallback (Saturday alignment)
	zeroStart := calculateCycleStart(time.Time{}, now)
	expectedZero := time.Date(2026, 11, 14, 0, 0, 0, 0, loc)
	if !zeroStart.Equal(expectedZero) {
		t.Errorf("Expected zero fallback to be %v, got %v", expectedZero, zeroStart)
	}

	// If reset time was 5 minutes ago (2026-11-18 10:30:00) and now is 10:35:00,
	// quota has just reset today, cycleStart should still be today (2026-11-18)
	pastReset := time.Date(2026, 11, 18, 10, 30, 0, 0, loc)
	pastStart := calculateCycleStart(pastReset, now)
	if !pastStart.Equal(expectedStart) {
		t.Errorf("Expected past reset cycleStart to be %v, got %v", expectedStart, pastStart)
	}
}

func TestResetWeeklyCycleOverride(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agyp-cycle-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	client := NewClient(tmpDir)

	// Before override, getCycleOverride should return false
	now := time.Now()
	_, ok := client.getCycleOverride(now)
	if ok {
		t.Errorf("Expected no override initially")
	}

	// Trigger ResetWeeklyCycle
	if err := client.ResetWeeklyCycle(); err != nil {
		t.Fatalf("ResetWeeklyCycle failed: %v", err)
	}

	overrideTime, ok := client.getCycleOverride(now)
	if !ok {
		t.Fatalf("Expected override to be active after ResetWeeklyCycle")
	}

	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if !overrideTime.Equal(todayMidnight) {
		t.Errorf("Expected override time %v, got %v", todayMidnight, overrideTime)
	}
}

func TestPauseAndSingleAccountRouting(t *testing.T) {
	tmpDir := t.TempDir()
	profilesJSON := `{
  "active_profile": "primary",
  "profiles": {
    "secondary": {"email": "a@example.com", "paused": true, "tier": {"id": "g1-pro-tier", "name": "Google AI Pro", "label": "Pro"}, "last_quota": {"gemini_5h": 1.0, "gemini_weekly": 0.9}},
    "primary": {"email": "b@example.com", "tier": {"id": "g1-ultra-tier", "name": "Google AI Ultra", "label": "Ultra"}, "last_quota": {"gemini_5h": 0.8, "gemini_weekly": 0.8}},
    "backup": {"email": "c@example.com", "paused": true, "tier": {"id": "free-tier", "name": "Antigravity Starter Quota", "label": "Free"}, "last_quota": {"gemini_5h": 0.9, "gemini_weekly": 0.9}}
  }
}`
	if err := os.WriteFile(tmpDir+"/profiles.json", []byte(profilesJSON), 0644); err != nil {
		t.Fatalf("write profiles.json: %v", err)
	}
	if err := os.WriteFile(tmpDir+"/metrics.json", []byte(`{"summary":{},"by_date":{},"by_profile":{}}`), 0644); err != nil {
		t.Fatalf("write metrics.json: %v", err)
	}

	client := NewClient(tmpDir)
	dash, err := client.GetDashboardData(1)
	if err != nil {
		t.Fatalf("GetDashboardData failed: %v", err)
	}
	if len(dash.EnabledProfiles) != 1 || dash.EnabledProfiles[0] != "primary" {
		t.Fatalf("unexpected enabledProfiles: %v", dash.EnabledProfiles)
	}
	if len(dash.PausedProfiles) != 2 || dash.PausedProfiles[0] != "backup" || dash.PausedProfiles[1] != "secondary" {
		t.Fatalf("unexpected pausedProfiles: %v", dash.PausedProfiles)
	}
	if len(dash.Profiles) != 3 || dash.Profiles[0].Name != "primary" || dash.Profiles[0].Paused || dash.Profiles[0].Tier != "Ultra" {
		t.Fatalf("expected active enabled Ultra profile primary first, got: %+v", dash.Profiles)
	}
	if dash.Profiles[1].Name != "backup" || dash.Profiles[1].Tier != "Free" || dash.Profiles[2].Name != "secondary" || dash.Profiles[2].Tier != "Pro" {
		t.Fatalf("unexpected tiers on paused profiles: %+v", dash.Profiles)
	}

	rc, err := client.GetRoutingConfig()
	if err != nil {
		t.Fatalf("GetRoutingConfig failed: %v", err)
	}
	if len(rc.PriorityChain) != 1 || rc.PriorityChain[0] != "primary" {
		t.Fatalf("expected priorityChain=[primary], got: %v", rc.PriorityChain)
	}
	if len(rc.PausedProfiles) != 2 {
		t.Fatalf("expected 2 paused profiles in routing config, got: %v", rc.PausedProfiles)
	}

	relay, err := client.GetRelayTimeline()
	if err != nil {
		t.Fatalf("GetRelayTimeline failed: %v", err)
	}
	if len(relay.RelayOrder) != 1 || relay.RelayOrder[0] != "primary" {
		t.Fatalf("expected relayOrder=[primary], got: %v", relay.RelayOrder)
	}
}

func TestMetricsAccuracyAndNoFakeFallbacks(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	today := now.Format("2006-01-02")

	// 1. 构造含「无 cached_tokens 历史旧天」和「今日有 cached_tokens」的 metrics.json，且无 latency.jsonl
	metricsPayload := map[string]any{
		"summary": map[string]any{
			"total_requests":       20,
			"total_input_tokens":   51000,
			"total_output_tokens":  200,
			"total_cached_tokens":  800,
			"total_thought_tokens": 100,
		},
		"by_date": map[string]any{
			"2026-10-01": map[string]any{
				"requests":      18,
				"input_tokens":  50000, // 历史没有 cached_tokens 字段，不应稀释缓存命中率分母
				"output_tokens": 500,
			},
			today: map[string]any{
				"requests":       2,
				"input_tokens":   1000,
				"cached_tokens":  800,
				"output_tokens":  200,
				"thought_tokens": 100,
				"total_tokens":   1300,
				"profiles": map[string]any{
					"primary": map[string]any{
						"requests":      2,
						"input_tokens":  1000,
						"cached_tokens": 800,
						"output_tokens": 200,
						"total_tokens":  1300,
					},
				},
			},
		},
		"by_profile": map[string]any{
			"primary": map[string]any{"requests": 2, "input_tokens": 1000, "cached_tokens": 800, "output_tokens": 200},
		},
	}
	metricsBytes, _ := json.Marshal(metricsPayload)
	if err := os.WriteFile(filepath.Join(dir, "metrics.json"), metricsBytes, 0644); err != nil {
		t.Fatal(err)
	}

	profilesPayload := map[string]any{
		"active_profile": "primary",
		"profiles": map[string]any{
			"primary": map[string]any{
				"email": "primary@example.com",
				"last_quota": map[string]any{
					"gemini_5h":     0.0,
					"gemini_weekly": 0.0, // 已耗尽，不能被错误回退成 1.0 (100% 剩余)
					"claude_weekly": 0.0,
				},
			},
		},
	}
	profBytes, _ := json.Marshal(profilesPayload)
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), profBytes, 0644); err != nil {
		t.Fatal(err)
	}

	client := NewClient(dir)

	// 验证无 latency.jsonl 时不返回假模型/假耗时/假速度/假 sparkline
	dashEmpty, err := client.GetDashboardData(90)
	if err != nil {
		t.Fatalf("GetDashboardData failed: %v", err)
	}
	if len(dashEmpty.Models) != 0 {
		t.Fatalf("expected 0 models when latency.jsonl is missing, got: %+v", dashEmpty.Models)
	}
	if dashEmpty.Summary.P50LatencyMs != 0 || dashEmpty.Summary.AvgSpeed != 0 || dashEmpty.Summary.SuccessRate != 0 {
		t.Fatalf("expected zeroed latency/speed/successRate without telemetry, got: %+v", dashEmpty.Summary)
	}
	if dashEmpty.Summary.CacheHitRate < 79.9 || dashEmpty.Summary.CacheHitRate > 80.1 {
		t.Fatalf("expected CacheHitRate ~80%% (not diluted by pre-cache day), got %.2f", dashEmpty.Summary.CacheHitRate)
	}
	if dashEmpty.Summary.ThoughtTokens != 100 {
		t.Fatalf("expected ThoughtTokens=100, got %d", dashEmpty.Summary.ThoughtTokens)
	}
	for _, v := range dashEmpty.Summary.Sparklines["speed"] {
		if v != 0 {
			t.Fatalf("expected zeroed speed sparkline when no latency.jsonl, got %v", dashEmpty.Summary.Sparklines["speed"])
		}
	}
	if len(dashEmpty.Profiles) != 1 || dashEmpty.Profiles[0].ClaudeWeekly != 0 {
		t.Fatalf("expected exhausted ClaudeWeekly=0, got: %+v", dashEmpty.Profiles)
	}

	// 验证 gemini_weekly=0.0 不会被误写成 100% 剩余
	weekly, err := client.GetWeeklyQuotaData()
	if err != nil {
		t.Fatalf("GetWeeklyQuotaData failed: %v", err)
	}
	if len(weekly.Accounts) != 1 || weekly.Accounts[0].RemainingPct != 0 || math.Abs(weekly.Accounts[0].UsedPct-100) > 0.1 {
		t.Fatalf("expected exhausted account (RemainingPct=0, UsedPct=100), got: %+v", weekly.Accounts)
	}
	var lastCumBurn float64
	for _, pt := range weekly.DailyTrend {
		if !pt.IsFuture && pt.CumBurnPct > lastCumBurn {
			lastCumBurn = pt.CumBurnPct
		}
	}
	if lastCumBurn <= 0 {
		t.Fatalf("expected positive calibrated CumBurnPct in DailyTrend when UsedPct=100, got: %+v", weekly.DailyTrend)
	}

	// 验证 GetRoutingConfig 动态指向当前活跃账号且不含硬编码假规则
	rc, err := client.GetRoutingConfig()
	if err != nil {
		t.Fatalf("GetRoutingConfig failed: %v", err)
	}
	if len(rc.Rules) == 0 || rc.Rules[0].Target != "primary" {
		t.Fatalf("expected dynamic default rule targeting primary, got: %+v", rc.Rules)
	}
	for _, r := range rc.Rules {
		if r.Target == "backup" || r.Target == "secondary" {
			t.Fatalf("unexpected non-active routing target: %+v", r)
		}
	}

	// 2. 写入真实 latency.jsonl（含 thought token 与 retry=1 救回）
	ts1 := now.Format("2006-01-02T15:04:05")
	latLines := "" +
		`{"ts":"` + ts1 + `","profile":"primary","path":"/v1internal:streamGenerateContent","status":200,"head_ms":1000,"stream_ms":2000,"total_ms":3000,"model":"gemini-3.5-pro","p_tok":2000,"cache_tok":1500,"c_tok":300,"t_tok":500,"retry":0}` + "\n" +
		`{"ts":"` + ts1 + `","profile":"primary","path":"/v1internal:streamGenerateContent","status":200,"head_ms":500,"stream_ms":1000,"total_ms":1500,"model":"gemini-3.5-pro","p_tok":1000,"cache_tok":0,"c_tok":200,"t_tok":0,"retry":1}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "latency.jsonl"), []byte(latLines), 0644); err != nil {
		t.Fatal(err)
	}

	dash, err := client.GetDashboardData(7)
	if err != nil {
		t.Fatalf("GetDashboardData(7) failed: %v", err)
	}
	if len(dash.Models) != 1 || dash.Models[0].Model != "gemini-3.5-pro" {
		t.Fatalf("expected 1 real model gemini-3.5-pro, got: %+v", dash.Models)
	}
	if dash.Models[0].ThoughtTokens != 500 || dash.Models[0].CostUSD <= 0 {
		t.Fatalf("expected model ThoughtTokens=500 and positive CostUSD, got: %+v", dash.Models[0])
	}
	if dash.Summary.RetriedRecovered != 1 {
		t.Fatalf("expected RetriedRecovered=1, got %d", dash.Summary.RetriedRecovered)
	}
	if dash.Summary.AvgSpeed <= dash.Summary.E2ESpeed || dash.Summary.AvgSpeed < 250 {
		t.Fatalf("expected decode AvgSpeed (tok/stream_ms, incl. t_tok) > E2ESpeed and >=250, got avg=%.2f e2e=%.2f",
			dash.Summary.AvgSpeed, dash.Summary.E2ESpeed)
	}
	hasSpeedPoint := false
	for _, v := range dash.Summary.Sparklines["speed"] {
		if v >= 250 {
			hasSpeedPoint = true
			break
		}
	}
	if !hasSpeedPoint {
		t.Fatalf("expected real per-day decode speed >=250 in sparklines[speed], got %v", dash.Summary.Sparklines["speed"])
	}
}

func TestDynamicProfilesNoPersonalHardcoding(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	today := now.Format("2006-01-02")

	// 1. 自定义任意账号名 work / personal，绝不能混入任何写死的个人账号名单
	profilesPayload := map[string]any{
		"active_profile": "work",
		"profiles": map[string]any{
			"work": map[string]any{
				"email": "work@example.com",
				"last_quota": map[string]any{
					"gemini_5h":     0.8,
					"gemini_weekly": 0.7,
					"claude_weekly": 0.9,
				},
			},
			"personal": map[string]any{
				"email": "personal@example.com",
				"last_quota": map[string]any{
					"gemini_5h":     1.0,
					"gemini_weekly": 0.9,
					"claude_weekly": 1.0,
				},
			},
		},
	}
	profBytes, _ := json.Marshal(profilesPayload)
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), profBytes, 0644); err != nil {
		t.Fatal(err)
	}
	metricsPayload := map[string]any{
		"summary": map[string]any{"total_requests": 1, "total_input_tokens": 100, "total_output_tokens": 50},
		"by_date": map[string]any{
			today: map[string]any{
				"requests":      1,
				"input_tokens":  100,
				"output_tokens": 50,
				"total_tokens":  150,
				"profiles": map[string]any{
					"work": map[string]any{"requests": 1, "input_tokens": 100, "output_tokens": 50, "total_tokens": 150},
				},
			},
		},
	}
	metBytes, _ := json.Marshal(metricsPayload)
	if err := os.WriteFile(filepath.Join(dir, "metrics.json"), metBytes, 0644); err != nil {
		t.Fatal(err)
	}

	client := NewClient(dir)
	weekly, err := client.GetWeeklyQuotaData()
	if err != nil {
		t.Fatalf("GetWeeklyQuotaData failed: %v", err)
	}
	if len(weekly.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(weekly.Accounts))
	}
	for _, acc := range weekly.Accounts {
		if acc.Color == "" {
			t.Fatalf("expected deterministic non-empty color for %s", acc.Name)
		}
		if acc.Name != "work" && acc.Name != "personal" {
			t.Fatalf("unexpected ghost profile in weekly.Accounts: %s", acc.Name)
		}
	}
	for _, pt := range weekly.DailyTrend {
		for k := range pt.ByProfile {
			if k != "work" && k != "personal" {
				t.Fatalf("unexpected hardcoded ghost profile %q in DailyTrend.ByProfile", k)
			}
		}
	}

	// 2. 空 profiles.json 时 ActiveProfile 应为空字符串而非个人别名
	emptyDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(emptyDir, "profiles.json"), []byte(`{"active_profile":"","profiles":{}}`), 0644)
	_ = os.WriteFile(filepath.Join(emptyDir, "metrics.json"), []byte(`{"summary":{},"by_date":{}}`), 0644)
	emptyClient := NewClient(emptyDir)
	dash, err := emptyClient.GetDashboardData(1)
	if err != nil {
		t.Fatalf("GetDashboardData on empty profiles failed: %v", err)
	}
	if dash.ActiveProfile != "" {
		t.Fatalf("expected empty ActiveProfile when profiles.json is empty, got %q", dash.ActiveProfile)
	}
}
