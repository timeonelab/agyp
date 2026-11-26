package agyp

type DashboardData struct {
	Period          string               `json:"period"`
	Summary         SummaryData          `json:"summary"`
	Models          []ModelStat          `json:"models"`
	Profiles        []ProfileQuota       `json:"profiles"`
	DailyTrends     []DailyTrendPoint    `json:"dailyTrends"`
	Errors          []ErrorBreakdownItem `json:"errors"`
	ActiveProfile   string               `json:"activeProfile"`
	PausedProfiles  []string             `json:"pausedProfiles,omitempty"`
	EnabledProfiles []string             `json:"enabledProfiles,omitempty"`
}

type ErrorBreakdownItem struct {
	Code           int      `json:"code"`
	Type           string   `json:"type"`
	Reason         string   `json:"reason"`
	Count          int      `json:"count"`
	Percentage     float64  `json:"percentage"`
	SampleErr      string   `json:"sampleErr"`
	Profile        string   `json:"profile"`
	AffectedModels []string `json:"affectedModels,omitempty"`
	ModelDetails   string   `json:"modelDetails,omitempty"`
}

type SummaryData struct {
	OutputTokens       int64                `json:"outputTokens"`
	OutputTokensStr    string               `json:"outputTokensStr"`
	ThoughtTokens      int64                `json:"thoughtTokens"`
	ThoughtTokensStr   string               `json:"thoughtTokensStr"`
	OutputDiff         string               `json:"outputDiff"`
	InputTokens        int64                `json:"inputTokens"`
	InputTokensStr     string               `json:"inputTokensStr"`
	CachedTokens       int64                `json:"cachedTokens"`
	CachedTokensStr    string               `json:"cachedTokensStr"`
	CacheEligibleInput int64                `json:"cacheEligibleInput,omitempty"`
	CacheHitRate       float64              `json:"cacheHitRate"`
	Requests           int                  `json:"requests"`
	RequestsStr        string               `json:"requestsStr"`
	SuccessRate        float64              `json:"successRate"`
	FailCount          int                  `json:"failCount"`
	RetriedRecovered   int                  `json:"retriedRecovered"`
	AvgSpeed           float64              `json:"avgSpeed"`
	AvgSpeedStr        string               `json:"avgSpeedStr"`
	E2ESpeed           float64              `json:"e2eSpeed"`
	E2ESpeedStr        string               `json:"e2eSpeedStr"`
	P50LatencyMs       float64              `json:"p50LatencyMs"`
	CostUSD            float64              `json:"costUsd"`
	CostCNY            float64              `json:"costCny"`
	Sparklines         map[string][]float64 `json:"sparklines"`
}

type ModelStat struct {
	Model           string               `json:"model"`
	OutputTokens    int64                `json:"outputTokens"`
	ThoughtTokens   int64                `json:"thoughtTokens"`
	OutputTokensStr string               `json:"outputTokensStr"`
	CostUSD         float64              `json:"costUsd"`
	CostUSDStr      string               `json:"costUsdStr"`
	Ratio           float64              `json:"ratio"`
	AvgSpeed        float64              `json:"avgSpeed"`
	AvgSpeedStr     string               `json:"avgSpeedStr"`
	E2ESpeed        float64              `json:"e2eSpeed"`
	E2ESpeedStr     string               `json:"e2eSpeedStr"`
	Requests        int                  `json:"requests"`
	RequestsStr     string               `json:"requestsStr"`
	FailCount       int                  `json:"failCount"`
	SuccessCount    int                  `json:"successCount"`
	SuccessRate     float64              `json:"successRate"`
	ErrorReason     string               `json:"errorReason"`
	ErrorBreakdown  []ModelErrorCodeStat `json:"errorBreakdown,omitempty"`
}

type ModelErrorCodeStat struct {
	Code   int    `json:"code"`
	Count  int    `json:"count"`
	Reason string `json:"reason"`
}

type CheckResult struct {
	Profile   string `json:"profile"`
	TestType  string `json:"testType"`
	Pass      bool   `json:"pass"`
	LatencyMs int64  `json:"latencyMs"`
	Summary   string `json:"summary"`
	Detail    string `json:"detail"`
	HTMLFile  string `json:"htmlFile"`
}

type WasteScoreData struct {
	ScoreGrade        string   `json:"scoreGrade"`        // A+, A, B, C
	HitRate           float64  `json:"hitRate"`           // 95.7%
	CachedTokens      int64    `json:"cachedTokens"`      // 559M
	InputTokens       int64    `json:"inputTokens"`       // 584M
	CostSavedUSD      float64  `json:"costSavedUsd"`      // $44.15
	CostSavedCNY      float64  `json:"costSavedCny"`      // ￥317.72
	PrefillTimeSavedS float64  `json:"prefillTimeSavedS"` // ~1200s
	Recommendations   []string `json:"recommendations"`
}

type RoutingRule struct {
	Pattern     string `json:"pattern"`
	Target      string `json:"target"`
	Description string `json:"description"`
}

type RoutingConfig struct {
	ActiveProfile  string        `json:"activeProfile"`
	PriorityChain  []string      `json:"priorityChain"`
	PausedProfiles []string      `json:"pausedProfiles,omitempty"`
	Rules          []RoutingRule `json:"rules"`
	FailoverMode   string        `json:"failoverMode"`
}

type CommandResult struct {
	Success bool   `json:"success"`
	Blocked bool   `json:"blocked"`
	Message string `json:"message"`
	PID     int    `json:"pid"`
}

type RelayTimelineData struct {
	SummaryRecommendation string                   `json:"summaryRecommendation"`
	RelayOrder            []string                 `json:"relayOrder"`
	TimelineEvents        []RelayEvent             `json:"timelineEvents"`
	HealthScore           float64                  `json:"healthScore"`
	SafeWorkingHours      string                   `json:"safeWorkingHours"`
	WeeklyQuota           *WeeklyQuotaRelayData    `json:"weeklyQuota,omitempty"`
	DailyTopSessions      []DailySessionLeaderboard `json:"dailyTopSessions,omitempty"`
}

type WeeklyQuotaRelayData struct {
	TotalCapacityPct  float64               `json:"totalCapacityPct"`
	TotalUsedPct      float64               `json:"totalUsedPct"`
	TotalRemainingPct float64               `json:"totalRemainingPct"`
	ResetWeeklyStr    string                `json:"resetWeeklyStr"`
	CurrentCycleLabel string                `json:"currentCycleLabel"`
	PrevCycleLabel    string                `json:"prevCycleLabel"`
	Accounts          []WeeklyAccountStat   `json:"accounts"`
	DailyTrend        []WeeklyDayQuotaPoint `json:"dailyTrend"`
	PreviousTrend     []WeeklyDayQuotaPoint `json:"previousTrend,omitempty"`
}

type WeeklyAccountStat struct {
	Name         string  `json:"name"`
	Email        string  `json:"email"`
	UsedPct      float64 `json:"usedPct"`
	RemainingPct float64 `json:"remainingPct"`
	TodayTokens  int64   `json:"todayTokens"`
	WeekTokens   int64   `json:"weekTokens"`
	IsActive     bool    `json:"isActive"`
	Paused       bool    `json:"paused"`
	Tier         string  `json:"tier,omitempty"`
	Color        string  `json:"color"`
}

type WeeklyDayQuotaPoint struct {
	Date          string             `json:"date"`
	DayLabel      string             `json:"dayLabel"`
	IsToday       bool               `json:"isToday"`
	IsFuture      bool               `json:"isFuture,omitempty"`
	FleetBurnPct  float64            `json:"fleetBurnPct"`
	CumBurnPct    float64            `json:"cumBurnPct"`
	FleetTokens   int64              `json:"fleetTokens"`
	ByProfile     map[string]float64 `json:"byProfile"`
	ProfileTokens map[string]int64   `json:"profileTokens"`
}

type DailyTopSession struct {
	SessionID    string  `json:"sessionId"`
	SessionUUID  string  `json:"sessionUuid"`
	Title        string  `json:"title"`
	Date         string  `json:"date"`
	TotalTokens  int64   `json:"totalTokens"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CachedTokens int64   `json:"cachedTokens"`
	Requests     int     `json:"requests"`
	TopProfile   string  `json:"topProfile"`
	TopModel     string  `json:"topModel"`
	PctOfDay     float64 `json:"pctOfDay"`
}

type DailySessionLeaderboard struct {
	Date        string            `json:"date"`
	DayLabel    string            `json:"dayLabel"`
	TotalTokens int64             `json:"totalTokens"`
	Sessions    []DailyTopSession `json:"sessions"`
}

type RelayEvent struct {
	Profile          string  `json:"profile"`
	Type             string  `json:"type"`             // "5h" or "weekly"
	TypeLabel        string  `json:"typeLabel"`        // "5小时滚动回满" or "每周全额重置"
	TargetTime       string  `json:"targetTime"`
	FormattedTime    string  `json:"formattedTime"`    // "今天 17:25 (4时00分后)"
	RemainingSeconds int64   `json:"remainingSeconds"`
	CurrentCapacity  float64 `json:"currentCapacity"`
	RecoverCapacity  float64 `json:"recoverCapacity"`
	IsActive         bool    `json:"isActive"`
	Paused           bool    `json:"paused"`
	Urgency          string  `json:"urgency"`          // "soon", "today", "later"
}

type ConnectInfo struct {
	LocalURL      string        `json:"localUrl"`
	LanURL        string        `json:"lanUrl"`
	Port          int           `json:"port"`
	Proxy         string        `json:"proxy"`
	ActiveProfile string        `json:"activeProfile"`
	Clients       []ClientSetup `json:"clients"`
}

type ClientSetup struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	EnvCmd      string `json:"envCmd"`
	ConfigTip   string `json:"configTip"`
}

type EndpointTestResult struct {
	Path      string `json:"path"`
	Profile   string `json:"profile"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latencyMs"`
	Success   bool   `json:"success"`
	Detail    string `json:"detail"`
}

type ProfileQuota struct {
	Name            string  `json:"name"`
	Email           string  `json:"email"`
	Gemini5h        float64 `json:"gemini5h"`
	GeminiWeekly    float64 `json:"geminiWeekly"`
	ClaudeWeekly    float64 `json:"claudeWeekly"`
	Reset5h         string  `json:"reset5h"`
	ResetWeekly     string  `json:"resetWeekly"`
	Reset5hRaw      string  `json:"reset5hRaw,omitempty"`
	ResetWeeklyRaw  string  `json:"resetWeeklyRaw,omitempty"`
	Reset5hFull     string  `json:"reset5hFull,omitempty"`
	ResetWeeklyFull string  `json:"resetWeeklyFull,omitempty"`
	TodayTokens     int64   `json:"todayTokens"`
	TodayTokensStr  string  `json:"todayTokensStr"`
	IsActive        bool    `json:"isActive"`
	Paused          bool    `json:"paused"`
	Tier            string  `json:"tier,omitempty"`
	TierName        string  `json:"tierName,omitempty"`
	TierID          string  `json:"tierId,omitempty"`
}

type DailyTrendPoint struct {
	Date         string  `json:"date"`
	OutputTokens int64   `json:"outputTokens"`
	InputTokens  int64   `json:"inputTokens"`
	Requests     int     `json:"requests"`
	CostUSD      float64 `json:"costUsd"`
	CostCNY      float64 `json:"costCny"`
	IsToday      bool    `json:"isToday"`
}

type LiveDiag struct {
	Online          bool           `json:"online"`
	Pid             int            `json:"pid"`
	ActiveProfile   string         `json:"activeProfile"`
	PausedProfiles  []string       `json:"pausedProfiles,omitempty"`
	EnabledProfiles []string       `json:"enabledProfiles,omitempty"`
	InflightNow     int            `json:"inflightNow"`
	InflightPeak    int            `json:"inflightPeak"`
	PoolHitRate     float64        `json:"poolHitRate"`
	PoolHits        int            `json:"poolHits"`
	PoolMiss        int            `json:"poolMiss"`
	PoolIdle        map[string]int `json:"poolIdle"`
	CacheHits       int            `json:"cacheHits"`
	CacheMiss       int            `json:"cacheMiss"`
	CacheSavedMB    float64        `json:"cacheSavedMb"`
	RecentSuccess     int            `json:"recentSuccess"`
	RecentFail        int            `json:"recentFail"`
	RecentFailRate    float64        `json:"recentFailRate"`
	RecentSpeed       float64        `json:"recentSpeed"`
	RecentDecodeSpeed float64        `json:"recentDecodeSpeed"`
	RecentE2ESpeed    float64        `json:"recentE2ESpeed"`
	P50HeadMs         float64        `json:"p50HeadMs"`
}

type RequestLogRow struct {
	Time            string  `json:"time"`
	TimestampMs     int64   `json:"tsMs,omitempty"`
	Path            string  `json:"path"`
	Status          int     `json:"status"`
	Model           string  `json:"model"`
	Profile         string  `json:"profile"`
	Session         string  `json:"session"`
	SessionName     string  `json:"sessionName"`
	ParentSession   string  `json:"parentSession,omitempty"`
	AgentName       string  `json:"agentName,omitempty"`
	HeadMs          float64 `json:"headMs"`
	StreamMs        float64 `json:"streamMs"`
	TotalMs         float64 `json:"totalMs"`
	InTok           int     `json:"inTok"`
	OutTok          int     `json:"outTok"`
	ThoughtTok      int     `json:"thoughtTok"`
	CacheTok        int     `json:"cacheTok"`
	CacheAnomaly    string  `json:"cacheAnomaly,omitempty"`
	CacheAnomalyTip string  `json:"cacheAnomalyTip,omitempty"`
	Speed           float64 `json:"speed"`
	DecodeSpeed     float64 `json:"decodeSpeed"`
	Pool            string  `json:"pool"`
	Cache           string  `json:"cache"`
	Retry           int     `json:"retry"`
	Error           string  `json:"error"`
}

type ContextCategory struct {
	Key       string  `json:"key"`
	Label     string  `json:"label"`
	Tokens    int64   `json:"tokens"`
	Percent   float64 `json:"percent"`
	Tone      string  `json:"tone"`
	Estimated bool    `json:"estimated"`
}

type ModelContextEntry struct {
	Role        string  `json:"role"`
	ModelName   string  `json:"modelName"`
	ShortName   string  `json:"shortName"`
	UsedTokens  int64   `json:"usedTokens"`
	MaxTokens   int64   `json:"maxTokens"`
	UsedPercent float64 `json:"usedPercent"`
	Compactions int     `json:"compactions"`
	IsCurrent   bool    `json:"isCurrent"`
}

type SessionContextWindow struct {
	UsedTokens        int64               `json:"usedTokens"`
	MaxTokens         int64               `json:"maxTokens"`
	UsedPercent       float64             `json:"usedPercent"`
	FreeTokens        int64               `json:"freeTokens"`
	FreePercent       float64             `json:"freePercent"`
	Compactions       int                 `json:"compactions"`
	CompactedPending  bool                `json:"compactedPending"`
	Categories        []ContextCategory   `json:"categories"`
	CacheReusedTokens int64               `json:"cacheReusedTokens"`
	CacheReusedPct    float64             `json:"cacheReusedPct"`
	NewInputTokens    int64               `json:"newInputTokens"`
	NewInputPct       float64             `json:"newInputPct"`
	Models            []ModelContextEntry `json:"models,omitempty"`
	Source            string              `json:"source"`
}

type SubagentSummary struct {
	ConversationID  string  `json:"conversationId"`
	ParentID        string  `json:"parentId"`
	Name            string  `json:"name"`
	ModelName       string  `json:"modelName"`
	ShortModel      string  `json:"shortModel"`
	Depth           int     `json:"depth"`
	Status          string  `json:"status"`
	UsedTokens      int64   `json:"usedTokens"`
	MaxTokens       int64   `json:"maxTokens"`
	UsedPercent     float64 `json:"usedPercent"`
	TotalTokens     int64   `json:"totalTokens"`
	InputTokens     int64   `json:"inputTokens"`
	CacheReadTokens int64   `json:"cacheReadTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CacheHitRate    float64 `json:"cacheHitRate"`
	AvgSpeed        float64 `json:"avgSpeed"`
	CostUSD         float64 `json:"costUsd"`
	Requests        int     `json:"requests"`
	ToolCalls       int     `json:"toolCalls"`
}

type SubagentRollup struct {
	Count               int     `json:"count"`
	TotalCount          int     `json:"totalCount"`
	TotalTokens         int64   `json:"totalTokens"`
	CombinedTotalTokens int64   `json:"combinedTotalTokens"`
	Requests            int     `json:"requests"`
	ToolCalls           int     `json:"toolCalls"`
	CacheHitRate        float64 `json:"cacheHitRate"`
	CostUSD             float64 `json:"costUsd"`
	CombinedCostUSD     float64 `json:"combinedCostUsd"`
}

type SessionDetail struct {
	SessionID        string                `json:"sessionId"`
	SessionUUID      string                `json:"sessionUuid"`
	Title            string                `json:"title"`
	AgentName        string                `json:"agentName,omitempty"`
	ParentSessionID  string                `json:"parentSessionId,omitempty"`
	ParentTitle      string                `json:"parentTitle,omitempty"`
	ModelName        string                `json:"modelName"`
	ShortModel       string                `json:"shortModel"`
	Source           string                `json:"source"`
	Context          *SessionContextWindow `json:"context,omitempty"`
	Turns            int                   `json:"turns"`
	Steps            int                   `json:"steps"`
	Requests         int                   `json:"requests"`
	ToolCalls        int                   `json:"toolCalls"`
	TotalTokens      int64                 `json:"totalTokens"`
	InputTokens      int64                 `json:"inputTokens"`
	CacheReadTokens  int64                 `json:"cacheReadTokens"`
	OutputTokens     int64                 `json:"outputTokens"`
	ThinkingTokens   int64                 `json:"thinkingTokens"`
	CostUSD          float64               `json:"costUsd"`
	CostCNY          float64               `json:"costCny"`
	AvgSpeed         float64               `json:"avgSpeed"`
	PeakSpeed        float64               `json:"peakSpeed"`
	LatestSpeed      float64               `json:"latestSpeed"`
	CacheHitRate     float64               `json:"cacheHitRate"`
	LastRequestAtMs  int64                 `json:"lastRequestAtMs"`
	CacheWindowMs    int64                 `json:"cacheWindowMs"`
	CacheRemainingMs int64                 `json:"cacheRemainingMs"`
	ModelTimeMs      float64               `json:"modelTimeMs"`
	ToolTimeMs       float64               `json:"toolTimeMs"`
	LatestTtftMs     float64               `json:"latestTtftMs"`
	AvgTtftMs        float64               `json:"avgTtftMs"`
	P50TtftMs        float64               `json:"p50TtftMs"`
	Profiles         []string              `json:"profiles,omitempty"`
	ProfileSwitches  int                   `json:"profileSwitches"`
	GatewayRetries   int                   `json:"gatewayRetries"`
	GatewayErrors    int                   `json:"gatewayErrors"`
	Subagents        []SubagentSummary     `json:"subagents,omitempty"`
	SubagentRollup   *SubagentRollup       `json:"subagentRollup,omitempty"`
}
