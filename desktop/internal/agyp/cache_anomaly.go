package agyp

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

var (
	reParenEffort    = regexp.MustCompile(`\s*\([^)]*\)\s*`)
	reTrailingEffort = regexp.MustCompile(`-(?:low|medium|high|xhigh|extra-high|max|minimal|thinking)$`)
	reClaudeDotVer   = regexp.MustCompile(`(claude-(?:opus|sonnet|haiku)-\d+)\.(\d+)`)
	reMultiSpace     = regexp.MustCompile(`\s+`)
)

func normalizeModelFamily(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	s = reParenEffort.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	s = reMultiSpace.ReplaceAllString(s, "-")
	s = reTrailingEffort.ReplaceAllString(s, "")
	s = reClaudeDotVer.ReplaceAllString(s, "$1-$2")
	return s
}

func annotateRequestCacheAnomalies(rows []RequestLogRow) {
	if len(rows) == 0 {
		return
	}

	const (
		maxGapMs       int64   = 180_000
		minPrevCache   int     = 4000
		minPrevHit     float64 = 50.0
		minCurInput    int     = 4000
		minMissedInput int     = 16000
		minKeepRatio   float64 = 0.60
	)

	cachedModels := make(map[string]bool)
	for i := range rows {
		if rows[i].CacheTok > 0 && rows[i].Model != "" {
			cachedModels[normalizeModelFamily(rows[i].Model)] = true
		}
	}

	type prevState struct {
		tsMs      int64
		inTok     int
		cacheTok  int
		hit       float64
		profile   string
		anomalous bool
	}
	lastByKey := make(map[string]prevState)

	// rows is sorted newest-first; iterate chronologically from oldest to newest.
	for i := len(rows) - 1; i >= 0; i-- {
		r := &rows[i]
		if r.Status != 200 || r.Error != "" || r.Session == "" || r.Model == "" || r.InTok <= 0 {
			continue
		}
		if strings.HasPrefix(r.Session, "checkpoint/") {
			continue
		}

		sessUUID := stripSessionPrefix(r.Session)
		if sessUUID == "" {
			continue
		}
		modelKey := normalizeModelFamily(r.Model)
		key := sessUUID + "::" + modelKey

		curIn := r.InTok
		curCache := r.CacheTok
		curHit := math.Min(100.0, math.Round(float64(curCache)*1000.0/float64(curIn))/10.0)

		anomalous := false
		if prev, ok := lastByKey[key]; ok && curIn >= minCurInput && float64(curIn) >= float64(prev.inTok)*minKeepRatio {
			gapMs := int64(0)
			hasGap := r.TimestampMs > 0 && prev.tsMs > 0
			if hasGap {
				gapMs = r.TimestampMs - prev.tsMs
			}
			inWindow := !hasGap || (gapMs >= 0 && gapMs <= maxGapMs)
			if inWindow {
				kind := ""
				if prev.cacheTok >= minPrevCache && prev.hit >= minPrevHit {
					if curCache == 0 {
						kind = "cleared"
					} else if prev.hit-curHit >= 40.0 && curHit < 20.0 && float64(curCache) < float64(prev.cacheTok)*0.5 {
						kind = "dropped"
					}
				} else if curCache == 0 && cachedModels[modelKey] && (prev.anomalous || prev.inTok >= minMissedInput) {
					kind = "missed"
				}

				if kind != "" {
					anomalous = true
					r.CacheAnomaly = kind
					r.CacheAnomalyTip = formatCacheAnomalyTip(kind, gapMs, hasGap, prev.hit, curHit, prev.profile, r.Profile, r.Retry)
				}
			}
		}

		if curIn >= 2000 {
			lastByKey[key] = prevState{
				tsMs:      r.TimestampMs,
				inTok:     curIn,
				cacheTok:  curCache,
				hit:       curHit,
				profile:   r.Profile,
				anomalous: anomalous,
			}
		}
	}
}

func formatCacheAnomalyTip(kind string, gapMs int64, hasGap bool, prevHit, curHit float64, prevProf, curProf string, retry int) string {
	var title string
	switch kind {
	case "cleared":
		title = "异常清零"
	case "dropped":
		title = "缓存断崖下跌"
	default:
		title = "大上下文未命中缓存"
	}

	var parts []string
	if hasGap {
		parts = append(parts, fmt.Sprintf("距上次 %ds", (gapMs+500)/1000))
	}
	if kind == "missed" {
		parts = append(parts, fmt.Sprintf("命中率 %.1f%% → 0%%", prevHit))
	} else {
		parts = append(parts, fmt.Sprintf("命中率 %.1f%% → %.1f%%", prevHit, curHit))
	}

	var notes []string
	if prevProf != "" && curProf != "" && prevProf != curProf {
		notes = append(notes, fmt.Sprintf("发生切号: %s → %s", prevProf, curProf))
	}
	if retry > 0 {
		notes = append(notes, "伴随容灾重试")
	}

	msg := fmt.Sprintf("%s：%s", title, strings.Join(parts, "，"))
	if len(notes) > 0 {
		msg += "（" + strings.Join(notes, "，") + "）"
	}
	return msg
}
