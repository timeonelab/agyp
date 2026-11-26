package agyp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

var profileColorPalette = []string{
	"#10b981", // Emerald Green
	"#a855f7", // Purple
	"#3b82f6", // Blue
	"#f59e0b", // Amber
	"#06b6d4", // Cyan
	"#ec4899", // Pink
	"#6366f1", // Indigo
	"#14b8a6", // Teal
}

func profileColorForName(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return profileColorPalette[int(h.Sum32())%len(profileColorPalette)]
}

// calculateCycleStart determines the 00:00:00 local time start of the 7-day weekly cycle given a resetTime.
// If resetTime is zero/invalid, it falls back to Saturday-aligned cycle.
func calculateCycleStart(resetTime, now time.Time) time.Time {
	loc := now.Location()
	if loc == nil {
		loc = time.Local
	}
	nowLocal := now.In(loc)
	todayMidnight := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
	if resetTime.IsZero() {
		daysSinceSat := (int(nowLocal.Weekday()) + 1) % 7
		return todayMidnight.AddDate(0, 0, -daysSinceSat)
	}

	t := resetTime.In(loc)

	// If resetTime is in the past (quota already passed reset timestamp but cache hasn't refreshed),
	// roll forward by 7 days until it represents the upcoming reset time.
	for t.Before(nowLocal) {
		t = t.AddDate(0, 0, 7)
	}

	// If resetTime is way too far in the future (> 7 days + 12 hours), roll backward
	for t.Sub(nowLocal) > 7*24*time.Hour+12*time.Hour {
		t = t.AddDate(0, 0, -7)
	}

	// Current cycle started 7 days before the upcoming reset time
	cycleStartLocal := t.AddDate(0, 0, -7)
	cycleStartMidnight := time.Date(cycleStartLocal.Year(), cycleStartLocal.Month(), cycleStartLocal.Day(), 0, 0, 0, 0, nowLocal.Location())

	// Clamp: cycle start date cannot be in the future relative to today
	if cycleStartMidnight.After(todayMidnight) {
		cycleStartMidnight = todayMidnight
	}

	// Clamp: cycle start cannot be more than 6 days in the past
	if todayMidnight.Sub(cycleStartMidnight).Hours() > 6*24 {
		cycleStartMidnight = todayMidnight.AddDate(0, 0, -6)
	}

	return cycleStartMidnight
}

// getCycleOverride reads manual cycle start override from cycle_override.json if valid and not expired.
func (c *Client) getCycleOverride(now time.Time) (time.Time, bool) {
	p := filepath.Join(c.baseDir, "cycle_override.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}, false
	}
	var o struct {
		CycleStart string `json:"cycle_start"`
	}
	if err := json.Unmarshal(data, &o); err != nil || o.CycleStart == "" {
		return time.Time{}, false
	}
	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	t, err := time.ParseInLocation("2006-01-02", o.CycleStart, now.Location())
	if err != nil {
		return time.Time{}, false
	}
	if t.After(todayMidnight) {
		return todayMidnight, true
	}
	if todayMidnight.Sub(t).Hours() > 6*24 {
		return time.Time{}, false
	}
	return t, true
}

// ResetWeeklyCycle forces the current weekly quota cycle start to today.
func (c *Client) ResetWeeklyCycle() error {
	now := time.Now()
	todayStr := now.Format("2006-01-02")
	override := map[string]string{
		"cycle_start": todayStr,
		"reset_at":    now.Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(override, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(c.baseDir, "cycle_override.json")
	if err := os.WriteFile(p, data, 0644); err != nil {
		return err
	}

	// Trigger background agyp quota refresh
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, c.getAgypBin(), "quota").Run()
	}()

	return nil
}

// GetWeeklyQuotaData calculates the 5 accounts' weekly quota burn and multi-day trend.
func (c *Client) GetWeeklyQuotaData() (*WeeklyQuotaRelayData, error) {
	profilesRaw, err := c.ReadProfiles()
	if err != nil {
		return nil, err
	}

	metrics, _ := c.ReadMetrics()
	byDate, _ := metrics["by_date"].(map[string]any)

	activeProfile := resolveActiveProfileName(profilesRaw)

	profsMap, _ := profilesRaw["profiles"].(map[string]any)

	// Collect dynamic profile names from profiles.json (active profile first, then sorted)
	var otherNames []string
	hasActive := false
	for k := range profsMap {
		if k == activeProfile {
			hasActive = true
		} else {
			otherNames = append(otherNames, k)
		}
	}
	sort.Strings(otherNames)
	profileNames := make([]string, 0, len(profsMap))
	if hasActive && activeProfile != "" {
		profileNames = append(profileNames, activeProfile)
	}
	profileNames = append(profileNames, otherNames...)

	now := time.Now()
	nowDate := now.Format("2006-01-02")
	yesterdayDate := now.AddDate(0, 0, -1).Format("2006-01-02")
	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// Find reset time for fleet (prefer active profile, then first available)
	var fleetResetTime time.Time
	var resetCountdown string

	if apMap, ok := profsMap[activeProfile].(map[string]any); ok {
		if q, ok := apMap["last_quota"].(map[string]any); ok {
			if rWeekStr, ok := q["reset_weekly"].(string); ok && rWeekStr != "" {
				if t, err := time.Parse(time.RFC3339, rWeekStr); err == nil {
					fleetResetTime = t
					resetCountdown = formatRelativeTime(t, now)
				}
			}
		}
	}

	if fleetResetTime.IsZero() {
		for _, name := range profileNames {
			if pMap, ok := profsMap[name].(map[string]any); ok {
				if q, ok := pMap["last_quota"].(map[string]any); ok {
					if rWeekStr, ok := q["reset_weekly"].(string); ok && rWeekStr != "" {
						if t, err := time.Parse(time.RFC3339, rWeekStr); err == nil {
							fleetResetTime = t
							resetCountdown = formatRelativeTime(t, now)
							break
						}
					}
				}
			}
		}
	}

	if resetCountdown == "" {
		resetCountdown = "-"
	}

	var currCycleStart time.Time
	if overrideStart, ok := c.getCycleOverride(now); ok {
		currCycleStart = overrideStart
	} else {
		currCycleStart = calculateCycleStart(fleetResetTime, now)
	}
	prevCycleStart := currCycleStart.AddDate(0, 0, -7)

	currCycleLabel := fmt.Sprintf("本周周期 (%s ~ %s)", currCycleStart.Format("01-02"), currCycleStart.AddDate(0, 0, 6).Format("01-02"))
	prevCycleLabel := fmt.Sprintf("上周周期 (%s ~ %s)", prevCycleStart.Format("01-02"), prevCycleStart.AddDate(0, 0, 6).Format("01-02"))

	// 1. Account Stats
	var accounts []WeeklyAccountStat
	var totalUsedPct, totalRemainingPct float64

	var todayProfiles map[string]any
	if todayMap, ok := byDate[nowDate].(map[string]any); ok {
		todayProfiles, _ = todayMap["profiles"].(map[string]any)
	}

	for _, name := range profileNames {
		pMap, ok := profsMap[name].(map[string]any)
		if !ok {
			continue
		}
		email, _ := pMap["email"].(string)
		paused, _ := pMap["paused"].(bool)
		tierLabel, _, _ := extractProfileTier(pMap)
		q, _ := pMap["last_quota"].(map[string]any)
		gWeek := getFloat(q, "gemini_weekly")
		if _, hasWeek := q["gemini_weekly"]; !hasWeek {
			gWeek = 1.0
		}

		remPct := math.Round(gWeek*1000) / 10.0
		usedPct := math.Round((1.0-gWeek)*1000) / 10.0
		if usedPct < 0 {
			usedPct = 0
		}

		todayTok := int64(0)
		if tp, ok := todayProfiles[name].(map[string]any); ok {
			todayTok = int64(getFloat(tp, "total_tokens"))
		}

		// Calculate tokens consumed in current cycle for this account (from account's cycle start to today)
		accCycleStart := currCycleStart
		if overrideStart, ok := c.getCycleOverride(now); ok {
			accCycleStart = overrideStart
		} else if rWeekStr, ok := q["reset_weekly"].(string); ok && rWeekStr != "" {
			if t, err := time.Parse(time.RFC3339, rWeekStr); err == nil {
				accCycleStart = calculateCycleStart(t, now)
			}
		}

		daysSinceAccStart := int(todayMidnight.Sub(accCycleStart).Hours() / 24)
		if daysSinceAccStart < 0 {
			daysSinceAccStart = 0
		}
		if daysSinceAccStart > 6 {
			daysSinceAccStart = 6
		}

		weekTok := int64(0)
		for d := 0; d <= daysSinceAccStart; d++ {
			dt := accCycleStart.AddDate(0, 0, d).Format("2006-01-02")
			if dayMap, ok := byDate[dt].(map[string]any); ok {
				if dayProfs, ok := dayMap["profiles"].(map[string]any); ok {
					if dp, ok := dayProfs[name].(map[string]any); ok {
						weekTok += int64(getFloat(dp, "total_tokens"))
					}
				}
			}
		}

		col := profileColorForName(name)

		accounts = append(accounts, WeeklyAccountStat{
			Name:         name,
			Email:        email,
			UsedPct:      usedPct,
			RemainingPct: remPct,
			TodayTokens:  todayTok,
			WeekTokens:   weekTok,
			IsActive:     name == activeProfile,
			Paused:       paused,
			Tier:         tierLabel,
			Color:        col,
		})

		totalUsedPct += usedPct
		totalRemainingPct += remPct
	}

	accWeekTok := make(map[string]int64)
	accUsedPct := make(map[string]float64)
	var sumCalibTok int64
	var sumCalibPct float64
	for _, a := range accounts {
		accWeekTok[a.Name] = a.WeekTokens
		accUsedPct[a.Name] = a.UsedPct
		if a.WeekTokens > 0 && a.UsedPct > 0 {
			sumCalibTok += a.WeekTokens
			sumCalibPct += a.UsedPct
		}
	}

	// 2. Multi-Day Weekly Quota Burn Trend based on Google Reset Cycle
	weekdays := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	numAccs := float64(len(accounts))
	if numAccs < 1 {
		numAccs = 1
	}

	buildCyclePoints := func(cycleStart time.Time, calibrate bool) []WeeklyDayQuotaPoint {
		var points []WeeklyDayQuotaPoint
		cumBurnPct := 0.0

		for d := 0; d < 7; d++ {
			dayTime := cycleStart.AddDate(0, 0, d)
			dtStr := dayTime.Format("2006-01-02")
			isToday := (dtStr == nowDate)
			isFuture := dayTime.After(now) && !isToday

			dayLabel := weekdays[dayTime.Weekday()]
			if isToday {
				dayLabel = "今天"
			} else if dtStr == yesterdayDate {
				dayLabel = "昨天"
			}

			profileTokens := make(map[string]int64)
			byProfilePct := make(map[string]float64)
			fleetTokens := int64(0)
			fleetBurnPct := 0.0

			if !isFuture {
				var sumAccPct float64
				if dayMap, ok := byDate[dtStr].(map[string]any); ok {
					if profs, ok := dayMap["profiles"].(map[string]any); ok {
						for _, name := range profileNames {
							if pData, ok := profs[name].(map[string]any); ok {
								tok := int64(getFloat(pData, "total_tokens"))
								profileTokens[name] = tok
								fleetTokens += tok

								var accPct float64
								if calibrate && accWeekTok[name] > 0 && accUsedPct[name] > 0 {
									accPct = (float64(tok) / float64(accWeekTok[name])) * accUsedPct[name]
								} else if sumCalibTok > 0 && sumCalibPct > 0 {
									// Fallback calibration using observed fleet-wide tokens-per-% ratio
									accPct = (float64(tok) / float64(sumCalibTok)) * sumCalibPct
								}
								byProfilePct[name] = math.Round(accPct*10) / 10.0
								sumAccPct += accPct
							} else {
								profileTokens[name] = 0
								byProfilePct[name] = 0
							}
						}
					}
				}
				fleetBurnPct = math.Round((sumAccPct/numAccs)*10) / 10.0
				cumBurnPct = math.Round((cumBurnPct+fleetBurnPct)*10) / 10.0
			}

			points = append(points, WeeklyDayQuotaPoint{
				Date:          dtStr,
				DayLabel:      dayLabel,
				IsToday:       isToday,
				IsFuture:      isFuture,
				FleetBurnPct:  fleetBurnPct,
				CumBurnPct:    cumBurnPct,
				FleetTokens:   fleetTokens,
				ByProfile:     byProfilePct,
				ProfileTokens: profileTokens,
			})
		}
		return points
	}

	dailyTrend := buildCyclePoints(currCycleStart, true)
	previousTrend := buildCyclePoints(prevCycleStart, false)

	return &WeeklyQuotaRelayData{
		TotalCapacityPct:  float64(len(accounts)) * 100.0,
		TotalUsedPct:      math.Round(totalUsedPct*10) / 10.0,
		TotalRemainingPct: math.Round(totalRemainingPct*10) / 10.0,
		ResetWeeklyStr:    resetCountdown,
		CurrentCycleLabel: currCycleLabel,
		PrevCycleLabel:    prevCycleLabel,
		Accounts:          accounts,
		DailyTrend:        dailyTrend,
		PreviousTrend:     previousTrend,
	}, nil
}

// GetDailyTopSessions parses recent latency records to aggregate top 5 sessions per day.
func (c *Client) GetDailyTopSessions(days int) ([]DailySessionLeaderboard, error) {
	if days <= 0 {
		days = 7
	}

	type sessStat struct {
		inTok    int64
		outTok   int64
		cacheTok int64
		reqs     int
		models   map[string]int
		profiles map[string]int
	}

	datesMap := make(map[string]map[string]*sessStat)
	dailyTotals := make(map[string]int64)
	var allUUIDs []string
	seenUUIDs := make(map[string]bool)

	cutoffDate := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	// Read both current and rotated latency logs
	logFiles := []string{
		filepath.Join(c.baseDir, "latency.jsonl.1"),
		filepath.Join(c.baseDir, "latency.jsonl"),
	}

	for _, p := range logFiles {
		file, err := os.Open(p)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		buf := make([]byte, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) < 20 || !bytes.Contains(line, []byte(`"session"`)) {
				continue
			}

			var r struct {
				TS       string  `json:"ts"`
				Session  string  `json:"session"`
				PTok     float64 `json:"p_tok"`
				CTok     float64 `json:"c_tok"`
				CacheTok float64 `json:"cache_tok"`
				Status   float64 `json:"status"`
				Model    string  `json:"model"`
				Profile  string  `json:"profile"`
			}
			if json.Unmarshal(line, &r) != nil || r.Session == "" || len(r.TS) < 10 {
				continue
			}

			date := r.TS[:10]
			if date < cutoffDate {
				continue
			}

			tot := int64(r.PTok + r.CTok)
			if tot <= 0 && int(r.Status) != 200 {
				continue
			}

			if datesMap[date] == nil {
				datesMap[date] = make(map[string]*sessStat)
			}
			s := datesMap[date][r.Session]
			if s == nil {
				s = &sessStat{
					models:   make(map[string]int),
					profiles: make(map[string]int),
				}
				datesMap[date][r.Session] = s
			}

			s.inTok += int64(r.PTok)
			s.outTok += int64(r.CTok)
			s.cacheTok += int64(r.CacheTok)
			s.reqs++
			dailyTotals[date] += tot

			if r.Model != "" {
				s.models[r.Model]++
			}
			if r.Profile != "" {
				s.profiles[r.Profile]++
			}

			u := stripSessionPrefix(r.Session)
			if u != "" && !seenUUIDs[u] {
				seenUUIDs[u] = true
				allUUIDs = append(allUUIDs, u)
			}
		}
		file.Close()
	}

	// Resolve titles from SQLite conversation database
	titles := c.sessionNames(allUUIDs)

	weekdays := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	nowToday := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	var sortedDates []string
	for d := range datesMap {
		sortedDates = append(sortedDates, d)
	}
	// Sort dates descending (newest date first)
	sort.Slice(sortedDates, func(i, j int) bool {
		return sortedDates[i] > sortedDates[j]
	})

	var result []DailySessionLeaderboard

	for _, d := range sortedDates {
		sessions := datesMap[d]
		totalTok := dailyTotals[d]

		type kv struct {
			id   string
			stat *sessStat
			tot  int64
		}
		var list []kv
		for id, st := range sessions {
			list = append(list, kv{
				id:   id,
				stat: st,
				tot:  st.inTok + st.outTok,
			})
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].tot > list[j].tot
		})

		topN := 5
		if len(list) < topN {
			topN = len(list)
		}

		var topSessions []DailyTopSession
		for i := 0; i < topN; i++ {
			item := list[i]
			u := stripSessionPrefix(item.id)
			tName := titles[u]
			if tName == "" {
				if len(u) >= 8 {
					tName = "会话 " + u[:8]
				} else {
					tName = item.id
				}
			}

			// Find dominant model and profile
			topM, topP := "-", "-"
			maxM, maxP := 0, 0
			for m, count := range item.stat.models {
				if count > maxM {
					maxM = count
					topM = m
				}
			}
			for p, count := range item.stat.profiles {
				if count > maxP {
					maxP = count
					topP = p
				}
			}

			pct := 0.0
			if totalTok > 0 {
				pct = math.Round((float64(item.tot)/float64(totalTok))*1000) / 10.0
			}

			topSessions = append(topSessions, DailyTopSession{
				SessionID:    item.id,
				SessionUUID:  u,
				Title:        tName,
				Date:         d,
				TotalTokens:  item.tot,
				InputTokens:  item.stat.inTok,
				OutputTokens: item.stat.outTok,
				CachedTokens: item.stat.cacheTok,
				Requests:     item.stat.reqs,
				TopProfile:   topP,
				TopModel:     topM,
				PctOfDay:     pct,
			})
		}

		dayLabel := d
		if t, err := time.Parse("2006-01-02", d); err == nil {
			if d == nowToday {
				dayLabel = "今天"
			} else if d == yesterday {
				dayLabel = "昨天"
			} else {
				dayLabel = weekdays[t.Weekday()]
			}
		}

		result = append(result, DailySessionLeaderboard{
			Date:        d,
			DayLabel:    dayLabel,
			TotalTokens: totalTok,
			Sessions:    topSessions,
		})
	}

	return result, nil
}
