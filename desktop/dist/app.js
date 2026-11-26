(function () {
  'use strict';

  let currentDays = 1;
  let activeTab = 'overview';
  let cachedDashboard = null;
  let cachedDiag = null;
  let cachedLogs = [];
  let sessionNameById = {};
  let sessionAgentById = {};
  let sessionParentById = {};
  let currentInspectedLog = null;
  let currentSessionTarget = '';
  let sessionDetailCache = {};
  let cacheCountdownTimer = null;
  let cachedRelayData = null;
  function readPersistedChoice(key, allowed, fallback) {
    try {
      const val = localStorage.getItem(key);
      if (val && allowed.includes(val)) return val;
    } catch (e) {}
    return fallback;
  }

  let activeChartView = readPersistedChoice('agyp_chart_view', ['tokens', 'quota'], 'tokens');
  let activeCurveMode = readPersistedChoice('agyp_curve_mode', ['cumulative', 'daily'], 'cumulative');
  let activeDailyMetric = readPersistedChoice('agyp_daily_metric', ['cost', 'tokens', 'requests'], 'cost');
  let focusedQuotaAccount = '';
  let activeSessionLeaderboardDate = '';
  let activeUsageRange = 'day'; // 'day' | 'week' for the usage analysis ranking
  let isPrivacyMode = localStorage.getItem('agyp_privacy_mode') !== '0';

  const ICONS = {
    eyeOpen: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>',
    eyeOff: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>',
    sun: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/></svg>',
    moon: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>',
    edit: '<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>',
    trash: '<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>',
    pause: '<svg viewBox="0 0 24 24" width="11" height="11" fill="currentColor"><rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/></svg>',
    play: '<svg viewBox="0 0 24 24" width="11" height="11" fill="currentColor"><polygon points="6 4 20 12 6 20 6 4"/></svg>',
  };

  function maskEmail(email) {
    const raw = String(email || '').trim();
    if (!raw) return '';
    if (!isPrivacyMode) return raw;
    const atIdx = raw.indexOf('@');
    if (atIdx <= 0) {
      return raw.length <= 2 ? raw[0] + '*' : raw.slice(0, 2) + '***';
    }
    const local = raw.slice(0, atIdx);
    const domain = raw.slice(atIdx + 1);
    const maskedLocal = local.length <= 2 ? local[0] + '*' : local.slice(0, 2) + '***';
    const dotIdx = domain.lastIndexOf('.');
    if (dotIdx <= 0) {
      return `${maskedLocal}@${domain.slice(0, 1)}***`;
    }
    const host = domain.slice(0, dotIdx);
    const tld = domain.slice(dotIdx + 1);
    const maskedHost = host.length <= 2 ? host[0] + '*' : host.slice(0, 2) + '***';
    return `${maskedLocal}@${maskedHost}.${tld}`;
  }

  const profileColorPalette = [
    '#10b981',
    '#a855f7',
    '#3b82f6',
    '#f59e0b',
    '#06b6d4',
    '#ec4899',
    '#6366f1',
    '#14b8a6'
  ];

  function getProfileColor(name) {
    let h = 2166136261;
    const s = String(name || '');
    for (let i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      h = Math.imul(h, 16777619) >>> 0;
    }
    return profileColorPalette[h % profileColorPalette.length];
  }

  // DOM Elements
  const rangeBtns = document.querySelectorAll('.range-btn');
  const tabBtns = document.querySelectorAll('.tab-btn');
  const tabPanes = document.querySelectorAll('.tab-pane');
  const exportBtn = document.getElementById('exportBtn');
  const notifTestBtn = document.getElementById('notifTestBtn');
  const refreshBtn = document.getElementById('refreshBtn');
  const privacyToggleBtn = document.getElementById('privacyToggleBtn');
  const themeToggleBtn = document.getElementById('themeToggleBtn');

  // Check elements
  const startCheckBtn = document.getElementById('startCheckBtn');
  const checkProfileSelect = document.getElementById('checkProfileSelect');
  const checkTypeSelect = document.getElementById('checkTypeSelect');

  // Drawer elements
  const inspectOverlay = document.getElementById('inspectOverlay');
  const drawerCloseBtn = document.getElementById('drawerCloseBtn');

  // Stream Log Controls & Filters
  let currentLogLimit = 100;
  let isStreamPaused = false;
  let isStreamExpanded = false;

  let logFilter = {
    scope: 'ALL',
    session: 'ALL',
    model: 'ALL',
    path: 'ALL',
    profile: 'ALL',
    status: 'ALL',
    keyword: ''
  };

  const logLimitSelect = document.getElementById('logLimitSelect');
  const logPauseToggleBtn = document.getElementById('logPauseToggleBtn');
  const logPauseIcon = document.getElementById('logPauseIcon');
  const logPauseText = document.getElementById('logPauseText');
  const logExpandToggleBtn = document.getElementById('logExpandToggleBtn');
  const logExpandIcon = document.getElementById('logExpandIcon');
  const logExpandText = document.getElementById('logExpandText');
  const logExportCurrentBtn = document.getElementById('logExportCurrentBtn');
  const streamMiniKpiBar = document.getElementById('streamMiniKpiBar');
  const streamTableContainer = document.getElementById('streamTableContainer');

  const logFilterSession = document.getElementById('logFilterSession');
  const logFilterModel = document.getElementById('logFilterModel');
  const logFilterPath = document.getElementById('logFilterPath');
  const logFilterProfile = document.getElementById('logFilterProfile');
  const logFilterStatus = document.getElementById('logFilterStatus');
  const logSearchInput = document.getElementById('logSearchInput');
  const logResetFiltersBtn = document.getElementById('logResetFiltersBtn');
  const streamFilteredCount = document.getElementById('streamFilteredCount');

  // Initialize
  window.addEventListener('DOMContentLoaded', () => {
    initTheme();
    initPrivacyToggle();
    initDialogEvents();
    initEvents();
    initPaneResizer();
    setupChartResizeObserver();
    loadAll();
    const leftPane = document.querySelector('.left-pane-scroll');
    if (leftPane) leftPane.scrollTop = 0;
    // Live polling every 2.5s for real-time status & recent requests
    setInterval(pollRealtime, 2500);
  });

  // Draggable Splitter to freely resize Left Pane vs Right Stream Console
  function initPaneResizer() {
    const resizer = document.getElementById('paneResizer');
    const leftPane = document.getElementById('leftPane');
    const container = document.querySelector('.dashboard-split-layout');
    if (!resizer || !leftPane || !container) return;

    const STORAGE_KEY = 'agyp_left_pane_width';
    const savedWidth = localStorage.getItem(STORAGE_KEY);
    if (savedWidth) {
      const w = parseInt(savedWidth, 10);
      if (w >= 360 && w <= window.innerWidth - 420) {
        leftPane.style.flex = `0 0 ${w}px`;
        leftPane.style.maxWidth = `${w}px`;
      }
    }

    let isDragging = false;
    let startX = 0;
    let startWidth = 0;

    resizer.addEventListener('mousedown', (e) => {
      isDragging = true;
      startX = e.clientX;
      startWidth = leftPane.getBoundingClientRect().width;
      document.body.classList.add('is-resizing');
      resizer.classList.add('is-dragging');
      e.preventDefault();
    });

    document.addEventListener('mousemove', (e) => {
      if (!isDragging) return;
      const delta = e.clientX - startX;
      let newWidth = startWidth + delta;

      const minWidth = 360;
      const maxWidth = Math.max(minWidth, container.clientWidth - 420);
      if (newWidth < minWidth) newWidth = minWidth;
      if (newWidth > maxWidth) newWidth = maxWidth;

      leftPane.style.flex = `0 0 ${newWidth}px`;
      leftPane.style.maxWidth = `${newWidth}px`;
    });

    const stopDragging = () => {
      if (!isDragging) return;
      isDragging = false;
      document.body.classList.remove('is-resizing');
      resizer.classList.remove('is-dragging');
      const finalWidth = leftPane.getBoundingClientRect().width;
      localStorage.setItem(STORAGE_KEY, Math.round(finalWidth));
    };

    document.addEventListener('mouseup', stopDragging);
    window.addEventListener('blur', stopDragging);
  }

  function initTheme() {
    const saved = localStorage.getItem('agyp_theme') || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    const syncNativeTheme = (isDark) => {
      const metaTheme = document.getElementById('metaThemeColor');
      if (metaTheme) metaTheme.content = isDark ? '#0e1015' : '#ffffff';
      callMyGo('GatewayService.SetTheme', isDark ? 'dark' : 'light').catch(() => {});
    };
    const syncIcon = (isDark) => {
      if (!themeToggleBtn) return;
      themeToggleBtn.innerHTML = isDark ? ICONS.sun : ICONS.moon;
      themeToggleBtn.title = isDark ? '切换为浅色主题' : '切换为深色主题';
    };
    const initialDark = saved === 'dark';
    document.body.classList.toggle('dark', initialDark);
    syncIcon(initialDark);
    syncNativeTheme(initialDark);

    if (themeToggleBtn) {
      themeToggleBtn.addEventListener('click', () => {
        const isDark = document.body.classList.toggle('dark');
        localStorage.setItem('agyp_theme', isDark ? 'dark' : 'light');
        syncIcon(isDark);
        syncNativeTheme(isDark);
      });
    }
  }

  function initPrivacyToggle() {
    const syncBtn = () => {
      if (!privacyToggleBtn) return;
      privacyToggleBtn.innerHTML = isPrivacyMode ? ICONS.eyeOff : ICONS.eyeOpen;
      privacyToggleBtn.classList.toggle('active', isPrivacyMode);
      privacyToggleBtn.title = isPrivacyMode ? '隐私模式已开启：隐藏账号邮箱（点击显示明文）' : '当前显示完整邮箱（点击开启隐私脱敏）';
    };
    syncBtn();
    if (privacyToggleBtn) {
      privacyToggleBtn.addEventListener('click', () => {
        isPrivacyMode = !isPrivacyMode;
        try { localStorage.setItem('agyp_privacy_mode', isPrivacyMode ? '1' : '0'); } catch (e) {}
        syncBtn();
        if (cachedDashboard && cachedDashboard.profiles) {
          renderProfiles(cachedDashboard.profiles, cachedDashboard.activeProfile);
        }
        showToast(isPrivacyMode ? '已开启邮箱隐私脱敏' : '已切换为显示完整邮箱');
      });
    }
  }

  let dialogResolver = null;
  let dialogMode = 'confirm';

  function closeBuiltinDialog(val) {
    const overlay = document.getElementById('agypDialogOverlay');
    if (overlay) overlay.style.display = 'none';
    const res = dialogResolver;
    dialogResolver = null;
    if (res) res(val);
  }

  function initDialogEvents() {
    const overlay = document.getElementById('agypDialogOverlay');
    const cancelBtn = document.getElementById('agypDialogCancel');
    const closeBtn = document.getElementById('agypDialogClose');
    const confirmBtn = document.getElementById('agypDialogConfirm');
    const inputEl = document.getElementById('agypDialogInput');
    if (cancelBtn) cancelBtn.addEventListener('click', () => closeBuiltinDialog(dialogMode === 'prompt' ? null : false));
    if (closeBtn) closeBtn.addEventListener('click', () => closeBuiltinDialog(dialogMode === 'prompt' ? null : false));
    if (overlay) {
      overlay.addEventListener('click', (e) => {
        if (e.target === overlay) closeBuiltinDialog(dialogMode === 'prompt' ? null : false);
      });
    }
    if (confirmBtn) {
      confirmBtn.addEventListener('click', () => {
        if (dialogMode === 'prompt') {
          closeBuiltinDialog(inputEl ? inputEl.value : '');
        } else {
          closeBuiltinDialog(true);
        }
      });
    }
    if (inputEl) {
      inputEl.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          closeBuiltinDialog(inputEl.value);
        } else if (e.key === 'Escape') {
          e.preventDefault();
          closeBuiltinDialog(null);
        }
      });
    }
  }

  function showConfirmDialog({ title = '确认操作', message = '', confirmText = '确认', danger = false } = {}) {
    return new Promise((resolve) => {
      dialogMode = 'confirm';
      dialogResolver = resolve;
      const overlay = document.getElementById('agypDialogOverlay');
      const titleEl = document.getElementById('agypDialogTitle');
      const msgEl = document.getElementById('agypDialogMessage');
      const inputEl = document.getElementById('agypDialogInput');
      const confirmBtn = document.getElementById('agypDialogConfirm');
      if (titleEl) titleEl.textContent = title;
      if (msgEl) msgEl.textContent = message;
      if (inputEl) inputEl.style.display = 'none';
      if (confirmBtn) {
        confirmBtn.textContent = confirmText;
        confirmBtn.className = danger ? 'btn btn-danger' : 'btn btn-primary';
      }
      if (overlay) overlay.style.display = 'flex';
    });
  }

  function showPromptDialog({ title = '请输入', message = '', defaultValue = '', placeholder = '', confirmText = '确定' } = {}) {
    return new Promise((resolve) => {
      dialogMode = 'prompt';
      dialogResolver = resolve;
      const overlay = document.getElementById('agypDialogOverlay');
      const titleEl = document.getElementById('agypDialogTitle');
      const msgEl = document.getElementById('agypDialogMessage');
      const inputEl = document.getElementById('agypDialogInput');
      const confirmBtn = document.getElementById('agypDialogConfirm');
      if (titleEl) titleEl.textContent = title;
      if (msgEl) msgEl.textContent = message;
      if (inputEl) {
        inputEl.style.display = 'block';
        inputEl.value = defaultValue;
        inputEl.placeholder = placeholder;
        setTimeout(() => { inputEl.focus(); inputEl.select(); }, 40);
      }
      if (confirmBtn) {
        confirmBtn.textContent = confirmText;
        confirmBtn.className = 'btn btn-primary';
      }
      if (overlay) overlay.style.display = 'flex';
    });
  }

  function initEvents() {
    // Time Range Click
    rangeBtns.forEach(btn => {
      btn.addEventListener('click', () => {
        rangeBtns.forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        currentDays = parseInt(btn.dataset.days, 10);
        loadDashboard();
      });
    });

    const leftPeriodBar = document.getElementById('leftPeriodBar');
    const syncLeftPeriodBar = (tab) => {
      if (leftPeriodBar) leftPeriodBar.style.display = tab === 'usage' ? 'none' : 'flex';
    };
    syncLeftPeriodBar(activeTab);

    // Tab Click
    tabBtns.forEach(btn => {
      btn.addEventListener('click', () => {
        tabBtns.forEach(b => b.classList.remove('active'));
        tabPanes.forEach(p => p.classList.remove('active'));
        btn.classList.add('active');
        activeTab = btn.dataset.tab;
        try { localStorage.setItem('agyp_active_tab', activeTab); } catch(e) {}
        const targetPane = document.getElementById('pane' + capitalize(activeTab));
        if (targetPane) targetPane.classList.add('active');
        syncLeftPeriodBar(activeTab);

        // Always scroll left pane to top on tab switch
        const leftPane = document.querySelector('.left-pane-scroll');
        if (leftPane) leftPane.scrollTop = 0;

        // Lazy load tab data
        // Relay timeline feeds both the overview (quota curve + account cards) and the usage tab
        if (activeTab === 'overview' || activeTab === 'usage') loadRelay();
        // The trend chart sizes its SVG to the pane width; while this tab was hidden
        // clientWidth was 0, so redraw once it becomes visible.
        if (activeTab === 'usage') {
          if (activeChartView === 'tokens' && cachedDashboard) {
            renderDailyChart(cachedDashboard.dailyTrends || []);
          } else if (activeChartView === 'quota' && cachedRelayData && cachedRelayData.weeklyQuota) {
            renderWeeklyQuota(cachedRelayData.weeklyQuota);
          }
        }
      });
    });

    window.switchTab = function(tabName) {
      const targetBtn = Array.from(tabBtns).find(b => b.dataset.tab === tabName);
      if (targetBtn) targetBtn.click();
    };

    // Usage analysis: daily (Top5 per day) vs weekly (Top10 tasks over 7 days)
    const usageRangeBtns = document.querySelectorAll('.usage-range-toggle .usage-range-btn');
    usageRangeBtns.forEach(btn => {
      btn.addEventListener('click', () => {
        const range = btn.dataset.range;
        if (!range || range === activeUsageRange) return;
        activeUsageRange = range;
        usageRangeBtns.forEach(b => b.classList.toggle('active', b === btn));
        if (cachedRelayData && cachedRelayData.dailyTopSessions) {
          renderDailyTopSessions(cachedRelayData.dailyTopSessions);
        }
      });
    });

    // Main chart: one chart, two views (tokens/cost vs weekly quota %)
    const chartViewBtns = document.querySelectorAll('.chart-view-toggle .chart-view-btn');
    function applyChartView(view, shouldRender) {
      const next = view === 'quota' ? 'quota' : 'tokens';
      activeChartView = next;
      try { localStorage.setItem('agyp_chart_view', next); } catch (e) {}
      chartViewBtns.forEach(b => b.classList.toggle('active', b.dataset.view === next));
      const tokenPane = document.getElementById('chartViewTokens');
      const quotaPane = document.getElementById('chartViewQuota');
      if (tokenPane) tokenPane.classList.toggle('active', next === 'tokens');
      if (quotaPane) quotaPane.classList.toggle('active', next === 'quota');
      if (shouldRender) {
        if (next === 'tokens' && cachedDashboard) {
          renderDailyChart(cachedDashboard.dailyTrends || []);
        } else if (next === 'quota' && cachedRelayData && cachedRelayData.weeklyQuota) {
          renderWeeklyQuota(cachedRelayData.weeklyQuota);
        }
      }
    }
    chartViewBtns.forEach(btn => {
      btn.addEventListener('click', () => applyChartView(btn.dataset.view, true));
    });
    applyChartView(activeChartView, false);

    // Open the consolidated overview on startup
    try {
      localStorage.setItem('agyp_active_tab', 'overview');
      const targetBtn = Array.from(tabBtns).find(b => b.dataset.tab === 'overview');
      if (targetBtn) targetBtn.click();
    } catch(e) {}

    // Gateway Daemon Controls (restart / stop / start, all in the status pill)
    setupGatewayControls();
    setupCycleControls();

    // Export CSV
    if (exportBtn) {
      exportBtn.addEventListener('click', async () => {
        try {
          const csv = await callMyGo('GatewayService.ExportCSV', currentDays);
          downloadFile(`agyp-stats-${new Date().toISOString().slice(0, 10)}.csv`, csv);
        } catch (err) {
          showToast('导出失败: ' + (err.message || err));
        }
      });
    }

    // Notification Test
    if (notifTestBtn) {
      notifTestBtn.addEventListener('click', async () => {
        try {
          await callMyGo('GatewayService.SendNotification', 'agyp 网关通知', '桌面通知服务测试成功！账号配额不足 20% 或 429 容灾时将即时告警。');
        } catch (err) {
          showToast('发送通知失败: ' + (err.message || err));
        }
      });
    }


    // Inspect Drawer events
    if (drawerCloseBtn) {
      drawerCloseBtn.addEventListener('click', closeInspectDrawer);
    }
    if (inspectOverlay) {
      inspectOverlay.addEventListener('click', (e) => {
        if (e.target === inspectOverlay) closeInspectDrawer();
      });
    }

    // Refresh Click
    if (refreshBtn) {
      refreshBtn.addEventListener('click', () => {
        loadAll();
      });
    }

    // Stream Log Controls
    if (logLimitSelect) {
      logLimitSelect.addEventListener('change', (e) => {
        currentLogLimit = parseInt(e.target.value, 10) || 100;
        pollRealtime(true);
      });
    }
    if (logPauseIcon) logPauseIcon.innerHTML = ICONS.pause;
    if (logPauseToggleBtn) {
      logPauseToggleBtn.addEventListener('click', () => {
        isStreamPaused = !isStreamPaused;
        if (isStreamPaused) {
          logPauseToggleBtn.classList.add('active');
          if (logPauseIcon) logPauseIcon.innerHTML = ICONS.play;
          if (logPauseText) logPauseText.textContent = '已暂停';
        } else {
          logPauseToggleBtn.classList.remove('active');
          if (logPauseIcon) logPauseIcon.innerHTML = ICONS.pause;
          if (logPauseText) logPauseText.textContent = '滚动中';
          pollRealtime(true);
        }
      });
    }

    // Quick stream filter pills (全部 / 仅对话 / 仅推理 / 仅异常) + collapsible advanced bar
    document.querySelectorAll('#streamQuickFilters .quick-pill-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const mode = btn.dataset.quick;
        if (mode === 'chat') {
          logFilter.scope = 'CHAT';
          logFilter.path = 'ALL';
          logFilter.status = 'ALL';
        } else if (mode === 'gen') {
          logFilter.scope = 'ALL';
          logFilter.path = 'streamGenerateContent';
          logFilter.status = 'ALL';
        } else if (mode === 'err') {
          logFilter.scope = 'ALL';
          logFilter.path = 'ALL';
          logFilter.status = 'ERR';
        } else {
          window.resetLogFilters();
          return;
        }
        if (logFilterPath) logFilterPath.value = logFilter.path;
        if (logFilterStatus) logFilterStatus.value = logFilter.status;
        renderRecentLogs(cachedLogs);
      });
    });

    const moreFiltersBtn = document.getElementById('btnToggleMoreFilters');
    const advFiltersBox = document.getElementById('streamAdvancedFilters');
    if (moreFiltersBtn && advFiltersBox) {
      moreFiltersBtn.addEventListener('click', () => {
        const open = advFiltersBox.style.display === 'none' || advFiltersBox.style.display === '';
        advFiltersBox.style.display = open ? 'flex' : 'none';
        moreFiltersBtn.classList.toggle('is-open', open);
      });
    }
    if (logExpandToggleBtn) {
      logExpandToggleBtn.addEventListener('click', () => {
        isStreamExpanded = !isStreamExpanded;
        if (isStreamExpanded) {
          logExpandToggleBtn.classList.add('active');
          if (streamTableContainer) streamTableContainer.classList.add('expanded');
          if (logExpandIcon) logExpandIcon.textContent = '🗗';
          if (logExpandText) logExpandText.textContent = '还原高度';
        } else {
          logExpandToggleBtn.classList.remove('active');
          if (streamTableContainer) streamTableContainer.classList.remove('expanded');
          if (logExpandIcon) logExpandIcon.textContent = '⛶';
          if (logExpandText) logExpandText.textContent = '展开面板';
        }
      });
    }
    if (logExportCurrentBtn) {
      logExportCurrentBtn.addEventListener('click', () => {
        window.exportCurrentLogs();
      });
    }

    // Stream Log Filters
    if (logFilterSession) {
      logFilterSession.addEventListener('change', (e) => {
        logFilter.session = e.target.value;
        renderRecentLogs(cachedLogs);
      });
    }
    if (logFilterModel) {
      logFilterModel.addEventListener('change', (e) => {
        logFilter.model = e.target.value;
        renderRecentLogs(cachedLogs);
      });
    }
    if (logFilterPath) {
      logFilterPath.addEventListener('change', (e) => {
        logFilter.path = e.target.value;
        renderRecentLogs(cachedLogs);
      });
    }
    if (logFilterProfile) {
      logFilterProfile.addEventListener('change', (e) => {
        logFilter.profile = e.target.value;
        renderRecentLogs(cachedLogs);
      });
    }
    if (logFilterStatus) {
      logFilterStatus.addEventListener('change', (e) => {
        logFilter.status = e.target.value;
        renderRecentLogs(cachedLogs);
      });
    }
    if (logSearchInput) {
      logSearchInput.addEventListener('input', (e) => {
        logFilter.keyword = e.target.value.trim().toLowerCase();
        renderRecentLogs(cachedLogs);
      });
    }
    if (logResetFiltersBtn) {
      logResetFiltersBtn.addEventListener('click', () => {
        window.resetLogFilters();
      });
    }
  }

  function setupGatewayControls() {
    const pill = document.getElementById('gatewayPill');
    const wrap = document.getElementById('gatewayStatusWrap');
    const menu = document.getElementById('gatewayMenu');

    const menuRestartNormal = document.getElementById('menuRestartNormal');
    const menuRestartForce = document.getElementById('menuRestartForce');
    const menuStopDaemon = document.getElementById('menuStopDaemon');
    const menuStartDaemon = document.getElementById('menuStartDaemon');
    const menuExportCsv = document.getElementById('menuExportCsv');

    let isBusy = false;

    const setBusy = (busy, text = '处理中...') => {
      isBusy = busy;
      if (pill) {
        pill.disabled = busy;
        pill.classList.toggle('is-busy', busy);
        pill.title = busy ? text : '网关操作';
      }
    };

    const closeMenu = () => {
      if (menu) menu.style.display = 'none';
      if (pill) {
        pill.classList.remove('is-open');
        pill.setAttribute('aria-expanded', 'false');
      }
    };

    if (pill && menu) {
      pill.addEventListener('click', (e) => {
        e.stopPropagation();
        if (isBusy) return;
        const isOpen = menu.style.display !== 'none' && menu.style.display !== '';
        if (isOpen) {
          closeMenu();
        } else {
          menu.style.display = 'flex';
          pill.classList.add('is-open');
          pill.setAttribute('aria-expanded', 'true');
        }
      });
      document.addEventListener('click', (e) => {
        if (wrap && !wrap.contains(e.target)) closeMenu();
      });
      document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
          closeMenu();
          if (dialogResolver) closeBuiltinDialog(dialogMode === 'prompt' ? null : false);
        }
      });
    }

    const doRestart = async (force = false) => {
      if (isBusy) return;
      closeMenu();
      setBusy(true, force ? '强制重启中...' : '平滑重启中...');
      try {
        const res = await callMyGo('GatewayService.RestartGateway', force);
        if (!res) {
          showToast('无法执行网关重启 (IPC 无响应)');
          return;
        }
        if (res.blocked) {
          const proceed = await showConfirmDialog({
            title: '检测到在途请求',
            message: res.message + '\n\n是否立即执行强制重启？',
            confirmText: '强制重启',
            danger: true,
          });
          if (proceed) {
            setBusy(false);
            await doRestart(true);
          }
        } else if (!res.success) {
          showToast('重启网关失败: ' + (res.message || res.error || '未知错误'));
        } else {
          showToast('网关已重启');
          setTimeout(() => {
            loadAll();
          }, 1200);
        }
      } catch (e) {
        showToast('重启网关出错: ' + (e.message || e));
      } finally {
        setBusy(false);
      }
    };

    if (menuRestartNormal) {
      menuRestartNormal.addEventListener('click', () => doRestart(false));
    }
    if (menuRestartForce) {
      menuRestartForce.addEventListener('click', async () => {
        closeMenu();
        const ok = await showConfirmDialog({
          title: '确认强制重启网关',
          message: '如果有正在生成的流式请求将会被打断，确认继续吗？',
          confirmText: '强制重启',
          danger: true,
        });
        if (ok) doRestart(true);
      });
    }
    if (menuStopDaemon) {
      menuStopDaemon.addEventListener('click', async () => {
        closeMenu();
        const ok = await showConfirmDialog({
          title: '停止网关守护进程',
          message: '停止后本地代理端口 (127.0.0.1:8783) 将暂停服务，确认停止吗？',
          confirmText: '停止服务',
          danger: true,
        });
        if (!ok) return;
        setBusy(true, '停止中...');
        try {
          const res = await callMyGo('GatewayService.StopGateway');
          if (res && res.success) {
            showToast('网关守护进程已停止');
            setTimeout(loadAll, 1000);
          } else {
            showToast('停止网关失败: ' + (res?.message || res?.error || '未知错误'));
          }
        } catch (e) {
          showToast('操作出错: ' + (e.message || e));
        } finally {
          setBusy(false);
        }
      });
    }
    if (menuStartDaemon) {
      menuStartDaemon.addEventListener('click', async () => {
        closeMenu();
        setBusy(true, '启动中...');
        try {
          const res = await callMyGo('GatewayService.StartGateway');
          if (res && res.success) {
            showToast('网关守护进程已启动');
            setTimeout(loadAll, 1200);
          } else {
            showToast('启动网关失败: ' + (res?.message || res?.error || '未知错误'));
          }
        } catch (e) {
          showToast('操作出错: ' + (e.message || e));
        } finally {
          setBusy(false);
        }
      });
    }
    if (menuExportCsv) {
      menuExportCsv.addEventListener('click', async () => {
        closeMenu();
        try {
          const csv = await callMyGo('GatewayService.ExportCSV', currentDays);
          if (csv) {
            downloadFile(`agyp-stats-${new Date().toISOString().slice(0, 10)}.csv`, csv);
            showToast('已导出当前周期 CSV 统计');
          }
        } catch (err) {
          showToast('导出失败: ' + (err.message || err));
        }
      });
    }
  }

  function showToast(msg) {
    let toast = document.getElementById('agypToast');
    if (!toast) {
      toast = document.createElement('div');
      toast.id = 'agypToast';
      toast.className = 'agyp-toast';
      document.body.appendChild(toast);
    }
    toast.textContent = msg;
    toast.classList.add('show');
    clearTimeout(toast._timer);
    toast._timer = setTimeout(() => {
      toast.classList.remove('show');
    }, 2800);
  }

  let isResettingCycle = false;

  function setupCycleControls() {
    document.addEventListener('click', async (e) => {
      const btn = e.target.closest('#btnCycleReset');
      if (!btn) return;
      e.preventDefault();
      if (isResettingCycle) return;

      const ok = await showConfirmDialog({
        title: '重置周配额周期',
        message: '确认将本周周期的起点重置为今天吗？\n（将以今日为第 1 天重新累计本周消耗并刷新最新配额）',
        confirmText: '确认重置',
      });
      if (!ok) return;

      isResettingCycle = true;
      const origText = btn.textContent;
      btn.classList.add('is-resetting');
      btn.textContent = '重置中...';

      try {
        await callMyGo('GatewayService.ResetWeeklyCycle');
        showToast('已将周配额周期起点重置为今日');
        await Promise.all([
          loadRelay(),
          loadDashboard()
        ]);
      } catch (err) {
        console.error('ResetWeeklyCycle failed:', err);
        showToast('重置周期失败: ' + (err.message || err));
      } finally {
        isResettingCycle = false;
        btn.classList.remove('is-resetting');
        btn.textContent = origText;
      }
    });
  }

  // Safe MyGo IPC wrapper
  async function callMyGo(method, ...args) {
    if (window.mygo && typeof window.mygo.call === 'function') {
      return await window.mygo.call(method, ...args);
    }
    console.warn('MyGo IPC not available for:', method);
    return null;
  }

  async function loadAll() {
    await Promise.all([loadDashboard(), pollRealtime(true), loadRelay()]);
    document.getElementById('lastUpdatedTime').textContent = new Date().toLocaleTimeString();
  }

  async function loadDashboard() {
    try {
      const data = await callMyGo('GatewayService.GetStats', currentDays);
      if (data) {
        cachedDashboard = data;
        renderDashboard(data);
      }
    } catch (err) {
      console.error('loadDashboard failed:', err);
    }
  }

  async function pollRealtime(forceLogs = false) {
    try {
      const promises = [callMyGo('GatewayService.GetLiveDiag')];
      if (!isStreamPaused || forceLogs) {
        promises.push(callMyGo('GatewayService.GetRecentLogs', currentLogLimit));
      }
      const results = await Promise.all(promises);
      const diag = results[0];
      const logs = results[1];

      if (diag) {
        cachedDiag = diag;
        renderLiveDiag(diag);
      }
      if (logs) {
        cachedLogs = logs;
        renderRecentLogs(logs);
      }
    } catch (err) {
      console.error('pollRealtime failed:', err);
    }
  }

  async function loadRelay() {
    try {
      console.log('[agyp] Requesting GatewayService.GetRelayTimeline...');
      const relay = await callMyGo('GatewayService.GetRelayTimeline');
      if (!relay) {
        console.warn('[agyp] GatewayService.GetRelayTimeline returned null/empty');
        showRelayError('未获取到接力配额数据（网关数据尚未就绪）');
        return;
      }
      renderRelay(relay);
    } catch (err) {
      console.error('[agyp] loadRelay failed:', err);
      showRelayError('加载配额数据失败: ' + (err.message || String(err)));
    }
  }

  function showRelayError(msg) {
    const box1 = document.getElementById('weeklyCurveBox');
    const box2 = document.getElementById('relaySessionsBox');
    const summary = document.getElementById('weeklySummary');
    if (summary) summary.innerHTML = '<div class="ws-empty">周配额数据未就绪</div>';
    if (box1) {
      box1.innerHTML = `
        <div class="ws-empty">
          <div style="font-weight:700; font-size:14px; margin-bottom:6px;">${escapeHTML(msg)}</div>
          <button class="btn btn-primary" onclick="loadRelay()">🔄 重试</button>
        </div>
      `;
    }
    if (box2) {
      box2.innerHTML = `<div class="ws-empty">会话数据未就绪</div>`;
    }
  }

  function renderRelay(relay) {
    if (!relay) return;
    cachedRelayData = relay;

    // 1. Weekly Quota Analysis & Curve Chart
    const weeklyBox = document.getElementById('weeklyCurveBox');
    if (relay.weeklyQuota) {
      try {
        renderWeeklyQuota(relay.weeklyQuota);
      } catch (e) {
        console.error('[agyp] renderWeeklyQuota error:', e);
        if (weeklyBox) {
          weeklyBox.innerHTML = `
            <div style="padding:30px 20px; text-align:center; color:#ef4444;">
              <p style="font-weight:700; margin-bottom:6px;">周配额图表渲染失败: ${escapeHTML(e.message || String(e))}</p>
              <button class="btn btn-sm" onclick="loadRelay()">重试</button>
            </div>
          `;
        }
      }
    } else if (weeklyBox) {
      weeklyBox.innerHTML = `<div style="padding:30px 20px; text-align:center; color:#94a3b8;">暂无本周配额走势数据</div>`;
    }

    // 2. Daily Session Leaderboard (Top 5)
    const sessionsBox = document.getElementById('relaySessionsBox');
    if (relay.dailyTopSessions) {
      try {
        renderDailyTopSessions(relay.dailyTopSessions);
      } catch (e) {
        console.error('[agyp] renderDailyTopSessions error:', e);
        if (sessionsBox) {
          sessionsBox.innerHTML = `
            <div style="padding:24px; text-align:center; color:#ef4444;">
              <p style="font-weight:700; margin-bottom:6px;">会话用量排行渲染失败: ${escapeHTML(e.message || String(e))}</p>
              <button class="btn btn-sm" onclick="loadRelay()">重试</button>
            </div>
          `;
        }
      }
    } else if (sessionsBox) {
      sessionsBox.innerHTML = `<div class="ws-empty">暂无会话用量记录</div>`;
    }

    // Account cards merge weekly usage from this timeline; re-render once it arrives
    if (cachedDashboard && cachedDashboard.profiles) {
      renderProfiles(cachedDashboard.profiles, cachedDashboard.activeProfile);
    }
  }

  function renderWeeklyQuota(weekly) {
    const box = document.getElementById('weeklyCurveBox');
    const summaryBox = document.getElementById('weeklySummary');
    if (!box || !weekly) return;

    const accounts = weekly.accounts || [];
    const burnList = weekly.dailyTrend || weekly.dailyBurnRate || [];

    // Calculate fleet-wide used & remaining percentage (fleet pool is 5 accounts = 500%)
    const numAccs = Math.max(1, accounts.length);
    const fleetUsedCapPct = weekly.totalUsedPct != null ? (weekly.totalUsedPct / numAccs) : (weekly.totalWeeklyUsedCapPct || 0);

    let totalUsedTokens = weekly.totalWeeklyUsedTokens || 0;
    if (!totalUsedTokens && accounts.length > 0) {
      totalUsedTokens = accounts.reduce((sum, a) => sum + (a.weekTokens || a.usedTokens || 0), 0);
    }
    const maxTokensApprox = weekly.totalWeeklyMaxTokens || (numAccs * 1000000000);
    const resetTimeStr = escapeHTML(weekly.resetWeeklyStr || weekly.nextResetTime || '-');

    const cycleLabel = escapeHTML(weekly.currentCycleLabel || '本周周期');
    const fleetLevel = fleetUsedCapPct >= 85 ? 'danger' : (fleetUsedCapPct >= 60 ? 'warn' : 'ok');
    const barPct = fleetUsedCapPct > 0 ? Math.max(2, Math.min(100, fleetUsedCapPct)) : 0;

    if (summaryBox) {
      summaryBox.innerHTML = `
        <div class="wf-top-row">
          <div class="wf-cycle-meta">
            <span class="wf-cycle">${cycleLabel}</span>
            <span class="wf-sep">·</span>
            <span class="wf-accounts">${numAccs} 个账号累计</span>
          </div>
          <div class="wf-reset-meta">
            <span class="wf-reset-time" title="周配额自动重置时间">${resetTimeStr}</span>
            <button type="button" class="cycle-reset-btn" id="btnCycleReset" title="服务端提前重置时，点击可将本周周期起点立即重置为今天并刷新配额">重置</button>
          </div>
        </div>
        <div class="wf-metrics-row">
          <span class="wf-metric">周已用 <strong>${formatTokens(totalUsedTokens)}</strong></span>
          <span class="wf-divider"></span>
          <span class="wf-metric">全队水位 <strong class="wf-pct ${fleetLevel}">${fleetUsedCapPct.toFixed(1)}%</strong></span>
          <div class="wf-bar-track" title="全队配额水位按官方配额校准：已用 ${fleetUsedCapPct.toFixed(1)}%">
            <div class="wf-bar-fill ${fleetLevel}" style="width: ${barPct}%;"></div>
          </div>
        </div>
      `;
    }

    // Precalculate running cumulative % for daily trend
    let cum = 0;
    const enrichedBurnList = burnList.map(pt => {
      const burnPct = pt.fleetBurnPct != null ? pt.fleetBurnPct : (pt.burnCapPct || 0);
      cum += burnPct;
      return {
        ...pt,
        burnCapPct: burnPct,
        cumCapPct: pt.cumCapPct != null ? pt.cumCapPct : cum,
        totalTokens: pt.fleetTokens != null ? pt.fleetTokens : (pt.totalTokens || 0),
        accountTokens: pt.profileTokens || pt.accountTokens || {}
      };
    });

    // One quota curve + interactive per-account focus & daily stacked bars
    const rawAccNames = accounts.length > 0
      ? accounts.map(a => a.name).filter(Boolean)
      : Array.from(new Set(enrichedBurnList.flatMap(pt => Object.keys(pt.byProfile || pt.accountTokens || {}))));
    const accCumByDay = {};
    const accTotals = {};
    const accInfoMap = {};
    accounts.forEach(a => { if (a.name) accInfoMap[a.name] = a; });

    rawAccNames.forEach(acc => {
      let running = 0;
      accCumByDay[acc] = enrichedBurnList.map(pt => {
        if (pt.isFuture) return null;
        running += (pt.byProfile && pt.byProfile[acc]) || 0;
        return Math.round(running * 10) / 10;
      });
      const fallbackPct = (accInfoMap[acc] && accInfoMap[acc].usedPct) || 0;
      accTotals[acc] = running > 0 ? Math.round(running * 10) / 10 : fallbackPct;
    });

    const accountNames = rawAccNames.slice().sort((a, b) => (accTotals[b] || 0) - (accTotals[a] || 0));
    if (focusedQuotaAccount && !accountNames.includes(focusedQuotaAccount)) {
      focusedQuotaAccount = '';
    }

    const meta = {
      cycleLabel: weekly.currentCycleLabel || '本周周期',
      resetWeeklyStr: weekly.resetWeeklyStr || weekly.nextResetTime || '',
      fleetUsedCapPct,
      numAccs,
      accTotals,
      accCumByDay,
    };
    const boxWidth = box.clientWidth || (box.parentElement && box.parentElement.clientWidth) || 600;
    box.innerHTML = buildWeeklyCurveChartHtml(enrichedBurnList, activeCurveMode, accountNames, boxWidth, meta);

    setupCurveChartEvents(enrichedBurnList, activeCurveMode, accountNames, meta);
  }

  function buildWeeklyCurveChartHtml(burnList, mode, accountNames, containerWidth, meta) {
    if (!burnList || burnList.length === 0) {
      return `
        <div class="curve-chart-card">
          <div style="padding:20px; text-align:center; color:#94a3b8;">暂无本周配额走势数据</div>
        </div>
      `;
    }

    const isCum = mode === 'cumulative';
    const focusAcc = focusedQuotaAccount && accountNames.includes(focusedQuotaAccount) ? focusedQuotaAccount : '';
    const primaryColor = focusAcc ? getProfileColor(focusAcc) : '#2563eb';
    const accTotals = (meta && meta.accTotals) || {};
    const accCumByDay = (meta && meta.accCumByDay) || {};
    const cycleLabel = (meta && meta.cycleLabel) || '本周周期';

    const n = burnList.length;
    const activeEntries = burnList.map((p, idx) => ({ p, idx })).filter(e => !e.p.isFuture);
    const elapsedDays = activeEntries.length;
    const lastActiveIdx = elapsedDays > 0 ? activeEntries[elapsedDays - 1].idx : -1;
    const hasFuture = elapsedDays > 0 && elapsedDays < n;

    const latestFleetCum = lastActiveIdx >= 0
      ? (burnList[lastActiveIdx].cumCapPct || 0)
      : ((meta && meta.fleetUsedCapPct) || 0);
    const fleetAvgDaily = elapsedDays > 0 ? latestFleetCum / elapsedDays : 0;
    const fleetRawProj = fleetAvgDaily * n;

    const latestFocusCum = focusAcc
      ? (accTotals[focusAcc] || 0)
      : latestFleetCum;
    const focusAvgDaily = elapsedDays > 0 ? latestFocusCum / elapsedDays : 0;
    const focusRawProj = focusAvgDaily * n;

    const targetLabel = focusAcc ? escapeHTML(focusAcc) : '全队';
    const curUsed = focusAcc ? latestFocusCum : latestFleetCum;
    const curRem = Math.max(0, 100 - curUsed);
    const curAvg = focusAcc ? focusAvgDaily : fleetAvgDaily;
    const curProj = focusAcc ? focusRawProj : fleetRawProj;

    const projBadge = hasFuture && curAvg > 0
      ? ` · <span class="curve-proj-swatch" style="--proj-accent:${primaryColor};"></span>预计周耗 <strong class="${curProj >= 85 ? 'text-warn' : ''}">${curProj > 100 ? '100%+' : Math.round(curProj) + '%'}</strong>`
      : '';
    const summaryHtml = isCum
      ? `${escapeHTML(cycleLabel)} · ${targetLabel}已用 <strong>${curUsed.toFixed(1)}%</strong> · 剩余 <strong>${curRem.toFixed(1)}%</strong>${projBadge}`
      : `${escapeHTML(cycleLabel)} · ${targetLabel}累计 <strong>${curUsed.toFixed(1)}%</strong> · <span class="chart-avg-swatch" style="--avg-accent:${primaryColor};"></span>日均 <strong>${curAvg.toFixed(1)}%/天</strong> · 剩余 <strong>${curRem.toFixed(1)}%</strong>`;

    const legendHtml = `
      <div class="curve-legend-list" id="curveLegendList">
        <button type="button" class="curve-legend-pill ${!focusAcc ? 'is-selected' : ''}" data-acc="" style="--pill-accent:#2563eb;" title="聚焦查看全队配额走势">
          <span class="curve-legend-dot" style="background:#2563eb;"></span>
          <span>全队</span>
          <strong>${latestFleetCum.toFixed(1)}%</strong>
        </button>
        ${accountNames.map(acc => {
          const accColor = getProfileColor(acc);
          const isSel = focusAcc === acc;
          const isDim = !!focusAcc && !isSel;
          return `
            <button type="button" class="curve-legend-pill ${isSel ? 'is-selected' : (isDim ? 'is-dimmed' : '')}" data-acc="${escapeHTML(acc)}" style="--pill-accent:${accColor};" title="点击聚焦查看 ${escapeHTML(acc)} 配额对比">
              <span class="curve-legend-dot" style="background:${accColor};"></span>
              <span>${escapeHTML(acc)}</span>
              <strong>${(accTotals[acc] || 0).toFixed(1)}%</strong>
            </button>
          `;
        }).join('')}
      </div>
    `;

    // Match actual rendered width cleanly inside the shared inset chart canvas
    const svgW = Math.max(360, Math.round((containerWidth || 600) - 12));
    const svgH = 224;
    const padLeft = 44;
    const padRight = 22;
    const padTop = 24;
    const padBottom = 28;
    const plotW = svgW - padLeft - padRight;
    const plotH = svgH - padTop - padBottom;
    const bottomY = padTop + plotH;

    // Determine Y max across ALL actually visible data (fixes single-account clipping bug)
    let rawMax = 0;
    burnList.forEach((p, idx) => {
      if (p.isFuture) return;
      if (isCum) {
        rawMax = Math.max(rawMax, p.cumCapPct || 0);
        if (focusAcc && accCumByDay[focusAcc]) {
          rawMax = Math.max(rawMax, accCumByDay[focusAcc][idx] || 0);
        }
      } else {
        const val = focusAcc ? ((p.byProfile && p.byProfile[focusAcc]) || 0) : (p.burnCapPct || 0);
        rawMax = Math.max(rawMax, val);
      }
    });
    if (isCum && hasFuture && curAvg > 0) {
      rawMax = Math.max(rawMax, Math.min(100, curProj));
    }

    let scale;
    if (isCum && rawMax >= 85) {
      scale = { max: 100, step: 25, ticks: [0, 25, 50, 75, 100] };
    } else {
      scale = niceAxisScale(Math.max(rawMax * 1.18, isCum ? 10 : 5), 4);
      if (isCum && scale.max > 100 && rawMax <= 100) {
        scale = { max: 100, step: 25, ticks: [0, 25, 50, 75, 100] };
      }
    }
    const yMax = Math.max(1, scale.max);
    const yAt = val => bottomY - (Math.max(0, Math.min(yMax, val)) / yMax) * plotH;

    let gridLinesHtml = '';
    scale.ticks.forEach((val, idx) => {
      const y = yAt(val);
      const label = val >= 10 || Number.isInteger(val) ? `${Math.round(val)}%` : `${val.toFixed(1)}%`;
      gridLinesHtml += `
        <line x1="${padLeft}" y1="${y.toFixed(1)}" x2="${padLeft + plotW}" y2="${y.toFixed(1)}" stroke="currentColor" stroke-opacity="${idx === 0 ? 0.18 : 0.08}" stroke-dasharray="${idx === 0 ? '0' : '3,3'}" />
        <text x="${padLeft - 8}" y="${(y + 3.5).toFixed(1)}" font-size="10.5" fill="currentColor" opacity="0.55" text-anchor="end">${label}</text>
      `;
    });

    const slotW = plotW / Math.max(1, n);
    const xAt = idx => (n === 1 ? padLeft + plotW / 2 : padLeft + slotW * (idx + 0.5));

    let chartBodyHtml = '';
    let xLabelsHtml = '';
    let hoverColsHtml = '';

    if (isCum) {
      const fleetPoints = activeEntries.map(({ p, idx }) => {
        const val = p.cumCapPct || 0;
        return { x: xAt(idx), y: yAt(val), val, idx };
      });
      const focusPoints = focusAcc
        ? activeEntries.map(({ idx }) => {
            const val = (accCumByDay[focusAcc] && accCumByDay[focusAcc][idx]) || 0;
            return { x: xAt(idx), y: yAt(val), val, idx };
          })
        : fleetPoints;

      const heroPoints = focusPoints;
      const fleetPathD = getSmoothSvgPath(fleetPoints);
      const heroPathD = getSmoothSvgPath(heroPoints);
      const areaPathD = heroPoints.length > 0
        ? `${heroPathD} L ${heroPoints[heroPoints.length - 1].x.toFixed(1)},${bottomY} L ${heroPoints[0].x.toFixed(1)},${bottomY} Z`
        : '';

      let baselineHtml = '';
      if (focusAcc && fleetPathD) {
        baselineHtml = `<path d="${fleetPathD}" fill="none" stroke="#94a3b8" stroke-width="1.5" stroke-dasharray="4,3" stroke-opacity="0.72" />`;
      }

      // Projected end-of-cycle dashed trajectory along current daily burn rate
      let projHtml = '';
      if (hasFuture && curAvg > 0 && heroPoints.length > 0) {
        const lastPt = heroPoints[heroPoints.length - 1];
        if (curProj <= 100) {
          const endX = xAt(n - 1);
          const endY = yAt(curProj);
          projHtml = `
            <line x1="${lastPt.x.toFixed(1)}" y1="${lastPt.y.toFixed(1)}" x2="${endX.toFixed(1)}" y2="${endY.toFixed(1)}" stroke="${primaryColor}" stroke-width="1.6" stroke-dasharray="4,4" stroke-opacity="0.62" />
            <circle cx="${endX.toFixed(1)}" cy="${endY.toFixed(1)}" r="3.2" fill="var(--bg-hover, #ffffff)" stroke="${primaryColor}" stroke-width="1.6" stroke-opacity="0.75" />
            <text x="${Math.min(padLeft + plotW - 2, endX).toFixed(1)}" y="${Math.max(padTop + 10, endY - 8).toFixed(1)}" font-size="10" font-weight="600" fill="${primaryColor}" opacity="0.82" text-anchor="end">预计 ${Math.round(curProj)}%</text>
          `;
        } else {
          const hitIdx = Math.min(n - 1, Math.max(lastActiveIdx, lastActiveIdx + (100 - curUsed) / curAvg));
          const endX = xAt(hitIdx);
          const endY = yAt(100);
          const anchor = hitIdx > n - 1.6 ? 'end' : 'middle';
          projHtml = `
            <line x1="${lastPt.x.toFixed(1)}" y1="${lastPt.y.toFixed(1)}" x2="${endX.toFixed(1)}" y2="${endY.toFixed(1)}" stroke="#f59e0b" stroke-width="1.6" stroke-dasharray="4,4" stroke-opacity="0.78" />
            <circle cx="${endX.toFixed(1)}" cy="${endY.toFixed(1)}" r="3.2" fill="var(--bg-hover, #ffffff)" stroke="#f59e0b" stroke-width="1.8" />
            <text x="${endX.toFixed(1)}" y="${Math.max(padTop + 10, endY - 8).toFixed(1)}" font-size="10" font-weight="600" fill="#d97706" text-anchor="${anchor}">预计满额</text>
          `;
        }
      }

      // Direct value label on the latest active point so users don't need to guess or hover
      let latestLabelHtml = '';
      if (heroPoints.length > 0) {
        const lastPt = heroPoints[heroPoints.length - 1];
        const clampedX = Math.max(padLeft + 18, Math.min(padLeft + plotW - 18, lastPt.x));
        latestLabelHtml = `<text x="${clampedX.toFixed(1)}" y="${Math.max(padTop + 10, lastPt.y - 9).toFixed(1)}" font-size="10.5" font-weight="700" fill="${primaryColor}" text-anchor="middle">${lastPt.val.toFixed(1)}%</text>`;
      }

      let focusDotsHtml = '';
      heroPoints.forEach(pt => {
        focusDotsHtml += `<circle class="curve-focus-dot" data-idx="${pt.idx}" cx="${pt.x.toFixed(1)}" cy="${pt.y.toFixed(1)}" r="3.5" fill="${primaryColor}" stroke="#ffffff" stroke-width="1.8" />`;
        const hitX = (pt.x - slotW / 2).toFixed(1);
        hoverColsHtml += `<rect class="curve-hit-col" data-idx="${pt.idx}" data-x="${pt.x.toFixed(1)}" data-y="${pt.y.toFixed(1)}" data-slot-x="${hitX}" x="${hitX}" y="${padTop}" width="${slotW.toFixed(1)}" height="${plotH}" fill="transparent" />`;
      });

      chartBodyHtml = `
        ${areaPathD ? `<path d="${areaPathD}" fill="url(#curveAreaGrad)" />` : ''}
        ${baselineHtml}
        ${projHtml}
        <line id="curveCrosshair" x1="0" y1="${padTop}" x2="0" y2="${bottomY}" stroke="${primaryColor}" stroke-width="1" stroke-dasharray="3,3" opacity="0" />
        ${heroPathD ? `<path d="${heroPathD}" fill="none" stroke="${primaryColor}" stroke-width="2.5" stroke-linecap="round" />` : ''}
        ${focusDotsHtml}
        ${latestLabelHtml}
      `;
    } else {
      // Daily incremental stacked/focused rounded column bars
      const barW = Math.max(16, Math.min(36, slotW * 0.46));
      const stackOrder = accountNames.slice().reverse();
      let clipDefsHtml = '';
      let barsHtml = '';
      let barLabelsHtml = '';

      burnList.forEach((p, idx) => {
        const cx = xAt(idx);
        const bx = cx - barW / 2;
        if (p.isFuture) {
          barsHtml += `<rect x="${bx.toFixed(1)}" y="${(bottomY - 2).toFixed(1)}" width="${barW.toFixed(1)}" height="2" rx="1" fill="currentColor" fill-opacity="0.1" />`;
          return;
        }

        const dayTotalVal = focusAcc ? ((p.byProfile && p.byProfile[focusAcc]) || 0) : (p.burnCapPct || 0);
        const by = yAt(dayTotalVal);
        const bh = Math.max(0, bottomY - by);
        const barPath = roundedTopBarPath(bx, by, barW, bh, 4);

        if (dayTotalVal <= 0 || !barPath) {
          barsHtml += `<rect class="curve-daily-bar" data-idx="${idx}" x="${bx.toFixed(1)}" y="${(bottomY - 2).toFixed(1)}" width="${barW.toFixed(1)}" height="2" rx="1" fill="${primaryColor}" fill-opacity="0.25" />`;
        } else if (focusAcc) {
          barsHtml += `<path class="curve-daily-bar" data-idx="${idx}" d="${barPath}" fill="${primaryColor}" fill-opacity="0.88" />`;
        } else {
          // Build proportional stacked account segments clipped by roundedTopBarPath
          const segs = stackOrder
            .map(acc => ({ acc, val: (p.byProfile && p.byProfile[acc]) || 0 }))
            .filter(s => s.val > 0);
          const segSum = segs.reduce((s, item) => s + item.val, 0);
          if (segs.length === 0 || segSum <= 0) {
            barsHtml += `<path class="curve-daily-bar" data-idx="${idx}" d="${barPath}" fill="#2563eb" fill-opacity="0.85" />`;
          } else {
            const clipId = `qbarClip_${idx}`;
            clipDefsHtml += `<clipPath id="${clipId}"><path d="${barPath}" /></clipPath>`;
            let curBottom = bottomY;
            let segRectsHtml = '';
            segs.forEach((s, sIdx) => {
              const segH = sIdx === segs.length - 1 ? Math.max(0, curBottom - by) : (s.val / segSum) * bh;
              const segY = curBottom - segH;
              segRectsHtml += `<rect x="${bx.toFixed(1)}" y="${segY.toFixed(1)}" width="${barW.toFixed(1)}" height="${(segH + 0.6).toFixed(1)}" fill="${getProfileColor(s.acc)}" fill-opacity="0.9" />`;
              curBottom = segY;
            });
            barsHtml += `<g class="curve-daily-bar" data-idx="${idx}" clip-path="url(#${clipId})">${segRectsHtml}</g>`;
          }
        }

        if (dayTotalVal >= 0.05) {
          barLabelsHtml += `<text x="${cx.toFixed(1)}" y="${Math.max(padTop + 10, by - 6).toFixed(1)}" font-size="10" font-weight="600" fill="${primaryColor}" text-anchor="middle">${dayTotalVal.toFixed(1)}%</text>`;
        }

        const slotX = (cx - slotW / 2).toFixed(1);
        hoverColsHtml += `<rect class="curve-hit-col" data-idx="${idx}" data-x="${cx.toFixed(1)}" data-y="${by.toFixed(1)}" data-slot-x="${slotX}" x="${slotX}" y="${padTop}" width="${slotW.toFixed(1)}" height="${plotH}" fill="transparent" />`;
      });

      const avgLineHtml = elapsedDays > 1 && curAvg > 0
        ? `<line x1="${padLeft}" y1="${yAt(curAvg).toFixed(1)}" x2="${padLeft + plotW}" y2="${yAt(curAvg).toFixed(1)}" stroke="${primaryColor}" stroke-width="1.1" stroke-opacity="0.48" stroke-dasharray="4,4" />`
        : '';

      chartBodyHtml = `
        <defs>${clipDefsHtml}</defs>
        <rect id="curveHoverBand" x="0" y="${padTop}" width="${slotW.toFixed(1)}" height="${plotH}" rx="4" fill="${primaryColor}" fill-opacity="0.08" style="display:none; pointer-events:none;" />
        ${barsHtml}
        ${avgLineHtml}
        ${barLabelsHtml}
      `;
    }

    burnList.forEach((p, idx) => {
      const cx = xAt(idx);
      const shortDate = p.date && p.date.length > 5 ? p.date.slice(5) : p.date;
      const dayLabel = p.dayLabel || p.dayOfWeek || '';
      const isToday = !!p.isToday;
      xLabelsHtml += `
        <text x="${cx.toFixed(1)}" y="${bottomY + 18}" font-size="10.5" fill="${isToday ? primaryColor : 'currentColor'}" opacity="${p.isFuture ? '0.35' : (isToday ? '1' : '0.62')}" font-weight="${isToday ? '700' : '400'}" text-anchor="middle">
          ${escapeHTML(shortDate)} ${escapeHTML(dayLabel)}
        </text>
      `;
    });

    return `
      <div class="curve-chart-card">
        <div class="curve-chart-header">
          <div class="curve-mode-toggle">
            <button type="button" class="mode-btn ${isCum ? 'active' : ''}" data-mode="cumulative">累计走势</button>
            <button type="button" class="mode-btn ${!isCum ? 'active' : ''}" data-mode="daily">每日新增</button>
          </div>
          <div class="chart-total-row">${summaryHtml}</div>
        </div>

        ${legendHtml}

        <div class="curve-svg-container">
          <svg class="curve-svg" width="${svgW}" height="${svgH}" viewBox="0 0 ${svgW} ${svgH}">
            <defs>
              <linearGradient id="curveAreaGrad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="${primaryColor}" stop-opacity="0.22" />
                <stop offset="100%" stop-color="${primaryColor}" stop-opacity="0.0" />
              </linearGradient>
            </defs>
            ${gridLinesHtml}
            ${chartBodyHtml}
            ${xLabelsHtml}
            ${hoverColsHtml}
          </svg>
          <div class="curve-tooltip-box" id="curveTooltip"></div>
        </div>
      </div>
    `;
  }

  // Fritsch-Carlson monotone cubic spline: smooth without overshoot or negative dips
  function getSmoothSvgPath(points) {
    if (!points || points.length === 0) return '';
    const n = points.length;
    if (n === 1) return `M ${points[0].x.toFixed(1)},${points[0].y.toFixed(1)}`;
    if (n === 2) {
      return `M ${points[0].x.toFixed(1)},${points[0].y.toFixed(1)} L ${points[1].x.toFixed(1)},${points[1].y.toFixed(1)}`;
    }
    const dx = new Array(n - 1);
    const dy = new Array(n - 1);
    const slopes = new Array(n - 1);
    for (let i = 0; i < n - 1; i++) {
      dx[i] = Math.max(1e-6, points[i + 1].x - points[i].x);
      dy[i] = points[i + 1].y - points[i].y;
      slopes[i] = dy[i] / dx[i];
    }
    const m = new Array(n);
    m[0] = slopes[0];
    m[n - 1] = slopes[n - 2];
    for (let i = 1; i < n - 1; i++) {
      if (slopes[i - 1] * slopes[i] <= 0) {
        m[i] = 0;
      } else {
        m[i] = (slopes[i - 1] + slopes[i]) / 2;
      }
    }
    for (let i = 0; i < n - 1; i++) {
      if (Math.abs(slopes[i]) < 1e-9) {
        m[i] = 0;
        m[i + 1] = 0;
      } else {
        const a = m[i] / slopes[i];
        const b = m[i + 1] / slopes[i];
        const h = Math.hypot(a, b);
        if (h > 3) {
          const tau = 3 / h;
          m[i] = tau * a * slopes[i];
          m[i + 1] = tau * b * slopes[i];
        }
      }
    }
    let d = `M ${points[0].x.toFixed(1)},${points[0].y.toFixed(1)}`;
    for (let i = 0; i < n - 1; i++) {
      const c1x = points[i].x + dx[i] / 3;
      const c1y = points[i].y + m[i] * (dx[i] / 3);
      const c2x = points[i + 1].x - dx[i] / 3;
      const c2y = points[i + 1].y - m[i + 1] * (dx[i] / 3);
      d += ` C ${c1x.toFixed(1)},${c1y.toFixed(1)} ${c2x.toFixed(1)},${c2y.toFixed(1)} ${points[i + 1].x.toFixed(1)},${points[i + 1].y.toFixed(1)}`;
    }
    return d;
  }

  function setupCurveChartEvents(burnList, mode, accountNames, meta) {
    const modeBtns = document.querySelectorAll('.curve-mode-toggle .mode-btn');
    modeBtns.forEach(btn => {
      btn.onclick = () => {
        const nextMode = btn.dataset.mode;
        if (nextMode && nextMode !== activeCurveMode) {
          activeCurveMode = nextMode;
          try { localStorage.setItem('agyp_curve_mode', nextMode); } catch (e) {}
          if (cachedRelayData && cachedRelayData.weeklyQuota) {
            renderWeeklyQuota(cachedRelayData.weeklyQuota);
          }
        }
      };
    });

    document.querySelectorAll('#curveLegendList .curve-legend-pill').forEach(btn => {
      btn.onclick = () => {
        const acc = btn.dataset.acc || '';
        focusedQuotaAccount = (acc && focusedQuotaAccount === acc) ? '' : acc;
        if (cachedRelayData && cachedRelayData.weeklyQuota) {
          renderWeeklyQuota(cachedRelayData.weeklyQuota);
        }
      };
    });

    const isCum = mode === 'cumulative';
    const focusAcc = focusedQuotaAccount && accountNames.includes(focusedQuotaAccount) ? focusedQuotaAccount : '';
    const accCumByDay = (meta && meta.accCumByDay) || {};
    const container = document.querySelector('.curve-svg-container');
    const svg = container && container.querySelector('svg');
    const tooltip = document.getElementById('curveTooltip');
    if (!container || !svg || !tooltip) return;

    const cols = svg.querySelectorAll('.curve-hit-col');
    const dots = svg.querySelectorAll('.curve-focus-dot');
    const bars = svg.querySelectorAll('.curve-daily-bar');
    const crosshair = document.getElementById('curveCrosshair');
    const hoverBand = document.getElementById('curveHoverBand');

    const clearHover = () => {
      svg.classList.remove('has-hover');
      tooltip.style.display = 'none';
      if (crosshair) crosshair.setAttribute('opacity', '0');
      if (hoverBand) hoverBand.style.display = 'none';
      dots.forEach(d => d.classList.remove('is-active'));
      bars.forEach(b => b.classList.remove('is-active'));
    };

    cols.forEach(col => {
      col.addEventListener('mouseenter', () => {
        const idx = parseInt(col.dataset.idx, 10);
        const p = burnList && burnList[idx];
        if (!p) return;
        const px = parseFloat(col.dataset.x);
        const py = parseFloat(col.dataset.y);
        const slotX = parseFloat(col.dataset.slotX);

        svg.classList.add('has-hover');
        if (crosshair) {
          crosshair.setAttribute('x1', px);
          crosshair.setAttribute('x2', px);
          crosshair.setAttribute('opacity', '0.7');
        }
        if (hoverBand && !isNaN(slotX)) {
          hoverBand.setAttribute('x', slotX.toFixed(1));
          hoverBand.style.display = 'block';
        }
        dots.forEach(d => d.classList.toggle('is-active', parseInt(d.dataset.idx, 10) === idx));
        bars.forEach(b => b.classList.toggle('is-active', parseInt(b.dataset.idx, 10) === idx));

        const shortDate = p.date && p.date.length > 5 ? p.date.slice(5) : p.date;
        const dayLabel = p.dayLabel || p.dayOfWeek || '';
        const fleetCum = p.cumCapPct || 0;
        const fleetDay = p.burnCapPct || 0;

        let headMetricHtml = '';
        if (focusAcc) {
          const accCum = (accCumByDay[focusAcc] && accCumByDay[focusAcc][idx]) || 0;
          const accDay = (p.byProfile && p.byProfile[focusAcc]) || 0;
          const accTok = (p.accountTokens && p.accountTokens[focusAcc]) || 0;
          headMetricHtml = `
            <div class="dt-main-metric">
              <strong>${escapeHTML(focusAcc)} · ${(isCum ? accCum : accDay).toFixed(1)}%</strong>
              <span class="dt-delta ${isCum ? 'up' : 'down'}">${isCum ? '+' + accDay.toFixed(1) + '% 当日' : '累计 ' + accCum.toFixed(1) + '%'}</span>
            </div>
            <div class="dt-row" style="margin-bottom:3px;"><span>${isCum ? '全队累计 / 当日' : '全队当日 / 累计'}</span><strong>${isCum ? `${fleetCum.toFixed(1)}% (+${fleetDay.toFixed(1)}%)` : `${fleetDay.toFixed(1)}% (${fleetCum.toFixed(1)}%)`}</strong></div>
            ${accTok > 0 ? `<div class="dt-row" style="margin-bottom:2px;"><span>${escapeHTML(focusAcc)} Token</span><strong>${formatTokens(accTok)}</strong></div>` : ''}
          `;
        } else {
          headMetricHtml = `
            <div class="dt-main-metric">
              <strong>${(isCum ? fleetCum : fleetDay).toFixed(1)}%</strong>
              <span class="dt-delta ${isCum ? 'up' : 'down'}">${isCum ? '当日 +' + fleetDay.toFixed(1) + '%' : '累计 ' + fleetCum.toFixed(1) + '%'}</span>
            </div>
            <div class="dt-row" style="margin-bottom:2px;"><span>当日总量</span><strong>${formatTokens(p.totalTokens || 0)}</strong></div>
          `;
        }

        const accRows = accountNames.map(acc => {
          const cumPct = (accCumByDay[acc] && accCumByDay[acc][idx]) || 0;
          const dayPct = (p.byProfile && p.byProfile[acc]) || 0;
          const tok = (p.accountTokens && p.accountTokens[acc]) || 0;
          return { acc, cumPct, dayPct, tok };
        }).filter(r => r.cumPct > 0 || r.dayPct > 0 || r.tok > 0)
          .sort((a, b) => isCum ? (b.cumPct - a.cumPct) : (b.dayPct - a.dayPct));

        let accHtml = '';
        accRows.forEach(r => {
          const valStr = isCum
            ? `${r.cumPct.toFixed(1)}%${r.dayPct > 0 ? ` (+${r.dayPct.toFixed(1)}%)` : ''}`
            : `${r.dayPct.toFixed(1)}%${r.tok > 0 ? ` · ${formatTokens(r.tok)}` : ''}`;
          accHtml += `<div class="dt-row"><span><span class="dt-dot" style="background:${getProfileColor(r.acc)};"></span>${escapeHTML(r.acc)}</span><strong>${valStr}</strong></div>`;
        });

        tooltip.innerHTML = `
          <div class="dt-head">
            <span>${escapeHTML(shortDate)}${dayLabel ? ' · ' + escapeHTML(dayLabel) : ''}</span>
            ${p.isToday ? '<span class="dt-badge">今日</span>' : ''}
          </div>
          ${headMetricHtml}
          ${accHtml ? `<div class="dt-divider"></div><div class="dt-grid">${accHtml}</div>` : ''}
        `;
        tooltip.style.display = 'block';

        const cw = container.clientWidth || 500;
        const tw = tooltip.offsetWidth || 195;
        const th = tooltip.offsetHeight || 110;
        const preferRight = px < cw * 0.55;
        const rawLeft = preferRight ? px + 14 : px - tw - 14;
        const clampedLeft = Math.max(8, Math.min(cw - tw - 8, rawLeft));
        const clampedTop = Math.max(6, Math.min(container.clientHeight - th - 6, py - th * 0.4));
        tooltip.style.left = `${clampedLeft}px`;
        tooltip.style.top = `${clampedTop}px`;
      });
    });

    container.addEventListener('mouseleave', clearHover);
  }

  function buildWeeklySessionRanking(dailyTopSessions) {
    const map = new Map();
    let periodTotal = 0;
    const dates = [];

    dailyTopSessions.forEach(day => {
      periodTotal += (day.totalTokens || 0);
      if (day.date) dates.push(day.date);
      (day.sessions || day.topSessions || []).forEach(s => {
        const key = s.sessionUuid || s.sessionId || s.title || 'unknown';
        const cur = map.get(key) || {
          sessionId: s.sessionId || '',
          title: s.title || key,
          totalTokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          requests: 0,
          topProfile: s.topProfile || '-',
          topModel: s.topModel || '-',
          dayCount: 0
        };
        cur.totalTokens += (s.totalTokens != null ? s.totalTokens : (s.tokens || 0));
        cur.inputTokens += (s.inputTokens != null ? s.inputTokens : (s.promptTokens || 0));
        cur.outputTokens += (s.outputTokens != null ? s.outputTokens : (s.compTokens || 0));
        cur.requests += (s.requests != null ? s.requests : (s.reqCount || 0));
        cur.dayCount += 1;
        map.set(key, cur);
      });
    });

    const list = Array.from(map.values()).sort((a, b) => b.totalTokens - a.totalTokens).slice(0, 10);
    const total = periodTotal > 0 ? periodTotal : list.reduce((sum, x) => sum + x.totalTokens, 0);
    dates.sort();
    const rangeText = dates.length > 1
      ? `${dates[0].slice(5)} ~ ${dates[dates.length - 1].slice(5)}`
      : (dates[0] || '近 7 天');
    return { list, total, rangeText };
  }

  function sessionCardHtml(sess, rank, pct, dayCount) {
    const color = getProfileColor(sess.topProfile);
    const titleStr = sess.title || ('会话 ' + (sess.sessionId || '').slice(0, 12));
    const tok = sess.totalTokens != null ? sess.totalTokens : (sess.tokens || 0);
    const inTok = sess.inputTokens != null ? sess.inputTokens : (sess.promptTokens || 0);
    const outTok = sess.outputTokens != null ? sess.outputTokens : (sess.compTokens || 0);
    const reqs = sess.requests != null ? sess.requests : (sess.reqCount || 0);
    const sessId = sess.sessionId || '';
    if (sessId && sess.title && !sessionNameById[sessId]) {
      sessionNameById[sessId] = sess.title;
    }

    return `
      <div class="top-session-card rank-${rank}" onclick="filterBySession('${escapeHTML(sessId)}')" title="点击在右侧实时流水中定位此会话">
        <div class="sess-row-top">
          <div class="sess-title-wrap">
            <span class="sess-rank-badge rank-${rank}">${rank}</span>
            <span class="sess-title-text" title="${escapeHTML(titleStr)}">${escapeHTML(titleStr)}</span>
          </div>
          <div class="sess-total-wrap">
            <strong class="sess-total-val">${formatTokens(tok)}</strong>
            <span class="sess-share-pct">${pct.toFixed(1)}%</span>
            <button type="button" class="sess-inspect-btn" onclick="event.stopPropagation(); window.openSessionInspector('${escapeHTML(sessId)}')" title="打开所属会话透视（上下文 / 耗时 / 子智能体）">透视</button>
            <svg class="sess-locate-icon" viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6"/></svg>
          </div>
        </div>

        <div class="sess-bar-track">
          <div class="sess-bar-fill" style="width: ${Math.min(100, Math.max(2, pct))}%;"></div>
        </div>

        <div class="sess-meta-row">
          <div class="sess-meta-chips">
            <span class="sess-profile-chip">
              <span class="sess-acc-dot" style="background:${color};"></span>
              <span>${escapeHTML(sess.topProfile || '-')}</span>
            </span>
            <span class="sess-model-chip" title="${escapeHTML(sess.topModel || '-')}">${escapeHTML(sess.topModel || '-')}</span>
            ${dayCount > 1 ? `<span class="sess-day-chip">${dayCount} 天</span>` : ''}
          </div>
          <div class="sess-meta-stats">
            ${reqs} 次 · 入 ${formatTokens(inTok)} / 出 ${formatTokens(outTok)}
          </div>
        </div>
      </div>
    `;
  }

  function renderDailyTopSessions(dailyTopSessions) {
    const box = document.getElementById('relaySessionsBox');
    if (!box) return;

    if (!dailyTopSessions || dailyTopSessions.length === 0) {
      box.innerHTML = `<div class="ws-empty">暂无会话用量记录</div>`;
      return;
    }

    const isWeek = activeUsageRange === 'week';
    let headerHtml = '';
    let rows = [];

    if (isWeek) {
      const weekly = buildWeeklySessionRanking(dailyTopSessions);
      rows = weekly.list.map(s => ({
        sess: s,
        pct: weekly.total > 0 ? (s.totalTokens / weekly.total) * 100 : 0,
        dayCount: s.dayCount
      }));
      headerHtml = `
        <div class="sessions-header-row">
          <span class="section-tip">${escapeHTML(weekly.rangeText)} · 合并每日 Top5</span>
          <span class="sess-summary-meta">Top ${rows.length} · ${formatTokens(weekly.total)}</span>
        </div>
      `;
    } else {
      if (!activeSessionLeaderboardDate || !dailyTopSessions.some(d => d.date === activeSessionLeaderboardDate)) {
        activeSessionLeaderboardDate = dailyTopSessions[0].date;
      }
      const datePillsHtml = dailyTopSessions.map(d => {
        const isActive = d.date === activeSessionLeaderboardDate;
        const shortDate = d.date && d.date.length > 5 ? d.date.slice(5) : (d.date || '');
        const label = d.dayLabel === '今天' ? '今日' : (d.dayLabel === '昨天' ? '昨日' : shortDate);
        return `<button class="sess-date-btn ${isActive ? 'active' : ''}" data-date="${escapeHTML(d.date)}" title="${escapeHTML(d.date)} (${escapeHTML(d.dayLabel || '')})">${escapeHTML(label)}</button>`;
      }).join('');
      const day = dailyTopSessions.find(d => d.date === activeSessionLeaderboardDate) || dailyTopSessions[0];
      const topSessions = day.sessions || day.topSessions || [];
      rows = topSessions.map(s => ({
        sess: s,
        pct: s.pctOfDay != null ? s.pctOfDay : (s.percentOfDay || 0),
        dayCount: 0
      }));
      headerHtml = `
        <div class="sessions-header-row">
          <div class="session-date-pills">${datePillsHtml}</div>
          <span class="sess-summary-meta">共 ${formatTokens(day.totalTokens || 0)}</span>
        </div>
      `;
    }

    const cardsHtml = rows.length === 0
      ? `<div class="ws-empty">该范围暂无会话消耗记录</div>`
      : rows.map((r, idx) => sessionCardHtml(r.sess, idx + 1, r.pct, r.dayCount)).join('');

    box.innerHTML = `
      <div class="sessions-leaderboard-container">
        ${headerHtml}
        <div class="top-sessions-list">${cardsHtml}</div>
      </div>
    `;

    setupSessionLeaderboardEvents();
  }

  function setupSessionLeaderboardEvents() {
    const dateBtns = document.querySelectorAll('.session-date-pills .sess-date-btn');
    dateBtns.forEach(btn => {
      btn.addEventListener('click', () => {
        const date = btn.dataset.date;
        if (date && date !== activeSessionLeaderboardDate) {
          activeSessionLeaderboardDate = date;
          if (cachedRelayData && cachedRelayData.dailyTopSessions) {
            renderDailyTopSessions(cachedRelayData.dailyTopSessions);
          }
        }
      });
    });
  }

  async function loadRules() {
    try {
      const rules = await callMyGo('GatewayService.GetRoutingConfig');
      renderRules(rules);
    } catch (err) {
      console.error('loadRules failed:', err);
    }
  }

  async function loadWaste() {
    try {
      const waste = await callMyGo('GatewayService.GetWasteScore', currentDays);
      renderWaste(waste);
    } catch (err) {
      console.error('loadWaste failed:', err);
    }
  }

  async function loadConnect() {
    try {
      const connect = await callMyGo('GatewayService.GetConnectInfo');
      renderConnect(connect);
    } catch (err) {
      console.error('loadConnect failed:', err);
    }
  }

  function renderConnect(info) {
    const box = document.getElementById('connectBox');
    if (!box || !info) return;

    let clientsHtml = (info.clients || []).map(c => `
      <div class="client-card">
        <div class="client-header">
          <span class="client-title">${escapeHTML(c.name)}</span>
        </div>
        <div class="client-desc">${escapeHTML(c.description)}</div>
        <div class="code-box">
          <code>${escapeHTML(c.envCmd)}</code>
          <button class="btn-copy-code" onclick="window.copyCode('${escapeHTML(c.envCmd)}')">复制</button>
        </div>
        <div class="client-tip">💡 ${escapeHTML(c.configTip)}</div>
      </div>
    `).join('');

    box.innerHTML = `
      <div class="connect-hero-card">
        <div>
          <div class="connect-hero-title">🌐 多客户端与移动端接入中枢 (Connect Hub)</div>
          <div class="connect-hero-meta">
            本地端口: <strong>${info.port}</strong> · 局域网访问地址: <strong>${info.lanUrl}</strong> · 当前活动账号: <strong>${info.activeProfile}</strong>
          </div>
        </div>
        <button class="btn btn-secondary" onclick="window.copyCode('${info.lanUrl}')">复制局域网地址</button>
      </div>
      <div class="client-grid">
        ${clientsHtml}
      </div>
    `;
  }

  // Render Functions
  function renderDashboard(data) {
    const s = data.summary;

    const subtitle = document.getElementById('periodSubtitle');
    if (subtitle) {
      const pausedCnt = (data.pausedProfiles || []).length;
      const enabledCnt = (data.enabledProfiles || []).length;
      const lockSuffix = (pausedCnt > 0 && enabledCnt === 1)
        ? ' · 单账号锁定'
        : (pausedCnt > 0 ? ` · 已暂停 ${pausedCnt}` : '');
      subtitle.textContent = `活动 ${data.activeProfile || '未配置'}${lockSuffix}`;
    }

    // Compact top KPI strip: numbers only, sparklines/diffs dropped (trend chart covers the time axis)
    setText('kpiOutputVal', s.outputTokensStr || '0');
    const outCard = document.getElementById('kpiOutputVal')?.closest('.kpi-stat');
    if (outCard) {
      outCard.title = s.thoughtTokens > 0
        ? `输出 Token ${s.outputTokensStr} · 另含思考 Token ${s.thoughtTokensStr || formatTokens(s.thoughtTokens)}`
        : '所选时间段内累计生成的 Output Tokens';
    }
    setText('kpiInputVal', s.inputTokensStr || '0');
    setText('kpiReqVal', s.requestsStr || '0');
    setText('kpiSpeedVal', s.avgSpeedStr || '-');
    const speedCard = document.getElementById('kpiSpeedVal')?.closest('.kpi-stat');
    if (speedCard) {
      speedCard.title = `纯吐字均速 ${s.avgSpeedStr || '-'}（不含首字等待，含思考 Token） · 端到端均速 ${s.e2eSpeedStr || '-'}`;
    }
    setText('kpiCostVal', `$${s.costUsd.toFixed(2)}`);
    const cacheHitPct = (s.cacheHitRate != null && !isNaN(s.cacheHitRate))
      ? `${s.cacheHitRate.toFixed(1)}%`
      : '-';
    setText('kpiCacheHitVal', cacheHitPct);
    const cacheCard = document.getElementById('kpiCacheHitCard');
    const cachedStr = s.cachedTokensStr || (s.cachedTokens ? formatTokens(s.cachedTokens) : '');
    if (cacheCard && cachedStr) {
      cacheCard.title = `缓存命中率 ${cacheHitPct} (累计命中 ${cachedStr} Tokens)`;
    }

    // Render Models (with Requests count)
    renderModels(data.models || []);

    // Render Dedicated Model Error Group (Tab 2)
    renderModelErrorGroup(data.errors || [], s.failCount || 0, s.successRate || 100, data.models || []);

    // Render Profiles Pool
    renderProfiles(data.profiles || [], data.activeProfile);

    // Render Error Breakdown
    renderErrorBreakdown(data.errors || [], s.failCount || 0);

    // Render Daily Chart
    renderDailyChart(data.dailyTrends || []);
  }

  // Model breakdown table with "请求次数" column
  function renderModels(models) {
    const container = document.getElementById('modelList');
    if (!container) return;

    if (!models || models.length === 0) {
      container.innerHTML = '<div class="loading-placeholder">暂无模型明细记录</div>';
      return;
    }

    let html = '';
    models.forEach(m => {
      const widthPct = Math.max(4, Math.min(100, m.ratio));
      const reqStr = (m.requests || 0).toLocaleString() + ' 次';
      const hasErrors = (m.failCount || 0) > 0;
      const successPct = typeof m.successRate === 'number' ? m.successRate.toFixed(1) + '%' : '100%';

      let errBreakdownSummary = '';
      if (hasErrors && m.errorBreakdown && m.errorBreakdown.length > 0) {
        errBreakdownSummary = m.errorBreakdown.map(eb => `${eb.code}: ${eb.count}次`).join(', ');
      }

      html += `
        <div class="model-item-card">
          <div class="model-item-top">
            <div class="model-item-identity">
              <span class="model-color-dot"></span>
              <span class="model-name-text" title="${escapeHTML(m.model)}">${escapeHTML(m.model)}</span>
            </div>
            <div class="model-badges-group">
              ${hasErrors 
                ? `<span class="model-error-badge" onclick="window.filterByModelAndCode('${escapeHTML(m.model)}', 'ERR')" style="cursor:pointer;" title="点击联动筛选日志: ${escapeHTML(errBreakdownSummary || m.errorReason)}"><span class="status-dot err"></span>${m.failCount} 次异常${errBreakdownSummary ? ` (${errBreakdownSummary})` : ''}</span>` 
                : `<span class="model-ok-badge"><span class="status-dot ok"></span>正常</span>`
              }
              <div class="model-speed-badge" title="纯吐字均速 ${escapeHTML(m.avgSpeedStr)} · 端到端均速 ${escapeHTML(m.e2eSpeedStr || '-')}">
                ${escapeHTML(m.avgSpeedStr)}
              </div>
            </div>
          </div>

          <div class="model-progress-bar-row">
            <div class="progress-track">
              <div class="progress-bar-fill" style="width: ${widthPct}%;"></div>
            </div>
            <span class="model-pct-text">${m.ratio}%</span>
          </div>

          <div class="model-stats-row" style="grid-template-columns: repeat(4, 1fr);">
            <div class="model-stat-pill">
              <span class="stat-pill-label">请求次数</span>
              <span class="stat-pill-val">${reqStr}</span>
            </div>
            <div class="model-stat-pill">
              <span class="stat-pill-label">输出${m.thoughtTokens > 0 ? ' / 思考' : ''} Token</span>
              <span class="stat-pill-val">${escapeHTML(m.outputTokensStr)}${m.thoughtTokens > 0 ? ` <small style="color:var(--text-light);font-weight:500;">+${formatTokens(m.thoughtTokens)}</small>` : ''}</span>
            </div>
            <div class="model-stat-pill">
              <span class="stat-pill-label">成功率 / 异常</span>
              <span class="stat-pill-val ${hasErrors ? 'val-danger' : 'val-green'}">${successPct} (${m.failCount || 0} 败)</span>
            </div>
            <div class="model-stat-pill">
              <span class="stat-pill-label">等效费用</span>
              <span class="stat-pill-val">${escapeHTML(m.costUsdStr)}</span>
            </div>
          </div>

          ${hasErrors && m.errorReason ? `
            <div class="model-error-footer">
              <span class="error-footer-label">异常原因:</span>
              <span class="error-footer-msg" title="${escapeHTML(m.errorReason)}">${escapeHTML(m.errorReason)}</span>
            </div>
          ` : ''}
        </div>
      `;
    });
    container.innerHTML = html;
  }

  function formatTimeCountdown(targetIso, displayStr) {
    const iso = targetIso || (displayStr && displayStr.includes('T') ? displayStr : null);
    if (iso) {
      const d = new Date(iso);
      if (!isNaN(d.getTime())) {
        const diffMs = d.getTime() - Date.now();
        if (diffMs <= 0) return '已满额';
        const diffMins = Math.floor(diffMs / 60000);
        if (diffMins < 60) {
          return `${Math.max(1, diffMins)}分后`;
        }
        const diffHrs = Math.floor(diffMins / 60);
        const remMins = diffMins % 60;
        if (diffHrs < 24) {
          return remMins > 0 ? `${diffHrs}时${remMins}分` : `${diffHrs}小时`;
        }
        const diffDays = Math.floor(diffHrs / 24);
        const remHrs = diffHrs % 24;
        return remHrs > 0 ? `${diffDays}天${remHrs}时` : `${diffDays}天`;
      }
    }
    if (displayStr && displayStr !== '-') {
      const parts = displayStr.trim().split(/\s+/);
      return parts[parts.length - 1];
    }
    return '已满额';
  }

  function formatReset5h(displayStr, rawIso) {
    return formatTimeCountdown(rawIso, displayStr);
  }

  function formatResetWeekly(displayStr, rawIso) {
    return formatTimeCountdown(rawIso, displayStr);
  }

  function getResetTooltip(displayStr, rawIso, fullTime) {
    if (fullTime) return `具体到期重置时间: ${fullTime}`;
    const targetIso = rawIso || (displayStr && displayStr.includes('T') ? displayStr : null);
    if (targetIso) {
      const d = new Date(targetIso);
      if (!isNaN(d.getTime())) {
        const pad = n => String(n).padStart(2, '0');
        const days = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
        const dayStr = days[d.getDay()] || '';
        return `具体到期重置时间: ${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())} (${dayStr})`;
      }
    }
    return displayStr && displayStr !== '-' ? `到期重置时间: ${displayStr}` : '';
  }

  function parseResetWeeklyDate(rawIso, displayStr) {
    const iso = rawIso || (displayStr && displayStr.includes('T') ? displayStr : null);
    if (iso) {
      const d = new Date(iso);
      if (!isNaN(d.getTime())) {
        const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
        const weekdaysShort = ['日', '一', '二', '三', '四', '五', '六'];
        const day = d.getDate();
        const month = d.getMonth() + 1;
        const weekday = weekdays[d.getDay()] || '周三';
        const weekdayShort = weekdaysShort[d.getDay()] || '三';
        return {
          dayNum: String(day),
          monthNum: String(month),
          weekday,
          weekdayShort,
          label: `${month}月${day}日 (${weekday})`,
          shortLabel: `${month}-${day}`,
        };
      }
    }
    return {
      dayNum: '--',
      monthNum: '--',
      weekday: '待同步',
      weekdayShort: '-',
      label: '待同步',
      shortLabel: '待同步',
    };
  }

  function renderProfiles(profiles, activeProfile) {
    const container = document.getElementById('profilePoolGrid');
    if (!container) return;

    if (!profiles || profiles.length === 0) {
      const modeBadge = document.getElementById('poolModeBadge');
      const resumeAllBtn = document.getElementById('btnResumeAllProfiles');
      if (modeBadge) {
        modeBadge.className = 'pool-mode-pill';
        modeBadge.textContent = '0 账号';
      }
      if (resumeAllBtn) resumeAllBtn.style.display = 'none';
      container.innerHTML = `
        <div class="profile-empty-state">
          <p>暂无已配置的 Google 账号</p>
          <button class="btn-add-profile" onclick="openAddProfileModal()">+ 添加第一个账号</button>
        </div>
      `;
      return;
    }

    const enabledCount = profiles.filter(x => !x.paused).length;
    const pausedCount = profiles.length - enabledCount;
    const modeBadge = document.getElementById('poolModeBadge');
    const resumeAllBtn = document.getElementById('btnResumeAllProfiles');
    if (modeBadge) {
      if (pausedCount > 0 && enabledCount === 1) {
        modeBadge.className = 'pool-mode-pill locked';
        modeBadge.textContent = `单账号锁定 (${enabledCount}/${profiles.length})`;
      } else if (pausedCount > 0) {
        modeBadge.className = 'pool-mode-pill partial';
        modeBadge.textContent = `启用 ${enabledCount}/${profiles.length} · 暂停 ${pausedCount}`;
      } else {
        modeBadge.className = 'pool-mode-pill';
        modeBadge.textContent = `全池启用 ${enabledCount}/${profiles.length}`;
      }
    }
    if (resumeAllBtn) {
      resumeAllBtn.style.display = pausedCount > 0 ? 'inline-flex' : 'none';
    }

    // Weekly usage comes from the relay timeline; merge it into the same account card
    const weeklyAccounts = (cachedRelayData && cachedRelayData.weeklyQuota && cachedRelayData.weeklyQuota.accounts) || [];
    const weeklyByName = new Map(weeklyAccounts.map(a => [a.name, a]));

    let html = '';
    profiles.forEach(p => {
      const isPaused = Boolean(p.paused);
      const isAct = p.name === activeProfile && !isPaused;
      const isSoleEnabled = !isPaused && enabledCount === 1 && pausedCount > 0;
      const g5hColor = p.gemini5h < 20 ? 'danger' : (p.gemini5h < 50 ? 'warn' : 'emerald');
      const gWkColor = p.geminiWeekly < 20 ? 'danger' : (p.geminiWeekly < 50 ? 'warn' : 'emerald');

      const reset5hCountdown = formatTimeCountdown(p.reset5hRaw, p.reset5h);
      const resetWkCountdown = formatTimeCountdown(p.resetWeeklyRaw, p.resetWeekly);
      const reset5hTitle = getResetTooltip(p.reset5h, p.reset5hRaw, p.reset5hFull);
      const resetWkTitle = getResetTooltip(p.resetWeekly, p.resetWeeklyRaw, p.resetWeeklyFull);
      const wkDateInfo = parseResetWeeklyDate(p.resetWeeklyRaw, p.resetWeekly);

      const w = weeklyByName.get(p.name);
      const usedPct = w ? (w.usedPct != null ? w.usedPct : (w.weeklyUsedPct || 0)) : 0;
      const weekTok = w ? (w.weekTokens != null ? w.weekTokens : (w.usedTokens || 0)) : 0;
      const usageBadge = w
        ? `周已用 <strong>${usedPct.toFixed(1)}%</strong> · ${formatTokens(weekTok)}`
        : `今日消耗: <strong>${escapeHTML(p.todayTokensStr)}</strong>`;

      // Burn hints
      const g5hBurn = p.gemini5h >= 100
        ? '<span class="burn-full">● 配额充沛</span>'
        : `<span class="burn-used">已用 ${(100 - p.gemini5h).toFixed(0)}%</span>`;
      
      const gWkBurn = (w && w.weekTokens)
        ? `<span class="burn-used">本周已耗 ${formatTokens(w.weekTokens)}</span>`
        : (p.geminiWeekly >= 100
            ? '<span class="burn-full">● 额度满格</span>'
            : `<span class="burn-used">已用 ${(100 - p.geminiWeekly).toFixed(1)}%</span>`);

      const pNameEsc = escapeHTML(p.name);
      const tierRaw = p.tier || '';
      const tierLower = tierRaw.toLowerCase();
      const tierCls = tierLower === 'ultra' ? 'ultra' : (tierLower === 'pro' ? 'pro' : (tierLower === 'plus' ? 'plus' : 'free'));
      const tierTitle = escapeHTML(p.tierName || tierRaw);
      const tierTagHtml = tierRaw
        ? `<span class="profile-tier-tag ${tierCls}" title="订阅等级: ${tierTitle}">${escapeHTML(tierRaw)}</span>`
        : '';

      // 只在暂停或单号锁定时展示状态标签；常规活动/备用通过卡片边框与右侧按钮区清晰区分
      let statusTagHtml = '';
      if (isPaused) {
        statusTagHtml = '<span class="profile-paused-tag">已暂停</span>';
      } else if (isAct && isSoleEnabled) {
        statusTagHtml = '<span class="profile-active-tag"><span class="active-pulse-dot"></span>单号锁定</span>';
      }

      let actionBtnsHtml = '';
      if (isPaused) {
        actionBtnsHtml += `<button class="btn-resume-modern" onclick="togglePauseAccount('${pNameEsc}', false)" title="恢复 ${pNameEsc} 参与调度与容灾">恢复</button>`;
        actionBtnsHtml += `<button class="btn-switch-modern" onclick="switchAccount('${pNameEsc}')" title="自动恢复并热切换网关出口至 ${pNameEsc}"><span>切换</span></button>`;
      } else {
        if (!isSoleEnabled && profiles.length > 1) {
          actionBtnsHtml += `<button class="btn-only-modern" onclick="onlyAccount('${pNameEsc}')" title="单账号模式：只使用 ${pNameEsc} 并暂停其余账号">只用</button>`;
        }
        if (enabledCount > 1) {
          actionBtnsHtml += `<button class="btn-pause-modern" onclick="togglePauseAccount('${pNameEsc}', true)" title="暂停 ${pNameEsc}，不参与调度与容灾">暂停</button>`;
        }
        actionBtnsHtml += !isAct
          ? `<button class="btn-switch-modern" onclick="switchAccount('${pNameEsc}')" title="无感热切换网关出口至 ${pNameEsc}">
               <span>切换</span>
             </button>`
          : `<span class="profile-current-pill"><span class="check-mark">✓</span> ${isSoleEnabled ? '独占中' : '当前'}</span>`;
      }
      const emailDisplay = maskEmail(p.email);
      const emailTitle = isPrivacyMode
        ? '隐私模式已开启（点击顶栏眼睛图标查看明文）'
        : escapeHTML(p.email || '');
      const weekMeta = (w && w.weekTokens)
        ? `周已耗 ${formatTokens(w.weekTokens)}`
        : (weekTok > 0 ? `周已耗 ${formatTokens(weekTok)}` : '');

      actionBtnsHtml += `<button class="profile-icon-btn" data-profile="${pNameEsc}" onclick="renameAccount(this.dataset.profile)" title="重命名 ${pNameEsc}">${ICONS.edit}</button>`;
      actionBtnsHtml += `<button class="profile-icon-btn danger" data-profile="${pNameEsc}" onclick="removeAccount(this.dataset.profile, ${profiles.length})" title="删除 ${pNameEsc}">${ICONS.trash}</button>`;

      html += `
        <div class="profile-card ${isAct ? 'active' : ''} ${isPaused ? 'paused' : ''}" data-profile="${pNameEsc}">
          <!-- Card Header: Identity + Badges + Action -->
          <div class="profile-card-top">
            <div class="profile-identity">
              <div class="profile-title-col">
                <div class="profile-name-row">
                  <span class="profile-name-text">${pNameEsc}</span>
                  ${tierTagHtml}
                  ${statusTagHtml}
                </div>
                ${emailDisplay ? `<span class="profile-email-sub" title="${emailTitle}">${escapeHTML(emailDisplay)}</span>` : ''}
              </div>
            </div>

            <div class="profile-actions-area">
              <span class="profile-consumed-badge">${usageBadge}</span>
              ${actionBtnsHtml}
            </div>
          </div>

          <!-- Slim Dual-Bar Quota Grid (5h & Weekly) -->
          <div class="profile-gauges-grid">
            <div class="quota-bar-cell" title="${reset5hTitle}">
              <div class="quota-cell-head">
                <span class="quota-cell-label">5h 滚动</span>
                <span class="quota-cell-right">
                  <strong class="quota-cell-pct ${g5hColor}">${p.gemini5h}%</strong>
                  <span class="quota-cell-time">${reset5hCountdown}</span>
                </span>
              </div>
              <div class="battery-track">
                <div class="battery-fill ${g5hColor}" style="width: ${p.gemini5h}%;"></div>
              </div>
            </div>

            <div class="quota-bar-cell" title="${resetWkTitle}${weekMeta ? ' · ' + weekMeta : ''}">
              <div class="quota-cell-head">
                <span class="quota-cell-label">周度 (${wkDateInfo.shortLabel})</span>
                <span class="quota-cell-right">
                  <strong class="quota-cell-pct ${gWkColor}">${p.geminiWeekly}%</strong>
                  <span class="quota-cell-time">${resetWkCountdown}</span>
                </span>
              </div>
              <div class="battery-track">
                <div class="battery-fill ${gWkColor}" style="width: ${p.geminiWeekly}%;"></div>
              </div>
            </div>
          </div>
        </div>
      `;
    });
    container.innerHTML = html;
  }

  function renderErrorBreakdown(errors, failCount) {
    const grid = document.getElementById('errorItemsGrid');
    const badge = document.getElementById('errorTotalBadge');
    if (!grid) return;

    if (badge) {
      if (failCount > 0) {
        badge.className = 'error-total-badge has-error';
        badge.textContent = `${failCount} 次异常`;
      } else {
        badge.className = 'error-total-badge ok';
        badge.textContent = `0 异常 (全部正常)`;
      }
    }

    if (!errors || errors.length === 0) {
      grid.innerHTML = '<div class="error-empty-state">当前周期内未发现异常报错，系统运行健康</div>';
      return;
    }

    let html = '';
    errors.forEach(err => {
      let tagClass = 'err-tag-other';
      if (err.code === 503) {
        tagClass = 'err-tag-503';
      } else if (err.code === 429) {
        tagClass = 'err-tag-429';
      } else if (err.code === 502) {
        tagClass = 'err-tag-502';
      } else if (err.code === 403) {
        tagClass = 'err-tag-403';
      }

      const affected = err.profile ? `涉及账号: ${escapeHTML(err.profile)}` : '';
      html += `
        <div class="error-item-card" onclick="window.filterByErrorCode(${err.code})" title="点击联动筛选右侧此类型日志">
          <div class="error-item-header">
            <span class="error-badge ${tagClass}">HTTP ${err.code}</span>
            <span class="error-count-pill">${err.count} 次 (${err.percentage}%)</span>
          </div>
          <div class="error-type-name">${escapeHTML(err.type)}</div>
          <div class="error-reason-text" title="${escapeHTML(err.sampleErr || err.reason)}">
            ${escapeHTML(err.sampleErr || err.reason)}
          </div>
          ${affected ? `<div class="error-affected-row">${affected}</div>` : ''}
        </div>
      `;
    });
    grid.innerHTML = html;
  }

  window.filterByErrorCode = function(code) {
    const strCode = String(code);
    if (logFilterStatus) {
      if (['503', '429', '502'].includes(strCode)) {
        logFilter.status = strCode;
        logFilterStatus.value = strCode;
      } else {
        logFilter.status = 'ERR';
        logFilterStatus.value = 'ERR';
      }
    }
    if (logSearchInput) {
      logSearchInput.value = '';
      logFilter.keyword = '';
    }
    renderRecentLogs(cachedLogs);
  };

  window.filterByModelAndCode = function(model, code) {
    if (code) {
      const strCode = String(code);
      if (logFilterStatus) {
        if (['503', '429', '502'].includes(strCode)) {
          logFilter.status = strCode;
          logFilterStatus.value = strCode;
        } else {
          logFilter.status = 'ERR';
          logFilterStatus.value = 'ERR';
        }
      }
    }
    if (model && model !== 'ALL') {
      logFilter.model = model;
      if (logFilterModel) {
        if (!Array.from(logFilterModel.options).some(o => o.value === model)) {
          const opt = document.createElement('option');
          opt.value = model;
          opt.textContent = model;
          logFilterModel.appendChild(opt);
        }
        logFilterModel.value = model;
      }
    }
    if (logSearchInput) {
      logSearchInput.value = '';
      logFilter.keyword = '';
    }
    renderRecentLogs(cachedLogs);
  };

  function renderModelErrorGroup(errors, failCount, successRate, models) {
    const summaryBar = document.getElementById('modelErrorSummaryBar');
    const itemsGrid = document.getElementById('modelErrorItemsGrid');
    const badge = document.getElementById('modelErrorGroupBadge');
    if (!itemsGrid) return;

    if (badge) {
      if (failCount > 0) {
        badge.className = 'error-total-badge has-error';
        badge.textContent = `${failCount} 次异常`;
      } else {
        badge.className = 'error-total-badge ok';
        badge.textContent = `0 异常 (运行稳定)`;
      }
    }

    // 1. Render Summary Bar
    if (summaryBar) {
      const topError = errors && errors.length > 0 ? errors[0] : null;
      const primaryReason = topError ? `${topError.type} (${topError.count}次)` : '无故障 (100% 畅通)';

      let affectedModels = [];
      if (models && models.length > 0) {
        models.forEach(m => {
          if ((m.failCount || 0) > 0) {
            affectedModels.push(`${m.model} (${m.failCount}败)`);
          }
        });
      }
      const modelScopeText = affectedModels.length > 0 ? affectedModels.join(', ') : '全模型正常';

      summaryBar.innerHTML = `
        <div class="model-error-stat-tile">
          <div class="error-tile-label">异常总次数</div>
          <div class="error-tile-val ${failCount > 0 ? 'text-danger' : 'text-ok'}">
            ${failCount > 0 ? `${failCount} 次` : '0 次'}
          </div>
        </div>
        <div class="model-error-stat-tile">
          <div class="error-tile-label">模型整体健康度</div>
          <div class="error-tile-val ${failCount > 0 && successRate < 99 ? 'text-warn' : 'text-ok'}">
            ${typeof successRate === 'number' ? successRate.toFixed(1) + '%' : '100%'}
          </div>
        </div>
        <div class="model-error-stat-tile">
          <div class="error-tile-label">主要异常类型</div>
          <div class="error-tile-val text-truncate" title="${escapeHTML(primaryReason)}">
            ${escapeHTML(primaryReason)}
          </div>
        </div>
        <div class="model-error-stat-tile">
          <div class="error-tile-label">故障受影响模型</div>
          <div class="error-tile-val text-truncate ${affectedModels.length > 0 ? 'text-warn' : 'text-ok'}" title="${escapeHTML(modelScopeText)}">
            ${escapeHTML(modelScopeText)}
          </div>
        </div>
      `;
    }

    // 2. Render Items Grid
    if (!errors || errors.length === 0) {
      itemsGrid.innerHTML = `
        <div class="model-error-empty-box">
          <div class="error-empty-title">当前周期内模型推理 100% 正常，未发生异常报错</div>
          <div class="error-empty-desc">上游未触发 503 容量饱和、429 配额耗尽、502 连接断开或 403 鉴权拒绝，网关多账号错峰接力监控就绪。</div>
          <div class="error-empty-tags">
            <span class="status-pill ok">503 容量限制: 0</span>
            <span class="status-pill ok">429 访问限流: 0</span>
            <span class="status-pill ok">502 网络超时: 0</span>
            <span class="status-pill ok">403 权限异常: 0</span>
          </div>
        </div>
      `;
      return;
    }

    let html = '';
    errors.forEach(err => {
      let tagClass = 'err-tag-other';
      let meaning = '通用上游服务或网络链路异常';
      if (err.code === 503) {
        tagClass = 'err-tag-503';
        meaning = '上游服务模型容量饱和/无可用推理槽位，网关已尝试自动热切';
      } else if (err.code === 429) {
        tagClass = 'err-tag-429';
        meaning = '当前账号并发速率或 5 小时/周额度触顶，需等待配额回满';
      } else if (err.code === 502) {
        tagClass = 'err-tag-502';
        meaning = '网关与上游 SSL 连接握手超时或网络链路断开 (SSLEOFError / RemoteDisconnected)';
      } else if (err.code === 403) {
        tagClass = 'err-tag-403';
        meaning = 'API Key 或 OAuth 凭据失效，权限拒绝 (PERMISSION_DENIED)';
      } else if (err.code === 400) {
        tagClass = 'err-tag-400';
        meaning = '请求 Body 参数异常或单次 Prompt 超出模型上下文窗口';
      }

      const modelText = err.modelDetails || (err.affectedModels && err.affectedModels.length > 0 ? err.affectedModels.join(', ') : '通用推理链路 / 流式生成');
      const profileText = err.profile || '-';

      html += `
        <div class="model-error-card" onclick="window.filterByErrorCode(${err.code})" title="点击联动筛选右侧此类型日志">
          <div class="model-error-card-top">
            <span class="error-badge ${tagClass}">HTTP ${err.code}</span>
            <span class="model-error-count-pill">${err.count} 次 (${err.percentage}%)</span>
          </div>
          
          <div class="model-error-type-title">${escapeHTML(err.type)}</div>
          <div class="model-error-meaning">${escapeHTML(meaning)}</div>

          <div class="model-error-details-grid">
            <div class="model-error-detail-row">
              <span class="detail-label">关联模型:</span>
              <span class="detail-val" title="${escapeHTML(modelText)}">${escapeHTML(modelText)}</span>
            </div>
            <div class="model-error-detail-row">
              <span class="detail-label">关联账号:</span>
              <span class="detail-val" title="${escapeHTML(profileText)}">${escapeHTML(profileText)}</span>
            </div>
            <div class="model-error-detail-row">
              <span class="detail-label">典型特征:</span>
              <span class="detail-val text-code" title="${escapeHTML(err.sampleErr || err.reason)}">
                ${escapeHTML(err.sampleErr || err.reason)}
              </span>
            </div>
          </div>

          <div class="model-error-card-footer">
            <span class="action-hint">点击联动筛选日志流 →</span>
          </div>
        </div>
      `;
    });
    itemsGrid.innerHTML = html;
  }

  function niceAxisScale(rawMax, targetTicks = 4) {
    const safeMax = rawMax > 0 ? rawMax : 1;
    const rawStep = safeMax / Math.max(1, targetTicks);
    const exp = Math.floor(Math.log10(rawStep));
    const base = Math.pow(10, exp);
    const frac = rawStep / base;
    const niceFrac = frac <= 1 ? 1 : frac <= 1.5 ? 1.5 : frac <= 2 ? 2 : frac <= 2.5 ? 2.5 : frac <= 4 ? 4 : frac <= 5 ? 5 : frac <= 8 ? 8 : 10;
    const step = niceFrac * base;
    const count = Math.max(targetTicks, Math.ceil(safeMax / step));
    const ticks = [];
    for (let i = 0; i <= count; i++) {
      ticks.push(i * step);
    }
    return { max: step * count, step, ticks };
  }

  let chartResizeTimer = null;
  function setupChartResizeObserver() {
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => {
      clearTimeout(chartResizeTimer);
      chartResizeTimer = setTimeout(() => {
        if (activeTab !== 'usage') return;
        const tokenPane = document.getElementById('chartViewTokens');
        const quotaPane = document.getElementById('chartViewQuota');
        if (tokenPane && tokenPane.classList.contains('active') && cachedDashboard) {
          renderDailyChart(cachedDashboard.dailyTrends || []);
        } else if (quotaPane && quotaPane.classList.contains('active') && cachedRelayData && cachedRelayData.weeklyQuota) {
          renderWeeklyQuota(cachedRelayData.weeklyQuota);
        }
      }, 90);
    });
    const dailyBox = document.getElementById('dailyChartBox');
    const weeklyBox = document.getElementById('weeklyCurveBox');
    if (dailyBox) ro.observe(dailyBox);
    if (weeklyBox) ro.observe(weeklyBox);
  }

  function roundedTopBarPath(x, y, w, h, r) {
    if (h <= 0.5) return '';
    const radius = Math.min(r, w / 2, h);
    if (radius < 1) {
      return `M ${x.toFixed(1)},${y.toFixed(1)} h ${w.toFixed(1)} v ${h.toFixed(1)} h ${(-w).toFixed(1)} Z`;
    }
    const bottom = y + h;
    return [
      `M ${x.toFixed(1)},${bottom.toFixed(1)}`,
      `L ${x.toFixed(1)},${(y + radius).toFixed(1)}`,
      `Q ${x.toFixed(1)},${y.toFixed(1)} ${(x + radius).toFixed(1)},${y.toFixed(1)}`,
      `L ${(x + w - radius).toFixed(1)},${y.toFixed(1)}`,
      `Q ${(x + w).toFixed(1)},${y.toFixed(1)} ${(x + w).toFixed(1)},${(y + radius).toFixed(1)}`,
      `L ${(x + w).toFixed(1)},${bottom.toFixed(1)}`,
      'Z',
    ].join(' ');
  }

  // Daily trend: interactive column chart with metric switcher, avg benchmark & custom tooltip
  function renderDailyChart(trends) {
    const container = document.getElementById('dailyChartBox');
    if (!container) return;

    if (!trends || trends.length === 0) {
      container.innerHTML = '<div class="loading-placeholder">暂无趋势数据</div>';
      return;
    }

    const points = trends.length > 30 ? trends.slice(-30) : trends;
    const totalTokens = points.reduce((a, t) => a + (t.outputTokens || 0), 0);
    const totalCostUsd = points.reduce((a, t) => a + (t.costUsd || 0), 0);
    const totalCostCny = points.reduce((a, t) => a + (t.costCny || 0), 0);
    const totalReqs = points.reduce((a, t) => a + (t.requests || 0), 0);

    const METRICS = {
      tokens: {
        label: '输出 Token',
        accent: '#3b82f6',
        accentDark: '#2563eb',
        val: t => t.outputTokens || 0,
        fmt: v => formatTokens(Math.round(v)),
        tickFmt: v => (v === 0 ? '0' : formatTokens(v)),
        totalStr: formatTokens(totalTokens),
      },
      cost: {
        label: '等效价值',
        accent: '#10b981',
        accentDark: '#059669',
        val: t => t.costUsd || 0,
        fmt: v => '$' + (v >= 100 ? v.toFixed(0) : v.toFixed(v >= 10 ? 1 : 2)),
        tickFmt: v => (v === 0 ? '$0' : '$' + (v >= 10 || Number.isInteger(v) ? Math.round(v) : v.toFixed(1))),
        totalStr: `$${totalCostUsd.toFixed(2)}`,
      },
      requests: {
        label: '请求次数',
        accent: '#8b5cf6',
        accentDark: '#7c3aed',
        val: t => t.requests || 0,
        fmt: v => (v >= 1000 ? (v / 1000).toFixed(1) + 'k 次' : Math.round(v) + ' 次'),
        tickFmt: v => (v >= 1000 ? (v / 1000).toFixed(1) + 'k' : String(Math.round(v))),
        totalStr: `${totalReqs.toLocaleString()} 次`,
      },
    };
    const cfg = METRICS[activeDailyMetric] || METRICS.cost;

    const vals = points.map(cfg.val);
    const rawMax = Math.max(...vals, 0);
    const avgVal = vals.reduce((a, b) => a + b, 0) / Math.max(1, vals.length);
    let peakIdx = -1;
    let peakVal = -1;
    vals.forEach((v, idx) => {
      if (v > peakVal) {
        peakVal = v;
        peakIdx = idx;
      }
    });

    const measuredW = container.clientWidth || (container.parentElement && container.parentElement.clientWidth) || 600;
    const svgW = Math.max(360, Math.round(measuredW - 12));
    const svgH = 236;
    const padL = 48;
    const padR = 14;
    const padT = 26;
    const padB = 28;
    const plotW = svgW - padL - padR;
    const plotH = svgH - padT - padB;
    const bottomY = padT + plotH;

    const scale = niceAxisScale(Math.max(rawMax * 1.14, activeDailyMetric === 'cost' ? 1 : 10), 4);
    const yAt = v => bottomY - (v / scale.max) * plotH;

    // Horizontal hairline grid & clean left Y ticks
    let gridHtml = '';
    scale.ticks.forEach((tv, idx) => {
      const y = yAt(tv);
      gridHtml += `
        <line x1="${padL}" y1="${y.toFixed(1)}" x2="${padL + plotW}" y2="${y.toFixed(1)}" stroke="currentColor" stroke-opacity="${idx === 0 ? 0.18 : 0.08}" stroke-dasharray="${idx === 0 ? '0' : '3,3'}" />
        <text x="${padL - 8}" y="${(y + 3.5).toFixed(1)}" font-size="10.5" fill="currentColor" opacity="0.55" text-anchor="end">${escapeHTML(cfg.tickFmt(tv))}</text>
      `;
    });

    const n = points.length;
    const slotW = plotW / Math.max(1, n);
    const barW = Math.max(6, Math.min(28, slotW * 0.56));
    const labelStep = n > 14 ? Math.ceil(n / 9) : 1;
    const todayIso = new Date().toISOString().slice(0, 10);

    let barsHtml = '';
    let xLabelsHtml = '';
    let hitColsHtml = '';
    let peakLabelHtml = '';

    points.forEach((t, i) => {
      const val = vals[i];
      const cx = padL + slotW * (i + 0.5);
      const bx = cx - barW / 2;
      const by = yAt(val);
      const bh = Math.max(0, bottomY - by);
      const isToday = t.isToday || t.date === todayIso;
      const isPeak = i === peakIdx && peakVal > 0;

      const gradId = isToday ? 'dailyBarTodayGrad' : 'dailyBarGrad';
      const barPath = roundedTopBarPath(bx, by, barW, bh, 4);
      if (barPath) {
        barsHtml += `<path class="daily-bar ${isToday ? 'is-today' : ''} ${isPeak ? 'is-peak' : ''}" data-idx="${i}" d="${barPath}" fill="url(#${gradId})" />`;
      } else {
        barsHtml += `<rect class="daily-bar" data-idx="${i}" x="${bx.toFixed(1)}" y="${(bottomY - 2).toFixed(1)}" width="${barW.toFixed(1)}" height="2" rx="1" fill="${cfg.accent}" fill-opacity="0.25" />`;
      }

      if (isPeak) {
        const clampedX = Math.max(padL + 34, Math.min(padL + plotW - 34, cx));
        peakLabelHtml = `<text x="${clampedX.toFixed(1)}" y="${Math.max(13, by - 7).toFixed(1)}" font-size="10" font-weight="600" fill="${cfg.accentDark}" text-anchor="middle">峰值 ${escapeHTML(cfg.fmt(val))}</text>`;
      }

      const showLabel = isToday || i === 0 || i === n - 1 || (i % labelStep === 0 && (n - 1 - i) >= Math.ceil(labelStep / 2));
      if (showLabel) {
        const label = t.date ? t.date.slice(5) : '';
        xLabelsHtml += `<text x="${cx.toFixed(1)}" y="${bottomY + 18}" font-size="10.5" fill="${isToday ? cfg.accentDark : 'currentColor'}" opacity="${isToday ? 1 : 0.58}" font-weight="${isToday ? 700 : 400}" text-anchor="middle">${escapeHTML(label)}</text>`;
      }

      hitColsHtml += `<rect class="daily-hit-col" data-idx="${i}" data-cx="${cx.toFixed(1)}" data-by="${by.toFixed(1)}" data-slot-x="${(cx - slotW / 2).toFixed(1)}" x="${(cx - slotW / 2).toFixed(1)}" y="${padT}" width="${slotW.toFixed(1)}" height="${plotH}" fill="transparent" />`;
    });

    // Period average reference line (clean dashed line; value already shown in summary header)
    let avgLineHtml = '';
    if (avgVal > 0 && n > 1) {
      const avgY = yAt(avgVal);
      avgLineHtml = `<line x1="${padL}" y1="${avgY.toFixed(1)}" x2="${padL + plotW}" y2="${avgY.toFixed(1)}" stroke="${cfg.accentDark}" stroke-width="1.1" stroke-opacity="0.48" stroke-dasharray="4,4" />`;
    }

    const totalSummaryHtml = activeDailyMetric === 'cost'
      ? `近 ${n} 天 · 累计 <strong>${escapeHTML(cfg.totalStr)}</strong> <span>(≈ ￥${totalCostCny.toFixed(0)})</span> · <span class="chart-avg-swatch" style="--avg-accent:${cfg.accentDark};"></span>日均 <strong>${escapeHTML(cfg.fmt(avgVal))}</strong>`
      : `近 ${n} 天 · 累计 <strong>${escapeHTML(cfg.totalStr)}</strong> · <span class="chart-avg-swatch" style="--avg-accent:${cfg.accentDark};"></span>日均 <strong>${escapeHTML(cfg.fmt(avgVal))}</strong>`;

    container.innerHTML = `
      <div class="daily-chart-wrapper">
        <div class="chart-header-row">
          <div class="daily-metric-pills" id="dailyMetricPills">
            <button type="button" class="daily-metric-btn ${activeDailyMetric === 'cost' ? 'active' : ''}" data-metric="cost">等效价值</button>
            <button type="button" class="daily-metric-btn ${activeDailyMetric === 'tokens' ? 'active' : ''}" data-metric="tokens">输出 Token</button>
            <button type="button" class="daily-metric-btn ${activeDailyMetric === 'requests' ? 'active' : ''}" data-metric="requests">请求次数</button>
          </div>
          <div class="chart-total-row">${totalSummaryHtml}</div>
        </div>
        <div class="daily-line-chart" id="dailyChartCanvas">
          <svg class="daily-line-svg" width="${svgW}" height="${svgH}" viewBox="0 0 ${svgW} ${svgH}">
            <defs>
              <linearGradient id="dailyBarGrad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="${cfg.accent}" stop-opacity="0.88" />
                <stop offset="100%" stop-color="${cfg.accent}" stop-opacity="0.48" />
              </linearGradient>
              <linearGradient id="dailyBarTodayGrad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="${cfg.accentDark}" stop-opacity="1" />
                <stop offset="100%" stop-color="${cfg.accent}" stop-opacity="0.72" />
              </linearGradient>
            </defs>
            ${gridHtml}
            <rect id="dailyHoverBand" x="0" y="${padT}" width="${slotW.toFixed(1)}" height="${plotH}" rx="4" fill="${cfg.accent}" fill-opacity="0.08" style="display:none; pointer-events:none;" />
            ${barsHtml}
            ${avgLineHtml}
            ${peakLabelHtml}
            ${xLabelsHtml}
            ${hitColsHtml}
          </svg>
          <div class="daily-chart-tooltip" id="dailyChartTooltip"></div>
        </div>
      </div>
    `;

    setupDailyChartEvents(trends, points, vals, avgVal, peakIdx, cfg);
  }

  function setupDailyChartEvents(allTrends, points, vals, avgVal, peakIdx, cfg) {
    document.querySelectorAll('#dailyMetricPills .daily-metric-btn').forEach(btn => {
      btn.onclick = () => {
        const m = btn.dataset.metric;
        if (m && m !== activeDailyMetric) {
          activeDailyMetric = m;
          try { localStorage.setItem('agyp_daily_metric', m); } catch (e) {}
          renderDailyChart(allTrends);
        }
      };
    });

    const canvas = document.getElementById('dailyChartCanvas');
    const svg = canvas && canvas.querySelector('svg');
    const band = document.getElementById('dailyHoverBand');
    const tooltip = document.getElementById('dailyChartTooltip');
    if (!canvas || !svg || !tooltip) return;

    const bars = svg.querySelectorAll('.daily-bar');
    const todayIso = new Date().toISOString().slice(0, 10);
    const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];

    const clearHover = () => {
      svg.classList.remove('has-hover');
      bars.forEach(b => b.classList.remove('is-active'));
      if (band) band.style.display = 'none';
      tooltip.style.display = 'none';
    };

    svg.querySelectorAll('.daily-hit-col').forEach(col => {
      col.addEventListener('mouseenter', () => {
        const idx = parseInt(col.dataset.idx, 10);
        const t = points[idx];
        if (!t) return;
        const cx = parseFloat(col.dataset.cx);
        const by = parseFloat(col.dataset.by);
        const slotX = parseFloat(col.dataset.slotX);

        svg.classList.add('has-hover');
        bars.forEach(b => b.classList.toggle('is-active', parseInt(b.dataset.idx, 10) === idx));
        if (band) {
          band.setAttribute('x', slotX.toFixed(1));
          band.style.display = 'block';
        }

        const val = vals[idx] || 0;
        const isToday = t.isToday || t.date === todayIso;
        const isPeak = idx === peakIdx && val > 0;
        let wd = '';
        if (t.date) {
          const dt = new Date(t.date + 'T00:00:00');
          if (!isNaN(dt.getTime())) wd = weekdays[dt.getDay()] || '';
        }
        let deltaHtml = '';
        if (avgVal > 0) {
          const diffPct = ((val - avgVal) / avgVal) * 100;
          const cls = diffPct >= 0 ? 'up' : 'down';
          deltaHtml = `<span class="dt-delta ${cls}">${diffPct >= 0 ? '+' : ''}${diffPct.toFixed(0)}% vs 日均</span>`;
        }

        const badgeHtml = isToday
          ? '<span class="dt-badge">今日</span>'
          : (isPeak ? '<span class="dt-badge peak">峰值</span>' : '');

        tooltip.innerHTML = `
          <div class="dt-head">
            <span>${escapeHTML(t.date || '')}${wd ? ' · ' + wd : ''}</span>
            ${badgeHtml}
          </div>
          <div class="dt-main-metric">
            <strong>${escapeHTML(cfg.fmt(val))}</strong>
            ${deltaHtml}
          </div>
          <div class="dt-divider"></div>
          <div class="dt-grid">
            <div class="dt-row"><span>输出 Token</span><strong>${formatTokens(t.outputTokens || 0)}</strong></div>
            <div class="dt-row"><span>输入 Token</span><strong>${formatTokens(t.inputTokens || 0)}</strong></div>
            <div class="dt-row"><span>等效价值</span><strong>$${(t.costUsd || 0).toFixed(2)} (￥${(t.costCny || 0).toFixed(0)})</strong></div>
            <div class="dt-row"><span>请求次数</span><strong>${(t.requests || 0).toLocaleString()} 次</strong></div>
          </div>
        `;
        tooltip.style.display = 'block';

        const cw = canvas.clientWidth || 500;
        const tw = tooltip.offsetWidth || 190;
        const th = tooltip.offsetHeight || 120;
        const preferRight = cx < cw * 0.55;
        const rawLeft = preferRight ? cx + 16 : cx - tw - 16;
        const clampedLeft = Math.max(8, Math.min(cw - tw - 8, rawLeft));
        const clampedTop = Math.max(6, Math.min(canvas.clientHeight - th - 6, by - th * 0.4));
        tooltip.style.left = `${clampedLeft}px`;
        tooltip.style.top = `${clampedTop}px`;
      });
    });

    canvas.addEventListener('mouseleave', clearHover);
  }

  function renderCheckResult(res) {
    const badge = document.getElementById('checkStatusBadge');
    const latency = document.getElementById('checkLatencyText');
    const title = document.getElementById('checkSummaryTitle');
    const detail = document.getElementById('checkDetailBox');

    if (badge) {
      badge.className = `badge-status ${res.pass ? 'ok' : 'err'}`;
      badge.textContent = res.pass ? '满血通过 (Pass)' : '异常/降智';
    }
    if (latency) {
      latency.textContent = `耗时: ${res.latencyMs} ms · 账号 [${res.profile}]`;
    }
    if (title) {
      title.textContent = res.summary;
    }
    if (detail) {
      detail.textContent = res.detail || '未捕获到异常特征，模型推理链完好。';
    }
  }

  function renderRules(cfg) {
    const box = document.getElementById('rulesBox');
    if (!box || !cfg) return;

    let chainHtml = cfg.priorityChain.map((p, idx) => `
      <span class="badge-status ${p === cfg.activeProfile ? 'ok' : ''}" style="font-size:12px; padding:4px 8px;">
        ${idx + 1}. ${p} ${p === cfg.activeProfile ? '★' : ''}
      </span>
    `).join(' ➔ ');

    let rowsHtml = cfg.rules.map(r => `
      <tr>
        <td style="font-family:monospace; font-weight:600; color:#2563eb;">${escapeHTML(r.pattern)}</td>
        <td><span class="badge-status ok">${escapeHTML(r.target)}</span></td>
        <td style="color:var(--text-muted);">${escapeHTML(r.description)}</td>
      </tr>
    `).join('');

    const pausedLine = (cfg.pausedProfiles && cfg.pausedProfiles.length > 0)
      ? `<div class="text-xs text-muted" style="margin-top:4px;">已暂停账号 (跳过调度): ${cfg.pausedProfiles.map(p => escapeHTML(p)).join(', ')}</div>`
      : '';

    box.innerHTML = `
      <div class="priority-chain-card">
        <div class="text-xs text-muted">当前账号容灾优先级链:</div>
        <div class="priority-chain-flow">${chainHtml || '<span class="text-muted">无启用账号</span>'}</div>
        ${pausedLine}
        <div class="text-xs text-muted" style="margin-top:6px;">容灾模式: ${escapeHTML(cfg.failoverMode)}</div>
      </div>
      <table class="rule-table">
        <thead>
          <tr>
            <th>匹配模式</th>
            <th>路由目标</th>
            <th>规则说明</th>
          </tr>
        </thead>
        <tbody>
          ${rowsHtml}
        </tbody>
      </table>
    `;
  }

  function renderWaste(waste) {
    const box = document.getElementById('wasteBox');
    if (!box || !waste) return;

    let recHtml = waste.recommendations.map(r => `
      <li class="waste-rec-item">
        <span>💡</span>
        <span>${escapeHTML(r)}</span>
      </li>
    `).join('');

    box.innerHTML = `
      <div class="waste-hero">
        <div class="waste-badge">${waste.scoreGrade}</div>
        <div>
          <h3 style="font-size:18px; font-weight:700;">Token 缓存与利用效率评分</h3>
          <p style="font-size:13px; color:var(--text-muted); margin-top:4px;">
            缓存命中率 <strong>${waste.hitRate.toFixed(1)}%</strong> · 累计命中 <strong>${formatTokens(waste.cachedTokens)}</strong> Tokens · 累计节省 <strong>$${waste.costSavedUsd.toFixed(2)} USD</strong> (约折合人民币 ￥${waste.costSavedCny.toFixed(2)})
          </p>
        </div>
      </div>
      <div>
        <h4 style="font-size:14px; font-weight:600; margin-bottom:8px;">智能优化建议:</h4>
        <ul class="waste-rec-list">
          ${recHtml}
        </ul>
      </div>
    `;
  }

  function renderLiveDiag(diag) {
    const dot = document.getElementById('statusDot');
    const badge = document.getElementById('inflightBadge');
    const footerInfo = document.getElementById('footerGatewayInfo');

    if (dot) {
      // SVG className is read-only; setAttribute keeps the busy override in CSS working
      dot.setAttribute('class', `brand-bolt ${diag.online ? 'online' : 'offline'}`);
    }
    if (badge) {
      badge.textContent = `在途 ${diag.inflightNow} (峰值 ${diag.inflightPeak})`;
    }
    if (footerInfo) {
      footerInfo.textContent = diag.online
        ? `网关在线 (127.0.0.1:8783) · 出站连接池命中率 ${diag.poolHitRate.toFixed(1)}% · 缓存省下 ${diag.cacheSavedMb.toFixed(1)}MB`
        : '网关离线 (请在终端执行 agyp 或开启服务)';
    }
  }

  function fmtMs(ms) {
    if (!ms || ms <= 0) return '-';
    if (ms >= 1000) return (ms / 1000).toFixed(1) + 's';
    return Math.round(ms) + 'ms';
  }

  function sessionShortId(s) {
    if (!s) return '';
    if (s.startsWith('agent_monitoring/')) {
      return `监控探活 · ${s.slice(17, 25)}`;
    }
    if (s.startsWith('agent_monitor/')) {
      return `监控探活 · ${s.slice(14, 22)}`;
    }
    let id = s;
    if (id.startsWith('agent/')) id = id.slice(6);
    else if (id.startsWith('checkpoint/')) id = id.slice(11);
    return id.length > 8 ? id.slice(0, 8) + '…' : id;
  }

  function sessionDisplayById(s) {
    if (!s) return '-';
    const name = sessionNameById[s];
    if (name) return name;
    const agent = sessionAgentById[s];
    if (agent) return `${agent} · ${sessionShortId(s)}`;
    return sessionShortId(s) || '-';
  }

  function updateFilterOptions(logs) {
    if (!logFilterSession || !logFilterModel || !logFilterProfile) return;

    const sessCounts = {};
    const sessions = [];
    const seenSess = new Set();
    logs.forEach(l => {
      if (!l.session) return;
      if (l.sessionName) sessionNameById[l.session] = l.sessionName;
      else if (!(l.session in sessionNameById)) sessionNameById[l.session] = '';
      if (l.agentName) sessionAgentById[l.session] = l.agentName;
      if (l.parentSession) sessionParentById[l.session] = l.parentSession;

      if (!isSystemOrBackgroundLog(l)) {
        sessCounts[l.session] = (sessCounts[l.session] || 0) + 1;
        if (!seenSess.has(l.session)) {
          seenSess.add(l.session);
          sessions.push(l.session);
        }
      }
    });

    // Collect sessions (recent active order, excluding background monitoring noise)
    const currSession = logFilter.session;
    let sessHtml = `<option value="ALL">全部会话 (${sessions.length})</option>`;
    const sessValues = ['ALL', ...sessions];
    sessions.forEach(s => {
      const prefix = sessionParentById[s] ? '↳ ' : '';
      const label = `${prefix}${sessionDisplayById(s)} (${sessCounts[s]})`;
      sessHtml += `<option value="${escapeHTML(s)}" ${s === currSession ? 'selected' : ''}>${escapeHTML(label)}</option>`;
    });
    if (currSession !== 'ALL' && !seenSess.has(currSession)) {
      sessValues.push(currSession);
      const prefix = sessionParentById[currSession] ? '↳ ' : '';
      sessHtml += `<option value="${escapeHTML(currSession)}" selected>${escapeHTML(prefix + sessionDisplayById(currSession))} (0)</option>`;
    }
    const newSessSig = sessValues.map(v => `${v}:${sessCounts[v] || 0}:${sessionDisplayById(v)}`).join('|');
    if (document.activeElement !== logFilterSession && logFilterSession.dataset.sig !== newSessSig) {
      logFilterSession.innerHTML = sessHtml;
      logFilterSession.value = currSession;
      logFilterSession.dataset.sig = newSessSig;
    }

    // Collect models
    const models = Array.from(new Set(logs.map(l => l.model).filter(Boolean))).sort();
    const currModel = logFilter.model;
    let modelHtml = `<option value="ALL">全部模型 (${models.length})</option>`;
    models.forEach(m => {
      modelHtml += `<option value="${escapeHTML(m)}" ${m === currModel ? 'selected' : ''}>${escapeHTML(m)}</option>`;
    });
    const oldModelOptions = Array.from(logFilterModel.options).map(o => o.value).join(',');
    const newModelValues = ['ALL', ...models].join(',');
    if (oldModelOptions !== newModelValues) {
      logFilterModel.innerHTML = modelHtml;
      logFilterModel.value = currModel;
    }

    // Collect profiles
    const profiles = Array.from(new Set(logs.map(l => l.profile).filter(Boolean))).sort();
    const currProf = logFilter.profile;
    let profHtml = `<option value="ALL">全部账号</option>`;
    profiles.forEach(p => {
      profHtml += `<option value="${escapeHTML(p)}" ${p === currProf ? 'selected' : ''}>${escapeHTML(p)}</option>`;
    });
    const oldProfOptions = Array.from(logFilterProfile.options).map(o => o.value).join(',');
    const newProfValues = ['ALL', ...profiles].join(',');
    if (oldProfOptions !== newProfValues) {
      logFilterProfile.innerHTML = profHtml;
      logFilterProfile.value = currProf;
    }
  }

  window.resetLogFilters = function() {
    logFilter = { scope: 'ALL', session: 'ALL', model: 'ALL', path: 'ALL', profile: 'ALL', status: 'ALL', keyword: '' };
    if (logFilterSession) logFilterSession.value = 'ALL';
    if (logFilterModel) logFilterModel.value = 'ALL';
    if (logFilterPath) logFilterPath.value = 'ALL';
    if (logFilterProfile) logFilterProfile.value = 'ALL';
    if (logFilterStatus) logFilterStatus.value = 'ALL';
    if (logSearchInput) logSearchInput.value = '';
    renderRecentLogs(cachedLogs);
  };

  window.setLogStatusFilter = function(status) {
    logFilter.status = status;
    if (logFilterStatus) logFilterStatus.value = status;
    renderRecentLogs(cachedLogs);
  };

  window.filterBySession = function(sess) {
    if (!sess) return;
    if (isSystemOrBackgroundLog({ session: sess })) {
      logFilter.scope = 'ALL';
    }
    logFilter.session = sess;
    if (logFilterSession) {
      if (!Array.from(logFilterSession.options).some(o => o.value === sess)) {
        const opt = document.createElement('option');
        opt.value = sess;
        opt.textContent = sessionDisplayById(sess);
        logFilterSession.appendChild(opt);
      }
      logFilterSession.value = sess;
      logFilterSession.dataset.sig = '';
    }
    const hasMatchInCurrent = cachedLogs.some(l => l.session === sess);
    renderRecentLogs(cachedLogs);
    if (!hasMatchInCurrent && currentLogLimit < 1000) {
      currentLogLimit = 1000;
      if (logLimitSelect) logLimitSelect.value = '1000';
      pollRealtime(true);
    }
  };

  function isSystemOrBackgroundLog(l) {
    const s = (l && l.session) ? String(l.session).trim() : '';
    if (!s) return true;
    if (s.startsWith('agent_monitoring') || s.startsWith('agent_monitor') || s.startsWith('checkpoint/')) return true;
    return false;
  }

  function getFilteredLogs() {
    if (!cachedLogs || cachedLogs.length === 0) return [];
    return cachedLogs.filter(l => {
      if (logFilter.scope === 'CHAT' && isSystemOrBackgroundLog(l)) return false;
      if (logFilter.session !== 'ALL' && l.session !== logFilter.session) return false;
      if (logFilter.model !== 'ALL' && l.model !== logFilter.model) return false;
      if (logFilter.path !== 'ALL' && !l.path.includes(logFilter.path)) return false;
      if (logFilter.profile !== 'ALL' && l.profile !== logFilter.profile) return false;
      if (logFilter.status === 'OK' && (l.status !== 200 || l.error)) return false;
      if (logFilter.status === 'ERR' && (l.status === 200 && !l.error)) return false;
      if (logFilter.status === '503' && l.status !== 503) return false;
      if (logFilter.status === '429' && l.status !== 429) return false;
      if (logFilter.status === '502' && l.status !== 502 && !String(l.error).includes('502')) return false;
      if (logFilter.status === 'CACHE' && l.cache !== 'HIT') return false;
      if (logFilter.status === 'CACHE_DROP' && !l.cacheAnomaly) return false;
      if (logFilter.status === 'RETRY' && !(l.retry > 0)) return false;
      if (logFilter.keyword) {
        const text = [l.session, l.sessionName, l.agentName, sessionDisplayById(l.session), l.model, l.path, l.profile, l.time, l.status, l.error, l.cache, l.pool].filter(Boolean).join(' ').toLowerCase();
        if (!text.includes(logFilter.keyword)) return false;
      }
      return true;
    });
  }

  function renderStreamMiniKpi(filtered, totalLogs) {
    if (!streamMiniKpiBar) return;
    const count = filtered.length;
    const total = totalLogs.length;
    const successCount = filtered.filter(l => l.status === 200 && !l.error).length;
    const successRate = count > 0 ? ((successCount / count) * 100).toFixed(1) : '100.0';

    // TTFT P50 & P95
    const headTimes = filtered.map(l => l.headMs).filter(h => h > 0).sort((a, b) => a - b);
    const p50 = headTimes.length > 0 ? Math.round(headTimes[Math.floor(headTimes.length * 0.5)]) : 0;
    const p95 = headTimes.length > 0 ? Math.round(headTimes[Math.floor(headTimes.length * 0.95)]) : 0;

    // Token-weighted decode speed (excl. TTFT wait, incl. thinking tokens), fallback to e2e
    const decGen = filtered.reduce((a, l) => a + (l.streamMs >= 50 ? ((l.outTok || 0) + (l.thoughtTok || 0)) : 0), 0);
    const decMs = filtered.reduce((a, l) => a + (l.streamMs >= 50 ? l.streamMs : 0), 0);
    const totGen = filtered.reduce((a, l) => a + (l.outTok || 0) + (l.thoughtTok || 0), 0);
    const totMs = filtered.reduce((a, l) => a + (l.totalMs || 0), 0);
    const decSpeed = decMs > 0 && decGen > 0 ? (decGen / (decMs / 1000)).toFixed(1) : '';
    const e2eSpeed = totMs > 0 && totGen > 0 ? (totGen / (totMs / 1000)).toFixed(1) : '0.0';
    const avgSpeed = decSpeed || e2eSpeed;

    const cacheHitCount = filtered.filter(l => l.cache === 'HIT').length;
    const cacheDropCount = filtered.filter(l => !!l.cacheAnomaly).length;
    const retryCount = filtered.filter(l => l.retry > 0).length;
    const errCount = filtered.filter(l => l.status !== 200 || !!l.error).length;
    const activeSessInsight = (logFilter.session && logFilter.session !== 'ALL' && !isSystemOrBackgroundLog({ session: logFilter.session }))
      ? `<span class="st-sep">|</span><button type="button" class="st-insight-chip" onclick="window.openSessionInspector('${escapeHTML(logFilter.session)}')" title="查看该会话的上下文窗口拆解、耗时分布与子智能体汇总">会话透视 ↗</button>`
      : '';
    const cacheDropHtml = cacheDropCount > 0
      ? `<span class="st-sep">|</span><span class="st-link warn" onclick="window.setLogStatusFilter('CACHE_DROP')" title="点击筛选短间隔内异常掉缓存的请求">掉缓存 ${cacheDropCount}</span>`
      : '';

    streamMiniKpiBar.innerHTML = `
      <div class="stream-stats">
        <span class="st-main ${parseFloat(successRate) >= 99 ? 'good' : 'bad'}">
          <span class="st-lab">成功率</span><b>${successRate}%</b>
        </span>
        <span class="st-sep">|</span>
        <span class="st-main">
          <span class="st-lab">P50 首字</span><b>${fmtMs(p50)}</b><small>P95 ${fmtMs(p95)}</small>
        </span>
        <span class="st-sep">|</span>
        <span class="st-main" title="纯吐字均速 ${decSpeed || '-'} t/s（不含首字等待） · 端到端均速 ${e2eSpeed} t/s">
          <span class="st-lab">吐字均速</span><b>${avgSpeed}<i>t/s</i></b>
        </span>
        <span class="st-sep">|</span>
        <span class="st-link" onclick="window.setLogStatusFilter('CACHE')" title="点击筛选命中网关本地 HTTP 元数据缓存的请求">元数据缓存 ${cacheHitCount}</span>
        ${cacheDropHtml}
        <span class="st-sep">|</span>
        <span class="st-link" onclick="window.setLogStatusFilter('RETRY')" title="点击筛选所有自动触发容灾重试并成功救回的请求">容灾救回 ${retryCount}</span>
        <span class="st-sep">|</span>
        <span class="st-link ${errCount > 0 ? 'bad' : ''}" onclick="window.setLogStatusFilter('ERR')" title="点击筛选所有失败或异常报错的请求">异常 ${errCount}</span>
        ${activeSessInsight}
      </div>
    `;
  }

  window.clearSingleLogFilter = function(key) {
    if (key === 'session') {
      logFilter.session = 'ALL';
      if (logFilterSession) logFilterSession.value = 'ALL';
    } else if (key === 'model') {
      logFilter.model = 'ALL';
      if (logFilterModel) logFilterModel.value = 'ALL';
    } else if (key === 'profile') {
      logFilter.profile = 'ALL';
      if (logFilterProfile) logFilterProfile.value = 'ALL';
    } else if (key === 'path') {
      logFilter.path = 'ALL';
      if (logFilterPath) logFilterPath.value = 'ALL';
    } else if (key === 'status') {
      logFilter.status = 'ALL';
      if (logFilterStatus) logFilterStatus.value = 'ALL';
    } else if (key === 'keyword') {
      logFilter.keyword = '';
      if (logSearchInput) logSearchInput.value = '';
    } else if (key === 'scope') {
      logFilter.scope = 'ALL';
    }
    renderRecentLogs(cachedLogs);
  };

  function filterStatusLabel(st) {
    const map = {
      OK: '仅 200',
      ERR: '仅异常',
      503: '仅 503',
      429: '仅 429',
      502: '仅 502',
      CACHE: '元数据缓存 HIT',
      CACHE_DROP: '异常掉缓存',
      RETRY: '容灾救回'
    };
    return map[st] || st;
  }

  function filterPathLabel(p) {
    if (p === 'streamGenerateContent') return 'streamGenerate';
    if (p === 'fetchAvailableModels') return 'fetchModels';
    if (p === 'loadCodeAssist') return 'loadCode';
    return p;
  }

  function syncQuickFilterUI() {
    const hasAdvSession = logFilter.session !== 'ALL';
    const hasAdvModel = logFilter.model !== 'ALL';
    const hasAdvProfile = logFilter.profile !== 'ALL';
    const hasAdvStatus = logFilter.status !== 'ALL' && logFilter.status !== 'ERR';
    const hasAdvPath = logFilter.path !== 'ALL' && logFilter.path !== 'streamGenerateContent';
    const advCount = (hasAdvSession ? 1 : 0) + (hasAdvModel ? 1 : 0) + (hasAdvProfile ? 1 : 0) + (hasAdvStatus ? 1 : 0) + (hasAdvPath ? 1 : 0);
    const advActive = advCount > 0;
    const noExtraFilters = !hasAdvSession && !hasAdvModel && !hasAdvProfile && !logFilter.keyword;

    let mode = '';
    if (noExtraFilters && logFilter.scope === 'CHAT' && logFilter.path === 'ALL' && logFilter.status === 'ALL') {
      mode = 'chat';
    } else if (noExtraFilters && logFilter.scope === 'ALL' && logFilter.path === 'streamGenerateContent' && logFilter.status === 'ALL') {
      mode = 'gen';
    } else if (noExtraFilters && logFilter.scope === 'ALL' && logFilter.path === 'ALL' && logFilter.status === 'ERR') {
      mode = 'err';
    } else if (noExtraFilters && logFilter.scope === 'ALL' && logFilter.path === 'ALL' && logFilter.status === 'ALL') {
      mode = 'all';
    }
    document.querySelectorAll('#streamQuickFilters .quick-pill-btn').forEach(btn => {
      btn.classList.toggle('active', btn.dataset.quick === mode);
    });

    const moreBtn = document.getElementById('btnToggleMoreFilters');
    const countBadge = document.getElementById('moreFiltersCount');
    if (moreBtn) moreBtn.classList.toggle('has-active', advActive);
    if (countBadge) {
      countBadge.style.display = advActive ? 'inline-flex' : 'none';
      countBadge.textContent = advActive ? String(advCount) : '';
    }

    const anyFilter = advActive || logFilter.scope !== 'ALL' || logFilter.status !== 'ALL' || logFilter.path !== 'ALL' || logFilter.keyword !== '';
    const toolbar = document.querySelector('.stream-toolbar-compact');
    if (toolbar) toolbar.classList.toggle('is-filtered', anyFilter);

    const chipsBox = document.getElementById('streamActiveFilterChips');
    let hasChips = false;
    if (chipsBox) {
      const chips = [];
      if (logFilter.session !== 'ALL') {
        chips.push({ key: 'session', label: '会话', val: sessionDisplayById(logFilter.session) });
      }
      if (logFilter.model !== 'ALL') {
        chips.push({ key: 'model', label: '模型', val: logFilter.model });
      }
      if (logFilter.profile !== 'ALL') {
        chips.push({ key: 'profile', label: '账号', val: logFilter.profile });
      }
      if (logFilter.path !== 'ALL') {
        chips.push({ key: 'path', label: '接口', val: filterPathLabel(logFilter.path) });
      }
      if (logFilter.status !== 'ALL') {
        chips.push({ key: 'status', label: '状态', val: filterStatusLabel(logFilter.status) });
      }
      if (logFilter.keyword) {
        chips.push({ key: 'keyword', label: '搜索', val: logFilter.keyword });
      }
      if (chips.length > 0) {
        hasChips = true;
        let html = '<span class="afc-prefix">已选</span>';
        chips.forEach(c => {
          html += `<span class="active-filter-chip"><span class="afc-key">${escapeHTML(c.label)}</span><span class="afc-val" title="${escapeHTML(c.val)}">${escapeHTML(c.val)}</span><button type="button" class="afc-remove" onclick="window.clearSingleLogFilter('${c.key}')" title="移除此筛选">✕</button></span>`;
        });
        html += '<button type="button" class="afc-clear-all" onclick="window.resetLogFilters()">清空全部</button>';
        chipsBox.innerHTML = html;
        chipsBox.style.display = 'flex';
      } else {
        chipsBox.innerHTML = '';
        chipsBox.style.display = 'none';
      }
    }
    if (logResetFiltersBtn) logResetFiltersBtn.style.display = (anyFilter && !hasChips) ? 'inline-flex' : 'none';
  }

  window.exportCurrentLogs = function() {
    const logsToExport = getFilteredLogs();
    if (!logsToExport || logsToExport.length === 0) {
      showToast('当前筛选条件下没有日志可导出');
      return;
    }
    const headers = ['时间', '状态', '会话', '会话名称', '模型', '接口', '首字延迟(ms)', '吐字速率(tok/s)', '端到端吞吐(tok/s)', '输出Token', '思考Token', '输入Token', '缓存Token', '元数据缓存', '连接模式', '重试救回', '账号', '错误信息'];
    const csvRows = [headers.join(',')];
    logsToExport.forEach(l => {
      const row = [
        `"${l.time || ''}"`,
        l.status || '',
        `"${(l.session || '').replace(/"/g, '""')}"`,
        `"${(l.sessionName || '').replace(/"/g, '""')}"`,
        `"${(l.model || '').replace(/"/g, '""')}"`,
        `"${(l.path || '').replace(/"/g, '""')}"`,
        Math.round(l.headMs || 0),
        (l.decodeSpeed || 0).toFixed(1),
        (l.speed || 0).toFixed(1),
        l.outTok || 0,
        l.thoughtTok || 0,
        l.inTok || 0,
        l.cacheTok || 0,
        `"${l.cache || ''}"`,
        `"${l.pool || ''}"`,
        l.retry || 0,
        `"${(l.profile || '').replace(/"/g, '""')}"`,
        `"${(l.error || '').replace(/"/g, '""')}"`
      ];
      csvRows.push(row.join(','));
    });
    const bom = '\uFEFF';
    const dateStr = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-');
    downloadFile(`agyp-stream-logs-${dateStr}.csv`, bom + csvRows.join('\r\n'));
  };

  function fmtCompactTok(n) {
    const v = Number(n) || 0;
    if (v >= 1000000) return `${(v / 1000000).toFixed(2)}M`;
    if (v >= 100000) return `${Math.round(v / 1000)}k`;
    if (v >= 1000) return `${(v / 1000).toFixed(1)}k`;
    return String(Math.round(v));
  }

  function renderRecentLogs(logs) {
    const tbody = document.getElementById('streamTableBody');
    if (!tbody) return;

    if (!logs || logs.length === 0) {
      tbody.innerHTML = '<tr><td colspan="11" class="loading-placeholder">暂无实时日志</td></tr>';
      if (streamFilteredCount) streamFilteredCount.textContent = '暂无记录';
      if (streamMiniKpiBar) streamMiniKpiBar.innerHTML = '';
      return;
    }

    updateFilterOptions(logs);
    syncQuickFilterUI();

    // Apply filters
    const filtered = getFilteredLogs();
    renderStreamMiniKpi(filtered, logs);

    if (streamFilteredCount) {
      const isFiltered = logFilter.scope !== 'ALL' || logFilter.session !== 'ALL' || logFilter.model !== 'ALL' || logFilter.path !== 'ALL' || logFilter.profile !== 'ALL' || logFilter.status !== 'ALL' || logFilter.keyword !== '';
      streamFilteredCount.innerHTML = isFiltered
        ? `<span style="color:var(--primary); font-weight:600;">${filtered.length} / ${logs.length}</span>`
        : `${logs.length} 条`;
    }

    if (filtered.length === 0) {
      const isSpecificSess = logFilter.session !== 'ALL' && !isSystemOrBackgroundLog({ session: logFilter.session });
      const emptyMsg = isSpecificSess
        ? `当前近 ${logs.length} 条流水窗口内暂无该会话的请求记录`
        : '未找到符合当前筛选条件的请求流水';
      const insightAction = isSpecificSess
        ? `<button type="button" onclick="window.openSessionInspector('${escapeHTML(logFilter.session)}')" class="btn-filter-reset" style="margin-top:10px; margin-right:8px; display:inline-block; color:var(--primary); border-color:var(--primary);">查看会话透视 ↗</button>`
        : '';
      tbody.innerHTML = `
        <tr>
          <td colspan="11" style="text-align:center; padding:32px 12px; color:var(--text-muted);">
            <div>${emptyMsg}</div>
            <div>${insightAction}<button onclick="window.resetLogFilters()" class="btn-filter-reset" style="margin-top:10px; display:inline-block;">清空筛选条件</button></div>
          </td>
        </tr>
      `;
      return;
    }

    let html = '';
    filtered.forEach(l => {
      const originalIdx = logs.indexOf(l);
      const isOk = l.status === 200 && !l.error;
      const statusClass = isOk ? 'ok' : 'err';
      const primarySpeed = l.decodeSpeed > 0 ? l.decodeSpeed : l.speed;
      const speedStr = primarySpeed > 0 ? `${primarySpeed.toFixed(1)} t/s` : '-';
      const speedTitle = (l.decodeSpeed > 0 || l.speed > 0)
        ? `吐字速率: ${l.decodeSpeed > 0 ? l.decodeSpeed.toFixed(1) + ' t/s' : '-'} · 端到端: ${l.speed > 0 ? l.speed.toFixed(1) + ' t/s' : '-'}`
        : '';
      const headStr = fmtMs(l.headMs);

      let ttftClass = 'ttft-normal';
      if (l.headMs > 0 && l.headMs < 300) ttftClass = 'ttft-fast';
      else if (l.headMs >= 800 && l.headMs < 2000) ttftClass = 'ttft-warn';
      else if (l.headMs >= 2000) ttftClass = 'ttft-slow';

      // Cache hit rides on the status badge (⚡ after 200); retry/error keep their own tags
      let extraBadges = '';
      if (l.retry > 0) {
        extraBadges += `<span class="badge-tag retry-tag" title="容灾救回重试 ${l.retry} 次">救回×${l.retry}</span>`;
      }
      if (!isOk && l.error) {
        let errLabel = '异常';
        if (l.error.includes('MODEL_CAPACITY_EXHAUSTED')) errLabel = '无容量';
        else if (l.error.includes('RESOURCE_EXHAUSTED') || l.error.includes('Quota')) errLabel = '配额打满';
        else if (l.error.includes('Timeout') || l.error.includes('502') || l.error.includes('SSLEOF')) errLabel = '超时/断开';
        extraBadges += `<span class="badge-tag err-tag" title="${escapeHTML(l.error)}">${errLabel}</span>`;
      }

      const isHit = l.cache === 'HIT';
      const hitTitle = isHit ? ' title="网关本地 HTTP 元数据缓存命中 (X-Agyp-Cache: HIT)"' : '';
      const hitMark = isHit ? '<span class="status-hit">HIT</span>' : '';

      // Session pill
      let sessionHtml = '<span style="color:var(--text-light); font-size:11px;">系统内部</span>';
      if (l.session) {
        const isAgent = l.session.startsWith('agent/');
        const isCkpt = l.session.startsWith('checkpoint/');
        const isSub = Boolean(l.parentSession);
        const badgeKind = isAgent ? 'agent' : (isCkpt ? 'checkpoint' : 'other');
        const subCls = isSub ? ' is-subagent' : '';
        const subPrefix = isSub ? '<span class="subagent-mark">↳</span>' : '';
        const agentLine = l.agentName ? `子智能体 (${l.agentName})\n` : (isSub ? '子智能体会话\n' : '');
        const tip = l.sessionName
          ? `${agentLine}${l.sessionName}\n${l.session} (点击筛选此会话)`
          : `${agentLine}会话: ${l.session} (点击筛选此会话)`;
        sessionHtml = `
          <span class="session-pill ${badgeKind}${subCls}" 
                onclick="event.stopPropagation(); window.filterBySession('${escapeHTML(l.session)}')"
                title="${escapeHTML(tip)}">
            ${subPrefix}${escapeHTML(sessionDisplayById(l.session))}
          </span>
        `;
      }

      // Clean path
      let shortPath = l.path || '-';
      if (shortPath.startsWith('streamGenerateContent')) shortPath = 'streamGenerate';
      else if (shortPath.startsWith('fetchAvailableModels')) shortPath = 'fetchModels';
      else if (shortPath.startsWith('loadCodeAssist')) shortPath = 'loadCode';

      // Token throughput (compact merged output, exact breakdown in tooltip) + dedicated cache hit column
      const outTok = l.outTok || 0;
      const thoughtTok = l.thoughtTok || 0;
      const totalOutTok = outTok + thoughtTok;
      const inTok = l.inTok || 0;
      const tokHtml = `<span class="tok-out">${fmtCompactTok(totalOutTok)}</span> <span style="color:var(--text-light);">/</span> <span class="tok-in">${fmtCompactTok(inTok)}</span>`;
      const tokTitle = thoughtTok > 0
        ? `总输出 ${totalOutTok.toLocaleString()} (正文 ${outTok.toLocaleString()} + 思考 ${thoughtTok.toLocaleString()}) / 输入 ${inTok.toLocaleString()}`
        : `输出 ${outTok.toLocaleString()} / 输入 ${inTok.toLocaleString()}`;

      let cacheHitHtml = '<span style="color:var(--text-light);">-</span>';
      if (l.cacheAnomaly) {
        const rawPct = inTok > 0 ? Math.min(100, (l.cacheTok * 100) / inTok) : 0;
        const pctLabel = Math.round(rawPct);
        cacheHitHtml = `<span class="tok-cache-pill anomaly" title="${escapeHTML(l.cacheAnomalyTip || '短间隔内异常掉缓存')}">⚠ ${pctLabel}%</span>`;
      } else if (l.cacheTok > 0) {
        if (inTok > 0) {
          const rawPct = Math.min(100, (l.cacheTok * 100) / inTok);
          const cachePct = Math.round(rawPct);
          const lowCls = cachePct < 50 ? ' low' : '';
          cacheHitHtml = `<span class="tok-cache-pill${lowCls}" title="上下文缓存命中: ${l.cacheTok.toLocaleString()} / ${inTok.toLocaleString()} (${rawPct.toFixed(1)}%)">${cachePct}%</span>`;
        } else {
          cacheHitHtml = `<span class="tok-cache-pill" title="上下文缓存命中 Token: ${l.cacheTok.toLocaleString()}">${fmtCompactTok(l.cacheTok)}</span>`;
        }
      }

      // Connection pool badge
      let poolHtml = '<span style="color:var(--text-light);">-</span>';
      if (l.pool === 'pool' || l.pool === 'reused') {
        poolHtml = '<span class="pool-tag reused" title="长连接复用，免 TLS 握手">复用</span>';
      } else if (l.pool === 'new') {
        poolHtml = '<span class="pool-tag new" title="新建 HTTP/2 连接">新建</span>';
      }

      html += `
        <tr onclick="window.inspectLog(${originalIdx})" title="点击查看请求现场诊断与原地重放测试">
          <td style="color:var(--text-muted); font-family:monospace; font-size:11px;">${escapeHTML(l.time)}</td>
          <td>
            <span class="badge-status ${statusClass}"${hitTitle}>${l.status || 'ERR'}${hitMark}</span>
            ${extraBadges}
          </td>
          <td>${sessionHtml}</td>
          <td style="font-weight:500;">${escapeHTML(l.model || '-')}</td>
          <td style="color:var(--text-muted); font-size:11px;" title="${escapeHTML(l.path)}">${escapeHTML(shortPath)}</td>
          <td style="font-variant-numeric:tabular-nums;"><span class="${ttftClass}">${headStr}</span></td>
          <td class="speed-tag" title="${escapeHTML(speedTitle)}">${speedStr}</td>
          <td style="font-variant-numeric:tabular-nums;" title="${escapeHTML(tokTitle)}">${tokHtml}</td>
          <td style="font-variant-numeric:tabular-nums;">${cacheHitHtml}</td>
          <td>${poolHtml}</td>
          <td><span style="font-size:11px; color:var(--text-main); background:var(--bg-hover); border:1px solid var(--border-color); padding:2px 6px; border-radius:4px;">${escapeHTML(l.profile || '-')}</span></td>
        </tr>
      `;
    });
    tbody.innerHTML = html;
  }


  const CAT_COLORS = {
    messages: '#3b82f6',
    system_prompt: '#ea580c',
    tools: '#0d9488',
    skills: '#78716c',
    subagents: '#6366f1',
    plugins: '#a855f7',
    mcp: '#06b6d4',
    rules: '#64748b',
    other: '#94a3b8',
    input: '#3b82f6',
    free: '#e2e8f0'
  };

  function fmtDurationCN(ms) {
    const v = Number(ms);
    if (!Number.isFinite(v) || v <= 0) return '-';
    if (v < 1000) return `${Math.round(v)}ms`;
    const totalSec = Math.round(v / 1000);
    if (totalSec < 60) return `${(v / 1000).toFixed(1)}秒`;
    const mins = Math.floor(totalSec / 60);
    const secs = totalSec % 60;
    return `${mins}分${secs}秒`;
  }

  function fmtCountdownClock(ms) {
    const v = Number(ms);
    if (!Number.isFinite(v) || v < 0) return '';
    const sec = Math.floor(v / 1000);
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    return `${m}:${String(s).padStart(2, '0')}`;
  }

  function startCacheCountdownTicker(detail) {
    if (cacheCountdownTimer) {
      clearInterval(cacheCountdownTimer);
      cacheCountdownTimer = null;
    }
    if (!detail || !detail.cacheWindowMs || !detail.lastRequestAtMs) return;
    const updateEl = () => {
      const el = document.getElementById('siCacheObserveVal');
      if (!el) return;
      const age = Date.now() - detail.lastRequestAtMs;
      const rem = Math.max(0, detail.cacheWindowMs - age);
      if (rem <= 0) {
        el.textContent = '已过观察窗';
        el.className = 'si-kv-val si-muted';
      } else {
        el.textContent = `≈ ${fmtCountdownClock(rem)}`;
        el.className = 'si-kv-val si-observe-active';
      }
    };
    updateEl();
    cacheCountdownTimer = setInterval(updateEl, 1000);
  }

  function updateDrawerHeaderActions(sessionId) {
    const slot = document.getElementById('drawerHeaderActions');
    if (!slot) return;
    if (!sessionId || isSystemOrBackgroundLog({ session: sessionId })) {
      slot.innerHTML = '';
      return;
    }
    slot.innerHTML = `
      <button type="button" class="drawer-mini-btn" onclick="window.filterBySession('${escapeHTML(sessionId)}'); window.closeInspectDrawer();" title="在实时流水中仅看此会话">筛选流水</button>
      <button type="button" class="drawer-mini-btn" onclick="window.refreshSessionDetail('${escapeHTML(sessionId)}')" title="重新解析本地数据库与网关日志">刷新</button>
    `;
  }

  window.openSessionInspector = function (sessionId) {
    if (!sessionId) return;
    currentInspectedLog = null;
    currentSessionTarget = sessionId;

    const overlay = document.getElementById('inspectOverlay');
    const drawer = document.querySelector('#inspectOverlay .inspect-drawer');
    const badge = document.getElementById('drawerStatusBadge');
    const title = document.getElementById('drawerTitle');
    const subtitle = document.getElementById('drawerSubtitle');
    const reqSec = document.getElementById('inspectRequestSection');
    const sessSec = document.getElementById('inspectSessionSection');

    if (badge) {
      badge.className = 'drawer-badge badge-status ok';
      badge.textContent = 'SESSION';
    }
    if (title) {
      title.textContent = sessionDisplayById(sessionId) || '所属会话透视';
    }
    if (subtitle) {
      subtitle.textContent = sessionId;
    }
    if (reqSec) {
      reqSec.style.display = 'none';
      reqSec.innerHTML = '';
    }
    if (sessSec) sessSec.style.display = 'block';
    if (drawer) drawer.classList.add('drawer-wide');

    updateDrawerHeaderActions(sessionId);
    if (overlay) overlay.style.display = 'flex';
    loadSessionDetail(sessionId, true);
  };

  async function loadSessionDetail(sessionId, forceRefresh) {
    const pane = document.getElementById('inspectSessionSection');
    if (!pane) return;
    if (!sessionId || isSystemOrBackgroundLog({ session: sessionId })) {
      pane.innerHTML = `<div class="si-empty-state">当前记录为系统内部调用，暂无关联的用户会话上下文与子智能体明细。</div>`;
      return;
    }

    if (!forceRefresh && sessionDetailCache[sessionId]) {
      renderSessionDetail(sessionDetailCache[sessionId]);
      return;
    }

    pane.innerHTML = `
      <div class="si-skeleton">
        <div class="si-skel-line" style="width:45%;"></div>
        <div class="si-skel-line" style="width:100%; height:14px;"></div>
        <div class="si-skel-grid">
          <div class="si-skel-card"></div>
          <div class="si-skel-card"></div>
        </div>
      </div>
    `;

    try {
      const detail = await callMyGo('GatewayService.GetSessionDetail', sessionId);
      if (currentSessionTarget !== sessionId) return;
      if (!detail) {
        pane.innerHTML = `<div class="si-empty-state">未找到该会话的本地数据库或网关记录。</div>`;
        return;
      }
      sessionDetailCache[sessionId] = detail;
      renderSessionDetail(detail);
    } catch (err) {
      if (currentSessionTarget !== sessionId) return;
      pane.innerHTML = `<div class="si-empty-state si-err">读取会话详情失败：${escapeHTML(err.message || err)}</div>`;
    }
  }

  window.refreshSessionDetail = function (sessionId) {
    if (!sessionId) return;
    delete sessionDetailCache[sessionId];
    loadSessionDetail(sessionId, true);
  };

  function renderSessionDetail(d) {
    const pane = document.getElementById('inspectSessionSection');
    if (!pane) return;

    const titleEl = document.getElementById('drawerTitle');
    if (!currentInspectedLog && titleEl) {
      titleEl.textContent = d.title || '所属会话透视';
    } else if (currentInspectedLog && titleEl && d.title && !currentInspectedLog.sessionName) {
      titleEl.textContent = d.title;
    }

    const ctx = d.context || null;
    const subagents = Array.isArray(d.subagents) ? d.subagents : [];
    const rollup = d.subagentRollup || null;
    const isLocal = d.source === 'local-db';

    const parentBreadcrumb = d.parentSessionId
      ? `<div class="si-breadcrumb">
           <span class="si-pill si-pill-sub">子智能体${d.agentName ? ` · ${escapeHTML(d.agentName)}` : ''}</span>
           <button type="button" class="si-link-btn" onclick="window.openSessionInspector('agent/${escapeHTML(d.parentSessionId)}')">← 返回父会话：${escapeHTML(d.parentTitle || d.parentSessionId.slice(0, 8))}</button>
         </div>`
      : '';

    let ctxHtml = '';
    if (ctx && (ctx.usedTokens > 0 || (ctx.categories && ctx.categories.length > 0))) {
      const hasMax = ctx.maxTokens > 0;
      const pctText = hasMax ? `已使用 ${ctx.usedPercent.toFixed(1)}%` : '本轮输入上下文';
      const capText = hasMax
        ? `${fmtCompactTok(ctx.usedTokens)} / ${fmtCompactTok(ctx.maxTokens)}`
        : `${fmtCompactTok(ctx.usedTokens)}`;
      const compBadge = ctx.compactions > 0
        ? `<span class="si-pill">已压缩 ${ctx.compactions} 次</span>`
        : '';
      const srcBadge = `<span class="si-pill ${isLocal ? 'si-pill-local' : 'si-pill-gw'}">${isLocal ? '本地 SQLite' : '仅网关聚合'}</span>`;

      let barSegs = '';
      if (hasMax && ctx.categories && ctx.categories.length > 0) {
        ctx.categories.forEach(c => {
          if (c.tone === 'free' || c.percent <= 0) return;
          const color = CAT_COLORS[c.key] || CAT_COLORS.other;
          barSegs += `<span class="si-bar-seg" style="width:${Math.max(0.6, c.percent)}%; background:${color};" title="${escapeHTML(c.label)}: ${c.tokens.toLocaleString()} (${c.percent.toFixed(1)}%)"></span>`;
        });
      } else if (ctx.cacheReusedTokens > 0 || ctx.newInputTokens > 0) {
        barSegs += `<span class="si-bar-seg" style="width:${ctx.cacheReusedPct}%; background:#2563eb;" title="缓存复用: ${ctx.cacheReusedTokens.toLocaleString()} (${ctx.cacheReusedPct.toFixed(1)}%)"></span>`;
        barSegs += `<span class="si-bar-seg" style="width:${ctx.newInputPct}%; background:#38bdf8;" title="本步新增: ${ctx.newInputTokens.toLocaleString()} (${ctx.newInputPct.toFixed(1)}%)"></span>`;
      }

      let catRows = '';
      (ctx.categories || []).forEach(c => {
        const color = CAT_COLORS[c.key] || CAT_COLORS.other;
        const pctCell = hasMax ? `<span class="si-cat-pct">${c.percent.toFixed(1)}%</span>` : '';
        const dotCls = c.tone === 'free' ? 'si-dot is-free' : 'si-dot';
        catRows += `
          <div class="si-cat-row ${c.tone === 'free' ? 'is-free' : ''}">
            <span class="si-cat-name"><i class="${dotCls}" style="background:${color};"></i>${escapeHTML(c.label)}</span>
            <span class="si-cat-vals"><b>${fmtCompactTok(c.tokens)}</b>${pctCell}</span>
          </div>
        `;
      });

      let stepReuseHtml = '';
      if (ctx.cacheReusedTokens > 0 || ctx.newInputTokens > 0) {
        stepReuseHtml = `
          <div class="si-divider"></div>
          <div class="si-cat-row">
            <span class="si-cat-name"><i class="si-dot" style="background:#2563eb;"></i>缓存复用上下文</span>
            <span class="si-cat-vals"><b>${fmtCompactTok(ctx.cacheReusedTokens)}</b><span class="si-cat-pct">${ctx.cacheReusedPct.toFixed(1)}%</span></span>
          </div>
          <div class="si-cat-row">
            <span class="si-cat-name"><i class="si-dot" style="background:#38bdf8;"></i>本步新增输入</span>
            <span class="si-cat-vals"><b>${fmtCompactTok(ctx.newInputTokens)}</b><span class="si-cat-pct">${ctx.newInputPct.toFixed(1)}%</span></span>
          </div>
        `;
      }

      let orchHtml = '';
      if (ctx.models && ctx.models.length > 1) {
        orchHtml = `<div class="si-divider"></div><div class="si-subhead">模型上下文拆解</div>`;
        ctx.models.forEach(m => {
          const roleBadge = m.role ? `<span class="si-role-tag">${escapeHTML(m.role === 'execution' ? '执行' : '规划')}</span>` : '';
          const mCap = m.maxTokens > 0 ? `${fmtCompactTok(m.usedTokens)} / ${fmtCompactTok(m.maxTokens)} (${m.usedPercent.toFixed(0)}%)` : fmtCompactTok(m.usedTokens);
          const activeDot = m.isCurrent ? '<i class="si-model-active-dot" title="当前活跃模型"></i>' : '';
          orchHtml += `
            <div class="si-kv-row">
              <span class="si-kv-label">${activeDot}${escapeHTML(m.shortName || m.modelName)} ${roleBadge}</span>
              <span class="si-kv-val">${mCap}</span>
            </div>
          `;
        });
      }

      ctxHtml = `
        <div class="si-card">
          <div class="si-card-header">
            <span class="si-card-title">上下文窗口</span>
            <div class="si-card-badges">${compBadge}${srcBadge}</div>
          </div>
          <div class="si-util-row">
            <strong>${pctText}</strong>
            <span>${capText}</span>
          </div>
          <div class="si-stacked-bar">${barSegs}</div>
          <div class="si-cat-list">${catRows}</div>
          ${stepReuseHtml}
          ${orchHtml}
        </div>
      `;
    } else {
      ctxHtml = `
        <div class="si-card">
          <div class="si-card-header">
            <span class="si-card-title">上下文窗口</span>
            <span class="si-pill si-pill-gw">${isLocal ? '本地 SQLite' : '仅网关聚合'}</span>
          </div>
          <div class="si-muted-box">暂无该会话的上下文窗口快照（可能是纯探活或无本地会话库）。</div>
        </div>
      `;
    }

    let subListHtml = '';
    if (subagents.length > 0) {
      let items = '';
      subagents.forEach(sa => {
        const pct = sa.maxTokens > 0 ? `${Math.round(sa.usedPercent)}%` : '';
        const cap = sa.maxTokens > 0 ? `${fmtCompactTok(sa.usedTokens)} / ${fmtCompactTok(sa.maxTokens)}` : fmtCompactTok(sa.totalTokens);
        const barW = sa.maxTokens > 0 ? Math.min(100, Math.max(2, sa.usedPercent)) : 0;
        const hitStr = sa.cacheHitRate > 0 ? `缓存 ${sa.cacheHitRate.toFixed(1)}%` : '缓存 -';
        const spdStr = sa.avgSpeed > 0 ? `${sa.avgSpeed.toFixed(1)} t/s` : '-';
        const costStr = sa.costUsd > 0 ? `$${sa.costUsd.toFixed(2)}` : '-';
        items += `
          <div class="si-subagent-item" onclick="window.openSessionInspector('agent/${escapeHTML(sa.conversationId)}')" title="点击透视子智能体明细">
            <div class="si-sa-top">
              <span class="si-sa-name">
                <i class="si-sa-dot"></i>
                <b>${escapeHTML(sa.name)}</b>
                ${sa.shortModel ? `<span class="si-model-chip">${escapeHTML(sa.shortModel)}</span>` : ''}
              </span>
              <span class="si-sa-pct">${pct}</span>
            </div>
            ${barW > 0 ? `<div class="si-sa-track"><div class="si-sa-fill" style="width:${barW}%;"></div></div>` : ''}
            <div class="si-sa-meta">
              <span>总 ${fmtCompactTok(sa.totalTokens)} · ${hitStr} · ${spdStr} · ${costStr}</span>
              <span>${cap}</span>
            </div>
          </div>
        `;
      });
      subListHtml = `
        <div class="si-card">
          <div class="si-card-header">
            <span class="si-card-title">子智能体上下文 <span class="si-count-badge">${rollup ? rollup.totalCount : subagents.length}</span></span>
          </div>
          <div class="si-subagent-list">${items}</div>
        </div>
      `;
    }

    const turnsSteps = d.turns > 0 || d.steps > 0
      ? `${d.turns || 0} / ${d.steps || 0}`
      : `${d.requests || 0} 次请求`;
    const costStr = d.costUsd > 0 ? `$${d.costUsd.toFixed(2)}` : '-';
    const costTitle = d.costCny > 0 ? `折合约 ¥${d.costCny.toFixed(2)}（按公开商业 API 单价估算）` : '';
    const avgSpdStr = d.avgSpeed > 0 ? `${d.avgSpeed.toFixed(1)} tok/s` : '-';
    const peakSpdStr = d.peakSpeed > 0 ? `${d.peakSpeed.toFixed(1)} tok/s` : '-';
    const latestSpdStr = d.latestSpeed > 0 ? `${d.latestSpeed.toFixed(1)} tok/s` : '-';
    const hitStr = (d.inputTokens + d.cacheReadTokens) > 0 ? `${d.cacheHitRate.toFixed(1)}%` : '-';
    const ttftStr = d.latestTtftMs > 0 ? fmtDurationCN(d.latestTtftMs) : '-';
    const ttftTitle = d.p50TtftMs > 0 ? `平均首字 ${fmtDurationCN(d.avgTtftMs)} · P50 ${fmtDurationCN(d.p50TtftMs)}` : '';

    let gwOverlayHtml = '';
    if (d.profiles && d.profiles.length > 0) {
      const profBadges = d.profiles.map(p => `<span class="si-prof-pill">${escapeHTML(p)}</span>`).join(' ');
      gwOverlayHtml = `
        <div class="si-divider"></div>
        <div class="si-kv-row"><span class="si-kv-label">承载账号</span><span class="si-kv-val">${profBadges}</span></div>
        <div class="si-kv-row"><span class="si-kv-label">切号 / 重试 / 异常</span><span class="si-kv-val">${d.profileSwitches || 0} / ${d.gatewayRetries || 0} / ${d.gatewayErrors || 0}</span></div>
      `;
    }

    let rollupHtml = '';
    if (rollup && rollup.count > 0) {
      rollupHtml = `
        <div class="si-card">
          <div class="si-card-header">
            <span class="si-card-title">子智能体汇总 <span class="si-count-badge">${rollup.count}</span></span>
          </div>
          <div class="si-kv-list">
            <div class="si-kv-row"><span class="si-kv-label">子智能体 Token</span><span class="si-kv-val">${fmtCompactTok(rollup.totalTokens)}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">含子智能体总计</span><span class="si-kv-val">${fmtCompactTok(rollup.combinedTotalTokens)}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">请求 / 工具调用</span><span class="si-kv-val">${rollup.requests} / ${rollup.toolCalls}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">子智能体缓存命中</span><span class="si-kv-val">${rollup.cacheHitRate.toFixed(1)}%</span></div>
            <div class="si-kv-row"><span class="si-kv-label">子智能体参考费用</span><span class="si-kv-val">$${rollup.costUsd.toFixed(2)}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">含子智能体总费用</span><span class="si-kv-val"><b>$${rollup.combinedCostUsd.toFixed(2)}</b></span></div>
          </div>
        </div>
      `;
    }

    const obsInitText = d.cacheWindowMs > 0
      ? (d.cacheRemainingMs > 0 ? `≈ ${fmtCountdownClock(d.cacheRemainingMs)}` : '已过观察窗')
      : '-';

    pane.innerHTML = `
      ${parentBreadcrumb}
      <div class="session-insight-grid">
        ${ctxHtml}
        <div class="si-card">
          <div class="si-card-header">
            <span class="si-card-title">会话统计</span>
          </div>
          <div class="si-kv-list">
            <div class="si-kv-row"><span class="si-kv-label">${d.turns > 0 || d.steps > 0 ? '轮次 / 步数' : '网关请求数'}</span><span class="si-kv-val">${turnsSteps}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">累计 Token</span><span class="si-kv-val" title="输入 ${d.inputTokens.toLocaleString()} · 缓存 ${d.cacheReadTokens.toLocaleString()} · 输出 ${d.outputTokens.toLocaleString()}">${fmtCompactTok(d.totalTokens)}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">API 参考费用</span><span class="si-kv-val" title="${escapeHTML(costTitle)}">${costStr}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">平均速度</span><span class="si-kv-val">${avgSpdStr}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">峰值速度</span><span class="si-kv-val">${peakSpdStr}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">最近速度</span><span class="si-kv-val">${latestSpdStr}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">缓存命中</span><span class="si-kv-val">${hitStr}</span></div>
            <div class="si-kv-row"><span class="si-kv-label" title="本地 5 分钟观察窗估算（非服务端真实 TTL）">缓存观察</span><span class="si-kv-val" id="siCacheObserveVal">${obsInitText}</span></div>
          </div>
          <div class="si-divider"></div>
          <div class="si-kv-list">
            <div class="si-kv-row"><span class="si-kv-label">模型用时</span><span class="si-kv-val">${fmtDurationCN(d.modelTimeMs)}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">工具用时</span><span class="si-kv-val">${fmtDurationCN(d.toolTimeMs)}</span></div>
            <div class="si-kv-row"><span class="si-kv-label">首 Token 延迟</span><span class="si-kv-val" title="${escapeHTML(ttftTitle)}">${ttftStr}</span></div>
          </div>
          ${gwOverlayHtml}
        </div>
        ${subListHtml}
        ${rollupHtml}
      </div>
    `;

    startCacheCountdownTicker(d);
  }

  window.replayInspectedRequest = async function () {
    if (!currentInspectedLog) return;
    const btn = document.getElementById('drawerReplayBtn');
    const resBox = document.getElementById('replayResultBox');
    if (btn) {
      btn.disabled = true;
      btn.textContent = '测试中...';
    }
    try {
      const res = await callMyGo('GatewayService.TestEndpoint', currentInspectedLog.profile, currentInspectedLog.path);
      if (resBox) {
        resBox.style.display = 'block';
        resBox.style.borderColor = res.success ? '#10b981' : '#ef4444';
        resBox.innerHTML = `<strong>重测结果 (${res.status}):</strong> ${escapeHTML(res.detail)}`;
      }
    } catch (err) {
      if (resBox) {
        resBox.style.display = 'block';
        resBox.innerHTML = `<span style="color:#ef4444;">测试报错: ${escapeHTML(err)}</span>`;
      }
    } finally {
      if (btn) {
        btn.disabled = false;
        btn.textContent = '原地重测';
      }
    }
  };

  function renderRequestCard(log) {
    const sec = document.getElementById('inspectRequestSection');
    if (!sec) return;
    sec.style.display = 'block';

    const poolLabel = (log.pool === 'pool' || log.pool === 'reused') ? '复用长连接' : (log.pool === 'new' ? '新建连接' : (log.pool || ''));
    const retryTag = log.retry > 0
      ? `<span class="req-tag req-tag-warn" title="触发容灾救回，重试 ${log.retry} 次成功">救回×${log.retry}</span>`
      : '<span class="req-tag">无重试</span>';
    const httpHitTag = log.cache === 'HIT'
      ? '<span class="req-tag req-tag-ok" title="网关本地 HTTP 元数据缓存命中 (X-Agyp-Cache: HIT)">HTTP 缓存 HIT</span>'
      : '';
    const agentTag = log.parentSession
      ? `<span class="req-tag req-tag-sub">子智能体${log.agentName ? ` · ${escapeHTML(log.agentName)}` : ''}</span>`
      : '';

    const tags = [
      log.profile ? `<span class="req-tag">${escapeHTML(log.profile)}</span>` : '',
      log.model ? `<span class="req-tag">${escapeHTML(log.model)}</span>` : '',
      poolLabel ? `<span class="req-tag">${escapeHTML(poolLabel)}</span>` : '',
      retryTag,
      httpHitTag,
      agentTag,
    ].filter(Boolean).join('');

    const outTok = log.outTok || 0;
    const thoughtTok = log.thoughtTok || 0;
    const totalOut = outTok + thoughtTok;
    const inTok = log.inTok || 0;
    const cached = Math.min(inTok, log.cacheTok || 0);
    const newIn = Math.max(0, inTok - cached);
    const cPct = inTok > 0 ? (Math.round((cached * 1000) / inTok) / 10) : 0;
    const nPct = inTok > 0 ? (Math.round((100 - cPct) * 10) / 10) : 0;

    const anomalyHtml = log.cacheAnomaly
      ? `<div class="inspect-anomaly-banner"><strong>⚠ 检测到短间隔缓存异常：</strong>${escapeHTML(log.cacheAnomalyTip || '命中率出现异常跌落')}</div>`
      : '';
    const errHtml = log.error
      ? `<div class="inspect-error-box"><div class="inspect-label" style="color:#ef4444;">报错信息 (Error):</div><div class="error-text">${escapeHTML(log.error)}</div></div>`
      : '';

    const inCellSub = inTok > 0
      ? `<div class="req-kpi-sub">复用 ${cPct.toFixed(1)}% · 新增 ${fmtCompactTok(newIn)}</div>
         <div class="req-mini-bar"><span class="step-cache-hit" style="width:${cPct}%;"></span><span class="step-cache-new" style="width:${nPct}%;"></span></div>`
      : '<div class="req-kpi-sub">无输入统计</div>';

    sec.innerHTML = `
      <div class="si-card req-card">
        <div class="req-card-header">
          <div class="req-card-left">
            <span class="si-card-title">本次请求</span>
            <div class="req-card-tags">${tags}</div>
          </div>
          <button type="button" class="drawer-mini-btn drawer-mini-primary" id="drawerReplayBtn" onclick="window.replayInspectedRequest()">原地重测</button>
        </div>
        <div class="req-kpi-grid">
          <div class="req-kpi-cell">
            <div class="req-kpi-label">首字延迟 (TTFT)</div>
            <div class="req-kpi-val">${fmtMs(log.headMs)}</div>
            <div class="req-kpi-sub">流式 ${fmtMs(log.streamMs)} · 总 ${fmtMs(log.totalMs)}</div>
          </div>
          <div class="req-kpi-cell">
            <div class="req-kpi-label">生成速度</div>
            <div class="req-kpi-val req-val-cyan">${log.decodeSpeed > 0 ? log.decodeSpeed.toFixed(1) + ' t/s' : '-'}</div>
            <div class="req-kpi-sub">端到端 ${log.speed > 0 ? log.speed.toFixed(1) + ' t/s' : '-'}</div>
          </div>
          <div class="req-kpi-cell">
            <div class="req-kpi-label">输出 Token</div>
            <div class="req-kpi-val req-val-blue">${totalOut.toLocaleString()}</div>
            <div class="req-kpi-sub">正文 ${outTok.toLocaleString()} · 思考 ${thoughtTok.toLocaleString()}</div>
          </div>
          <div class="req-kpi-cell">
            <div class="req-kpi-label">本步输入</div>
            <div class="req-kpi-val">${inTok > 0 ? fmtCompactTok(inTok) : '-'}</div>
            ${inCellSub}
          </div>
        </div>
        ${anomalyHtml}
        ${errHtml}
        <div class="replay-result" id="replayResultBox" style="display:none;"></div>
      </div>
    `;
  }

  // Inspect Drawer functions
  window.inspectLog = function (idx) {
    const log = cachedLogs[idx];
    if (!log) return;
    currentInspectedLog = log;
    currentSessionTarget = log.session || '';

    const overlay = document.getElementById('inspectOverlay');
    const drawer = document.querySelector('#inspectOverlay .inspect-drawer');
    const badge = document.getElementById('drawerStatusBadge');
    const title = document.getElementById('drawerTitle');
    const subtitle = document.getElementById('drawerSubtitle');
    const sessSec = document.getElementById('inspectSessionSection');

    const isOk = log.status === 200 && !log.error;
    if (badge) {
      badge.className = `drawer-badge badge-status ${isOk ? 'ok' : 'err'}`;
      badge.textContent = log.status ? `${log.status} ${isOk ? 'OK' : 'FAIL'}` : 'ERR';
    }
    if (title) {
      title.textContent = log.sessionName || (log.path ? `接口: ${log.path}` : '请求现场诊断');
    }
    if (subtitle) {
      const parts = [log.path || '-', log.time || '-'];
      if (log.session) parts.push(log.session);
      subtitle.textContent = parts.join(' · ');
    }

    renderRequestCard(log);

    const hasSess = Boolean(log.session && !isSystemOrBackgroundLog(log));
    if (drawer) drawer.classList.toggle('drawer-wide', hasSess);
    if (hasSess) {
      updateDrawerHeaderActions(log.session);
      if (sessSec) sessSec.style.display = 'block';
      loadSessionDetail(log.session, false);
    } else {
      updateDrawerHeaderActions('');
      if (sessSec) {
        sessSec.style.display = 'none';
        sessSec.innerHTML = '';
      }
      if (cacheCountdownTimer) {
        clearInterval(cacheCountdownTimer);
        cacheCountdownTimer = null;
      }
    }

    if (overlay) overlay.style.display = 'flex';
  };

  window.closeInspectDrawer = closeInspectDrawer;
  function closeInspectDrawer() {
    const overlay = document.getElementById('inspectOverlay');
    if (overlay) overlay.style.display = 'none';
    if (cacheCountdownTimer) {
      clearInterval(cacheCountdownTimer);
      cacheCountdownTimer = null;
    }
    currentInspectedLog = null;
    currentSessionTarget = '';
  }

  function setText(id, text) {
    const el = document.getElementById(id);
    if (el) el.textContent = text;
  }

  function capitalize(str) {
    return str.charAt(0).toUpperCase() + str.slice(1);
  }

  function escapeHTML(str) {
    if (str == null) return '';
    return String(str).replace(/[&<>'"]/g, tag => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;'
    }[tag] || tag));
  }

  function formatTokens(tok) {
    if (tok >= 1000000000) return (tok / 1000000000).toFixed(2) + 'B';
    if (tok >= 1000000) return (tok / 1000000).toFixed(2) + 'M';
    if (tok >= 1000) return (tok / 1000).toFixed(1) + 'k';
    return tok;
  }

  function downloadFile(filename, content) {
    const blob = new Blob([content], { type: 'text/csv;charset=utf-8;' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }

  window.switchAccount = async function (name) {
    try {
      showToast(`正在无感热切换网关活动出口至 ${name}...`);
      await callMyGo('GatewayService.SwitchProfile', name);
      showToast(`已切换至账号 ${name}`);
      setTimeout(loadAll, 200);
    } catch (e) {
      showToast(`切换账号失败: ${e.message || e}`);
    }
  };

  window.togglePauseAccount = async function (name, paused) {
    try {
      showToast(paused ? `正在暂停账号 ${name}...` : `正在恢复账号 ${name}...`);
      await callMyGo('GatewayService.PauseProfile', name, paused);
      showToast(paused ? `已暂停账号 ${name}` : `已恢复账号 ${name}`);
      setTimeout(loadAll, 200);
    } catch (e) {
      showToast(`${paused ? '暂停' : '恢复'}账号失败: ${e.message || e}`);
    }
  };

  window.onlyAccount = async function (name) {
    try {
      showToast(`正在锁定单账号模式至 ${name}...`);
      await callMyGo('GatewayService.OnlyProfile', name);
      showToast(`已锁定单账号模式：仅使用 ${name}`);
      setTimeout(loadAll, 200);
    } catch (e) {
      showToast(`锁定单账号失败: ${e.message || e}`);
    }
  };

  window.resumeAllAccounts = async function () {
    try {
      showToast('正在恢复全部暂停账号...');
      await callMyGo('GatewayService.ResumeAllProfiles');
      showToast('已恢复全部账号参与多号容灾池');
      setTimeout(loadAll, 200);
    } catch (e) {
      showToast(`全部恢复失败: ${e.message || e}`);
    }
  };

  let addProfileMode = 'oauth';

  window.openAddProfileModal = function () {
    const overlay = document.getElementById('addProfileOverlay');
    const nameInput = document.getElementById('addProfileNameInput');
    const rtInput = document.getElementById('addProfileRefreshTokenInput');
    const emailInput = document.getElementById('addProfileEmailInput');
    const feedback = document.getElementById('addProfileFeedback');
    if (nameInput) nameInput.value = '';
    if (rtInput) rtInput.value = '';
    if (emailInput) emailInput.value = '';
    if (feedback) {
      feedback.style.display = 'none';
      feedback.textContent = '';
    }
    window.switchAddProfileMode('oauth');
    if (overlay) overlay.style.display = 'flex';
    if (nameInput) setTimeout(() => nameInput.focus(), 50);
  };

  window.closeAddProfileModal = function () {
    const overlay = document.getElementById('addProfileOverlay');
    if (overlay) overlay.style.display = 'none';
  };

  window.switchAddProfileMode = function (mode) {
    addProfileMode = mode || 'oauth';
    document.querySelectorAll('#addModeTabs .add-mode-btn').forEach(btn => {
      btn.classList.toggle('active', btn.dataset.mode === addProfileMode);
    });
    const tokenFields = document.getElementById('addProfileTokenFields');
    if (tokenFields) {
      tokenFields.style.display = addProfileMode === 'token' ? 'flex' : 'none';
    }
    const hint = document.getElementById('addProfileModeHint');
    if (hint) {
      if (addProfileMode === 'oauth') {
        hint.textContent = '提交后将打开系统浏览器完成 Google OAuth 登录并自动拉取邮箱与配额（最长等待 180 秒）。需已配置 ~/.agyp/oauth.json。';
      } else if (addProfileMode === 'keychain') {
        hint.textContent = '从 macOS 钥匙串读取当前正在使用的 agy 官方登录凭据并导入到 agyp 账号池。';
      } else {
        hint.textContent = '直接粘贴已有 Google OAuth Refresh Token，网关将实时验证有效性并写入本地隔离配置。';
      }
    }
  };

  window.submitAddProfile = async function () {
    const nameEl = document.getElementById('addProfileNameInput');
    const rtEl = document.getElementById('addProfileRefreshTokenInput');
    const emailEl = document.getElementById('addProfileEmailInput');
    const feedback = document.getElementById('addProfileFeedback');
    const btn = document.getElementById('btnSubmitAddProfile');

    const name = (nameEl?.value || '').trim();
    const showErr = (msg) => {
      if (!feedback) return;
      feedback.className = 'add-form-feedback is-error';
      feedback.textContent = msg;
      feedback.style.display = 'block';
    };
    const showInfo = (msg) => {
      if (!feedback) return;
      feedback.className = 'add-form-feedback is-info';
      feedback.textContent = msg;
      feedback.style.display = 'block';
    };

    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/.test(name)) {
      showErr('账号别名需以字母或数字开头，仅含字母/数字/._-，长度 1~32');
      return;
    }

    const rt = (rtEl?.value || '').trim();
    const email = (emailEl?.value || '').trim();
    if (addProfileMode === 'token' && !rt) {
      showErr('请填写 Refresh Token');
      return;
    }

    if (btn) btn.disabled = true;
    try {
      let res;
      if (addProfileMode === 'oauth') {
        showInfo('正在打开浏览器等待 Google 授权，请在浏览器中完成登录...');
        res = await callMyGo('GatewayService.AddProfileOAuth', name);
      } else if (addProfileMode === 'keychain') {
        showInfo('正在从 macOS 钥匙串读取并验证凭据...');
        res = await callMyGo('GatewayService.ImportKeychainProfile', name);
      } else {
        showInfo('正在校验 Refresh Token 并写入账号配置...');
        res = await callMyGo('GatewayService.AddProfileToken', name, rt, email);
      }
      if (res && res.success === false) {
        showErr(res.message || '添加账号失败');
        return;
      }
      window.closeAddProfileModal();
      showToast(`已成功添加账号 ${name} ✓`);
      setTimeout(loadAll, 250);
    } catch (e) {
      showErr(e.message || String(e));
    } finally {
      if (btn) btn.disabled = false;
    }
  };

  window.renameAccount = async function (oldName) {
    const newName = await showPromptDialog({
      title: '重命名账号',
      message: `请输入账号 "${oldName}" 的新别名：`,
      defaultValue: oldName,
      placeholder: '仅限字母、数字、._-',
      confirmText: '保存',
    });
    if (newName == null) return;
    const clean = newName.trim();
    if (!clean || clean === oldName) return;
    try {
      showToast(`正在重命名 ${oldName} -> ${clean}...`);
      const res = await callMyGo('GatewayService.RenameProfile', oldName, clean);
      if (res && res.success === false) {
        showToast(`重命名失败: ${res.message}`);
        return;
      }
      showToast(`已重命名为 ${clean}`);
      setTimeout(loadAll, 200);
    } catch (e) {
      showToast(`重命名失败: ${e.message || e}`);
    }
  };

  window.removeAccount = async function (name, totalCount) {
    const isLast = Number(totalCount || 0) <= 1;
    const ok = await showConfirmDialog({
      title: isLast ? '清空最后一个账号' : '移除账号',
      message: isLast
        ? `"${name}" 是当前最后一个账号，确认删除并清空账号池吗？`
        : `确认移除账号 "${name}" 吗？（仅移除本地凭据映射，不影响历史用量统计）`,
      confirmText: '确认删除',
      danger: true,
    });
    if (!ok) return;
    try {
      showToast(`正在移除账号 ${name}...`);
      const res = await callMyGo('GatewayService.RemoveProfile', name, isLast);
      if (res && res.success === false) {
        showToast(`移除失败: ${res.message}`);
        return;
      }
      showToast(`已移除账号 ${name}`);
      setTimeout(loadAll, 200);
    } catch (e) {
      showToast(`移除失败: ${e.message || e}`);
    }
  };

  window.copyCode = function (text) {
    if (!navigator.clipboard) {
      const ta = document.createElement('textarea');
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
    } else {
      navigator.clipboard.writeText(text);
    }
    showToast('已复制到剪贴板: ' + text);
  };
})();
