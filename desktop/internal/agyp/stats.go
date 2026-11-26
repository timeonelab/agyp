package agyp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (c *Client) GetDashboardData(days int) (*DashboardData, error) {
	metrics, err := c.ReadMetrics()
	if err != nil {
		return nil, fmt.Errorf("read metrics failed: %w", err)
	}

	profilesRaw, _ := c.ReadProfiles()
	activeProfile := resolveActiveProfileName(profilesRaw)

	today := time.Now().Format("2006-01-02")
	byDate, _ := metrics["by_date"].(map[string]any)

	cutoffDate := today
	periodTitle := fmt.Sprintf("今日 (%s)", today)

	if days == 1 {
		cutoffDate = today
		periodTitle = fmt.Sprintf("今日 (%s)", today)
	} else if days == -1 {
		// Yesterday
		yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
		cutoffDate = yesterday
		today = yesterday
		periodTitle = fmt.Sprintf("昨天 (%s)", yesterday)
	} else if days > 1 {
		cutoffDate = time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
		periodTitle = fmt.Sprintf("近 %d 天 (%s ~ %s)", days, cutoffDate, today)
	} else {
		cutoffDate = "2020-01-01"
		periodTitle = "全部历史"
	}

	var filteredDates []string
	for d := range byDate {
		if d >= cutoffDate && d <= today {
			filteredDates = append(filteredDates, d)
		}
	}
	sort.Strings(filteredDates)

	var reqTotal, inpTotal, outTotal, thoughtTotal, cacheTotal, cacheEligibleInpTotal int64
	var costUSDTotal, costCNYTotal float64

	for _, d := range filteredDates {
		m, ok := byDate[d].(map[string]any)
		if !ok {
			continue
		}
		reqTotal += int64(getFloat(m, "requests"))
		dayInp := int64(getFloat(m, "input_tokens"))
		inpTotal += dayInp
		outTotal += int64(getFloat(m, "output_tokens"))
		thoughtTotal += int64(getFloat(m, "thought_tokens"))
		if _, hasCache := m["cached_tokens"]; hasCache {
			cacheTotal += int64(getFloat(m, "cached_tokens"))
			cacheEligibleInpTotal += dayInp
		}
		costUSDTotal += getFloat(m, "cost_usd")
		costCNYTotal += getFloat(m, "cost_cny")
	}

	// Historical trend dates: always provide at least 14 days of history so trend charts and sparklines are rich;
	// days == 0 means all-time, whereas days == -1 (Yesterday) still displays a 14-day trend window.
	trendDays := days
	if trendDays != 0 && trendDays < 14 {
		trendDays = 14
	}
	trendCutoff := time.Now().AddDate(0, 0, -(trendDays - 1)).Format("2006-01-02")
	if trendDays == 0 {
		trendCutoff = "2020-01-01"
	}
	nowToday := time.Now().Format("2006-01-02")

	var trendDates []string
	for d := range byDate {
		if d >= trendCutoff && d <= nowToday {
			trendDates = append(trendDates, d)
		}
	}
	sort.Strings(trendDates)

	var dailyTrends []DailyTrendPoint
	for _, d := range trendDates {
		m, ok := byDate[d].(map[string]any)
		if !ok {
			continue
		}
		r := int64(getFloat(m, "requests"))
		inp := int64(getFloat(m, "input_tokens"))
		out := int64(getFloat(m, "output_tokens"))
		usd := getFloat(m, "cost_usd")
		cny := getFloat(m, "cost_cny")

		dailyTrends = append(dailyTrends, DailyTrendPoint{
			Date:         d,
			OutputTokens: out,
			InputTokens:  inp,
			Requests:     int(r),
			CostUSD:      usd,
			CostCNY:      cny,
			IsToday:      d == nowToday,
		})
	}

	// Calculate cache hit rate strictly across days that have cached_tokens telemetry
	cacheHitRate := 0.0
	if cacheEligibleInpTotal > 0 {
		cacheHitRate = float64(cacheTotal) * 100.0 / float64(cacheEligibleInpTotal)
	}

	// Compare with yesterday or previous period for diff %
	yesterdayDate := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	yesterdayOut := int64(0)
	if yMap, ok := byDate[yesterdayDate].(map[string]any); ok {
		yesterdayOut = int64(getFloat(yMap, "output_tokens"))
	}
	outputDiff := "+0%"
	if yesterdayOut > 0 {
		diff := float64(outTotal-yesterdayOut) * 100.0 / float64(yesterdayOut)
		if diff >= 0 {
			outputDiff = fmt.Sprintf("+%.1f%%", diff)
		} else {
			outputDiff = fmt.Sprintf("%.1f%%", diff)
		}
	}

	// Aggregate latency rows for model speeds, cost & detailed latency
	modelMap, p50Head, avgSpeed, e2eSpeed, successRate, badCount, retriedRecovered, errorBreakdown, daySpeedMap := c.aggregateLatencyForPeriod(cutoffDate, today)

	var models []ModelStat
	totalModelGen := int64(0)
	for _, st := range modelMap {
		totalModelGen += st.OutputTokens + st.ThoughtTokens
	}
	if totalModelGen == 0 {
		totalModelGen = outTotal + thoughtTotal
	}

	for _, st := range modelMap {
		ratio := 0.0
		if totalModelGen > 0 {
			ratio = float64(st.OutputTokens+st.ThoughtTokens) * 100.0 / float64(totalModelGen)
		}
		st.Ratio = math.Round(ratio*10) / 10.0
		st.OutputTokensStr = formatTokens(st.OutputTokens)
		if st.CostUSD > 0 {
			st.CostUSDStr = fmt.Sprintf("US$%.2f", st.CostUSD)
		} else {
			st.CostUSDStr = "-"
		}
		if st.AvgSpeed > 0 {
			st.AvgSpeedStr = fmt.Sprintf("%.1f t/s", st.AvgSpeed)
		} else {
			st.AvgSpeedStr = "-"
		}
		if st.E2ESpeed > 0 {
			st.E2ESpeedStr = fmt.Sprintf("%.1f t/s", st.E2ESpeed)
		} else {
			st.E2ESpeedStr = "-"
		}
		st.RequestsStr = fmt.Sprintf("%d 次", st.Requests)
		models = append(models, *st)
	}

	sort.Slice(models, func(i, j int) bool {
		return (models[i].OutputTokens + models[i].ThoughtTokens) > (models[j].OutputTokens + models[j].ThoughtTokens)
	})

	// Profiles Quota
	var profiles []ProfileQuota
	var pausedProfiles []string
	var enabledProfiles []string
	profsMap, _ := profilesRaw["profiles"].(map[string]any)
	for name, val := range profsMap {
		pMap, ok := val.(map[string]any)
		if !ok {
			continue
		}
		email, _ := pMap["email"].(string)
		paused, _ := pMap["paused"].(bool)
		if paused {
			pausedProfiles = append(pausedProfiles, name)
		} else {
			enabledProfiles = append(enabledProfiles, name)
		}
		tierLabel, tierName, tierID := extractProfileTier(pMap)
		q, _ := pMap["last_quota"].(map[string]any)

		g5h := getFloat(q, "gemini_5h")
		gWeek := getFloat(q, "gemini_weekly")
		cWeek := getFloat(q, "claude_weekly")
		if _, hasCWeek := q["claude_weekly"]; !hasCWeek {
			cWeek = 1.0
		}
		r5h, _ := q["reset_5h"].(string)
		rWeek, _ := q["reset_weekly"].(string)

		todayTok := int64(0)
		if todayData, ok := byDate[today].(map[string]any); ok {
			if byProf, ok := todayData["profiles"].(map[string]any); ok {
				if pData, ok := byProf[name].(map[string]any); ok {
					todayTok = int64(getFloat(pData, "total_tokens"))
				}
			}
		}

		profiles = append(profiles, ProfileQuota{
			Name:            name,
			Email:           email,
			Gemini5h:        math.Round(g5h * 100.0),
			GeminiWeekly:    math.Round(gWeek * 100.0),
			ClaudeWeekly:    math.Round(cWeek * 100.0),
			Reset5h:         formatReset5hTime(r5h),
			ResetWeekly:     formatResetDateTime(rWeek),
			Reset5hRaw:      r5h,
			ResetWeeklyRaw:  rWeek,
			Reset5hFull:     formatFullDateTime(r5h),
			ResetWeeklyFull: formatFullDateTime(rWeek),
			TodayTokens:     todayTok,
			TodayTokensStr:  formatTokens(todayTok),
			IsActive:        name == activeProfile,
			Paused:          paused,
			Tier:            tierLabel,
			TierName:        tierName,
			TierID:          tierID,
		})
	}

	sort.Strings(pausedProfiles)
	sort.Strings(enabledProfiles)
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].IsActive != profiles[j].IsActive {
			return profiles[i].IsActive
		}
		if profiles[i].Paused != profiles[j].Paused {
			return !profiles[i].Paused
		}
		return profiles[i].Name < profiles[j].Name
	})

	// Generate Sparklines points for each card (7 sample intervals)
	sparklines := make(map[string][]float64)
	sparklines["output"] = generateSparkline(dailyTrends, func(p DailyTrendPoint) float64 { return float64(p.OutputTokens) })
	sparklines["input"] = generateSparkline(dailyTrends, func(p DailyTrendPoint) float64 { return float64(p.InputTokens) })
	sparklines["requests"] = generateSparkline(dailyTrends, func(p DailyTrendPoint) float64 { return float64(p.Requests) })
	sparklines["speed"] = generateSparkline(dailyTrends, func(p DailyTrendPoint) float64 { return math.Round(daySpeedMap[p.Date]*10) / 10.0 })
	sparklines["cost"] = generateSparkline(dailyTrends, func(p DailyTrendPoint) float64 { return p.CostUSD })

	avgSpeedStr := "-"
	if avgSpeed > 0 {
		avgSpeedStr = fmt.Sprintf("%.1f t/s", avgSpeed)
	}
	e2eSpeedStr := "-"
	if e2eSpeed > 0 {
		e2eSpeedStr = fmt.Sprintf("%.1f t/s", e2eSpeed)
	}

	summary := SummaryData{
		OutputTokens:       outTotal,
		OutputTokensStr:    formatTokens(outTotal),
		ThoughtTokens:      thoughtTotal,
		ThoughtTokensStr:   formatTokens(thoughtTotal),
		OutputDiff:         outputDiff,
		InputTokens:        inpTotal,
		InputTokensStr:     formatTokens(inpTotal),
		CachedTokens:       cacheTotal,
		CachedTokensStr:    formatTokens(cacheTotal),
		CacheEligibleInput: cacheEligibleInpTotal,
		CacheHitRate:       math.Round(cacheHitRate*10) / 10.0,
		Requests:           int(reqTotal),
		RequestsStr:        fmt.Sprintf("%d", reqTotal),
		SuccessRate:        math.Round(successRate*10) / 10.0,
		FailCount:          badCount,
		RetriedRecovered:   retriedRecovered,
		AvgSpeed:           math.Round(avgSpeed*10) / 10.0,
		AvgSpeedStr:        avgSpeedStr,
		E2ESpeed:           math.Round(e2eSpeed*10) / 10.0,
		E2ESpeedStr:        e2eSpeedStr,
		P50LatencyMs:       p50Head,
		CostUSD:            math.Round(costUSDTotal*100) / 100.0,
		CostCNY:            math.Round(costCNYTotal*100) / 100.0,
		Sparklines:         sparklines,
	}

	return &DashboardData{
		Period:          periodTitle,
		Summary:         summary,
		Models:          models,
		Profiles:        profiles,
		DailyTrends:     dailyTrends,
		Errors:          errorBreakdown,
		ActiveProfile:   activeProfile,
		PausedProfiles:  pausedProfiles,
		EnabledProfiles: enabledProfiles,
	}, nil
}

func extractProfileTier(pMap map[string]any) (label, name, id string) {
	tMap, ok := pMap["tier"].(map[string]any)
	if !ok {
		return "", "", ""
	}
	label, _ = tMap["label"].(string)
	name, _ = tMap["name"].(string)
	id, _ = tMap["id"].(string)
	if label == "" {
		lowID := strings.ToLower(id)
		lowName := strings.ToLower(name)
		switch {
		case strings.Contains(lowID, "ultra") || strings.Contains(lowName, "ultra"):
			label = "Ultra"
		case strings.Contains(lowID, "pro") || strings.Contains(lowName, "pro"):
			label = "Pro"
		case strings.Contains(lowID, "plus") || strings.Contains(lowName, "plus"):
			label = "Plus"
		case strings.Contains(lowID, "free") || strings.Contains(lowName, "starter"):
			label = "Free"
		case name != "":
			label = name
		default:
			label = id
		}
	}
	return label, name, id
}

func estimateModelCostUSD(model string, pTok, cTok, cacheTok, tTok int64) float64 {
	m := strings.ToLower(model)
	inP, cacheP, outP := 0.30, 0.03, 2.50
	switch {
	case strings.Contains(m, "opus"):
		inP, cacheP, outP = 15.0, 1.5, 75.0
	case strings.Contains(m, "sonnet"):
		inP, cacheP, outP = 3.0, 0.3, 15.0
	case strings.Contains(m, "haiku"):
		inP, cacheP, outP = 0.8, 0.08, 4.0
	case strings.Contains(m, "flash-lite") || strings.Contains(m, "lite"):
		inP, cacheP, outP = 0.10, 0.01, 0.40
	case strings.Contains(m, "gemini-4") || strings.Contains(m, "hydrogen") || strings.Contains(m, "neon") || strings.Contains(m, "pro"):
		inP, cacheP, outP = 1.25, 0.125, 10.0
	}
	cachedIn := cacheTok
	if cachedIn < 0 {
		cachedIn = 0
	}
	if cachedIn > pTok {
		cachedIn = pTok
	}
	uncachedIn := pTok - cachedIn
	if uncachedIn < 0 {
		uncachedIn = 0
	}
	billedOut := cTok + tTok
	if billedOut < 0 {
		billedOut = 0
	}
	return (float64(uncachedIn)*inP + float64(cachedIn)*cacheP + float64(billedOut)*outP) / 1_000_000.0
}

func (c *Client) aggregateLatencyForPeriod(cutoff, end string) (map[string]*ModelStat, float64, float64, float64, float64, int, int, []ErrorBreakdownItem, map[string]float64) {
	modelMap := make(map[string]*ModelStat)
	modelDecodeToks := make(map[string]int64)
	modelStreamMs := make(map[string]float64)
	modelE2EToks := make(map[string]int64)
	modelTotalMs := make(map[string]float64)
	dayDecodeToks := make(map[string]int64)
	dayStreamMs := make(map[string]float64)
	var heads []float64
	okCount := 0
	badCount := 0
	retriedRecovered := 0

	errCounts := make(map[int]int)
	errSamples := make(map[int]string)
	errProfiles := make(map[int]string)
	errModels := make(map[int]map[string]int)
	modelErrCounts := make(map[string]map[int]int)
	totalErrRows := 0

	var sumDecodeToks, sumE2EToks int64
	var sumStreamMs, sumTotalMs float64

	c.scanLatencyForPeriod(cutoff, end, func(r *latencyRow) {
		st := r.Status
		errStr := ""
		if r.Err != nil {
			errStr = fmt.Sprintf("%v", r.Err)
		}

		isErr := (st >= 400 || errStr != "")
		if !isErr && st == 200 {
			okCount++
			if r.Retry > 0 {
				retriedRecovered++
			}
		} else if isErr {
			badCount++
			totalErrRows++
			code := st
			if code == 0 {
				code = 502
			}
			errCounts[code]++
			if errStr != "" {
				errSamples[code] = errStr
			}
			if r.Profile != "" {
				if existing, has := errProfiles[code]; has && !strings.Contains(existing, r.Profile) {
					errProfiles[code] = existing + ", " + r.Profile
				} else if !has {
					errProfiles[code] = r.Profile
				}
			}

			m := r.Model
			if m == "" && strings.Contains(r.Path, "streamGenerateContent") {
				m = "streamGenerateContent"
			}
			if m != "" {
				if errModels[code] == nil {
					errModels[code] = make(map[string]int)
				}
				errModels[code][m]++
				if modelErrCounts[m] == nil {
					modelErrCounts[m] = make(map[int]int)
				}
				modelErrCounts[m][code]++
			}
		}

		if r.HeadMs > 0 {
			heads = append(heads, r.HeadMs)
		}

		model := r.Model
		if model == "" {
			return
		}

		stat, exists := modelMap[model]
		if !exists {
			stat = &ModelStat{Model: model}
			modelMap[model] = stat
		}

		genTok := r.CTok + r.TTok
		stat.OutputTokens += r.CTok
		stat.ThoughtTokens += r.TTok
		stat.CostUSD += estimateModelCostUSD(model, r.PTok, r.CTok, r.CacheTok, r.TTok)
		stat.Requests++

		if isErr {
			stat.FailCount++
			if errStr != "" && stat.ErrorReason == "" {
				stat.ErrorReason = errStr
			}
		} else {
			stat.SuccessCount++
		}

		if genTok > 0 && r.StreamMs >= 50 {
			modelDecodeToks[model] += genTok
			modelStreamMs[model] += r.StreamMs
			sumDecodeToks += genTok
			sumStreamMs += r.StreamMs
			if len(r.TS) >= 10 {
				d := r.TS[:10]
				dayDecodeToks[d] += genTok
				dayStreamMs[d] += r.StreamMs
			}
		}
		if genTok > 0 && r.TotalMs > 0 {
			modelE2EToks[model] += genTok
			modelTotalMs[model] += r.TotalMs
			sumE2EToks += genTok
			sumTotalMs += r.TotalMs
		}
	})

	for m, stat := range modelMap {
		if modelTotalMs[m] > 0 && modelE2EToks[m] > 0 {
			stat.E2ESpeed = float64(modelE2EToks[m]) / (modelTotalMs[m] / 1000.0)
		}
		if modelStreamMs[m] > 0 && modelDecodeToks[m] > 0 {
			stat.AvgSpeed = float64(modelDecodeToks[m]) / (modelStreamMs[m] / 1000.0)
		} else if stat.E2ESpeed > 0 {
			stat.AvgSpeed = stat.E2ESpeed
		}
		if stat.Requests > 0 {
			stat.SuccessRate = math.Round(float64(stat.SuccessCount)*1000.0/float64(stat.Requests)) / 10.0
		} else {
			stat.SuccessRate = 100.0
		}
		if ecMap, ok := modelErrCounts[m]; ok {
			for code, cnt := range ecMap {
				stat.ErrorBreakdown = append(stat.ErrorBreakdown, ModelErrorCodeStat{
					Code:  code,
					Count: cnt,
				})
			}
			sort.Slice(stat.ErrorBreakdown, func(i, j int) bool {
				return stat.ErrorBreakdown[i].Count > stat.ErrorBreakdown[j].Count
			})
		}
	}

	p50Head := 0.0
	if len(heads) > 0 {
		sort.Float64s(heads)
		p50Head = heads[len(heads)/2]
	}

	avgDecodeSpeed := 0.0
	if sumStreamMs > 0 && sumDecodeToks > 0 {
		avgDecodeSpeed = float64(sumDecodeToks) / (sumStreamMs / 1000.0)
	}
	e2eSpeed := 0.0
	if sumTotalMs > 0 && sumE2EToks > 0 {
		e2eSpeed = float64(sumE2EToks) / (sumTotalMs / 1000.0)
	}
	if avgDecodeSpeed == 0 && e2eSpeed > 0 {
		avgDecodeSpeed = e2eSpeed
	}

	totalReq := okCount + badCount
	successRate := 0.0
	if totalReq > 0 {
		successRate = float64(okCount) * 100.0 / float64(totalReq)
	}

	var errorItems []ErrorBreakdownItem
	for code, cnt := range errCounts {
		var typeLabel, reason string
		switch code {
		case 503:
			typeLabel = "503 上游模型容量不足"
			reason = "MODEL_CAPACITY_EXHAUSTED"
		case 429:
			typeLabel = "429 访问配额耗尽"
			reason = "RESOURCE_EXHAUSTED"
		case 502:
			typeLabel = "502 网关与上游断开"
			reason = "UPSTREAM_TIMEOUT / EOF"
		case 403:
			typeLabel = "403 鉴权与权限拒绝"
			reason = "PERMISSION_DENIED"
		case 400:
			typeLabel = "400 请求参数异常"
			reason = "INVALID_ARGUMENT"
		default:
			typeLabel = fmt.Sprintf("%d 异常响应", code)
			reason = fmt.Sprintf("HTTP_%d", code)
		}
		pct := 0.0
		if totalErrRows > 0 {
			pct = math.Round(float64(cnt)*1000.0/float64(totalErrRows)) / 10.0
		}
		sample := errSamples[code]
		if sample == "" {
			sample = reason
		}

		var modelsList []string
		var modelSummaryParts []string
		if mCountMap, ok := errModels[code]; ok {
			for modName, cCnt := range mCountMap {
				modelsList = append(modelsList, modName)
				modelSummaryParts = append(modelSummaryParts, fmt.Sprintf("%s (%d次)", modName, cCnt))
			}
			sort.Strings(modelsList)
			sort.Strings(modelSummaryParts)
		}

		errorItems = append(errorItems, ErrorBreakdownItem{
			Code:           code,
			Type:           typeLabel,
			Reason:         reason,
			Count:          cnt,
			Percentage:     pct,
			SampleErr:      sample,
			Profile:        errProfiles[code],
			AffectedModels: modelsList,
			ModelDetails:   strings.Join(modelSummaryParts, "、"),
		})
	}
	sort.Slice(errorItems, func(i, j int) bool {
		return errorItems[i].Count > errorItems[j].Count
	})

	daySpeedMap := make(map[string]float64, len(dayStreamMs))
	for d, ms := range dayStreamMs {
		if ms > 0 {
			daySpeedMap[d] = float64(dayDecodeToks[d]) / (ms / 1000.0)
		}
	}

	return modelMap, p50Head, avgDecodeSpeed, e2eSpeed, successRate, badCount, retriedRecovered, errorItems, daySpeedMap
}

func generateSparkline(trends []DailyTrendPoint, extract func(DailyTrendPoint) float64) []float64 {
	if len(trends) == 0 {
		return []float64{}
	}
	if len(trends) <= 7 {
		var res []float64
		for _, t := range trends {
			res = append(res, extract(t))
		}
		return res
	}
	// Sample down to 7 points
	step := float64(len(trends)-1) / 6.0
	var res []float64
	for i := 0; i < 7; i++ {
		idx := int(math.Round(float64(i) * step))
		if idx >= len(trends) {
			idx = len(trends) - 1
		}
		res = append(res, extract(trends[idx]))
	}
	return res
}

func formatTokens(tok int64) string {
	if tok >= 1_000_000_000 {
		return fmt.Sprintf("%.2fB", float64(tok)/1_000_000_000.0)
	}
	if tok >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(tok)/1_000_000.0)
	}
	if tok >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(tok)/1_000.0)
	}
	return fmt.Sprintf("%d", tok)
}

func formatResetDateTime(iso string) string {
	if iso == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "-"
	}
	if time.Until(t) <= 0 {
		return "已满额"
	}
	return t.Local().Format("01-02 15:04")
}

func formatReset5hTime(iso string) string {
	if iso == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "-"
	}
	if time.Until(t) <= 0 {
		return "已满额"
	}
	return t.Local().Format("15:04")
}

func formatFullDateTime(iso string) string {
	if iso == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func formatCountdown(iso string) string {
	if iso == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "-"
	}
	diff := time.Until(t)
	if diff <= 0 {
		return "已满额"
	}
	days := int(diff.Hours()) / 24
	hours := int(diff.Hours()) % 24
	mins := int(diff.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d天%d时", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%d时%d分", hours, mins)
	}
	return fmt.Sprintf("%d分", mins)
}

func (c *Client) ExportCSV(days int) (string, error) {
	data, err := c.GetDashboardData(days)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("日期,输入Token,输出Token,请求次数,费用(USD)\n")
	for _, p := range data.DailyTrends {
		sb.WriteString(fmt.Sprintf("%s,%d,%d,%d,%.4f\n", p.Date, p.InputTokens, p.OutputTokens, p.Requests, p.CostUSD))
	}
	return sb.String(), nil
}

func (c *Client) GetWasteScore(days int) (*WasteScoreData, error) {
	data, err := c.GetDashboardData(days)
	if err != nil {
		return nil, err
	}
	s := data.Summary

	grade := "B"
	if s.CacheHitRate >= 95.0 {
		grade = "A+"
	} else if s.CacheHitRate >= 80.0 {
		grade = "A"
	} else if s.CacheHitRate >= 60.0 {
		grade = "B"
	} else {
		grade = "C"
	}

	prefillSaved := float64(s.CachedTokens) / 1000.0 * 0.002 // estimate ~2ms per 1k tok
	if prefillSaved > 7200 {
		prefillSaved = 7200
	}

	// Conservative cache-read input savings vs uncached input (~$0.27/1M on Flash, higher on Pro)
	costSavedUSD := (float64(s.CachedTokens) / 1_000_000.0) * 0.27
	costSavedCNY := costSavedUSD * 7.20

	var recs []string
	if s.CacheHitRate >= 80.0 {
		recs = append(recs, fmt.Sprintf("当前输入缓存命中率达 %.1f%%（累计命中 %s Tokens），Prefill 耗时已显著压缩", s.CacheHitRate, formatTokens(s.CachedTokens)))
	} else if s.CacheHitRate >= 50.0 {
		recs = append(recs, fmt.Sprintf("当前输入缓存命中率为 %.1f%%，建议减少会话中途频繁切号以保留 KV 缓存", s.CacheHitRate))
	} else {
		recs = append(recs, fmt.Sprintf("当前输入缓存命中率为 %.1f%%，可固定系统提示词前缀并开启会话粘性提升缓存复用", s.CacheHitRate))
	}
	recs = append(recs,
		"保持系统提示词（System Prompt）和静态规则文件前缀稳定，有助于维持高命中率",
		"若只需要锁定单一主力账号，可开启单账号模式避免容灾切号打断长上下文缓存",
	)

	return &WasteScoreData{
		ScoreGrade:        grade,
		HitRate:           s.CacheHitRate,
		CachedTokens:      s.CachedTokens,
		InputTokens:       s.InputTokens,
		CostSavedUSD:      math.Round(costSavedUSD*100) / 100.0,
		CostSavedCNY:      math.Round(costSavedCNY*100) / 100.0,
		PrefillTimeSavedS: prefillSaved,
		Recommendations:   recs,
	}, nil
}

func (c *Client) GetRoutingConfig() (*RoutingConfig, error) {
	profilesRaw, _ := c.ReadProfiles()
	activeProfile := ""
	if ap, ok := profilesRaw["active_profile"].(string); ok && ap != "" {
		activeProfile = ap
	}

	profsMap, _ := profilesRaw["profiles"].(map[string]any)
	var chain []string
	var pausedProfs []string

	var names []string
	for name := range profsMap {
		names = append(names, name)
	}
	sort.Strings(names)

	if activeProfile != "" {
		if pMap, ok := profsMap[activeProfile].(map[string]any); ok {
			if paused, _ := pMap["paused"].(bool); !paused {
				chain = append(chain, activeProfile)
			}
		}
	}
	for _, name := range names {
		pMap, ok := profsMap[name].(map[string]any)
		if !ok {
			continue
		}
		if paused, _ := pMap["paused"].(bool); paused {
			pausedProfs = append(pausedProfs, name)
		} else if name != activeProfile {
			chain = append(chain, name)
		}
	}

	failoverMode := "会话粘性 + 无感自动热切"
	if len(chain) == 1 && len(pausedProfs) > 0 {
		failoverMode = fmt.Sprintf("单账号锁定模式 (仅 %s 启用，已暂停容灾切号)", chain[0])
	} else if len(pausedProfs) > 0 {
		failoverMode = fmt.Sprintf("会话粘性 + 自动容灾 (已暂停: %s)", strings.Join(pausedProfs, ", "))
	}

	activeTarget := activeProfile
	if activeTarget == "" {
		activeTarget = "自动选优"
	}

	failoverTarget := "按健康评分顺延切号"
	if len(chain) <= 1 {
		failoverTarget = "透传原始响应 (单账号不切号)"
	}

	rules := []RoutingRule{
		{
			Pattern:     "默认未绑定请求",
			Target:      activeTarget,
			Description: "走全局 active_profile 注入 OAuth Token（长连接池复用）",
		},
		{
			Pattern:     "同会话后续请求 (requestId)",
			Target:      "会话粘性绑定",
			Description: "优先沿用该会话绑定账号，保住上游 Prompt Cache；账号暂停或冷却时自动解绑",
		},
		{
			Pattern:     "HTTP 429 / 403 / 503",
			Target:      failoverTarget,
			Description: "多账号启用时自动跳过已暂停账号并切至候补；单账号锁定时不串号",
		},
		{
			Pattern:     "已暂停账号 (paused=true)",
			Target:      "跳过调度与刷新",
			Description: "不参与评分排序、容灾候选及后台配额轮询",
		},
	}

	return &RoutingConfig{
		ActiveProfile:  activeProfile,
		PriorityChain:  chain,
		PausedProfiles: pausedProfs,
		Rules:          rules,
		FailoverMode:   failoverMode,
	}, nil
}

func (c *Client) RunCheck(profile string, testType string) (*CheckResult, error) {
	if profile == "" {
		profilesRaw, _ := c.ReadProfiles()
		if ap, ok := profilesRaw["active_profile"].(string); ok && ap != "" {
			profile = ap
		}
	}
	if testType == "" {
		testType = "milk"
	}

	args := []string{"check"}
	if profile != "" {
		args = append(args, profile)
	}
	args = append(args, "-t", testType, "--json")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, c.getAgypBin(), args...)
	outBytes, err := cmd.CombinedOutput()
	elapsedMs := time.Since(start).Milliseconds()

	cleanOut := stripANSI(strings.TrimSpace(string(outBytes)))
	var parsed []struct {
		Profile   string  `json:"profile"`
		ProbeID   string  `json:"probe_id"`
		Verdict   string  `json:"verdict"`
		ElapsedS  float64 `json:"elapsed_s"`
		Error     string  `json:"error"`
		Summary   string  `json:"summary"`
		Note      string  `json:"note"`
		SavedFile string  `json:"saved_file"`
	}
	if jsonErr := json.Unmarshal(outBytes, &parsed); jsonErr == nil && len(parsed) > 0 {
		r := parsed[0]
		latMs := elapsedMs
		if r.ElapsedS > 0 {
			latMs = int64(r.ElapsedS * 1000)
		}
		detail := r.Note
		if r.Error != "" {
			detail = r.Error
		}
		summary := r.Summary
		if summary == "" {
			summary = r.Verdict
		}
		return &CheckResult{
			Profile:   profile,
			TestType:  testType,
			Pass:      strings.EqualFold(r.Verdict, "PASSED") && err == nil,
			LatencyMs: latMs,
			Summary:   summary,
			Detail:    detail,
			HTMLFile:  filepath.Base(r.SavedFile),
		}, nil
	}

	if err != nil {
		return &CheckResult{
			Profile:   profile,
			TestType:  testType,
			Pass:      false,
			LatencyMs: elapsedMs,
			Summary:   fmt.Sprintf("检测执行失败: %v", err),
			Detail:    cleanOut,
		}, nil
	}

	return &CheckResult{
		Profile:   profile,
		TestType:  testType,
		Pass:      true,
		LatencyMs: elapsedMs,
		Summary:   "检测完成",
		Detail:    cleanOut,
	}, nil
}

func (c *Client) SendNotification(title, message string) error {
	script := fmt.Sprintf(`display notification "%s" with title "%s" sound name "default"`,
		strings.ReplaceAll(message, `"`, `\"`),
		strings.ReplaceAll(title, `"`, `\"`))
	return exec.Command("osascript", "-e", script).Run()
}

func (c *Client) GetRelayTimeline() (*RelayTimelineData, error) {
	profilesRaw, err := c.ReadProfiles()
	if err != nil {
		return nil, err
	}

	activeProfile := resolveActiveProfileName(profilesRaw)

	profsMap, _ := profilesRaw["profiles"].(map[string]any)

	type profScore struct {
		name      string
		score     float64
		g5h       float64
		gWeekly   float64
		reset5h   time.Time
		resetWeek time.Time
	}

	var scores []profScore
	var events []RelayEvent

	now := time.Now()

	for name, val := range profsMap {
		pMap, ok := val.(map[string]any)
		if !ok {
			continue
		}
		paused, _ := pMap["paused"].(bool)
		q, _ := pMap["last_quota"].(map[string]any)
		g5h := getFloat(q, "gemini_5h")
		gWeek := getFloat(q, "gemini_weekly")
		r5hStr, _ := q["reset_5h"].(string)
		rWeekStr, _ := q["reset_weekly"].(string)

		t5h, _ := time.Parse(time.RFC3339, r5hStr)
		tWeek, _ := time.Parse(time.RFC3339, rWeekStr)

		// 5h event
		if !t5h.IsZero() {
			rem := int64(t5h.Sub(now).Seconds())
			urgency := "normal"
			if rem <= 3600 {
				urgency = "soon"
			} else if rem <= 86400 {
				urgency = "today"
			} else {
				urgency = "later"
			}

			events = append(events, RelayEvent{
				Profile:          name,
				Type:             "5h",
				TypeLabel:        "5小时滚动回满",
				TargetTime:       t5h.Local().Format("2006-01-02 15:04:05"),
				FormattedTime:    formatRelativeTime(t5h, now),
				RemainingSeconds: rem,
				CurrentCapacity:  math.Round(g5h * 100),
				RecoverCapacity:  100,
				IsActive:         name == activeProfile,
				Paused:           paused,
				Urgency:          urgency,
			})
		}

		// weekly event
		if !tWeek.IsZero() {
			rem := int64(tWeek.Sub(now).Seconds())
			events = append(events, RelayEvent{
				Profile:          name,
				Type:             "weekly",
				TypeLabel:        "每周全额重置",
				TargetTime:       tWeek.Local().Format("2006-01-02 15:04:05"),
				FormattedTime:    formatRelativeTime(tWeek, now),
				RemainingSeconds: rem,
				CurrentCapacity:  math.Round(gWeek * 100),
				RecoverCapacity:  100,
				IsActive:         name == activeProfile,
				Paused:           paused,
				Urgency:          "later",
			})
		}

		if paused {
			continue
		}

		// Compute relay fitness score for enabled accounts only
		score := g5h * 100.0
		if gWeek <= 0.05 {
			score -= 50.0 // heavily penalize depleted weekly quota
		}
		if name == activeProfile {
			score += 1000.0 // Active profile always holds the baton at Step 1
		}
		scores = append(scores, profScore{
			name:      name,
			score:     score,
			g5h:       g5h,
			gWeekly:   gWeek,
			reset5h:   t5h,
			resetWeek: tWeek,
		})
	}

	// Sort events chronologically
	sort.Slice(events, func(i, j int) bool {
		return events[i].RemainingSeconds < events[j].RemainingSeconds
	})

	// Sort relay priority
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	var relayOrder []string
	total5h := 0.0
	for _, sc := range scores {
		relayOrder = append(relayOrder, sc.name)
		total5h += sc.g5h
	}

	healthScore := 95.0
	if len(scores) > 0 {
		healthScore = math.Round((total5h / float64(len(scores))) * 1000) / 10.0
	}

	rec := "当前推荐优先由主力承载；其余候补账号陆续滚动回满。"
	if len(relayOrder) >= 3 {
		rec = fmt.Sprintf("当前建议接力链: [%s (余量充裕)] ➔ [%s (就绪)] ➔ [%s (备用)]；周重置前全账号周额度集体回满。",
			relayOrder[0], relayOrder[1], relayOrder[2])
	} else if len(relayOrder) > 0 {
		rec = fmt.Sprintf("当前推荐优先由主力 [%s] 承载；在 4 小时内其余候补账号将陆续滚动回满，形成无断流错峰闭环。", relayOrder[0])
	}

	weeklyData, _ := c.GetWeeklyQuotaData()
	topSessions, _ := c.GetDailyTopSessions(7)

	return &RelayTimelineData{
		SummaryRecommendation: rec,
		RelayOrder:            relayOrder,
		TimelineEvents:        events,
		HealthScore:           healthScore,
		SafeWorkingHours:      "连续 72+ 小时错峰无断流保障",
		WeeklyQuota:           weeklyData,
		DailyTopSessions:      topSessions,
	}, nil
}

func formatRelativeTime(target, now time.Time) string {
	diff := target.Sub(now)
	targetLocal := target.Local()
	nowLocal := now.Local()
	targetDate := time.Date(targetLocal.Year(), targetLocal.Month(), targetLocal.Day(), 0, 0, 0, 0, targetLocal.Location())
	nowDate := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, nowLocal.Location())
	daysDiff := int(targetDate.Sub(nowDate).Hours() / 24)

	dayName := "今天"
	if daysDiff == 1 {
		dayName = "明天"
	} else if daysDiff > 1 {
		weekdays := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
		dayName = weekdays[targetLocal.Weekday()]
	}

	timeStr := targetLocal.Format("15:04")
	if diff <= 0 {
		return fmt.Sprintf("%s %s (已回满)", dayName, timeStr)
	}

	hours := int(diff.Hours())
	mins := int(diff.Minutes()) % 60
	days := hours / 24
	hoursMod := hours % 24

	if days > 0 {
		return fmt.Sprintf("%s %s (%d天%d时后)", dayName, timeStr, days, hoursMod)
	}
	if hours > 0 {
		return fmt.Sprintf("%s %s (%d时%02d分后)", dayName, timeStr, hours, mins)
	}
	return fmt.Sprintf("%s %s (%d分钟后)", dayName, timeStr, mins)
}


