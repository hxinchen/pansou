(function () {
  'use strict';

  var API = {
    overview: '/api/admin/overview',
    trends: '/api/admin/trends',
    resources: '/api/admin/resources',
    keywords: '/api/admin/keywords',
    keywordAPISources: '/api/admin/keyword-api-sources',
    keywordAPISourceTest: '/api/admin/keyword-api-sources/test',
    keywordAPISyncRuns: '/api/admin/keyword-api-sync-runs',
    runs: '/api/admin/runs',
    users: '/api/admin/users',
    usageOverview: '/api/admin/usage/overview',
    usageTrends: '/api/admin/usage/trends',
    usageLogs: '/api/admin/usage/logs',
    sourceCatalog: '/api/admin/search-sources/catalog',
    sourceConfig: '/api/admin/search-sources/config',
    sourceValidate: '/api/admin/search-sources/validate',
    credentials: '/api/admin/plugin-credentials',
    userCredentials: '/api/admin/user-plugin-credentials',
    credentialLoginFlows: '/api/admin/plugin-credentials/login-flows'
  };

  var TOKEN_KEY = 'pansou.admin.token';
  var USER_KEY = 'pansou.admin.user';
  var ROLE_KEY = 'pansou.admin.role';
  var PAGE_SIZE = 20;
  var ACTIVE_STATUSES = ['pending', 'running'];
  var ACTIVE_KEYWORD_SYNC_STATUSES = ['queued', 'running'];
  var OVERVIEW_DAYS = 7;
  var OVERVIEW_ACTIVE_REFRESH_MS = 5000;
  var OVERVIEW_IDLE_REFRESH_MS = 30000;
  var OVERVIEW_FAST_REFRESH_MS = 2000;
  var OVERVIEW_FAST_REFRESH_LIMIT = 3;
  var viewLabels = {
    overview: '数据概览',
    resources: '资源库',
    keywords: '关键词',
    runs: '采集任务',
    'run-detail': '任务详情',
    users: '用户管理',
    usage: 'API 监控',
    sources: '搜索来源'
  };

  var state = {
    view: 'overview',
    token: localStorage.getItem(TOKEN_KEY) || '',
    username: localStorage.getItem(USER_KEY) || '管理员',
    role: localStorage.getItem(ROLE_KEY) || '',
    loaded: {},
    charts: {},
    overview: null,
    overviewTrends: [],
    overviewRefresh: { timer: null, controller: null, serial: 0, fastRetryCount: 0 },
    resources: { items: [], page: 1, pageSize: PAGE_SIZE, total: 0, pages: 1, query: {} },
    keywords: { items: [], page: 1, pageSize: PAGE_SIZE, total: 0, pages: 1, selected: new Set(), query: {}, tab: 'list' },
    keywordSources: { items: [], loaded: false, editing: null, testResponse: null, selectedPath: '', testedSignature: '', originalSignature: '', requestSerial: 0, pollTimer: null, editorModes: { query: 'kv', header: 'kv', form: 'kv' } },
    keywordSyncRuns: { items: [], page: 1, pageSize: PAGE_SIZE, total: 0, pages: 1, query: {}, loaded: false, requestSerial: 0, detailID: null, detailPollTimer: null, detailController: null, iterationController: null, iterations: [], iterationPage: 1, iterationPages: 1 },
    runs: { items: [], page: 1, pageSize: PAGE_SIZE, total: 0, pages: 1, query: {} },
    users: { items: [], page: 1, pageSize: PAGE_SIZE, total: 0, pages: 1, query: {} },
    usage: { overview: null, trends: [], logs: [], page: 1, pageSize: PAGE_SIZE, total: 0, pages: 1, range: '7d', query: {} },
    sources: { catalog: [], config: null, credentials: [], adminCredentials: [], tab: 'admin', configTab: 'tg', dirty: false, saving: false, sharedTotal: 0, pluginSearch: '', credentialEditing: null, credentialEditMode: 'login', loginFlow: null, loginFlowTimer: null, credentialQuery: {} },
    runPicker: { items: [], selected: new Set(), search: '', page: 1, pages: 1, total: 0, loading: false, controller: null, searchTimer: null },
    runDetail: { id: null, summary: null, items: [], page: 1, pages: 1, loadedPages: 0, total: 0, loading: false, query: {}, controller: null, itemsController: null, pollController: null, sourceControllers: {}, observer: null },
    pollTimer: null,
    detailPollTimer: null,
    requestSerial: { resources: 0, keywords: 0, runs: 0, users: 0, usage: 0, usageLogs: 0, sources: 0 },
    requestControllers: {},
    confirmCallback: null
  };

  var statusLabels = {
    pending: '待处理',
    queued: '排队中',
    running: '运行中',
    valid: '有效',
    invalid: '失效',
    unknown: '未知',
    unsupported: '不支持',
    success: '成功',
    success_empty: '无结果',
    failed: '失败',
    partial: '部分成功',
    interrupted: '已中断',
    cancelled: '已取消',
    legacy: '升级前记录',
    active: '可用',
    expired: '已过期',
    disabled: '已停用',
    admin_suspended: '管理员暂停'
  };

  var diskLabels = {
    baidu: '百度',
    aliyun: '阿里云',
    quark: '夸克',
    guangya: '光鸭',
    tianyi: '天翼',
    uc: 'UC',
    mobile: '移动云盘',
    '115': '115',
    pikpak: 'PikPak',
    xunlei: '迅雷',
    '123': '123',
    magnet: '磁力',
    ed2k: '电驴',
    others: '其他'
  };

  var keywordTypeLabels = {
    general: '通用',
    movie: '电影',
    series: '剧集',
    book: '图书',
    software: '软件',
    course: '课程'
  };

  var triggerLabels = {
    scheduled: '自动调度',
    schedule: '自动调度',
    manual: '手动',
    save: '保存并同步',
    legacy: '升级前记录',
    external: '外部搜索'
  };

  var sourceTypeLabels = {
    manual: '手动',
    api: 'API',
    import: '导入',
    tg: 'Telegram',
    plugin: '插件',
    external: '外部搜索'
  };

  function byId(id) {
    return document.getElementById(id);
  }

  function pick(object, keys, fallback) {
    if (!object || typeof object !== 'object') return fallback;
    for (var i = 0; i < keys.length; i += 1) {
      if (Object.prototype.hasOwnProperty.call(object, keys[i]) && object[keys[i]] !== null && object[keys[i]] !== undefined) {
        return object[keys[i]];
      }
    }
    return fallback;
  }

  function arrayFrom(data, keys) {
    if (Array.isArray(data)) return data;
    var value = pick(data, keys, []);
    return Array.isArray(value) ? value : [];
  }

  function numberValue(value, fallback) {
    var parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : (fallback || 0);
  }

  function boolValue(value, fallback) {
    if (typeof value === 'boolean') return value;
    if (value === 1 || value === '1' || value === 'true') return true;
    if (value === 0 || value === '0' || value === 'false') return false;
    return Boolean(fallback);
  }

  function escapeHTML(value) {
    return String(value === null || value === undefined ? '' : value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  function icon(name, className) {
    return '<i data-lucide="' + escapeHTML(name) + '"' + (className ? ' class="' + escapeHTML(className) + '"' : '') + ' aria-hidden="true"></i>';
  }

  function refreshIcons(root) {
    if (window.lucide && typeof window.lucide.createIcons === 'function') {
      window.lucide.createIcons({ attrs: { 'stroke-width': 1.8 }, root: root || document });
    }
  }

  function formatNumber(value) {
    return new Intl.NumberFormat('zh-CN').format(numberValue(value, 0));
  }

  function formatBytes(value) {
    var bytes = Math.max(0, numberValue(value, 0));
    if (bytes < 1024) return formatNumber(bytes) + ' B';
    if (bytes < 1024 * 1024) return (Math.round(bytes / 102.4) / 10) + ' KB';
    return (Math.round(bytes / 1024 / 102.4) / 10) + ' MB';
  }

  function toDate(value) {
    if (value === null || value === undefined || value === '') return null;
    if (value instanceof Date) return Number.isNaN(value.getTime()) ? null : value;
    if (typeof value === 'number') {
      return new Date(value < 100000000000 ? value * 1000 : value);
    }
    if (/^\d+$/.test(String(value))) {
      var numeric = Number(value);
      return new Date(numeric < 100000000000 ? numeric * 1000 : numeric);
    }
    var parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? null : parsed;
  }

  function formatDate(value, includeTime) {
    var date = toDate(value);
    if (!date) return '—';
    var options = includeTime === false
      ? { year: 'numeric', month: '2-digit', day: '2-digit' }
      : { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false };
    return new Intl.DateTimeFormat('zh-CN', options).format(date).replace(/\//g, '-');
  }

  function relativeTime(value) {
    var date = toDate(value);
    if (!date) return '—';
    var seconds = Math.round((date.getTime() - Date.now()) / 1000);
    var absolute = Math.abs(seconds);
    var unit = 'second';
    var amount = seconds;
    if (absolute >= 86400) {
      unit = 'day';
      amount = Math.round(seconds / 86400);
    } else if (absolute >= 3600) {
      unit = 'hour';
      amount = Math.round(seconds / 3600);
    } else if (absolute >= 60) {
      unit = 'minute';
      amount = Math.round(seconds / 60);
    }
    try {
      return new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' }).format(amount, unit);
    } catch (error) {
      return formatDate(date, true);
    }
  }

  function durationFrom(item) {
    var milliseconds = pick(item, ['duration_ms', 'elapsed_ms'], null);
    var seconds = pick(item, ['duration_seconds', 'elapsed_seconds', 'duration'], null);
    if (milliseconds !== null) seconds = numberValue(milliseconds) / 1000;
    if (seconds === null || seconds === undefined) {
      var start = toDate(pick(item, ['started_at', 'start_time', 'created_at'], null));
      var end = toDate(pick(item, ['finished_at', 'ended_at', 'completed_at'], null));
      if (start) seconds = ((end || new Date()).getTime() - start.getTime()) / 1000;
    }
    if (seconds === null || seconds === undefined || !Number.isFinite(Number(seconds))) return '—';
    seconds = Math.max(0, Math.round(Number(seconds)));
    if (seconds < 60) return seconds + ' 秒';
    var minutes = Math.floor(seconds / 60);
    var remaining = seconds % 60;
    if (minutes < 60) return minutes + ' 分 ' + remaining + ' 秒';
    var hours = Math.floor(minutes / 60);
    return hours + ' 小时 ' + (minutes % 60) + ' 分';
  }

  function normalizedStatus(item) {
    var value = pick(item, ['status', 'check_status', 'state'], 'unknown');
    if (value === 'ok') return 'valid';
    if (value === 'bad') return 'invalid';
    if (value === 'uncertain') return 'unknown';
    return String(value || 'unknown').toLowerCase();
  }

  function statusBadge(status) {
    var normalized = String(status || 'unknown').toLowerCase();
    return '<span class="status-badge status-' + escapeHTML(normalized) + '">' + escapeHTML(statusLabels[normalized] || normalized) + '</span>';
  }

  function runProgress(run) {
    var total = numberValue(pick(run, ['total_items', 'total_keywords', 'keyword_count', 'total'], 0));
    var completed = numberValue(pick(run, ['completed_items', 'completed_keywords', 'processed', 'completed'], 0));
    var explicit = pick(run, ['progress', 'progress_percent', 'percent'], null);
    var percent = explicit !== null ? numberValue(explicit) : (total > 0 ? completed / total * 100 : 0);
    if (percent > 0 && percent <= 1) percent *= 100;
    percent = Math.min(100, Math.max(0, Math.round(percent)));
    return { total: total, completed: completed, percent: percent };
  }

  function paginationMeta(data, items, page, pageSize) {
    var pagination = pick(data, ['pagination', 'meta'], {});
    var total = numberValue(pick(data, ['total', 'total_count', 'count'], pick(pagination, ['total', 'total_count'], items.length)));
    var currentPage = numberValue(pick(data, ['page', 'current_page'], pick(pagination, ['page', 'current_page'], page)), page);
    var size = numberValue(pick(data, ['page_size', 'per_page', 'limit'], pick(pagination, ['page_size', 'per_page', 'limit'], pageSize)), pageSize);
    var pages = numberValue(pick(data, ['total_pages', 'pages'], pick(pagination, ['total_pages', 'pages'], Math.max(1, Math.ceil(total / size)))), 1);
    return { total: total, page: currentPage, pageSize: size, pages: Math.max(1, pages) };
  }

  function queryString(query) {
    var params = new URLSearchParams();
    Object.keys(query || {}).forEach(function (key) {
      var value = query[key];
      if (value === undefined || value === null || value === '') return;
      if (Array.isArray(value)) {
        value.forEach(function (item) { params.append(key, item); });
      } else {
        params.set(key, String(value));
      }
    });
    var result = params.toString();
    return result ? '?' + result : '';
  }

  function APIError(message, status, payload) {
    this.name = 'APIError';
    this.message = message || '请求失败';
    this.status = status || 0;
    this.payload = payload;
  }
  APIError.prototype = Object.create(Error.prototype);

  async function apiRequest(path, options) {
    options = options || {};
    var headers = Object.assign({ Accept: 'application/json' }, options.headers || {});
    if (options.auth !== false && state.token) headers.Authorization = 'Bearer ' + state.token;
    if (options.body !== undefined && options.body !== null && !(options.body instanceof FormData)) {
      headers['Content-Type'] = 'application/json';
    }

    var response;
    try {
      response = await fetch(path + queryString(options.query), {
        method: options.method || 'GET',
        headers: headers,
        body: options.body === undefined || options.body === null
          ? undefined
          : (options.body instanceof FormData ? options.body : JSON.stringify(options.body)),
        signal: options.signal
      });
    } catch (error) {
      if (error && error.name === 'AbortError') throw error;
      throw new APIError('无法连接到 PanSou 服务', 0, null);
    }

    var text = await response.text();
    var payload = null;
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch (error) {
        payload = { message: text };
      }
    }

    var wrappedCode = payload && pick(payload, ['code'], null);
    var numericCode = Number(wrappedCode);
    var isWrappedError = wrappedCode !== null && Number.isFinite(numericCode) && numericCode >= 400;
    if (!response.ok || isWrappedError) {
      var status = isWrappedError ? numericCode : response.status;
      var message = pick(payload, ['message', 'error', 'detail'], response.statusText || '请求失败');
      if (status === 401 && !options.skipAuthRedirect) expireSession('登录状态已失效，请重新登录');
      if (status === 403 && String(path).indexOf('/api/admin/') === 0 && !options.skipAuthRedirect) {
        expireSession('当前账号需要管理员权限，请使用管理员账号登录');
      }
      throw new APIError(String(message), status, payload);
    }

    if (payload && Object.prototype.hasOwnProperty.call(payload, 'data')) return payload.data;
    return payload === null ? {} : payload;
  }

  function replaceRequestController(scope) {
    if (state.requestControllers[scope]) state.requestControllers[scope].abort();
    var controller = new AbortController();
    state.requestControllers[scope] = controller;
    return controller;
  }

  function finishRequestController(scope, controller) {
    if (state.requestControllers[scope] === controller) state.requestControllers[scope] = null;
  }

  function cancelViewRequests(view) {
    var scopes = view === 'keywords' ? ['keywords', 'keywordSyncRuns'] : (view === 'usage' ? ['usage', 'usageLogs'] : [view]);
    scopes.forEach(function (scope) {
      if (state.requestControllers[scope]) state.requestControllers[scope].abort();
      state.requestControllers[scope] = null;
    });
  }

  async function apiFallback(requests) {
    var lastError = null;
    for (var i = 0; i < requests.length; i += 1) {
      try {
        return await apiRequest(requests[i].path, requests[i].options || {});
      } catch (error) {
        lastError = error;
        if (!(error instanceof APIError) || (error.status !== 404 && error.status !== 405)) throw error;
      }
    }
    throw lastError || new APIError('请求失败', 0);
  }

  function setButtonLoading(button, loading, label) {
    if (!button) return;
    if (loading) {
      button.dataset.originalHtml = button.innerHTML;
      button.disabled = true;
      button.innerHTML = icon('loader-circle', 'spin') + '<span>' + escapeHTML(label || '处理中') + '</span>';
    } else {
      button.disabled = false;
      if (button.dataset.originalHtml) {
        button.innerHTML = button.dataset.originalHtml;
        delete button.dataset.originalHtml;
      }
    }
    refreshIcons(button);
  }

  function showAlert(id, message, type) {
    var element = byId(id);
    if (!element) return;
    if (!message) {
      element.hidden = true;
      element.textContent = '';
      return;
    }
    element.className = 'inline-alert ' + (type || 'error');
    element.textContent = message;
    element.hidden = false;
  }

  function toast(message, type) {
    type = type || 'info';
    var region = byId('toast-region');
    var node = document.createElement('div');
    node.className = 'toast ' + type;
    node.innerHTML = icon(type === 'success' ? 'circle-check' : type === 'error' ? 'circle-alert' : 'info') +
      '<span>' + escapeHTML(message) + '</span>' +
      '<button type="button" aria-label="关闭通知">' + icon('x') + '</button>';
    region.appendChild(node);
    refreshIcons(node);
    var remove = function () {
      if (node.parentNode) node.parentNode.removeChild(node);
    };
    node.querySelector('button').addEventListener('click', remove);
    window.setTimeout(remove, 4200);
  }

  function tableLoading(bodyId, columns) {
    byId(bodyId).innerHTML = '<tr class="table-loading"><td colspan="' + columns + '"><span>' + icon('loader-circle', 'spin') + '正在加载</span></td></tr>';
    refreshIcons(byId(bodyId));
  }

  function closeAllDialogs() {
    document.querySelectorAll('dialog[open]').forEach(function (dialog) {
      dialog.close();
    });
    stopDetailPolling();
  }

  function showLogin(message) {
    stopOverviewRefresh(true);
    byId('app-shell').hidden = true;
    byId('login-view').hidden = false;
    var error = byId('login-error');
    error.textContent = message || '';
    error.hidden = !message;
    window.setTimeout(function () { byId('login-username').focus(); }, 0);
    closeAllDialogs();
    stopRunPolling();
  }

  function showApp() {
    byId('login-view').hidden = true;
    byId('app-shell').hidden = false;
    byId('account-name').textContent = state.username || '管理员';
    byId('account-avatar').textContent = (state.username || 'A').trim().charAt(0).toUpperCase() || 'A';
    refreshIcons();
  }

  function expireSession(message) {
    state.token = '';
    state.role = '';
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(USER_KEY);
    localStorage.removeItem(ROLE_KEY);
    showLogin(message);
  }

  async function submitLogin(event) {
    event.preventDefault();
    var username = byId('login-username').value.trim();
    var password = byId('login-password').value;
    var error = byId('login-error');
    error.hidden = true;
    if (!username || !password) {
      error.textContent = '请输入用户名和密码';
      error.hidden = false;
      return;
    }

    var button = byId('login-submit');
    setButtonLoading(button, true, '登录中');
    try {
      var result = await apiRequest('/api/auth/login', {
        method: 'POST',
        body: { username: username, password: password },
        auth: false,
        skipAuthRedirect: true
      });
      var token = pick(result, ['token', 'access_token', 'jwt'], '');
      if (!token) throw new APIError('登录响应中缺少令牌', 500, result);
      state.token = token;
      state.username = pick(result, ['username', 'user_name'], username);
      state.role = pick(result, ['role'], pick(pick(result, ['user'], {}), ['role'], ''));
      localStorage.setItem(TOKEN_KEY, state.token);
      localStorage.setItem(USER_KEY, state.username);
      if (state.role) localStorage.setItem(ROLE_KEY, state.role);
      showApp();
      navigate(location.hash.slice(1) || 'overview', true);
      checkHealth();
    } catch (loginError) {
      error.textContent = loginError.message || '登录失败';
      error.hidden = false;
    } finally {
      setButtonLoading(button, false);
    }
  }

  async function logout() {
    var token = state.token;
    expireSession('');
    if (token) {
      try {
        await fetch('/api/auth/logout', { method: 'POST', headers: { Authorization: 'Bearer ' + token } });
      } catch (error) {
        // Logout is client-side; a network failure does not retain the local token.
      }
    }
  }

  async function restoreSession() {
    if (!state.token) {
      showLogin('');
      return;
    }
    var sessionToken = state.token;
    showApp();
    navigate(location.hash.slice(1) || 'overview', true);
    checkHealth();
    try {
      var verified = await apiRequest('/api/auth/verify', { method: 'POST', skipAuthRedirect: true });
      if (!state.token || state.token !== sessionToken) return;
      state.role = pick(verified, ['role'], pick(pick(verified, ['user'], {}), ['role'], state.role));
      if (state.role) localStorage.setItem(ROLE_KEY, state.role);
    } catch (error) {
      if (error instanceof APIError && error.status === 401) expireSession('登录状态已失效，请重新登录');
    }
  }

  async function checkHealth() {
    var dot = byId('health-dot');
    var label = byId('health-label');
    var detail = byId('health-detail');
    try {
      var health = await apiRequest('/api/health', { auth: false, skipAuthRedirect: true });
      dot.className = 'status-dot online';
      label.textContent = '服务正常';
      var database = pick(health, ['database', 'database_status', 'db_status'], '');
      detail.textContent = database ? '数据库 ' + database : 'API 服务';
    } catch (error) {
      dot.className = 'status-dot offline';
      label.textContent = '服务不可用';
      detail.textContent = error.message || '连接失败';
    }
  }

  function navigate(view, force) {
    var runMatch = String(view || '').match(/^runs\/(\d+)$/);
    if (runMatch) { state.runDetail.id = runMatch[1]; view = 'run-detail'; }
    if (!viewLabels[view]) view = 'overview';
    if (!force && state.view === view && state.loaded[view]) return;
    var previousView = state.view;
    if (previousView === 'overview' && view !== 'overview') stopOverviewRefresh(true);
    if (previousView === 'run-detail' && view !== 'run-detail') cancelRunDetailRequests();
    if (previousView !== view && previousView !== 'run-detail') cancelViewRequests(previousView);
    state.view = view;
    var targetHash = view === 'run-detail' ? '#runs/' + state.runDetail.id : '#' + view;
    if (location.hash !== targetHash) history.replaceState(null, '', targetHash);

    document.querySelectorAll('.view').forEach(function (section) {
      var active = section.dataset.page === view;
      section.hidden = !active;
      section.classList.toggle('active', active);
    });
    document.querySelectorAll('[data-view]').forEach(function (link) {
      link.classList.toggle('active', link.dataset.view === view);
      if (link.dataset.view === view) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    byId('current-view-label').textContent = viewLabels[view];
    closeMenu();
    stopDetailPolling();
    if (view !== 'keywords') stopKeywordSourcePolling();
    loadView(view, force || (view === 'overview' && previousView !== 'overview'));
    byId('main-content').focus({ preventScroll: true });
    window.setTimeout(resizeCharts, 50);
  }

  function loadView(view, force) {
    if (view === 'overview') return loadOverview(force);
    if (view === 'resources') return loadResources(force);
    if (view === 'keywords') {
      if (state.keywords.tab === 'api') return loadKeywordSources(force);
      if (state.keywords.tab === 'history') return loadKeywordSyncRuns({ force: force });
      return loadKeywords(force);
    }
    if (view === 'runs') return loadRuns({ force: force });
    if (view === 'run-detail') return loadRunPage(state.runDetail.id);
    if (view === 'users') return loadUsers(force);
    if (view === 'usage') return loadUsage(force);
    if (view === 'sources') return loadSources(force);
  }

  function refreshCurrentView() {
    if (state.view !== 'overview') state.loaded[state.view] = false;
    var button = document.querySelector('[data-action="refresh-view"] svg');
    if (button) button.classList.add('spin');
    var request = state.view === 'overview'
      ? loadOverview(true, { forceRefresh: true })
      : loadView(state.view, true);
    Promise.resolve(request).finally(function () {
      if (button) button.classList.remove('spin');
      checkHealth();
    });
  }

  function updateTimestamp(value) {
    var date = arguments.length ? toDate(value) : new Date();
    if (!date) {
      byId('last-updated').textContent = '更新时间未知';
      return;
    }
    byId('last-updated').textContent = '更新于 ' + new Intl.DateTimeFormat('zh-CN', {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false
    }).format(date);
  }

  function clearOverviewRefreshTimer() {
    if (!state.overviewRefresh.timer) return;
    window.clearTimeout(state.overviewRefresh.timer);
    state.overviewRefresh.timer = null;
  }

  function setOverviewRefreshStatus(refreshing) {
    var element = byId('overview-refresh-status');
    if (!element) return;
    var visible = Boolean(refreshing && state.view === 'overview' && state.overview);
    element.hidden = !visible;
    element.innerHTML = visible ? icon('loader-circle', 'spin') + '<span>正在更新</span>' : '';
    if (visible) refreshIcons(element);
  }

  function stopOverviewRefresh(abortRequest) {
    clearOverviewRefreshTimer();
    if (abortRequest && state.overviewRefresh.controller) {
      state.overviewRefresh.serial += 1;
      state.overviewRefresh.controller.abort();
      state.overviewRefresh.controller = null;
    }
    setOverviewRefreshStatus(false);
  }

  function overviewCanRefresh() {
    return Boolean(state.token && state.view === 'overview' && document.visibilityState === 'visible');
  }

  function overviewGeneratedAt(data) {
    return pick(data, ['generated_at', 'generatedAt'], null);
  }

  function overviewHasActiveRun(data) {
    var run = pick(data, ['active_run', 'active_batch', 'current_run', 'current_batch'], null);
    var status = String(pick(run, ['status', 'state'], '')).toLowerCase();
    return ACTIVE_STATUSES.indexOf(status) !== -1;
  }

  function formatOverviewClock(value) {
    var date = toDate(value);
    if (!date) return '未知时间';
    return new Intl.DateTimeFormat('zh-CN', {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false
    }).format(date);
  }

  function updateOverviewFreshness(data, error) {
    var generatedAt = overviewGeneratedAt(data);
    var cacheLabel = formatOverviewClock(generatedAt);
    if (error) {
      showAlert('overview-alert', '数据暂未更新，当前显示 ' + cacheLabel + ' 缓存。' + (error.message || '刷新失败'), 'warning');
      return;
    }
    if (boolValue(pick(data, ['stale'], false), false)) {
      var message = '数据暂未更新，当前显示 ' + cacheLabel + ' 缓存';
      if (boolValue(pick(data, ['refreshing'], false), false)) message += '，后台正在刷新';
      showAlert('overview-alert', message, 'warning');
      return;
    }
    showAlert('overview-alert', '');
  }

  function scheduleOverviewRefresh(data) {
    clearOverviewRefreshTimer();
    if (!overviewCanRefresh()) return;

    var stale = boolValue(pick(data, ['stale'], false), false);
    var refreshing = boolValue(pick(data, ['refreshing'], false), false);
    var delay;
    if (stale && refreshing && state.overviewRefresh.fastRetryCount < OVERVIEW_FAST_REFRESH_LIMIT) {
      state.overviewRefresh.fastRetryCount += 1;
      delay = OVERVIEW_FAST_REFRESH_MS;
    } else {
      if (!stale || !refreshing) state.overviewRefresh.fastRetryCount = 0;
      delay = overviewHasActiveRun(data) ? OVERVIEW_ACTIVE_REFRESH_MS : OVERVIEW_IDLE_REFRESH_MS;
    }

    state.overviewRefresh.timer = window.setTimeout(function () {
      state.overviewRefresh.timer = null;
      loadOverview(true, { background: true });
    }, delay);
  }

  function openMenu() {
    byId('sidebar').classList.add('open');
    byId('sidebar-backdrop').hidden = false;
  }

  function closeMenu() {
    byId('sidebar').classList.remove('open');
    byId('sidebar-backdrop').hidden = true;
  }

  function resizeCharts() {
    Object.keys(state.charts).forEach(function (key) {
      if (state.charts[key] && typeof state.charts[key].resize === 'function') state.charts[key].resize();
    });
  }

  function chartFor(id) {
    var element = byId(id);
    if (!element) return null;
    if (!window.echarts) {
      element.innerHTML = '<div class="no-active-batch"><span>图表组件加载失败</span></div>';
      return null;
    }
    if (!state.charts[id]) state.charts[id] = window.echarts.init(element, null, { renderer: 'canvas' });
    return state.charts[id];
  }

  function renderPagination(id, page, pages, scope) {
    var container = byId(id);
    if (pages <= 1) {
      container.innerHTML = '';
      return;
    }
    var values = [];
    var start = Math.max(1, page - 2);
    var end = Math.min(pages, page + 2);
    if (start > 1) values.push(1);
    if (start > 2) values.push('ellipsis-left');
    for (var value = start; value <= end; value += 1) values.push(value);
    if (end < pages - 1) values.push('ellipsis-right');
    if (end < pages) values.push(pages);

    var html = '<button class="page-button" type="button" data-page-scope="' + scope + '" data-page="' + (page - 1) + '" aria-label="上一页"' + (page <= 1 ? ' disabled' : '') + '>' + icon('chevron-left') + '</button>';
    values.forEach(function (item) {
      if (typeof item === 'string') {
        html += '<span class="page-button" aria-hidden="true">…</span>';
      } else {
        html += '<button class="page-button' + (item === page ? ' active' : '') + '" type="button" data-page-scope="' + scope + '" data-page="' + item + '"' + (item === page ? ' aria-current="page"' : '') + '>' + item + '</button>';
      }
    });
    html += '<button class="page-button" type="button" data-page-scope="' + scope + '" data-page="' + (page + 1) + '" aria-label="下一页"' + (page >= pages ? ' disabled' : '') + '>' + icon('chevron-right') + '</button>';
    container.innerHTML = html;
    refreshIcons(container);
  }

  function debounce(callback, delay) {
    var timeout;
    return function () {
      var args = arguments;
      clearTimeout(timeout);
      timeout = setTimeout(function () { callback.apply(null, args); }, delay);
    };
  }

  function copyText(text, label) {
    if (!text) return;
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(function () {
        toast((label || '内容') + '已复制', 'success');
      }).catch(function () {
        fallbackCopy(text, label);
      });
    } else {
      fallbackCopy(text, label);
    }
  }

  function fallbackCopy(text, label) {
    var area = document.createElement('textarea');
    area.value = text;
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.appendChild(area);
    area.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (error) { ok = false; }
    document.body.removeChild(area);
    toast(ok ? (label || '内容') + '已复制' : '复制失败，请手动复制', ok ? 'success' : 'error');
  }

  function safeExternalURL(value) {
    var text = String(value || '').trim();
    if (/^(https?:|magnet:|ed2k:)/i.test(text)) return text;
    return '';
  }

  function setupDialogBehavior() {
    document.querySelectorAll('dialog').forEach(function (dialog) {
      dialog.addEventListener('click', function (event) {
        if (event.target === dialog) {
          if (dialog.id === 'credential-dialog') attemptCloseCredentials();
          else dialog.close();
        }
      });
      dialog.addEventListener('cancel', function (event) {
        if (dialog.id === 'credential-dialog') {
          event.preventDefault();
          attemptCloseCredentials();
        }
      });
      dialog.addEventListener('close', function () {
        if (dialog.id === 'resource-dialog') cancelResourceDetailRequests(dialog);
        if (dialog.id === 'run-dialog') {
          if (state.runPicker.controller) state.runPicker.controller.abort();
          clearTimeout(state.runPicker.searchTimer);
          state.runPicker.controller = null;
          state.runPicker.loading = false;
        }
        if (dialog.id === 'keyword-sync-detail-dialog') {
          stopKeywordSyncDetailPolling();
          cancelKeywordSyncDetailRequests();
          state.keywordSyncRuns.detailID = null;
        }
      });
    });
  }

  function confirmAction(title, message, confirmLabel) {
    return new Promise(function (resolve) {
      var dialog = byId('confirm-dialog');
      byId('confirm-title').textContent = title;
      byId('confirm-message').textContent = message;
      byId('confirm-submit').textContent = confirmLabel || '确认';
      state.confirmCallback = resolve;
      dialog.showModal();
    });
  }

  function settleConfirm(result) {
    var callback = state.confirmCallback;
    state.confirmCallback = null;
    byId('confirm-dialog').close();
    if (callback) callback(result);
  }

  async function loadOverview(force, options) {
    options = options || {};
    if (state.loaded.overview && !force) {
      scheduleOverviewRefresh(state.overview || {});
      return;
    }
    if (!overviewCanRefresh()) {
      stopOverviewRefresh(true);
      return;
    }

    clearOverviewRefreshTimer();
    if (options.forceRefresh) state.overviewRefresh.fastRetryCount = 0;

    var hasExistingData = Boolean(state.overview);
    if (!hasExistingData) {
      showAlert('overview-alert', '');
      byId('stat-grid').innerHTML = '<article class="stat-card skeleton-card"></article>'.repeat(4);
      byId('recent-runs-body').innerHTML = '<tr class="table-loading"><td colspan="7"><span>' + icon('loader-circle', 'spin') + '正在加载</span></td></tr>';
      refreshIcons();
    } else {
      setOverviewRefreshStatus(true);
    }

    if (state.overviewRefresh.controller) state.overviewRefresh.controller.abort();
    var controller = new AbortController();
    var serial = state.overviewRefresh.serial + 1;
    state.overviewRefresh.serial = serial;
    state.overviewRefresh.controller = controller;

    try {
      var overviewResult = await apiRequest(API.overview, {
        query: { days: OVERVIEW_DAYS, force: options.forceRefresh ? 1 : undefined },
        signal: controller.signal
      });
      if (serial !== state.overviewRefresh.serial) return;

      overviewResult = overviewResult || {};
      var trendsResult = pick(overviewResult, ['trends', 'trend', 'daily_stats'], null);
      var trendError = null;
      if (trendsResult === null) {
        try {
          trendsResult = await apiRequest(API.trends, {
            query: { days: OVERVIEW_DAYS },
            signal: controller.signal
          });
        } catch (error) {
          if (error && error.name === 'AbortError') throw error;
          trendError = error;
          trendsResult = state.overviewTrends;
        }
      }
      if (serial !== state.overviewRefresh.serial) return;

      state.overview = overviewResult;
      state.overviewTrends = trendsResult || [];
      state.loaded.overview = true;
      renderOverview(overviewResult, state.overviewTrends);
      updateTimestamp(overviewGeneratedAt(overviewResult));
      updateOverviewFreshness(overviewResult);
      if (trendError && !boolValue(pick(overviewResult, ['stale'], false), false)) {
        showAlert('overview-alert', '概览已更新，但趋势数据加载失败：' + (trendError.message || '请求失败'), 'warning');
      }
      scheduleOverviewRefresh(overviewResult);
    } catch (error) {
      if (serial !== state.overviewRefresh.serial || (error && error.name === 'AbortError')) return;
      if (state.overview) {
        state.loaded.overview = true;
        updateOverviewFreshness(state.overview, error);
        scheduleOverviewRefresh(state.overview);
      } else {
        state.loaded.overview = false;
        showAlert('overview-alert', error.message || '概览数据加载失败');
        renderOverview({}, []);
        updateTimestamp(null);
        scheduleOverviewRefresh({});
      }
    } finally {
      if (serial === state.overviewRefresh.serial) {
        state.overviewRefresh.controller = null;
        setOverviewRefreshStatus(false);
      }
    }
  }

  function renderOverview(data, trends) {
    var statusCounts = normalizeStatusCounts(pick(data, ['status_counts', 'resource_statuses', 'validation_status'], {}));
    var totalResources = numberValue(pick(data, ['resource_count', 'resources_total', 'total_resources', 'total'], 0));
    var validCount = numberValue(pick(data, ['valid_count', 'valid_resources'], pick(statusCounts, ['valid'], 0)));
    var todayNew = numberValue(pick(data, ['new_today', 'today_new', 'resources_today'], 0));
    var sevenDayNew = numberValue(pick(data, ['last_seven_days_new', 'new_last_7_days', 'new_7d', 'seven_day_new'], 0));
    var keywordCount = numberValue(pick(data, ['keyword_count', 'keywords_total', 'total_keywords'], 0));
    var enabledKeywords = numberValue(pick(data, ['enabled_keyword_count', 'enabled_keywords'], keywordCount));

    var statCards = [
      { label: '资源总数', value: totalResources, icon: 'database', tone: 'blue', foot: validCount + ' 条有效链接' },
      { label: '今日新增', value: todayNew, icon: 'sparkles', tone: 'green', foot: '截至当前时间' },
      { label: '近 7 日新增', value: sevenDayNew, icon: 'trending-up', tone: 'amber', foot: '滚动 7 日统计' },
      { label: '关键词', value: keywordCount, icon: 'tags', tone: 'violet', foot: enabledKeywords + ' 个已启用' }
    ];
    byId('stat-grid').innerHTML = statCards.map(function (card) {
      return '<article class="stat-card">' +
        '<div class="stat-head"><span>' + escapeHTML(card.label) + '</span><span class="stat-icon ' + card.tone + '">' + icon(card.icon) + '</span></div>' +
        '<div class="stat-value">' + formatNumber(card.value) + '</div>' +
        '<div class="stat-foot">' + escapeHTML(card.foot) + '</div>' +
      '</article>';
    }).join('');

    renderTrendChart(trends);
    renderStatusChart(statusCounts, totalResources);
    renderSourceChart(pick(data, ['top_sources', 'source_contributions', 'source_stats', 'sources'], []));
    renderActiveBatch(pick(data, ['active_run', 'active_batch', 'current_run', 'current_batch'], null));
    renderRecentRuns(arrayFrom(pick(data, ['recent_runs', 'runs', 'collection_runs'], []), ['items', 'runs']));
    refreshIcons();
  }

  function normalizeStatusCounts(data) {
    if (Array.isArray(data)) {
      return data.reduce(function (result, item) {
        var key = String(pick(item, ['status', 'state'], 'unknown')).toLowerCase();
        result[key] = numberValue(pick(item, ['count', 'resource_count', 'total'], 0));
        return result;
      }, {});
    }
    return data && typeof data === 'object' ? data : {};
  }

  function normalizeTrend(trends) {
    if (Array.isArray(trends)) {
      return trends.map(function (item) {
        return {
          date: pick(item, ['date', 'day', 'label'], ''),
          added: numberValue(pick(item, ['new_resources', 'added', 'new_count', 'count'], 0)),
          valid: numberValue(pick(item, ['discoveries', 'valid_resources', 'valid', 'valid_count'], 0))
        };
      });
    }
    var dates = pick(trends, ['dates', 'labels'], []);
    var added = pick(trends, ['new_resources', 'added', 'counts'], []);
    var valid = pick(trends, ['valid_resources', 'valid', 'valid_counts'], []);
    return dates.map(function (date, index) {
      return { date: date, added: numberValue(added[index]), valid: numberValue(valid[index]) };
    });
  }

  function renderTrendChart(trends) {
    var chart = chartFor('trend-chart');
    if (!chart) return;
    var points = normalizeTrend(trends);
    chart.setOption({
      animationDuration: 420,
      color: ['#1769e0', '#16835b'],
      tooltip: { trigger: 'axis', backgroundColor: '#17191d', borderWidth: 0, textStyle: { color: '#fff', fontSize: 11 } },
      grid: { left: 8, right: 10, top: 24, bottom: 4, containLabel: true },
      xAxis: {
        type: 'category',
        boundaryGap: false,
        data: points.map(function (point) { return String(point.date).slice(5); }),
        axisLine: { lineStyle: { color: '#dfe2e6' } },
        axisTick: { show: false },
        axisLabel: { color: '#8c939d', fontSize: 10 }
      },
      yAxis: {
        type: 'value',
        minInterval: 1,
        splitLine: { lineStyle: { color: '#eceef0', type: 'dashed' } },
        axisLabel: { color: '#8c939d', fontSize: 10 }
      },
      series: [
        {
          name: '新增资源',
          type: 'line',
          smooth: 0.28,
          symbol: 'circle',
          symbolSize: 5,
          lineStyle: { width: 2 },
          areaStyle: { color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: 'rgba(23,105,224,0.18)' }, { offset: 1, color: 'rgba(23,105,224,0.01)' }] } },
          data: points.map(function (point) { return point.added; })
        },
        {
          name: '发现次数',
          type: 'line',
          smooth: 0.28,
          symbol: 'none',
          lineStyle: { width: 1.5, type: 'dashed' },
          data: points.map(function (point) { return point.valid; })
        }
      ]
    }, true);
  }

  function renderStatusChart(counts, totalResources) {
    var chart = chartFor('status-chart');
    var statuses = [
      { key: 'valid', label: '有效', color: '#16835b' },
      { key: 'pending', label: '待检测', color: '#9ca3ad' },
      { key: 'unknown', label: '未知', color: '#d28a24' },
      { key: 'unsupported', label: '不支持', color: '#7557c5' },
      { key: 'invalid', label: '失效', color: '#c23838' }
    ];
    var values = statuses.map(function (item) {
      return { name: item.label, value: numberValue(pick(counts, [item.key], 0)), itemStyle: { color: item.color } };
    });
    var computedTotal = values.reduce(function (sum, item) { return sum + item.value; }, 0);
    var total = totalResources || computedTotal;
    if (chart) {
      chart.setOption({
        animationDuration: 420,
        tooltip: { trigger: 'item', formatter: '{b}<br/>{c} ({d}%)', backgroundColor: '#17191d', borderWidth: 0, textStyle: { color: '#fff', fontSize: 11 } },
        title: {
          text: formatNumber(total),
          subtext: '全部资源',
          left: 'center',
          top: '34%',
          textStyle: { color: '#1c1f24', fontSize: 20, fontWeight: 700 },
          subtextStyle: { color: '#9299a3', fontSize: 9 }
        },
        series: [{
          type: 'pie',
          radius: ['57%', '77%'],
          center: ['50%', '48%'],
          avoidLabelOverlap: false,
          label: { show: false },
          emphasis: { scaleSize: 4 },
          data: values
        }]
      }, true);
    }
    byId('status-legend').innerHTML = statuses.map(function (item) {
      return '<span><i style="background:' + item.color + '"></i>' + item.label + ' ' + formatNumber(pick(counts, [item.key], 0)) + '</span>';
    }).join('');
  }

  function normalizeSources(data) {
    if (Array.isArray(data)) {
      return data.map(function (item) {
        return {
          name: pick(item, ['source', 'name', 'label'], '') || ((pick(item, ['source_type'], '') ? pick(item, ['source_type'], '') + ':' : '') + pick(item, ['source_key'], '未知来源')),
          count: numberValue(pick(item, ['count', 'resource_count', 'total', 'value'], 0))
        };
      }).sort(function (a, b) { return b.count - a.count; }).slice(0, 8);
    }
    if (data && typeof data === 'object') {
      return Object.keys(data).map(function (key) { return { name: key, count: numberValue(data[key]) }; })
        .sort(function (a, b) { return b.count - a.count; }).slice(0, 8);
    }
    return [];
  }

  function renderSourceChart(data) {
    var chart = chartFor('source-chart');
    if (!chart) return;
    var sources = normalizeSources(data).reverse();
    chart.setOption({
      animationDuration: 420,
      color: ['#1769e0'],
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, backgroundColor: '#17191d', borderWidth: 0, textStyle: { color: '#fff', fontSize: 11 } },
      grid: { left: 8, right: 16, top: 12, bottom: 5, containLabel: true },
      xAxis: {
        type: 'value',
        minInterval: 1,
        splitLine: { lineStyle: { color: '#eceef0', type: 'dashed' } },
        axisLabel: { color: '#8c939d', fontSize: 9 }
      },
      yAxis: {
        type: 'category',
        data: sources.map(function (source) { return String(source.name).slice(0, 18); }),
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: { color: '#59616b', fontSize: 10 }
      },
      series: [{
        type: 'bar',
        barMaxWidth: 13,
        itemStyle: { borderRadius: [0, 3, 3, 0] },
        data: sources.map(function (source) { return source.count; })
      }]
    }, true);
  }

  function renderActiveBatch(run) {
    var container = byId('active-batch');
    if (!run || !ACTIVE_STATUSES.includes(normalizedStatus(run))) {
      container.innerHTML = '<div class="no-active-batch">' + icon('circle-check-big') + '<strong>当前没有运行中的批次</strong><span>调度器处于待命状态</span></div>';
      refreshIcons(container);
      return;
    }
    var progress = runProgress(run);
    var id = pick(run, ['id', 'run_id', 'batch_id'], '—');
    var added = numberValue(pick(run, ['new_count', 'created_count', 'added_count'], 0));
    var duplicate = numberValue(pick(run, ['duplicate_count', 'duplicates'], 0));
    container.innerHTML =
      '<div class="batch-id">BATCH / ' + escapeHTML(id) + '</div>' +
      '<div class="batch-progress-label"><span>' + progress.completed + ' / ' + progress.total + ' 关键词</span><span>' + progress.percent + '%</span></div>' +
      '<div class="progress-track"><div class="progress-bar" style="width:' + progress.percent + '%"></div></div>' +
      '<div class="batch-metrics">' +
        '<div><strong>' + formatNumber(added) + '</strong><span>新增资源</span></div>' +
        '<div><strong>' + formatNumber(duplicate) + '</strong><span>重复资源</span></div>' +
        '<div><strong>' + escapeHTML(durationFrom(run)) + '</strong><span>运行时间</span></div>' +
      '</div>';
  }

  function renderRecentRuns(runs) {
    var body = byId('recent-runs-body');
    if (!runs.length) {
      body.innerHTML = '<tr class="table-empty"><td colspan="7">暂无任务记录</td></tr>';
      return;
    }
    body.innerHTML = runs.slice(0, 6).map(function (run) {
      var id = pick(run, ['id', 'run_id', 'batch_id'], '—');
      var status = normalizedStatus(run);
      var trigger = pick(run, ['trigger', 'trigger_type'], 'scheduled');
      var keywordCount = numberValue(pick(run, ['keyword_count', 'total_keywords', 'total_items'], 0));
      var added = numberValue(pick(run, ['new_count', 'created_count', 'added_count'], 0));
      var duplicate = numberValue(pick(run, ['duplicate_count', 'duplicates'], 0));
      return '<tr data-action="view-run" data-id="' + escapeHTML(id) + '" tabindex="0">' +
        '<td class="mono">#' + escapeHTML(id) + '</td>' +
        '<td>' + escapeHTML(triggerLabels[trigger] || trigger) + '</td>' +
        '<td>' + statusBadge(status) + '</td>' +
        '<td>' + formatNumber(keywordCount) + '</td>' +
        '<td><span class="success-text">+' + formatNumber(added) + '</span> / ' + formatNumber(duplicate) + '</td>' +
        '<td>' + escapeHTML(formatDate(pick(run, ['started_at', 'created_at', 'start_time'], null), true)) + '</td>' +
        '<td>' + escapeHTML(durationFrom(run)) + '</td>' +
      '</tr>';
    }).join('');
  }

  async function loadResources(force) {
    if (state.loaded.resources && !force) return;
    state.loaded.resources = true;
    var serial = ++state.requestSerial.resources;
    showAlert('resources-alert', '');
    byId('resources-empty').hidden = true;
    byId('resources-pagination').innerHTML = '';
    tableLoading('resources-body', 7);

    var query = Object.assign({}, state.resources.query, {
      page: state.resources.page,
      page_size: state.resources.pageSize
    });
    var controller = replaceRequestController('resources');
    try {
      var data = await apiRequest(API.resources, { query: query, signal: controller.signal });
      if (state.requestControllers.resources !== controller || serial !== state.requestSerial.resources) return;
      var items = arrayFrom(data, ['resources', 'items', 'results']);
      var meta = paginationMeta(data, items, state.resources.page, state.resources.pageSize);
      state.resources.items = items;
      Object.assign(state.resources, meta);
      renderResources();
      updateTimestamp();
    } catch (error) {
      if (error.name === 'AbortError') return;
      if (serial !== state.requestSerial.resources) return;
      state.loaded.resources = false;
      byId('resources-body').innerHTML = '';
      showAlert('resources-alert', error.message || '资源加载失败');
    } finally { finishRequestController('resources', controller); }
  }

  function resourceSources(resource) {
    var sources = arrayFrom(pick(resource, ['source_preview', 'sources', 'resource_sources'], []), ['items', 'sources']);
    if (!sources.length) {
      var source = pick(resource, ['source', 'source_key', 'source_type'], '');
      if (source) sources = [{ source: source, source_type: pick(resource, ['source_type'], '') }];
    }
    return sources;
  }

  function sourceBadge(source) {
    var type = String(pick(source, ['source_type', 'type'], '')).toLowerCase();
    var name = pick(source, ['source_key', 'source', 'name', 'channel', 'plugin'], type || '未知');
    if (!type && String(name).indexOf(':') > -1) type = String(name).split(':')[0];
    var displayName = String(name).replace(/^(plugin|tg|telegram|external):/i, '');
    var display = sourceTypeLabels[type] || displayName;
    if (displayName && displayName !== type && sourceTypeLabels[type]) display += ' · ' + displayName;
    return '<span class="source-badge ' + escapeHTML(type) + '" title="' + escapeHTML(name) + '">' + escapeHTML(display) + '</span>';
  }

  function renderResources() {
    var body = byId('resources-body');
    var empty = byId('resources-empty');
    byId('resource-total-label').textContent = formatNumber(state.resources.total) + ' 条资源';
    if (!state.resources.items.length) {
      body.innerHTML = '';
      empty.hidden = false;
      byId('resources-pagination').innerHTML = '';
      refreshIcons(empty);
      return;
    }
    empty.hidden = true;
    body.innerHTML = state.resources.items.map(function (resource) {
      var id = pick(resource, ['id', 'resource_id'], '');
      var title = pick(resource, ['title', 'work_title', 'name', 'note'], '未命名资源');
      var url = pick(resource, ['url', 'original_url', 'normalized_url', 'link'], '');
      var diskType = String(pick(resource, ['disk_type', 'platform', 'cloud_type', 'type'], 'others')).toLowerCase();
      var status = normalizedStatus(resource);
      var sources = resourceSources(resource);
      var sourceTotal = numberValue(pick(resource, ['source_count'], sources.length), sources.length);
      var discoveryCount = numberValue(pick(resource, ['discovery_count', 'found_count', 'discover_count'], 1));
      var lastSeen = pick(resource, ['last_seen_at', 'last_discovered_at', 'updated_at', 'discovered_at'], null);
      return '<tr>' +
        '<td><div class="resource-cell"><div class="resource-title" title="' + escapeHTML(title) + '">' + escapeHTML(title) + '</div><div class="resource-link" title="' + escapeHTML(url) + '">' + escapeHTML(url) + '</div></div></td>' +
        '<td><span class="type-badge">' + escapeHTML(diskLabels[diskType] || diskType) + '</span></td>' +
        '<td>' + statusBadge(status) + '</td>' +
        '<td><div class="source-stack">' + sources.slice(0, 2).map(sourceBadge).join('') + (sourceTotal > sources.length ? '<span class="muted">+' + (sourceTotal - sources.length) + '</span>' : '') + '</div></td>' +
        '<td>' + formatNumber(discoveryCount) + '</td>' +
        '<td title="' + escapeHTML(formatDate(lastSeen, true)) + '">' + escapeHTML(relativeTime(lastSeen)) + '</td>' +
        '<td><div class="row-actions">' +
          '<button class="row-action" type="button" data-action="copy-resource" data-url="' + escapeHTML(url) + '" aria-label="复制链接" title="复制链接">' + icon('copy') + '</button>' +
          '<button class="row-action" type="button" data-action="view-resource" data-id="' + escapeHTML(id) + '" aria-label="查看详情" title="查看详情">' + icon('panel-right-open') + '</button>' +
        '</div></td>' +
      '</tr>';
    }).join('');
    renderPagination('resources-pagination', state.resources.page, state.resources.pages, 'resources');
    refreshIcons(body);
  }

  function readResourceFilters() {
    var data = new FormData(byId('resource-filters'));
    var values = Object.fromEntries(data.entries());
    var query = {
      q: String(values.q || '').trim(),
      keyword_type: String(values.keyword_type || '').trim(),
      platform: String(values.disk_type || '').trim(),
      status: String(values.status || '').trim(),
      include_invalid: String(values.status || '').trim() === 'invalid' ? 'true' : '',
      source_type: String(values.source || '').trim(),
      from: dateBoundary(values.date_from, false),
      to: dateBoundary(values.date_to, true)
    };
    state.resources.query = query;
    state.resources.page = 1;
    state.loaded.resources = false;
    loadResources(true);
  }

  function dateBoundary(value, exclusiveEnd) {
    if (!value) return '';
    var parts = String(value).split('-').map(Number);
    if (parts.length !== 3 || parts.some(function (part) { return !Number.isFinite(part); })) return '';
    var date = new Date(parts[0], parts[1] - 1, parts[2] + (exclusiveEnd ? 1 : 0), 0, 0, 0, 0);
    return Number.isNaN(date.getTime()) ? '' : date.toISOString();
  }

  async function openResourceDetail(id) {
    var dialog = byId('resource-dialog');
    var container = byId('resource-detail');
    cancelResourceDetailRequests(dialog);
    var controller = new AbortController();
    dialog._requestController = controller;
    dialog.dataset.resourceId = String(id);
    if (!dialog.open) dialog.showModal();
    container.innerHTML = '<div class="table-loading"><span>' + icon('loader-circle', 'spin') + '正在加载</span></div>';
    refreshIcons(container);
    try {
      var data = await apiRequest(API.resources + '/' + encodeURIComponent(id), { signal: controller.signal });
      if (dialog._requestController !== controller || !dialog.open || dialog.dataset.resourceId !== String(id)) return;
      var resource = pick(data, ['resource'], data);
      renderResourceDetail(resource);
    } catch (error) {
      if (error.name !== 'AbortError') container.innerHTML = '<div class="inline-alert error">' + escapeHTML(error.message || '资源详情加载失败') + '</div>';
    } finally {
      if (dialog._requestController === controller) dialog._requestController = null;
    }
  }

  function cancelResourceDetailRequests(dialog) {
    dialog = dialog || byId('resource-dialog');
    if (!dialog) return;
    if (dialog._requestController) {
      dialog._requestController.abort();
      dialog._requestController = null;
    }
    dialog.querySelectorAll('[data-resource-related]').forEach(function (details) {
      if (details._controller) details._controller.abort();
      details._controller = null;
      details.dataset.loading = 'false';
    });
  }

  function renderResourceDetail(resource) {
    var title = pick(resource, ['title', 'work_title', 'name', 'note'], '未命名资源');
    var content = pick(resource, ['content', 'description'], '');
    var url = pick(resource, ['url', 'original_url', 'normalized_url', 'link'], '');
    var normalizedURL = pick(resource, ['normalized_url'], url);
    var password = pick(resource, ['password', 'extraction_code', 'code'], '');
    var diskType = String(pick(resource, ['disk_type', 'platform', 'cloud_type', 'type'], 'others')).toLowerCase();
    var resourceID = pick(resource, ['id', 'resource_id'], '');
    var sourceCount = numberValue(pick(resource, ['source_count'], 0));
    var keywordCount = numberValue(pick(resource, ['keyword_count'], 0));
    var externalURL = safeExternalURL(url);
    var linkHTML = '<div class="link-box">' +
      (externalURL ? '<a href="' + escapeHTML(externalURL) + '" target="_blank" rel="noopener noreferrer">' + escapeHTML(url) + '</a>' : '<span class="resource-link">' + escapeHTML(url) + '</span>') +
      '<button class="row-action" type="button" data-action="copy-resource" data-url="' + escapeHTML(url) + '" aria-label="复制链接">' + icon('copy') + '</button>' +
    '</div>';

    byId('resource-detail').innerHTML =
      '<section class="detail-section">' +
        '<h1 class="detail-title">' + escapeHTML(title) + '</h1>' +
        (content ? '<p class="detail-content">' + escapeHTML(content) + '</p>' : '') +
        '<div class="source-stack" style="margin-top:12px"><span class="type-badge">' + escapeHTML(diskLabels[diskType] || diskType) + '</span>' + statusBadge(normalizedStatus(resource)) + '</div>' +
      '</section>' +
      '<section class="detail-section"><h3>分享链接</h3>' + linkHTML +
        '<dl class="detail-grid" style="margin-top:13px">' +
          '<div class="detail-pair"><dt>提取码</dt><dd class="mono">' + escapeHTML(password || '无') + '</dd></div>' +
          '<div class="detail-pair"><dt>规范化链接</dt><dd class="mono">' + escapeHTML(normalizedURL) + '</dd></div>' +
        '</dl>' +
      '</section>' +
      '<section class="detail-section"><h3>发现信息</h3><dl class="detail-grid">' +
        '<div class="detail-pair"><dt>首次发现</dt><dd>' + escapeHTML(formatDate(pick(resource, ['first_seen_at', 'first_discovered_at', 'created_at'], null), true)) + '</dd></div>' +
        '<div class="detail-pair"><dt>最近发现</dt><dd>' + escapeHTML(formatDate(pick(resource, ['last_seen_at', 'last_discovered_at', 'updated_at'], null), true)) + '</dd></div>' +
        '<div class="detail-pair"><dt>发现次数</dt><dd>' + formatNumber(pick(resource, ['discovery_count', 'found_count', 'discover_count'], 1)) + '</dd></div>' +
        '<div class="detail-pair"><dt>最近检测</dt><dd>' + escapeHTML(formatDate(pick(resource, ['checked_at', 'last_checked_at'], null), true)) + '</dd></div>' +
      '</dl></section>' +
      '<details class="detail-section lazy-detail" data-resource-related="keywords" data-resource-id="' + escapeHTML(resourceID) + '"><summary><h3>关联关键词</h3><span class="muted">' + formatNumber(keywordCount) + ' 项 · 展开加载</span></summary><div data-resource-related-content="keywords"></div></details>' +
      '<details class="detail-section lazy-detail" data-resource-related="sources" data-resource-id="' + escapeHTML(resourceID) + '"><summary><h3>来源明细</h3><span class="muted">' + formatNumber(sourceCount) + ' 项 · 展开加载</span></summary><div data-resource-related-content="sources"></div></details>';
    refreshIcons(byId('resource-detail'));
  }

  async function loadResourceRelated(details) {
    if (details.dataset.loaded === 'true' || details.dataset.loading === 'true') return;
    var type = details.dataset.resourceRelated, id = details.dataset.resourceId, target = details.querySelector('[data-resource-related-content]');
    var page = numberValue(details.dataset.nextPage, 1); details.dataset.loading = 'true';
    var controller = new AbortController(); details._controller = controller; if (page === 1) target.innerHTML = '<div class="table-loading"><span>' + icon('loader-circle','spin') + '正在加载</span></div>';
    try {
      var data = await apiRequest(API.resources + '/' + encodeURIComponent(id) + '/' + type, { query: { page: page, page_size: 50 }, signal: controller.signal });
      if (details._controller !== controller || !details.isConnected || !details.closest('dialog').open) return;
      var items = arrayFrom(data, [type, 'items']), meta = paginationMeta(data, items, page, 50); details._items = (details._items || []).concat(items); details.dataset.nextPage = String(page + 1); details.dataset.loaded = page >= meta.pages ? 'true' : 'false';
      if (type === 'keywords') target.innerHTML = '<div class="source-stack">' + (details._items.length ? details._items.map(function(k){return '<span class="type-badge">'+escapeHTML(typeof k==='string'?k:pick(k,['keyword','name'],'—'))+'</span>';}).join('') : '<span class="muted">无关联关键词</span>') + '</div>';
      else target.innerHTML = '<div class="source-list">' + (details._items.length ? details._items.map(function(s){return '<article class="source-item"><strong>'+escapeHTML(pick(s,['source_key','source','name','channel','plugin'],'未知来源'))+'</strong>'+sourceBadge(s)+'<small class="full-row">发现于 '+escapeHTML(formatDate(pick(s,['discovered_at','created_at','first_seen_at'],null),true))+'</small></article>';}).join('') : '<span class="muted">暂无来源记录</span>') + '</div>';
      if (page < meta.pages) target.innerHTML += '<button class="button secondary" type="button" data-action="load-more-resource-related">加载更多（' + details._items.length + ' / ' + meta.total + '）</button>'; refreshIcons(target);
    } catch(error){if(error.name!=='AbortError')target.innerHTML+='<div class="inline-alert error">'+escapeHTML(error.message||'加载失败')+'</div>';} finally { if (details._controller === controller) { details._controller = null; details.dataset.loading='false'; } }
  }

  async function loadKeywords(force) {
    if (state.loaded.keywords && !force) return state.keywords.items;
    state.loaded.keywords = true;
    var serial = ++state.requestSerial.keywords;
    showAlert('keywords-alert', '');
    byId('keywords-empty').hidden = true;
    byId('keywords-pagination').innerHTML = '';
    tableLoading('keywords-body', 8);
    var query = Object.assign({}, state.keywords.query, { page: state.keywords.page, page_size: state.keywords.pageSize });
    var controller = replaceRequestController('keywords');
    try {
      var data = await apiRequest(API.keywords, { query: query, signal: controller.signal });
      var items = arrayFrom(data, ['keywords', 'items', 'results']);
      if (state.requestControllers.keywords !== controller || serial !== state.requestSerial.keywords) return [];
      var meta = paginationMeta(data, items, state.keywords.page, state.keywords.pageSize);
      state.keywords.items = items;
      Object.assign(state.keywords, meta);
      var currentIds = new Set(items.map(function (item) { return String(pick(item, ['id', 'keyword_id'], '')); }));
      state.keywords.selected.forEach(function (id) {
        if (!currentIds.has(id)) state.keywords.selected.delete(id);
      });
      renderKeywords();
      updateTimestamp();
      return items;
    } catch (error) {
      if (error.name === 'AbortError') return [];
      if (serial !== state.requestSerial.keywords) return [];
      state.loaded.keywords = false;
      byId('keywords-body').innerHTML = '';
      showAlert('keywords-alert', error.message || '关键词加载失败');
      return [];
    } finally { finishRequestController('keywords', controller); }
  }

  function renderKeywords() {
    var body = byId('keywords-body');
    var empty = byId('keywords-empty');
    if (!state.keywords.items.length) {
      body.innerHTML = '';
      empty.hidden = false;
      byId('keywords-pagination').innerHTML = '';
      updateKeywordBulkBar();
      refreshIcons(empty);
      return;
    }
    empty.hidden = true;
    body.innerHTML = state.keywords.items.map(function (keyword) {
      var id = String(pick(keyword, ['id', 'keyword_id'], ''));
      var name = pick(keyword, ['keyword', 'name'], '');
      var type = String(pick(keyword, ['keyword_type', 'type'], 'general'));
      var sourceType = String(pick(keyword, ['source_type'], 'manual'));
      var sourceKey = pick(keyword, ['source_key', 'external_id'], '');
      var priority = numberValue(pick(keyword, ['priority'], 0));
      var cooldownSeconds = keywordCooldownSeconds(keyword);
      var cooldownLabel = cooldownSeconds === null ? '默认（7 天）' : formatCooldownDays(cooldownSeconds);
      var enabled = boolValue(pick(keyword, ['enabled', 'is_enabled'], true), true);
      var selected = state.keywords.selected.has(id);
      var nextEligible = pick(keyword, ['next_eligible_at', 'next_run_at'], null);
      return '<tr>' +
        '<td class="check-cell"><input type="checkbox" data-action="select-keyword" data-id="' + escapeHTML(id) + '" aria-label="选择 ' + escapeHTML(name) + '"' + (selected ? ' checked' : '') + '></td>' +
        '<td><div class="keyword-name"><strong>' + escapeHTML(name) + '</strong><small>最近执行 ' + escapeHTML(relativeTime(pick(keyword, ['last_run_at', 'last_executed_at'], null))) + '</small></div></td>' +
        '<td><div class="type-source-stack"><span class="type-badge">' + escapeHTML(keywordTypeLabels[type] || type) + '</span><span class="source-badge">' + escapeHTML(sourceTypeLabels[sourceType] || sourceType) + (sourceKey ? ' · ' + escapeHTML(sourceKey) : '') + '</span></div></td>' +
        '<td><span class="mono">' + priority + '</span></td>' +
        '<td>' + escapeHTML(cooldownLabel) + '</td>' +
        '<td title="' + escapeHTML(formatDate(nextEligible, true)) + '">' + escapeHTML(nextEligible ? relativeTime(nextEligible) : '立即可用') + '</td>' +
        '<td><button class="toggle-field compact-toggle" type="button" data-action="toggle-keyword" data-id="' + escapeHTML(id) + '" role="switch" aria-checked="' + enabled + '" aria-label="' + (enabled ? '停用' : '启用') + ' ' + escapeHTML(name) + '"><input type="checkbox"' + (enabled ? ' checked' : '') + ' tabindex="-1"><span class="toggle" aria-hidden="true"></span></button></td>' +
        '<td><div class="row-actions">' +
          '<button class="row-action" type="button" data-action="run-keyword" data-id="' + escapeHTML(id) + '" aria-label="采集 ' + escapeHTML(name) + '" title="立即采集">' + icon('play') + '</button>' +
          '<button class="row-action" type="button" data-action="edit-keyword" data-id="' + escapeHTML(id) + '" aria-label="编辑 ' + escapeHTML(name) + '" title="编辑">' + icon('pencil') + '</button>' +
          '<button class="row-action danger" type="button" data-action="delete-keyword" data-id="' + escapeHTML(id) + '" aria-label="删除 ' + escapeHTML(name) + '" title="删除">' + icon('trash-2') + '</button>' +
        '</div></td>' +
      '</tr>';
    }).join('');
    renderPagination('keywords-pagination', state.keywords.page, state.keywords.pages, 'keywords');
    updateKeywordBulkBar();
    refreshIcons(body);
  }

  function updateKeywordFilters() {
    state.keywords.query = {
      q: byId('keyword-search').value.trim(),
      enabled: byId('keyword-enabled-filter').value,
      keyword_type: byId('keyword-type-filter').value
    };
    state.keywords.page = 1;
    state.keywords.selected.clear();
    state.loaded.keywords = false;
    loadKeywords(true);
  }

  function updateKeywordBulkBar() {
    var count = state.keywords.selected.size;
    byId('keyword-selected-count').textContent = count;
    byId('keyword-bulkbar').hidden = count === 0;
    var all = state.keywords.items.length > 0 && state.keywords.items.every(function (item) {
      return state.keywords.selected.has(String(pick(item, ['id', 'keyword_id'], '')));
    });
    byId('select-all-keywords').checked = all;
    byId('select-all-keywords').indeterminate = count > 0 && !all;
  }

  function setKeywordDialogMode(mode, locked) {
    var form = byId('keyword-form');
    mode = mode === 'api' ? 'api' : 'manual';
    form.dataset.mode = mode;
    document.querySelectorAll('[data-keyword-mode]').forEach(function (button) {
      var active = button.dataset.keywordMode === mode;
      button.classList.toggle('active', active);
      button.setAttribute('aria-selected', active ? 'true' : 'false');
      button.disabled = Boolean(locked) && !active;
    });
    document.querySelectorAll('[data-keyword-mode-panel]').forEach(function (panel) {
      panel.hidden = panel.dataset.keywordModePanel !== mode;
    });
    byId('keyword-save').querySelector('span').textContent = mode === 'api' ? '保存 API 来源' : '保存';
    byId('keyword-save-and-sync').hidden = mode !== 'api';
    byId('keyword-dialog-title').textContent = mode === 'api'
      ? (state.keywordSources.editing ? '编辑 API 关键词来源' : '新增 API 关键词来源')
      : (form.elements.id.value ? '编辑关键词' : '新增关键词');
  }

  function openKeywordDialog(keyword, mode) {
    var dialog = byId('keyword-dialog');
    var form = byId('keyword-form');
    form.reset();
    state.keywordSources.editing = null;
    state.keywordSources.testResponse = null;
    state.keywordSources.selectedPath = '';
    state.keywordSources.testedSignature = '';
    state.keywordSources.originalSignature = '';
    byId('keyword-form-error').hidden = true;
    form.elements.id.value = keyword ? pick(keyword, ['id', 'keyword_id'], '') : '';
    form.elements.keyword.value = keyword ? pick(keyword, ['keyword', 'name'], '') : '';
    form.elements.keyword_type.value = keyword ? pick(keyword, ['keyword_type', 'type'], 'general') : 'general';
    form.elements.source_type.value = keyword ? pick(keyword, ['source_type'], 'manual') : 'manual';
    form.elements.source_key.value = keyword ? pick(keyword, ['source_key'], '') : '';
    form.elements.priority.value = keyword ? numberValue(pick(keyword, ['priority'], 0)) : 0;
    var seconds = keyword ? keywordCooldownSeconds(keyword) : null;
    form.elements.cooldown_days.value = seconds === null ? '' : formatCooldownInputDays(seconds);
    form.elements.enabled.checked = keyword ? boolValue(pick(keyword, ['enabled', 'is_enabled'], true), true) : true;
    resetKeywordAPIBuilder();
    setKeywordDialogMode(mode === 'api' ? 'api' : 'manual', Boolean(keyword));
    dialog.showModal();
    window.setTimeout(function () { (form.dataset.mode === 'api' ? form.elements.api_name : form.elements.keyword).focus(); }, 0);
  }

  function keywordPayloadFromForm(form) {
    return {
      keyword: form.elements.keyword.value.trim(),
      keyword_type: form.elements.keyword_type.value.trim() || 'general',
      source_type: form.elements.source_type.value || 'manual',
      source_key: form.elements.source_key.value.trim(),
      priority: numberValue(form.elements.priority.value),
      cooldown_seconds: form.elements.cooldown_days.value.trim() === '' ? null : Math.round(numberValue(form.elements.cooldown_days.value) * 86400),
      enabled: form.elements.enabled.checked
    };
  }

  async function saveKeyword(event) {
    event.preventDefault();
    var form = event.currentTarget;
    if (form.dataset.mode === 'api') {
      await saveKeywordAPISource(form);
      return;
    }
    var id = form.elements.id.value;
    var payload = keywordPayloadFromForm(form);
    var error = byId('keyword-form-error');
    error.hidden = true;
    if (!payload.keyword) {
      error.textContent = '关键词不能为空';
      error.hidden = false;
      return;
    }
    var button = byId('keyword-save');
    setButtonLoading(button, true, '保存中');
    try {
      await apiRequest(id ? API.keywords + '/' + encodeURIComponent(id) : API.keywords, {
        method: id ? 'PUT' : 'POST',
        body: payload
      });
      byId('keyword-dialog').close();
      toast(id ? '关键词已更新' : '关键词已新增', 'success');
      state.loaded.keywords = false;
      await loadKeywords(true);
    } catch (saveError) {
      error.textContent = saveError.message || '保存失败';
      error.hidden = false;
    } finally {
      setButtonLoading(button, false);
    }
  }

  async function toggleKeyword(id, button) {
    var keyword = state.keywords.items.find(function (item) {
      return String(pick(item, ['id', 'keyword_id'], '')) === String(id);
    });
    if (!keyword) return;
    var nextEnabled = !boolValue(pick(keyword, ['enabled', 'is_enabled'], true), true);
    button.disabled = true;
    try {
      await apiRequest(API.keywords + '/' + encodeURIComponent(id) + '/toggle', { method: 'POST', body: { enabled: nextEnabled } });
      keyword.enabled = nextEnabled;
      keyword.is_enabled = nextEnabled;
      renderKeywords();
      toast(nextEnabled ? '关键词已启用' : '关键词已停用', 'success');
    } catch (error) {
      toast(error.message || '状态更新失败', 'error');
      button.disabled = false;
    }
  }

  function keywordPayloadFromItem(keyword) {
    return {
      keyword: pick(keyword, ['keyword', 'name'], ''),
      keyword_type: pick(keyword, ['keyword_type', 'type'], 'general'),
      source_type: pick(keyword, ['source_type'], 'manual'),
      source_key: pick(keyword, ['source_key'], ''),
      priority: numberValue(pick(keyword, ['priority'], 0)),
      cooldown_seconds: keywordCooldownSeconds(keyword),
      enabled: boolValue(pick(keyword, ['enabled', 'is_enabled'], true), true)
    };
  }

  function keywordCooldownSeconds(keyword) {
    if (keyword && Object.prototype.hasOwnProperty.call(keyword, 'cooldown_seconds')) {
      return keyword.cooldown_seconds === null || keyword.cooldown_seconds === '' ? null : numberValue(keyword.cooldown_seconds);
    }
    var legacyDays = pick(keyword, ['cooldown_days'], null);
    if (legacyDays !== null) return Math.round(numberValue(legacyDays) * 86400);
    var legacyHours = pick(keyword, ['cooldown_hours'], null);
    return legacyHours === null ? null : Math.round(numberValue(legacyHours) * 3600);
  }

  function formatCooldownDays(seconds) {
    var numeric = numberValue(seconds);
    if (numeric > 0 && numeric < 86400) {
      var hours = Math.round(numeric / 360) / 10;
      return hours + ' 小时';
    }
    var days = numeric / 86400;
    var rounded = Math.round(days * 10) / 10;
    return rounded + ' 天';
  }

  function formatCooldownInputDays(seconds) {
    var days = numberValue(seconds) / 86400;
    return String(Math.round(days * 1000000) / 1000000);
  }

  async function deleteKeyword(id) {
    var keyword = state.keywords.items.find(function (item) {
      return String(pick(item, ['id', 'keyword_id'], '')) === String(id);
    });
    if (!keyword) return;
    var confirmed = await confirmAction('删除关键词', '将删除“' + pick(keyword, ['keyword', 'name'], '') + '”，历史任务记录不会被移除。', '删除');
    if (!confirmed) return;
    try {
      await apiRequest(API.keywords + '/' + encodeURIComponent(id), { method: 'DELETE' });
      state.keywords.selected.delete(String(id));
      toast('关键词已删除', 'success');
      state.loaded.keywords = false;
      loadKeywords(true);
    } catch (error) {
      toast(error.message || '删除失败', 'error');
    }
  }

  function switchKeywordTab(tab) {
    state.keywords.tab = tab === 'api' || tab === 'history' ? tab : 'list';
    document.querySelectorAll('[data-keyword-tab]').forEach(function (button) {
      var active = button.dataset.keywordTab === state.keywords.tab;
      button.classList.toggle('active', active);
      button.setAttribute('aria-selected', active ? 'true' : 'false');
    });
    document.querySelectorAll('[data-keyword-panel]').forEach(function (panel) {
      panel.hidden = panel.dataset.keywordPanel !== state.keywords.tab;
    });
    var createLabel = document.querySelector('[data-action="new-keyword"] span');
    var createButton = document.querySelector('[data-action="new-keyword"]');
    if (createLabel) createLabel.textContent = state.keywords.tab === 'api' ? '新增 API 来源' : '新增关键词';
    if (createButton) createButton.hidden = state.keywords.tab === 'history';
    stopKeywordSourcePolling();
    if (state.keywords.tab === 'api') loadKeywordSources(false);
    else if (state.keywords.tab === 'history') {
      loadKeywordSources(false, { silent: true });
      loadKeywordSyncRuns({ force: false });
    }
    else loadKeywords(false);
  }

  async function loadKeywordSources(force, options) {
    options = options || {};
    if (state.keywordSources.loaded && !force) {
      configureKeywordSourcePolling();
      return;
    }
    state.keywordSources.loaded = true;
    var serial = ++state.keywordSources.requestSerial;
    if (!options.silent) {
      showAlert('keyword-api-alert', '');
      tableLoading('keyword-api-body', 6);
      byId('keyword-api-empty').hidden = true;
    }
    try {
      var data = await apiRequest(API.keywordAPISources, { query: { page: 1, page_size: 200 } });
      if (serial !== state.keywordSources.requestSerial) return;
      state.keywordSources.items = arrayFrom(data, ['items', 'sources', 'results']);
      renderKeywordSources();
      populateKeywordSyncSourceFilter();
      updateTimestamp();
      configureKeywordSourcePolling();
    } catch (error) {
      if (serial !== state.keywordSources.requestSerial) return;
      state.keywordSources.loaded = false;
      if (!options.silent) {
        state.keywordSources.items = [];
        byId('keyword-api-body').innerHTML = '';
        showAlert('keyword-api-alert', error.message || 'API 关键词来源加载失败');
        stopKeywordSourcePolling();
      } else configureKeywordSourcePolling();
    }
  }

  function keywordSourceID(source) {
    return String(pick(source, ['id', 'source_id'], ''));
  }

  function keywordSyncRunStatus(run) {
    return String(pick(run, ['status', 'state'], 'unknown')).toLowerCase();
  }

  function keywordSyncRunID(run) {
    return String(pick(run, ['id', 'run_id'], ''));
  }

  function keywordSyncRunProgress(run) {
    var totalValue = pick(run, ['total_iterations', 'total_rounds'], null);
    var status = keywordSyncRunStatus(run);
    var legacy = String(pick(run, ['trigger'], '')).toLowerCase() === 'legacy' || status === 'legacy';
    var unlimited = !legacy && (boolValue(pick(run, ['unlimited', 'iteration_unlimited'], false), false) || totalValue === null || totalValue === undefined || Number(totalValue) === 0);
    var completed = numberValue(pick(run, ['completed_iterations', 'completed_rounds', 'processed_iterations'], 0));
    var total = unlimited ? null : Math.max(0, numberValue(totalValue, 0));
    var percent = legacy ? 100 : (total ? Math.min(100, Math.max(0, Math.round(completed / total * 100))) : (['success', 'partial', 'failed', 'interrupted', 'cancelled'].indexOf(status) >= 0 ? 100 : 0));
    return { total: total, completed: completed, percent: percent, unlimited: unlimited, legacy: legacy };
  }

  function keywordSyncRunResult(run) {
    var raw = numberValue(pick(run, ['raw_extracted_count', 'raw_item_count', 'seen'], 0));
    var unique = numberValue(pick(run, ['unique_count', 'unique_item_count', 'item_count'], 0));
    var added = numberValue(pick(run, ['new_count', 'new_keyword_count', 'inserted_keywords'], 0));
    var existing = numberValue(pick(run, ['existing_count', 'existing_keyword_count', 'existing_keywords'], 0));
    return { raw: raw, unique: unique, added: added, existing: existing };
  }

  function keywordSourceLatestRun(source) {
    return pick(source, ['active_run', 'latest_run'], null) || null;
  }

  function keywordSourceIsStale(source) {
    if (boolValue(source.result_stale, false)) return true;
    var current = numberValue(pick(source, ['sync_config_revision'], 0));
    var applied = numberValue(pick(source, ['last_applied_config_revision'], 0));
    return current > 0 && applied > 0 && current > applied;
  }

  function keywordSourceStatus(source, run) {
    run = run || keywordSourceLatestRun(source);
    var status = run ? keywordSyncRunStatus(run) : String(pick(source, ['last_status', 'status'], 'pending')).toLowerCase();
    var labels = { pending: '尚未同步', queued: '排队中', running: '同步中', success: '同步成功', partial: '部分成功', failed: '同步失败', interrupted: '已中断', cancelled: '已取消', legacy: '升级前记录' };
    return '<span class="status-badge status-' + escapeHTML(status) + '">' + escapeHTML(labels[status] || status) + '</span>';
  }

  function keywordSourceProgressMarkup(run) {
    if (!run) return '';
    var progress = keywordSyncRunProgress(run);
    var active = ACTIVE_KEYWORD_SYNC_STATUSES.indexOf(keywordSyncRunStatus(run)) >= 0;
    var label = progress.legacy ? '历史汇总' : (progress.unlimited ? '第 ' + progress.completed + ' 轮 / 无上限' : progress.completed + '/' + progress.total + ' 轮');
    return '<div class="mini-progress keyword-source-progress"><div class="mini-progress-label"><span>' + escapeHTML(label) + '</span><span>' + (progress.unlimited ? (active ? '进行中' : '已结束') : progress.percent + '%') + '</span></div><div class="progress-track' + (progress.unlimited && active ? ' is-unlimited' : '') + '"><div class="progress-bar" style="width:' + progress.percent + '%"></div></div></div>';
  }

  function keywordSourceResultMarkup(source, run) {
    var result = run ? keywordSyncRunResult(run) : {
      raw: numberValue(pick(source, ['last_item_count'], 0)),
      unique: numberValue(pick(source, ['last_item_count'], 0)),
      added: numberValue(pick(source, ['last_new_count', 'new_count'], 0)),
      existing: numberValue(pick(source, ['last_existing_count', 'existing_count'], 0))
    };
    var detail = result.unique + ' 去重后 · +' + result.added + ' 新增 · ' + result.existing + ' 已存在';
    return '<strong>' + formatNumber(result.unique) + '</strong><small class="table-subline">' + escapeHTML(detail) + '</small>';
  }

  function renderKeywordSources() {
    var body = byId('keyword-api-body');
    var empty = byId('keyword-api-empty');
    var items = state.keywordSources.items || [];
    var enabledCount = items.filter(function (item) { return boolValue(item.enabled, false); }).length;
    var extractedCount = items.reduce(function (total, item) {
      var run = keywordSourceLatestRun(item);
      return total + (run ? keywordSyncRunResult(run).unique : numberValue(pick(item, ['last_item_count', 'item_count'], 0)));
    }, 0);
    byId('keyword-api-total').textContent = String(items.length);
    byId('keyword-api-enabled').textContent = String(enabledCount);
    byId('keyword-api-items').textContent = formatNumber(extractedCount);
    if (!items.length) {
      body.innerHTML = '';
      empty.hidden = false;
      refreshIcons(empty);
      return;
    }
    empty.hidden = true;
    body.innerHTML = items.map(function (source) {
      var id = keywordSourceID(source);
      var method = String(pick(source, ['request_method', 'method'], 'GET')).toUpperCase();
      var executor = String(pick(source, ['request_executor'], 'http')).toLowerCase();
      var executorLabel = executor === 'browser' ? '浏览器' : 'HTTP';
      var url = pick(source, ['request_url', 'url'], '');
      var interval = Math.max(1, Math.round(numberValue(pick(source, ['sync_interval_seconds'], 3600)) / 60));
      var enabled = boolValue(source.enabled, false);
      var activeRun = pick(source, ['active_run'], null);
      var latestRun = pick(source, ['latest_run'], null);
      var latest = activeRun || latestRun;
      var stale = keywordSourceIsStale(source);
      var requestCount = numberValue(pick(latest || source, ['request_count', 'last_request_count'], 0));
      var successCount = numberValue(pick(latest || source, ['success_count', 'last_success_count'], 0));
      var failureCount = numberValue(pick(latest || source, ['failure_count', 'last_failure_count'], 0));
      var roundSummary = requestCount > 0 ? ('请求 ' + requestCount + ' 轮 · 成功 ' + successCount + (failureCount ? ' · 失败 ' + failureCount : '')) : '';
      var active = activeRun && ACTIVE_KEYWORD_SYNC_STATUSES.indexOf(keywordSyncRunStatus(activeRun)) >= 0;
      var syncButtonLabel = active ? '同步正在进行' : (stale ? '按新配置同步' : '立即同步');
      return '<tr><td><div class="keyword-api-name"><strong>' + escapeHTML(source.name || '未命名来源') + '</strong><small>' + (enabled ? '自动同步已启用' : '草稿 / 已停用') + (stale ? ' · <span class="stale-label">旧配置结果 / 待同步</span>' : '') + '</small></div></td>' +
        '<td><div class="api-request-summary"><span class="http-status status-ok">' + escapeHTML(method) + '</span><span class="status-badge status-' + escapeHTML(executor === 'browser' ? 'running' : 'success') + '">' + escapeHTML(executorLabel) + '</span><code title="' + escapeHTML(url) + '">' + escapeHTML(url) + '</code></div></td>' +
        '<td>每 ' + interval + ' 分钟<small class="table-subline">下次 ' + escapeHTML(relativeTime(pick(source, ['next_sync_at'], null))) + '</small></td>' +
        '<td>' + keywordSourceStatus(source, latest) + (active ? keywordSourceProgressMarkup(activeRun) : '<small class="table-subline">' + escapeHTML(roundSummary || pick(latest || source, ['error', 'last_error'], '') || relativeTime(pick(latest || source, ['finished_at', 'last_synced_at'], null))) + '</small>') + '</td>' +
        '<td>' + keywordSourceResultMarkup(source, latest) + '</td>' +
        '<td><div class="row-actions"><button class="row-action" type="button" data-action="sync-keyword-api" data-id="' + escapeHTML(id) + '" title="' + escapeHTML(syncButtonLabel) + '" aria-label="' + escapeHTML(syncButtonLabel + ' ' + (source.name || '')) + '"' + (active ? ' disabled' : '') + '>' + icon(active ? 'loader-circle' : 'refresh-cw', active ? 'spin' : '') + '</button><button class="row-action" type="button" data-action="view-keyword-sync-history" data-id="' + escapeHTML(id) + '" title="查看同步记录" aria-label="查看 ' + escapeHTML(source.name || '') + ' 的同步记录">' + icon('history') + '</button><button class="row-action" type="button" data-action="copy-keyword-api" data-id="' + escapeHTML(id) + '" title="复制来源" aria-label="复制 ' + escapeHTML(source.name || '') + '">' + icon('copy') + '</button><button class="row-action" type="button" data-action="edit-keyword-api" data-id="' + escapeHTML(id) + '" title="编辑" aria-label="编辑 ' + escapeHTML(source.name || '') + '">' + icon('pencil') + '</button><button class="row-action danger" type="button" data-action="delete-keyword-api" data-id="' + escapeHTML(id) + '" title="删除" aria-label="删除 ' + escapeHTML(source.name || '') + '">' + icon('trash-2') + '</button></div></td></tr>';
    }).join('');
    refreshIcons(body);
  }

  function populateKeywordSyncSourceFilter() {
    var select = byId('keyword-sync-source-filter');
    if (!select) return;
    var current = select.value;
    var sources = {};
    (state.keywordSources.items || []).forEach(function (source) {
      var id = keywordSourceID(source);
      if (id) sources[id] = source.name || ('来源 #' + id);
    });
    (state.keywordSyncRuns.items || []).forEach(function (run) {
      var id = pick(run, ['source_id'], null);
      if (id !== null && id !== undefined && String(id)) sources[String(id)] = pick(run, ['source_name'], sources[String(id)] || ('来源 #' + id));
    });
    select.innerHTML = '<option value="">全部来源</option>' + Object.keys(sources).sort(function (left, right) {
      return String(sources[left]).localeCompare(String(sources[right]), 'zh-CN');
    }).map(function (id) {
      return '<option value="' + escapeHTML(id) + '">' + escapeHTML(sources[id]) + '</option>';
    }).join('');
    if (Object.prototype.hasOwnProperty.call(sources, current)) select.value = current;
  }

  async function loadKeywordSyncRuns(options) {
    options = options || {};
    if (state.keywordSyncRuns.loaded && !options.force && !options.silent) {
      configureKeywordSourcePolling();
      return;
    }
    state.keywordSyncRuns.loaded = true;
    var serial = ++state.keywordSyncRuns.requestSerial;
    if (!options.silent) {
      showAlert('keyword-sync-history-alert', '');
      byId('keyword-sync-history-empty').hidden = true;
      byId('keyword-sync-history-pagination').innerHTML = '';
      tableLoading('keyword-sync-history-body', 7);
    }
    var query = Object.assign({}, state.keywordSyncRuns.query, {
      page: state.keywordSyncRuns.page,
      page_size: state.keywordSyncRuns.pageSize
    });
    var controller = replaceRequestController('keywordSyncRuns');
    try {
      var data = await apiRequest(API.keywordAPISyncRuns, { query: query, signal: controller.signal });
      if (state.requestControllers.keywordSyncRuns !== controller || serial !== state.keywordSyncRuns.requestSerial) return;
      var items = arrayFrom(data, ['items', 'runs', 'results']);
      var meta = paginationMeta(data, items, state.keywordSyncRuns.page, state.keywordSyncRuns.pageSize);
      state.keywordSyncRuns.items = items;
      Object.assign(state.keywordSyncRuns, meta);
      renderKeywordSyncRuns();
      populateKeywordSyncSourceFilter();
      updateTimestamp();
      configureKeywordSourcePolling();
    } catch (error) {
      if (error.name === 'AbortError') return;
      if (serial !== state.keywordSyncRuns.requestSerial) return;
      state.keywordSyncRuns.loaded = false;
      if (!options.silent) {
        state.keywordSyncRuns.items = [];
        byId('keyword-sync-history-body').innerHTML = '';
        showAlert('keyword-sync-history-alert', error.message || '同步记录加载失败');
        stopKeywordSourcePolling();
      } else configureKeywordSourcePolling();
    } finally { finishRequestController('keywordSyncRuns', controller); }
  }

  function renderKeywordSyncRuns() {
    var body = byId('keyword-sync-history-body');
    var empty = byId('keyword-sync-history-empty');
    var items = state.keywordSyncRuns.items || [];
    if (!items.length) {
      body.innerHTML = '';
      empty.hidden = false;
      byId('keyword-sync-history-pagination').innerHTML = '';
      refreshIcons(empty);
      return;
    }
    empty.hidden = true;
    body.innerHTML = items.map(function (run) {
      var id = keywordSyncRunID(run);
      var sourceName = pick(run, ['source_name'], '已删除来源');
      var sourceID = pick(run, ['source_id'], null);
      var sourceExists = pick(run, ['source_exists'], true) !== false;
      var status = keywordSyncRunStatus(run);
      var active = ACTIVE_KEYWORD_SYNC_STATUSES.indexOf(status) >= 0;
      var progress = keywordSyncRunProgress(run);
      var progressLabel = progress.legacy ? '历史汇总' : (progress.unlimited ? '第 ' + progress.completed + ' 轮 / 无上限' : progress.completed + '/' + progress.total + ' 轮');
      var success = numberValue(pick(run, ['success_iterations', 'success_count'], 0));
      var failed = numberValue(pick(run, ['failed_iterations', 'failure_count'], 0));
      var result = keywordSyncRunResult(run);
      var trigger = String(pick(run, ['trigger'], 'manual')).toLowerCase();
      var started = pick(run, ['started_at', 'queued_at', 'created_at'], null);
      return '<tr>' +
        '<td><div class="keyword-api-name"><strong>' + escapeHTML(sourceName || '已删除来源') + '</strong><small class="mono">RUN #' + escapeHTML(id) + (!sourceExists ? ' · 来源已删除' : '') + '</small></div></td>' +
        '<td>' + statusBadge(status) + (pick(run, ['error'], '') ? '<small class="table-subline danger-text" title="' + escapeHTML(pick(run, ['error'], '')) + '">' + escapeHTML(pick(run, ['error'], '')) + '</small>' : '') + '</td>' +
        '<td><div class="mini-progress keyword-sync-progress"><div class="mini-progress-label"><span>' + escapeHTML(progressLabel) + '</span><span>' + (progress.unlimited ? (active ? '进行中' : '已结束') : progress.percent + '%') + '</span></div><div class="progress-track' + (progress.unlimited && active ? ' is-unlimited' : '') + '"><div class="progress-bar" style="width:' + progress.percent + '%"></div></div></div><small class="table-subline">成功 ' + success + (failed ? ' · 失败 ' + failed : '') + '</small></td>' +
        '<td><strong>' + formatNumber(result.unique) + ' 去重后</strong><small class="table-subline">原始 ' + formatNumber(result.raw) + ' · +' + formatNumber(result.added) + ' 新增 · ' + formatNumber(result.existing) + ' 已存在</small></td>' +
        '<td>' + escapeHTML(triggerLabels[trigger] || trigger) + '<small class="table-subline">配置 v' + formatNumber(pick(run, ['config_revision'], 0)) + '</small></td>' +
        '<td title="' + escapeHTML(formatDate(started, true)) + '">' + escapeHTML(formatDate(started, true)) + '<small class="table-subline">' + escapeHTML(durationFrom(run)) + '</small></td>' +
        '<td><div class="row-actions"><button class="row-action" type="button" data-action="view-keyword-sync-run" data-id="' + escapeHTML(id) + '" aria-label="查看同步运行 ' + escapeHTML(id) + '" title="查看详情">' + icon('panel-right-open') + '</button></div></td>' +
      '</tr>';
    }).join('');
    renderPagination('keyword-sync-history-pagination', state.keywordSyncRuns.page, state.keywordSyncRuns.pages, 'keywordSyncRuns');
    refreshIcons(body);
  }

  function updateKeywordSyncFilters() {
    state.keywordSyncRuns.query = {
      source_id: byId('keyword-sync-source-filter').value,
      status: byId('keyword-sync-status-filter').value,
      trigger: byId('keyword-sync-trigger-filter').value,
      from: dateBoundary(byId('keyword-sync-from-filter').value, false),
      to: dateBoundary(byId('keyword-sync-to-filter').value, true)
    };
    state.keywordSyncRuns.page = 1;
    state.keywordSyncRuns.loaded = false;
    loadKeywordSyncRuns({ force: true });
  }

  function resetKeywordSyncFilters() {
    byId('keyword-sync-filters').reset();
    updateKeywordSyncFilters();
  }

  function viewKeywordSyncHistory(sourceID) {
    switchKeywordTab('history');
    populateKeywordSyncSourceFilter();
    byId('keyword-sync-source-filter').value = String(sourceID || '');
    updateKeywordSyncFilters();
  }

  function keywordSourcesHaveActiveRun() {
    return (state.keywordSources.items || []).some(function (source) {
      var run = pick(source, ['active_run'], null);
      return run && ACTIVE_KEYWORD_SYNC_STATUSES.indexOf(keywordSyncRunStatus(run)) >= 0;
    });
  }

  function keywordSyncHistoryHasActiveRun() {
    return (state.keywordSyncRuns.items || []).some(function (run) {
      return ACTIVE_KEYWORD_SYNC_STATUSES.indexOf(keywordSyncRunStatus(run)) >= 0;
    });
  }

  function configureKeywordSourcePolling() {
    var active = state.keywords.tab === 'api'
      ? keywordSourcesHaveActiveRun()
      : state.keywords.tab === 'history' && keywordSyncHistoryHasActiveRun();
    var visiblePanel = document.visibilityState === 'visible' && state.view === 'keywords' && (state.keywords.tab === 'api' || state.keywords.tab === 'history');
    if (!active || !visiblePanel) {
      stopKeywordSourcePolling();
      return;
    }
    if (state.keywordSources.pollTimer) return;
    state.keywordSources.pollTimer = window.setInterval(function () {
      if (document.visibilityState !== 'visible' || state.view !== 'keywords') return;
      if (state.keywords.tab === 'api') loadKeywordSources(true, { silent: true });
      else if (state.keywords.tab === 'history') loadKeywordSyncRuns({ force: true, silent: true });
    }, 5000);
  }

  function stopKeywordSourcePolling() {
    if (!state.keywordSources.pollTimer) return;
    clearInterval(state.keywordSources.pollTimer);
    state.keywordSources.pollTimer = null;
  }

  function normalizeKVValues(values) {
    if (!values) return [];
    if (Array.isArray(values)) return values.map(function (item) { return { key: pick(item, ['key', 'name'], ''), value: pick(item, ['value'], '') }; });
    return Object.keys(values).map(function (key) { return { key: key, value: values[key] }; });
  }

  function renderAPIRows(targetID, values) {
    var target = byId(targetID);
    var rows = normalizeKVValues(values);
    target.innerHTML = rows.map(function (item) {
      return '<div class="api-kv-row"><input data-api-kv-key type="text" placeholder="键" value="' + escapeHTML(item.key) + '"><input data-api-kv-value type="text" placeholder="值" value="' + escapeHTML(item.value) + '"><button class="row-action danger" type="button" data-action="remove-api-kv" aria-label="删除字段">' + icon('x') + '</button></div>';
    }).join('');
    refreshIcons(target);
  }

  function addAPIRow(targetID, key, value) {
    var target = byId(targetID);
    var wrapper = document.createElement('div');
    wrapper.className = 'api-kv-row';
    wrapper.innerHTML = '<input data-api-kv-key type="text" placeholder="键" value="' + escapeHTML(key || '') + '"><input data-api-kv-value type="text" placeholder="值" value="' + escapeHTML(value || '') + '"><button class="row-action danger" type="button" data-action="remove-api-kv" aria-label="删除字段">' + icon('x') + '</button>';
    target.appendChild(wrapper);
    refreshIcons(wrapper);
    wrapper.querySelector('[data-api-kv-key]').focus();
  }

  function collectAPIRows(targetID) {
    var result = {};
    byId(targetID).querySelectorAll('.api-kv-row').forEach(function (row) {
      var key = row.querySelector('[data-api-kv-key]').value.trim();
      if (key) result[key] = row.querySelector('[data-api-kv-value]').value;
    });
    return result;
  }

  var apiEditorDefinitions = {
    query: { rows: 'api-query-rows', json: 'api-query-json', error: 'api-query-json-error', label: 'Query' },
    header: { rows: 'api-header-rows', json: 'api-header-json', error: 'api-header-json-error', label: 'Header' },
    form: { rows: 'api-form-rows', json: 'api-form-json', error: 'api-form-json-error', label: 'Form' }
  };

  function formatAPIObject(value) {
    return JSON.stringify(value && typeof value === 'object' && !Array.isArray(value) ? value : {}, null, 2);
  }

  function parseAPIObjectEditor(target) {
    var definition = apiEditorDefinitions[target];
    var text = byId(definition.json).value.trim();
    var value;
    try { value = text ? JSON.parse(text) : {}; }
    catch (error) { throw new Error(definition.label + ' JSON 格式无效：' + error.message); }
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      throw new Error(definition.label + ' JSON 的根节点必须是对象');
    }
    var normalized = {};
    Object.keys(value).forEach(function (key) {
      var item = value[key];
      if (item !== null && (item === undefined || typeof item === 'object')) {
        throw new Error(definition.label + ' JSON 的字段值必须是文本、数字、布尔值或 null：' + key);
      }
      normalized[key] = item === null ? '' : String(item);
    });
    return normalized;
  }

  function showAPIEditorError(target, message) {
    var error = byId(apiEditorDefinitions[target].error);
    error.textContent = message || '';
    error.hidden = !message;
    byId(apiEditorDefinitions[target].json).setAttribute('aria-invalid', message ? 'true' : 'false');
  }

  function renderAPIEditorMode(target) {
    var mode = state.keywordSources.editorModes[target] || 'kv';
    var definition = apiEditorDefinitions[target];
    document.querySelectorAll('[data-editor-target="' + target + '"]').forEach(function (button) {
      var active = button.dataset.editorMode === mode;
      button.classList.toggle('active', active);
      button.setAttribute('aria-selected', active ? 'true' : 'false');
    });
    document.querySelectorAll('[data-api-editor-panel^="' + target + '-"]').forEach(function (panel) {
      panel.hidden = panel.dataset.apiEditorPanel !== target + '-' + mode;
    });
    var section = target === 'form' ? byId('api-body-form') : document.querySelector('[data-api-config="' + target + '"]');
    var addButton = section ? section.querySelector('.api-add-row') : null;
    if (addButton) addButton.hidden = mode !== 'kv';
  }

  function switchAPIEditorMode(target, nextMode) {
    var definition = apiEditorDefinitions[target];
    if (!definition || (nextMode !== 'kv' && nextMode !== 'json')) return false;
    var currentMode = state.keywordSources.editorModes[target] || 'kv';
    if (currentMode === nextMode) return true;
    if (currentMode === 'kv') {
      byId(definition.json).value = formatAPIObject(collectAPIRows(definition.rows));
    } else {
      try {
        var value = parseAPIObjectEditor(target);
        renderAPIRows(definition.rows, value);
        byId(definition.json).value = formatAPIObject(value);
        showAPIEditorError(target, '');
      } catch (error) {
        showAPIEditorError(target, error.message);
        byId(definition.json).focus();
        return false;
      }
    }
    state.keywordSources.editorModes[target] = nextMode;
    renderAPIEditorMode(target);
    return true;
  }

  function validateAPIObjectEditor(target) {
    if ((state.keywordSources.editorModes[target] || 'kv') !== 'json') return true;
    try {
      var value = parseAPIObjectEditor(target);
      byId(apiEditorDefinitions[target].json).value = formatAPIObject(value);
      renderAPIRows(apiEditorDefinitions[target].rows, value);
      showAPIEditorError(target, '');
      return true;
    } catch (error) {
      showAPIEditorError(target, error.message);
      return false;
    }
  }

  function collectAPIEditorValue(target) {
    var definition = apiEditorDefinitions[target];
    return state.keywordSources.editorModes[target] === 'json' ? parseAPIObjectEditor(target) : collectAPIRows(definition.rows);
  }

  function resetAPIEditor(target, value) {
    var definition = apiEditorDefinitions[target];
    var normalized = value && typeof value === 'object' && !Array.isArray(value) ? value : {};
    renderAPIRows(definition.rows, normalized);
    byId(definition.json).value = formatAPIObject(normalized);
    state.keywordSources.editorModes[target] = 'kv';
    showAPIEditorError(target, '');
    renderAPIEditorMode(target);
  }

  function formatAPIIterationDuration(seconds) {
    seconds = Math.max(0, Math.trunc(numberValue(seconds, 0)));
    if (seconds === 0) return '0 秒';
    var hours = Math.floor(seconds / 3600);
    var minutes = Math.floor((seconds % 3600) / 60);
    var remainingSeconds = seconds % 60;
    var parts = [];
    if (hours) parts.push(hours + ' 小时');
    if (minutes) parts.push(minutes + ' 分');
    if (remainingSeconds || !parts.length) parts.push(remainingSeconds + ' 秒');
    return parts.join(' ');
  }

  function formatAPIIterationDurationRange(minimum, maximum) {
    var minText = formatAPIIterationDuration(minimum);
    var maxText = formatAPIIterationDuration(maximum);
    return minimum === maximum ? minText : minText + ' – ' + maxText;
  }

  function updateAPIIterationPreview() {
    var enabled = byId('api-iteration-enabled').checked;
    var unlimited = byId('api-iteration-unlimited').checked;
    var fields = byId('api-iteration-fields');
    var countInput = byId('api-iteration-count');
    fields.hidden = !enabled;
    countInput.disabled = unlimited;
    var location = byId('api-iteration-location').value;
    var bodyType = byId('api-body-type').value;
    var hint = byId('api-iteration-hint');
    if (location === 'query') hint.textContent = '将覆盖 Query 中对应键的值；现场测试只注入起始值。';
    else if (location === 'header') hint.textContent = '将以十进制文本覆盖 Header 中对应键的值。';
    else if (bodyType === 'json') hint.textContent = '使用点路径定位 JSON 字段，例如 pagination.offset。';
    else if (bodyType === 'form') hint.textContent = '参数路径为 Form 字段名，迭代值将按文本发送。';
    else hint.textContent = 'Body 迭代仅支持 JSON 或 Form，请先调整 Body 类型。';
    if (!enabled) return;
    var start = Math.trunc(numberValue(byId('api-iteration-start').value, 0));
    var step = Math.trunc(numberValue(byId('api-iteration-step').value, 20));
    var count = Math.max(1, Math.min(100, Math.trunc(numberValue(byId('api-iteration-count').value, 1))));
    var fixedDelay = Math.max(0, Math.min(3600, Math.trunc(numberValue(byId('api-iteration-delay').value, 0))));
    var randomDelayMin = Math.max(-3600, Math.min(3600, Math.trunc(numberValue(byId('api-iteration-random-delay-min').value, 0))));
    var randomDelayMax = Math.max(-3600, Math.min(3600, Math.trunc(numberValue(byId('api-iteration-random-delay-max').value, 0))));
    var noKeywordStopCount = Math.trunc(numberValue(byId('api-iteration-no-keyword-stop-count').value, 0));
    var delayMinimum = Math.max(0, fixedDelay + randomDelayMin);
    var delayMaximum = Math.max(0, fixedDelay + randomDelayMax);
    var previewCount = unlimited ? 6 : Math.min(count, 6);
    var values = [];
    for (var index = 0; index < previewCount; index += 1) values.push(start + step * index);
    var sequence = values.join(' → ');
    if (unlimited) {
      sequence += ' → …';
      var stopText = noKeywordStopCount >= 1 && noKeywordStopCount <= 100 ? '连续 ' + noKeywordStopCount + ' 轮无关键词' : '需设置 1–100 轮';
      var perRoundDelay = randomDelayMin > randomDelayMax ? '随机范围无效' : formatAPIIterationDurationRange(delayMinimum, delayMaximum);
      byId('api-iteration-preview').innerHTML = '<div><span>无限序列 · 持续迭代</span><strong>' + escapeHTML(sequence) + '</strong></div><div><span>停止条件</span><strong>' + escapeHTML(stopText) + '</strong><small>单轮等待 ' + escapeHTML(perRoundDelay) + ' · 不含网络请求时间</small></div>';
      return;
    }
    if (count > 6) sequence += ' → … → ' + (start + step * (count - 1));
    var totalDelay = randomDelayMin > randomDelayMax ? '随机范围无效' : formatAPIIterationDurationRange((count - 1) * delayMinimum, (count - 1) * delayMaximum);
    var earlyStop = noKeywordStopCount >= 1 && noKeywordStopCount <= 100 ? ' · 连续 ' + noKeywordStopCount + ' 轮无关键词时提前停止' : '';
    byId('api-iteration-preview').innerHTML = '<div><span>有限序列 · 共 ' + count + ' 轮</span><strong>' + escapeHTML(sequence) + '</strong></div><div><span>理论总等待</span><strong>' + escapeHTML(totalDelay) + '</strong><small>不含网络请求时间' + escapeHTML(earlyStop) + '</small></div>';
  }

  function syncAPIBodyEditor() {
    var type = byId('api-body-type').value;
    ['json', 'form', 'raw'].forEach(function (name) { byId('api-body-' + name).hidden = type !== name; });
    updateAPIIterationPreview();
  }

  function resetKeywordAPIBuilder() {
    var form = byId('keyword-form');
    resetAPIEditor('query', {});
    resetAPIEditor('header', {});
    resetAPIEditor('form', {});
    form.elements.request_executor.value = 'http';
    form.elements.request_method.value = 'GET';
    form.elements.body_type.value = 'none';
    form.elements.timeout_seconds.value = 15;
    form.elements.sync_interval_minutes.value = 60;
    form.elements.default_keyword_type.value = 'general';
    form.elements.default_priority.value = 0;
    form.elements.default_cooldown_days.value = '';
    form.elements.default_enabled.checked = true;
    form.elements.api_enabled.checked = false;
    form.elements.iteration_enabled.checked = false;
    form.elements.iteration_location.value = 'query';
    form.elements.iteration_path.value = '';
    form.elements.iteration_start.value = 0;
    form.elements.iteration_step.value = 20;
    form.elements.iteration_count.value = 1;
    form.elements.iteration_delay_seconds.value = 0;
    form.elements.iteration_unlimited.checked = false;
    form.elements.iteration_no_keyword_stop_count.value = 0;
    form.elements.iteration_random_delay_min_seconds.value = 0;
    form.elements.iteration_random_delay_max_seconds.value = 0;
    byId('api-response-path').value = '';
    byId('api-test-meta').innerHTML = '<span>尚未测试</span>';
    byId('api-json-tree').innerHTML = '<div class="api-inspector-empty">' + icon('braces') + '<span>测试成功后在此查看 JSON</span></div>';
    byId('api-extract-preview').innerHTML = '<strong>提取预览</strong><p>选择路径后显示匹配值。</p>';
    syncAPIBodyEditor();
    updateAPIIterationPreview();
    refreshIcons(byId('keyword-api-fields'));
  }

  function keywordAPIPayload(form) {
    var bodyType = form.elements.body_type.value;
    var requestBody = null;
    if (bodyType === 'json') {
      var jsonText = form.elements.json_body.value.trim();
      try { requestBody = jsonText ? JSON.parse(jsonText) : {}; }
      catch (error) { throw new Error('JSON Body 格式无效：' + error.message); }
    } else if (bodyType === 'form') requestBody = collectAPIEditorValue('form');
    else if (bodyType === 'raw') requestBody = form.elements.raw_body.value;
    return {
      name: form.elements.api_name.value.trim(),
      enabled: form.elements.api_enabled.checked,
      request_executor: form.elements.request_executor.value || 'http',
      request_method: form.elements.request_method.value,
      request_url: form.elements.request_url.value.trim(),
      request_headers: collectAPIEditorValue('header'),
      query_params: collectAPIEditorValue('query'),
      body_type: bodyType,
      request_body: requestBody,
      proxy_url: form.elements.proxy_url.value.trim(),
      timeout_seconds: numberValue(form.elements.timeout_seconds.value, 15),
      response_path: form.elements.response_path.value.trim(),
      sync_interval_seconds: Math.max(60, Math.round(numberValue(form.elements.sync_interval_minutes.value, 60) * 60)),
      default_keyword_type: form.elements.default_keyword_type.value.trim() || 'general',
      default_priority: numberValue(form.elements.default_priority.value),
      default_cooldown_seconds: form.elements.default_cooldown_days.value.trim() === '' ? null : Math.round(numberValue(form.elements.default_cooldown_days.value) * 86400),
      default_enabled: form.elements.default_enabled.checked,
      iteration_enabled: form.elements.iteration_enabled.checked,
      iteration_location: form.elements.iteration_location.value || 'query',
      iteration_path: form.elements.iteration_path.value.trim(),
      iteration_start: Math.trunc(numberValue(form.elements.iteration_start.value, 0)),
      iteration_step: Math.trunc(numberValue(form.elements.iteration_step.value, 20)),
      iteration_count: Math.trunc(numberValue(form.elements.iteration_count.value, 1)),
      iteration_delay_seconds: Math.trunc(numberValue(form.elements.iteration_delay_seconds.value, 0)),
      iteration_unlimited: form.elements.iteration_unlimited.checked,
      iteration_no_keyword_stop_count: Math.trunc(numberValue(form.elements.iteration_no_keyword_stop_count.value, 0)),
      iteration_random_delay_min_seconds: Math.trunc(numberValue(form.elements.iteration_random_delay_min_seconds.value, 0)),
      iteration_random_delay_max_seconds: Math.trunc(numberValue(form.elements.iteration_random_delay_max_seconds.value, 0))
    };
  }

  function keywordAPIRequestSignature(payload) {
    return JSON.stringify({
      request_method: payload.request_method,
      request_executor: payload.request_executor,
      request_url: payload.request_url,
      request_headers: payload.request_headers,
      query_params: payload.query_params,
      body_type: payload.body_type,
      request_body: payload.request_body,
      proxy_url: payload.proxy_url,
      timeout_seconds: payload.timeout_seconds,
      iteration_enabled: payload.iteration_enabled,
      iteration_location: payload.iteration_location,
      iteration_path: payload.iteration_path,
      iteration_start: payload.iteration_start,
      iteration_step: payload.iteration_step,
      iteration_count: payload.iteration_count,
      iteration_delay_seconds: payload.iteration_delay_seconds,
      iteration_unlimited: payload.iteration_unlimited,
      iteration_no_keyword_stop_count: payload.iteration_no_keyword_stop_count,
      iteration_random_delay_min_seconds: payload.iteration_random_delay_min_seconds,
      iteration_random_delay_max_seconds: payload.iteration_random_delay_max_seconds
    });
  }

  function keywordAPIIterationValidationError(payload) {
    if (payload.iteration_count < 1 || payload.iteration_count > 100) return '请求次数必须是 1–100 之间的整数';
    if (payload.iteration_delay_seconds < 0 || payload.iteration_delay_seconds > 3600) return '固定间隔必须是 0–3600 秒之间的整数';
    if (payload.iteration_no_keyword_stop_count < 0 || payload.iteration_no_keyword_stop_count > 100) return '连续无关键词停止次数必须是 0–100 之间的整数';
    if (payload.iteration_random_delay_min_seconds < -3600 || payload.iteration_random_delay_min_seconds > 3600 || payload.iteration_random_delay_max_seconds < -3600 || payload.iteration_random_delay_max_seconds > 3600) return '随机延迟最小值和最大值必须是 -3600–3600 秒之间的整数';
    if (payload.iteration_random_delay_min_seconds > payload.iteration_random_delay_max_seconds) return '随机延迟最小值不能大于最大值';
    if (!payload.iteration_enabled) return '';
    if (!payload.iteration_path) return '启用分页迭代后必须填写参数路径';
    if (payload.iteration_unlimited && payload.iteration_no_keyword_stop_count < 1) return '无限迭代必须设置连续无关键词停止次数（1–100）';
    if (payload.iteration_location === 'body' && ['json', 'form'].indexOf(payload.body_type) < 0) return 'Body 迭代仅支持 JSON 或 Form 请求体';
    return '';
  }

  async function saveKeywordAPISource(form, syncAfterSave) {
    var error = byId('keyword-form-error');
    error.hidden = true;
    var payload;
    try { payload = keywordAPIPayload(form); } catch (parseError) {
      error.textContent = parseError.message || '请求配置 JSON 格式无效';
      error.hidden = false;
      return;
    }
    if (!payload.name || !/^https?:\/\//i.test(payload.request_url)) {
      error.textContent = '请填写来源名称和有效的 HTTP/HTTPS URL';
      error.hidden = false;
      return;
    }
    if (payload.enabled && !payload.response_path) {
      error.textContent = '启用自动同步前必须测试并选择响应路径';
      error.hidden = false;
      return;
    }
    var iterationError = keywordAPIIterationValidationError(payload);
    if (iterationError) {
      error.textContent = iterationError;
      error.hidden = false;
      return;
    }
    var editing = state.keywordSources.editing;
    var requestSignature = keywordAPIRequestSignature(payload);
    var existingConfigurationIsStillValid = editing && requestSignature === state.keywordSources.originalSignature && payload.response_path === String(editing.response_path || '');
    if (payload.enabled && requestSignature !== state.keywordSources.testedSignature && !existingConfigurationIsStillValid) {
      error.textContent = '启用自动同步前，请先测试当前请求配置';
      error.hidden = false;
      return;
    }
    var button = syncAfterSave ? byId('keyword-save-and-sync') : byId('keyword-save');
    var otherButton = syncAfterSave ? byId('keyword-save') : byId('keyword-save-and-sync');
    setButtonLoading(button, true, syncAfterSave ? '保存中' : '保存中');
    otherButton.disabled = true;
    try {
      var saved = await apiRequest(editing ? API.keywordAPISources + '/' + encodeURIComponent(keywordSourceID(editing)) : API.keywordAPISources, { method: editing ? 'PUT' : 'POST', body: payload });
      var savedSource = pick(saved, ['source', 'item'], saved);
      var savedID = keywordSourceID(savedSource) || (editing ? keywordSourceID(editing) : '');
      byId('keyword-dialog').close();
      state.keywordSources.loaded = false;
      state.keywordSyncRuns.loaded = false;
      await loadKeywordSources(true);
      if (!syncAfterSave) {
        toast(editing ? 'API 来源已更新' : 'API 来源已新增', 'success');
        return savedSource;
      }
      try {
        if (!savedID) throw new Error('保存响应缺少来源 ID');
        var started = await triggerKeywordAPISourceSync(savedID, 'save');
        var run = pick(started, ['run'], null);
        var runID = pick(started, ['run_id'], run ? keywordSyncRunID(run) : '');
        toast(boolValue(pick(started, ['already_active'], false)) ? '配置已保存，已打开正在执行的同步' : '配置已保存并开始同步', 'success');
        state.keywordSyncRuns.loaded = false;
        switchKeywordTab('history');
        loadKeywordSyncRuns({ force: true, silent: true });
        if (runID) openKeywordSyncRunDetail(runID);
      } catch (syncError) {
        toast('配置已保存，但同步未启动：' + (syncError.message || '请稍后重试'), 'error');
      }
      return savedSource;
    } catch (saveError) {
      error.textContent = saveError.message || 'API 来源保存失败';
      error.hidden = false;
    } finally {
      setButtonLoading(button, false);
      otherButton.disabled = false;
    }
  }

  async function openKeywordAPISource(id) {
    try {
      var detail = await apiRequest(API.keywordAPISources + '/' + encodeURIComponent(id));
      var source = pick(detail, ['source', 'item'], detail);
      state.keywordSources.editing = source;
      var form = byId('keyword-form');
      form.reset();
      resetKeywordAPIBuilder();
      state.keywordSources.editing = source;
      form.elements.api_name.value = source.name || '';
      form.elements.api_enabled.checked = boolValue(source.enabled, false);
      form.elements.request_executor.value = source.request_executor || 'http';
      form.elements.request_method.value = String(source.request_method || 'GET').toUpperCase();
      form.elements.request_url.value = source.request_url || '';
      form.elements.body_type.value = source.body_type || 'none';
      form.elements.proxy_url.value = source.proxy_url || '';
      form.elements.timeout_seconds.value = numberValue(source.timeout_seconds, 15);
      form.elements.sync_interval_minutes.value = Math.max(1, numberValue(source.sync_interval_seconds, 3600) / 60);
      form.elements.response_path.value = source.response_path || '';
      form.elements.default_keyword_type.value = source.default_keyword_type || 'general';
      form.elements.default_priority.value = numberValue(source.default_priority, 0);
      form.elements.default_cooldown_days.value = source.default_cooldown_seconds === null || source.default_cooldown_seconds === undefined ? '' : numberValue(source.default_cooldown_seconds) / 86400;
      form.elements.default_enabled.checked = pick(source, ['default_enabled'], true) !== false;
      form.elements.iteration_enabled.checked = boolValue(source.iteration_enabled, false);
      form.elements.iteration_location.value = source.iteration_location || 'query';
      form.elements.iteration_path.value = source.iteration_path || '';
      form.elements.iteration_start.value = numberValue(source.iteration_start, 0);
      form.elements.iteration_step.value = numberValue(source.iteration_step, 20);
      form.elements.iteration_count.value = Math.max(1, numberValue(source.iteration_count, 1));
      form.elements.iteration_delay_seconds.value = Math.max(0, numberValue(source.iteration_delay_seconds, 0));
      form.elements.iteration_unlimited.checked = boolValue(source.iteration_unlimited, false);
      form.elements.iteration_no_keyword_stop_count.value = numberValue(source.iteration_no_keyword_stop_count, 0);
      form.elements.iteration_random_delay_min_seconds.value = numberValue(source.iteration_random_delay_min_seconds, 0);
      form.elements.iteration_random_delay_max_seconds.value = numberValue(source.iteration_random_delay_max_seconds, 0);
      resetAPIEditor('header', source.request_headers || {});
      resetAPIEditor('query', source.query_params || {});
      if (source.body_type === 'json') form.elements.json_body.value = typeof source.request_body === 'string' ? source.request_body : JSON.stringify(source.request_body || {}, null, 2);
      else if (source.body_type === 'form') resetAPIEditor('form', source.request_body || {});
      else if (source.body_type === 'raw') form.elements.raw_body.value = String(source.request_body || '');
      syncAPIBodyEditor();
      updateAPIIterationPreview();
      state.keywordSources.originalSignature = keywordAPIRequestSignature(keywordAPIPayload(form));
      setKeywordDialogMode('api', true);
      byId('keyword-dialog').showModal();
      refreshIcons(byId('keyword-dialog'));
    } catch (error) { toast(error.message || 'API 来源详情加载失败', 'error'); }
  }

  function renderKeywordAPITest(result) {
    var candidates = arrayFrom(result, ['candidates', 'fields', 'paths']);
    var extraction = pick(result, ['extraction'], null);
    var candidatesTotal = numberValue(pick(result, ['candidates_total'], candidates.length), candidates.length);
    state.keywordSources.testResponse = { candidates: candidates, extraction: extraction };
    var status = numberValue(pick(result, ['status_code', 'status'], 200), 200);
    var iterationValue = pick(result, ['iteration_value'], null);
    byId('api-test-meta').innerHTML = '<span class="http-status ' + (status >= 200 && status < 300 ? 'status-ok' : 'status-error') + '">' + status + '</span><span>' + numberValue(pick(result, ['duration_ms', 'elapsed_ms'], 0)) + ' ms</span><span>' + formatBytes(pick(result, ['response_size', 'size_bytes'], 0)) + '</span><span>字段 ' + formatNumber(candidates.length) + (candidatesTotal > candidates.length ? ' / ' + formatNumber(candidatesTotal) : '') + '</span>' + (iterationValue !== null && iterationValue !== undefined ? '<span class="api-test-iteration">首轮参数 <strong>' + escapeHTML(iterationValue) + '</strong></span>' : '');
    byId('api-json-tree').innerHTML = candidates.length ? candidates.map(function(c){var path=typeof c==='string'?c:pick(c,['path','json_path'],'');var samples=arrayFrom(pick(c,['samples','values'],[]),['items']).slice(0,3);return '<div class="json-tree-leaf" style="--depth:0"><button type="button" data-action="select-json-path" data-path="'+escapeHTML(path)+'"><span>'+escapeHTML(path||'$')+'</span><code>'+escapeHTML(samples.join(' · ')||'暂无样例')+'</code><em>'+formatNumber(pick(c,['count','total'],samples.length))+' 项</em></button></div>';}).join('') : '<div class="api-inspector-empty">未发现可提取字段</div>';
    refreshIcons(byId('keyword-api-fields'));
    updateKeywordAPIExtractPreview();
  }

  async function testKeywordAPI() {
    var form = byId('keyword-form');
    var error = byId('keyword-form-error');
    error.hidden = true;
    var payload;
    try { payload = keywordAPIPayload(form); } catch (parseError) {
      error.textContent = parseError.message || '请求配置 JSON 格式无效';
      error.hidden = false;
      return;
    }
    if (!/^https?:\/\//i.test(payload.request_url)) {
      error.textContent = '请输入有效的 HTTP/HTTPS URL';
      error.hidden = false;
      return;
    }
    var iterationError = keywordAPIIterationValidationError(payload);
    if (iterationError) {
      error.textContent = iterationError;
      error.hidden = false;
      return;
    }
    var button = byId('api-test-button');
    setButtonLoading(button, true, '测试中');
    try {
      var result = await apiRequest(API.keywordAPISourceTest, { method: 'POST', body: payload });
      state.keywordSources.testedSignature = keywordAPIRequestSignature(payload);
      renderKeywordAPITest(result);
      toast('请求测试成功，请选择提取字段', 'success');
    } catch (testError) {
      error.textContent = testError.message || '请求测试失败';
      error.hidden = false;
    } finally { setButtonLoading(button, false); }
  }

  function updateKeywordAPIExtractPreview() {
    var target = byId('api-extract-preview');
    var path = byId('api-response-path').value.trim();
    if (!state.keywordSources.testResponse || !path) {
      target.innerHTML = '<strong>提取预览</strong><p>选择路径后显示匹配值。</p>';
      return;
    }
    var candidate = arrayFrom(state.keywordSources.testResponse, ['candidates']).find(function(c){return String(typeof c==='string'?c:pick(c,['path','json_path'],''))===path;});
    if (candidate) { var samples=arrayFrom(pick(candidate,['samples','values'],[]),['items']); target.innerHTML='<div class="api-preview-heading"><strong>提取预览</strong><span>共 '+formatNumber(pick(candidate,['count','total'],samples.length))+' 项</span></div><div class="api-preview-values">'+samples.slice(0,30).map(function(v){return '<span>'+escapeHTML(v)+'</span>';}).join('')+'</div>'; return; }
    var extraction = pick(state.keywordSources.testResponse, ['extraction'], null);
    if (extraction && String(pick(extraction, ['path'], '')) === path) {
      var values = arrayFrom(extraction, ['values']).map(function (value) { return typeof value === 'object' ? pick(value, ['value', 'normalized'], '') : value; }).filter(function (value) { return value !== ''; });
      target.innerHTML = '<div class="api-preview-heading"><strong>提取预览</strong><span>原始 ' + formatNumber(pick(extraction, ['raw_count'], values.length)) + ' · 去重 ' + formatNumber(pick(extraction, ['unique_count'], values.length)) + '</span></div><div class="api-preview-values">' + values.slice(0, 20).map(function (value) { return '<span>' + escapeHTML(value) + '</span>'; }).join('') + '</div>';
      return;
    }
    target.innerHTML = '<strong>提取预览</strong><p>当前路径不在本次候选中，请重新测试以更新统计和样例。</p>';
  }

  function selectKeywordAPIPath(path) {
    state.keywordSources.selectedPath = path;
    byId('api-response-path').value = path;
    updateKeywordAPIExtractPreview();
  }

  function triggerKeywordAPISourceSync(id, trigger) {
    return apiRequest(API.keywordAPISources + '/' + encodeURIComponent(id) + '/sync', { method: 'POST', body: { trigger: trigger || 'manual' } });
  }

  async function syncKeywordAPISource(id) {
    try {
      var started = await triggerKeywordAPISourceSync(id, 'manual');
      var run = pick(started, ['run'], null);
      var runID = pick(started, ['run_id'], run ? keywordSyncRunID(run) : '');
      var alreadyActive = boolValue(pick(started, ['already_active'], false));
      toast(alreadyActive ? '该来源已有同步在执行，已打开当前进度' : 'API 来源已开始后台同步', 'success');
      state.keywordSources.loaded = false;
      state.keywordSyncRuns.loaded = false;
      await loadKeywordSources(true);
      state.loaded.keywords = false;
      if (runID) openKeywordSyncRunDetail(runID);
    } catch (error) { toast(error.message || '立即同步失败', 'error'); }
  }

  async function openKeywordSyncRunDetail(id, silent) {
    var dialog = byId('keyword-sync-detail-dialog');
    var container = byId('keyword-sync-detail');
    if (state.keywordSyncRuns.detailController) state.keywordSyncRuns.detailController.abort();
    var controller = new AbortController();
    state.keywordSyncRuns.detailController = controller;
    state.keywordSyncRuns.detailID = String(id);
    if (!silent) {
      if (!dialog.open) dialog.showModal();
      container.innerHTML = '<div class="table-loading"><span>' + icon('loader-circle', 'spin') + '正在加载</span></div>';
      refreshIcons(container);
    }
    try {
      var data = await apiRequest(API.keywordAPISyncRuns + '/' + encodeURIComponent(id), { signal: controller.signal });
      if (state.keywordSyncRuns.detailController !== controller || state.keywordSyncRuns.detailID !== String(id) || !dialog.open) return;
      var run = pick(data, ['run', 'item'], data);
      state.keywordSyncRuns.currentDetail = run;
      renderKeywordSyncRunDetail(run);
      if (!silent || !state.keywordSyncRuns.iterations.length) await loadKeywordSyncIterations(true, pick(run, ['request_summary'], {}));
      else await refreshKeywordSyncIterationTail(pick(run, ['request_summary'], {}));
      if (ACTIVE_KEYWORD_SYNC_STATUSES.indexOf(keywordSyncRunStatus(run)) >= 0) startKeywordSyncDetailPolling();
      else stopKeywordSyncDetailPolling();
    } catch (error) {
      if (error.name !== 'AbortError' && !silent) container.innerHTML = '<div class="inline-alert error">' + escapeHTML(error.message || '同步详情加载失败') + '</div>';
    } finally {
      if (state.keywordSyncRuns.detailController === controller) state.keywordSyncRuns.detailController = null;
    }
  }

  async function loadKeywordSyncIterations(reset, summary) {
    if (state.keywordSyncRuns.iterationController) {
      if (!reset) return;
      state.keywordSyncRuns.iterationController.abort();
    }
    if (reset) { state.keywordSyncRuns.iterations = []; state.keywordSyncRuns.iterationPage = 1; state.keywordSyncRuns.iterationPages = 1; }
    var page = state.keywordSyncRuns.iterationPage;
    if (!reset && page > state.keywordSyncRuns.iterationPages) return;
    var id = state.keywordSyncRuns.detailID;
    var controller = new AbortController();
    state.keywordSyncRuns.iterationController = controller;
    try {
      var data = await apiRequest(API.keywordAPISyncRuns + '/' + encodeURIComponent(id) + '/iterations', { query: { page: page, page_size: 50 }, signal: controller.signal });
      if (state.keywordSyncRuns.iterationController !== controller || state.keywordSyncRuns.detailID !== id || !byId('keyword-sync-detail-dialog').open) return;
      var items = arrayFrom(data, ['iterations','items']), meta = paginationMeta(data, items, page, 50); state.keywordSyncRuns.iterations = state.keywordSyncRuns.iterations.concat(items); state.keywordSyncRuns.iterationPages = meta.pages; state.keywordSyncRuns.iterationPage = page + 1;
      renderKeywordSyncIterations(summary);
    } catch(error){if(error.name!=='AbortError')toast(error.message||'迭代记录加载失败','error');}
    finally { if (state.keywordSyncRuns.iterationController === controller) state.keywordSyncRuns.iterationController = null; }
  }

  function renderKeywordSyncIterations(summary) {
    var body = byId('keyword-sync-iterations-body');
    if (body) body.innerHTML = state.keywordSyncRuns.iterations.length ? state.keywordSyncRuns.iterations.map(function(i){return renderKeywordSyncIteration(i, summary || {});}).join('') : '<tr class="table-empty"><td colspan="7">暂无逐轮记录</td></tr>';
    var more = byId('keyword-sync-iterations-more');
    if (more) more.hidden = state.keywordSyncRuns.iterationPage > state.keywordSyncRuns.iterationPages;
  }

  async function refreshKeywordSyncIterationTail(summary) {
    if (state.keywordSyncRuns.iterationController || !state.keywordSyncRuns.detailID) return;
    var id = state.keywordSyncRuns.detailID;
    var page = Math.max(1, state.keywordSyncRuns.iterationPage - 1);
    var controller = new AbortController();
    state.keywordSyncRuns.iterationController = controller;
    try {
      var data = await apiRequest(API.keywordAPISyncRuns + '/' + encodeURIComponent(id) + '/iterations', { query: { page: page, page_size: 50 }, signal: controller.signal });
      if (state.keywordSyncRuns.iterationController !== controller || state.keywordSyncRuns.detailID !== id || !byId('keyword-sync-detail-dialog').open) return;
      var items = arrayFrom(data, ['iterations','items']), meta = paginationMeta(data, items, page, 50);
      var offset = (page - 1) * 50;
      state.keywordSyncRuns.iterations = state.keywordSyncRuns.iterations.slice(0, offset).concat(items);
      state.keywordSyncRuns.iterationPages = meta.pages;
      state.keywordSyncRuns.iterationPage = page + 1;
      renderKeywordSyncIterations(summary);
    } catch (error) { if (error.name !== 'AbortError') toast(error.message || '迭代记录刷新失败', 'error'); }
    finally { if (state.keywordSyncRuns.iterationController === controller) state.keywordSyncRuns.iterationController = null; }
  }

  function cancelKeywordSyncDetailRequests() {
    if (state.keywordSyncRuns.detailController) state.keywordSyncRuns.detailController.abort();
    if (state.keywordSyncRuns.iterationController) state.keywordSyncRuns.iterationController.abort();
    state.keywordSyncRuns.detailController = null;
    state.keywordSyncRuns.iterationController = null;
  }

  function keywordSyncRequestSummaryMarkup(summary) {
    summary = summary && typeof summary === 'object' ? summary : {};
    var method = String(pick(summary, ['request_method'], 'GET')).toUpperCase();
    var executor = String(pick(summary, ['request_executor'], 'http')).toLowerCase();
    var executorLabel = executor === 'browser' ? '浏览器模式' : '标准 HTTP';
    var url = pick(summary, ['request_url'], '—');
    var headers = arrayFrom(pick(summary, ['header_keys'], []), ['items']);
    var query = arrayFrom(pick(summary, ['query_keys'], []), ['items']);
    var bodyType = pick(summary, ['body_type'], 'none');
    var iterationEnabled = boolValue(pick(summary, ['iteration_enabled'], false), false);
    var iterationText = '未启用';
    if (iterationEnabled) {
      iterationText = (boolValue(pick(summary, ['iteration_unlimited'], false), false) ? '无限迭代' : formatNumber(pick(summary, ['iteration_count'], 1)) + ' 轮') +
        ' · ' + escapeHTML(pick(summary, ['iteration_location'], 'query')) + ':' + escapeHTML(pick(summary, ['iteration_path'], '—')) +
        ' · 起始 ' + formatNumber(pick(summary, ['iteration_start'], 0)) + ' / 步长 ' + formatNumber(pick(summary, ['iteration_step'], 0));
    }
    return '<div class="sync-request-card">' +
      '<div class="api-request-summary"><span class="http-status status-ok">' + escapeHTML(method) + '</span><span class="status-badge status-' + escapeHTML(executor === 'browser' ? 'running' : 'success') + '">' + escapeHTML(executorLabel) + '</span><code title="' + escapeHTML(url) + '">' + escapeHTML(url) + '</code></div>' +
      '<dl class="detail-grid">' +
        '<div class="detail-pair"><dt>Header 名称</dt><dd>' + escapeHTML(headers.length ? headers.join(', ') : '无') + '</dd></div>' +
        '<div class="detail-pair"><dt>Query 名称</dt><dd>' + escapeHTML(query.length ? query.join(', ') : '无') + '</dd></div>' +
        '<div class="detail-pair"><dt>Body</dt><dd>' + escapeHTML(bodyType === 'none' ? '无' : bodyType + (boolValue(pick(summary, ['has_request_body'], false)) ? '（已配置，内容不保存）' : '')) + '</dd></div>' +
        '<div class="detail-pair"><dt>代理 / 超时</dt><dd>' + escapeHTML(pick(summary, ['proxy_scheme'], '无代理') || '无代理') + ' · ' + formatNumber(pick(summary, ['timeout_seconds'], 0)) + ' 秒</dd></div>' +
        '<div class="detail-pair full"><dt>提取路径</dt><dd class="mono">' + escapeHTML(pick(summary, ['response_path'], '—')) + '</dd></div>' +
        '<div class="detail-pair full"><dt>迭代配置</dt><dd>' + iterationText + '</dd></div>' +
      '</dl>' +
    '</div>';
  }

  function renderKeywordSyncIteration(iteration, summary) {
    var sequence = Math.max(1, numberValue(pick(iteration, ['index', 'sequence'], 1)));
    var status = keywordSyncRunStatus(iteration);
    var httpStatus = numberValue(pick(iteration, ['http_status', 'status_code'], 0));
    var raw = numberValue(pick(iteration, ['raw_extracted_count'], 0));
    var unique = numberValue(pick(iteration, ['unique_count'], 0));
    var crossNew = numberValue(pick(iteration, ['cross_iteration_new'], 0));
    var added = numberValue(pick(iteration, ['new_count'], 0));
    var existing = numberValue(pick(iteration, ['existing_keyword_count', 'existing_count'], 0));
    var samples = arrayFrom(pick(iteration, ['samples'], []), ['items']).slice(0, 5);
    var error = pick(iteration, ['error'], '');
    var iterationEnabled = boolValue(pick(summary, ['iteration_enabled'], false), false);
    return '<tr>' +
      '<td><strong>第 ' + sequence + ' 轮</strong><small class="table-subline mono">参数 ' + (iterationEnabled ? escapeHTML(pick(iteration, ['iteration_value'], '—')) : '—') + '</small></td>' +
      '<td>' + statusBadge(status) + '</td>' +
      '<td>' + (httpStatus ? '<span class="http-status ' + (httpStatus >= 200 && httpStatus < 300 ? 'status-ok' : 'status-error') + '">' + httpStatus + '</span>' : '<span class="muted">—</span>') + '</td>' +
      '<td>' + formatNumber(pick(iteration, ['duration_ms'], 0)) + ' ms<small class="table-subline">' + formatBytes(pick(iteration, ['response_size'], 0)) + '</small></td>' +
      '<td>原始 ' + formatNumber(raw) + '<small class="table-subline">轮内去重 ' + formatNumber(unique) + ' · 跨轮新增 ' + formatNumber(crossNew) + '</small></td>' +
      '<td>+' + formatNumber(added) + ' 新增<small class="table-subline">' + formatNumber(existing) + ' 已存在</small></td>' +
      '<td>' + (error ? '<span class="danger-text iteration-error" title="' + escapeHTML(error) + '">' + escapeHTML(error) + '</span>' : (samples.length ? '<div class="iteration-samples">' + samples.map(function (sample) { return '<span title="' + escapeHTML(sample) + '">' + escapeHTML(sample) + '</span>'; }).join('') + '</div>' : '<span class="muted">—</span>')) + '</td>' +
    '</tr>';
  }

  function renderKeywordSyncRunDetail(run) {
    var id = keywordSyncRunID(run);
    var sourceName = pick(run, ['source_name'], '已删除来源');
    var status = keywordSyncRunStatus(run);
    var trigger = String(pick(run, ['trigger'], 'manual')).toLowerCase();
    var progress = keywordSyncRunProgress(run);
    var result = keywordSyncRunResult(run);
    var summary = pick(run, ['request_summary'], {});
    var progressLabel = progress.legacy ? '历史汇总' : (progress.unlimited ? '第 ' + progress.completed + ' 轮 / 无上限' : progress.completed + ' / ' + progress.total + ' 轮');
    var recordsTotal = numberValue(pick(run, ['iteration_records_total'], 0), 0);
    byId('keyword-sync-detail-title').textContent = (sourceName || '已删除来源') + ' · RUN #' + id;
    byId('keyword-sync-detail').innerHTML =
      '<section class="detail-section keyword-sync-run-hero">' +
        '<div class="panel-heading"><div class="source-stack">' + statusBadge(status) + '<span class="type-badge">' + escapeHTML(triggerLabels[trigger] || trigger) + '</span><span class="type-badge">配置 v' + formatNumber(pick(run, ['config_revision'], 0)) + '</span></div><span class="batch-id">SYNC / ' + escapeHTML(id) + '</span></div>' +
        '<div class="batch-progress-label"><span>' + escapeHTML(progressLabel) + '</span><span>' + (progress.unlimited ? (ACTIVE_KEYWORD_SYNC_STATUSES.indexOf(status) >= 0 ? '进行中' : '已结束') : progress.percent + '%') + '</span></div>' +
        '<div class="progress-track' + (progress.unlimited && ACTIVE_KEYWORD_SYNC_STATUSES.indexOf(status) >= 0 ? ' is-unlimited' : '') + '"><div class="progress-bar" style="width:' + progress.percent + '%"></div></div>' +
        '<small class="sync-progress-caption">成功 ' + formatNumber(pick(run, ['success_iterations'], 0)) + ' 轮 · 失败 ' + formatNumber(pick(run, ['failed_iterations'], 0)) + ' 轮' + (progress.unlimited ? ' · 无限迭代按停止条件结束' : '') + '</small>' +
        '<div class="batch-metrics keyword-sync-metrics"><div><strong>' + formatNumber(result.raw) + '</strong><span>原始提取</span></div><div><strong>' + formatNumber(result.unique) + '</strong><span>跨轮去重</span></div><div><strong>+' + formatNumber(result.added) + '</strong><span>新增关键词</span></div><div><strong>' + formatNumber(result.existing) + '</strong><span>已有关键词</span></div></div>' +
      '</section>' +
      '<section class="detail-section"><h3>执行信息</h3><dl class="detail-grid">' +
        '<div class="detail-pair"><dt>进入队列</dt><dd>' + escapeHTML(formatDate(pick(run, ['queued_at', 'created_at'], null), true)) + '</dd></div>' +
        '<div class="detail-pair"><dt>开始时间</dt><dd>' + escapeHTML(formatDate(pick(run, ['started_at'], null), true)) + '</dd></div>' +
        '<div class="detail-pair"><dt>完成时间</dt><dd>' + escapeHTML(formatDate(pick(run, ['finished_at'], null), true)) + '</dd></div>' +
        '<div class="detail-pair"><dt>耗时</dt><dd>' + escapeHTML(durationFrom(run)) + '</dd></div>' +
        '<div class="detail-pair full"><dt>错误信息</dt><dd class="' + (pick(run, ['error'], '') ? 'danger-text' : '') + '">' + escapeHTML(pick(run, ['error'], '无')) + '</dd></div>' +
      '</dl></section>' +
      '<section class="detail-section"><h3>脱敏请求摘要</h3>' + keywordSyncRequestSummaryMarkup(summary) + '</section>' +
      '<section class="detail-section"><div class="sync-iterations-heading"><h3>迭代执行记录</h3><span>共 ' + formatNumber(recordsTotal) + ' 轮记录</span></div>' +
        '<div class="table-wrap keyword-sync-iterations-wrap"><table class="keyword-sync-iterations-table"><thead><tr><th>轮次 / 参数</th><th>状态</th><th>HTTP</th><th>耗时 / 响应</th><th>提取</th><th>关键词</th><th>样例 / 错误</th></tr></thead><tbody id="keyword-sync-iterations-body"><tr class="table-empty"><td colspan="7">正在加载…</td></tr></tbody></table></div><button id="keyword-sync-iterations-more" class="button secondary" type="button" data-action="load-more-sync-iterations" hidden>加载更多</button>' +
      '</section>';
    refreshIcons(byId('keyword-sync-detail'));
  }

  function startKeywordSyncDetailPolling() {
    if (document.visibilityState !== 'visible' || !byId('keyword-sync-detail-dialog').open) return;
    if (state.keywordSyncRuns.detailPollTimer) return;
    state.keywordSyncRuns.detailPollTimer = window.setInterval(function () {
      var id = state.keywordSyncRuns.detailID;
      if (id && document.visibilityState === 'visible' && byId('keyword-sync-detail-dialog').open) openKeywordSyncRunDetail(id, true);
    }, 4000);
  }

  function stopKeywordSyncDetailPolling() {
    if (!state.keywordSyncRuns.detailPollTimer) return;
    clearInterval(state.keywordSyncRuns.detailPollTimer);
    state.keywordSyncRuns.detailPollTimer = null;
  }

  async function copyKeywordAPISource(id, button) {
    if (button) button.disabled = true;
    try {
      await apiRequest(API.keywordAPISources + '/' + encodeURIComponent(id) + '/copy', { method: 'POST' });
      toast('来源已复制，副本默认停用', 'success');
      state.keywordSources.loaded = false;
      await loadKeywordSources(true);
    } catch (error) {
      toast(error.message || '复制 API 来源失败', 'error');
      if (button) button.disabled = false;
    }
  }

  async function deleteKeywordAPISource(id) {
    var source = state.keywordSources.items.find(function (item) { return keywordSourceID(item) === String(id); });
    var confirmed = await confirmAction('删除 API 来源', '删除“' + pick(source, ['name'], '该来源') + '”及来源关系，已生成的关键词不会删除。', '删除来源');
    if (!confirmed) return;
    try {
      await apiRequest(API.keywordAPISources + '/' + encodeURIComponent(id), { method: 'DELETE' });
      toast('API 来源已删除', 'success');
      state.keywordSources.loaded = false;
      await loadKeywordSources(true);
    } catch (error) { toast(error.message || 'API 来源删除失败', 'error'); }
  }

  async function loadRuns(options) {
    options = options || {};
    if (state.loaded.runs && !options.force && !options.silent) return;
    if (!options.silent) {
      state.loaded.runs = true;
      showAlert('runs-alert', '');
      byId('runs-empty').hidden = true;
      byId('runs-pagination').innerHTML = '';
      tableLoading('runs-body', 9);
    }
    var serial = ++state.requestSerial.runs;
    var query = Object.assign({}, state.runs.query, { page: state.runs.page, page_size: state.runs.pageSize });
    var controller = replaceRequestController('runs');
    try {
      var data = await apiRequest(API.runs, { query: query, signal: controller.signal });
      if (state.requestControllers.runs !== controller || serial !== state.requestSerial.runs) return;
      var items = arrayFrom(data, ['runs', 'collection_runs', 'items', 'results']);
      var meta = paginationMeta(data, items, state.runs.page, state.runs.pageSize);
      state.runs.items = items;
      Object.assign(state.runs, meta);
      renderRuns();
      updateTimestamp();
      configureRunPolling();
    } catch (error) {
      if (error.name === 'AbortError') return;
      if (serial !== state.requestSerial.runs) return;
      if (!options.silent) {
        state.loaded.runs = false;
        byId('runs-body').innerHTML = '';
        showAlert('runs-alert', error.message || '任务加载失败');
      }
      stopRunPolling();
    } finally { finishRequestController('runs', controller); }
  }

  function renderRuns() {
    var body = byId('runs-body');
    var empty = byId('runs-empty');
    if (!state.runs.items.length) {
      body.innerHTML = '';
      empty.hidden = false;
      byId('runs-pagination').innerHTML = '';
      byId('runs-live-region').innerHTML = '';
      refreshIcons(empty);
      return;
    }
    empty.hidden = true;
    body.innerHTML = state.runs.items.map(function (run) {
      var id = pick(run, ['id', 'run_id', 'batch_id'], '—');
      var status = normalizedStatus(run);
      var trigger = pick(run, ['trigger', 'trigger_type'], 'scheduled');
      var progress = runProgress(run);
      var added = numberValue(pick(run, ['new_count', 'created_count', 'added_count'], 0));
      var duplicate = numberValue(pick(run, ['duplicate_count', 'duplicates'], 0));
      return '<tr>' +
        '<td class="mono">#' + escapeHTML(id) + '</td>' +
        '<td>' + escapeHTML(triggerLabels[trigger] || trigger) + (boolValue(pick(run, ['force', 'forced'], false)) ? ' <span class="type-badge">强制</span>' : '') + '</td>' +
        '<td>' + statusBadge(status) + '</td>' +
        '<td><div class="mini-progress"><div class="mini-progress-label"><span>' + progress.completed + '/' + progress.total + '</span><span>' + progress.percent + '%</span></div><div class="progress-track"><div class="progress-bar" style="width:' + progress.percent + '%"></div></div></div></td>' +
        '<td class="success-text">+' + formatNumber(added) + '</td>' +
        '<td>' + formatNumber(duplicate) + '</td>' +
        '<td>' + escapeHTML(formatDate(pick(run, ['started_at', 'created_at', 'start_time'], null), true)) + '</td>' +
        '<td>' + escapeHTML(durationFrom(run)) + '</td>' +
        '<td><div class="row-actions"><button class="row-action" type="button" data-action="view-run" data-id="' + escapeHTML(id) + '" aria-label="查看批次 ' + escapeHTML(id) + '" title="查看详情">' + icon('panel-right-open') + '</button></div></td>' +
      '</tr>';
    }).join('');
    renderPagination('runs-pagination', state.runs.page, state.runs.pages, 'runs');
    renderRunLiveBanner();
    refreshIcons(body);
  }

  function renderRunLiveBanner() {
    var active = state.runs.items.find(function (run) { return ACTIVE_STATUSES.includes(normalizedStatus(run)); });
    var container = byId('runs-live-region');
    if (!active) {
      container.innerHTML = '';
      return;
    }
    var progress = runProgress(active);
    var id = pick(active, ['id', 'run_id', 'batch_id'], '—');
    container.innerHTML = '<div class="inline-alert info"><strong>批次 #' + escapeHTML(id) + ' ' + (normalizedStatus(active) === 'pending' ? '等待执行' : '正在采集') + '</strong> · ' + progress.completed + '/' + progress.total + ' 关键词，完成 ' + progress.percent + '%</div>';
  }

  function configureRunPolling() {
    var hasActive = state.runs.items.some(function (run) { return ACTIVE_STATUSES.includes(normalizedStatus(run)); });
    if (state.view === 'runs' && hasActive) {
      if (!state.pollTimer) {
        state.pollTimer = window.setInterval(function () {
          if (document.visibilityState === 'visible' && state.view === 'runs') loadRuns({ silent: true, force: true });
        }, 5000);
      }
    } else {
      stopRunPolling();
    }
  }

  function stopRunPolling() {
    if (state.pollTimer) {
      clearInterval(state.pollTimer);
      state.pollTimer = null;
    }
  }

  function updateRunFilters() {
    state.runs.query = {
      status: byId('run-status-filter').value,
      trigger: byId('run-trigger-filter').value
    };
    state.runs.page = 1;
    state.loaded.runs = false;
    loadRuns({ force: true });
  }

  async function openRunDialog(preselected, forced) {
    var dialog = byId('run-dialog');
    var list = byId('run-keyword-list');
    byId('run-form-error').hidden = true;
    byId('run-keyword-search').value = '';
    byId('run-form').elements.force.checked = Boolean(forced);
    state.runPicker.search = ''; state.runPicker.page = 1; state.runPicker.pages = 1; state.runPicker.items = [];
    state.runPicker.selected = new Set((preselected || []).map(String));
    list.innerHTML = '<div class="table-loading"><span>' + icon('loader-circle', 'spin') + '正在加载</span></div>';
    dialog.showModal();
    refreshIcons(list);
    try {
      await loadRunPickerKeywords(true);
      renderRunKeywordPicker();
    } catch (error) {
      list.innerHTML = '<div class="inline-alert error" style="margin:12px">' + escapeHTML(error.message || '关键词加载失败') + '</div>';
    }
  }

  async function loadRunPickerKeywords(reset) {
    if (reset) {
      if (state.runPicker.controller) state.runPicker.controller.abort();
      state.runPicker.page = 1; state.runPicker.pages = 1; state.runPicker.items = []; state.runPicker.loading = false;
    } else if (state.runPicker.loading) return;
    if (!reset && state.runPicker.page > state.runPicker.pages) return;
    var search = state.runPicker.search, page = state.runPicker.page;
    var controller = new AbortController(); state.runPicker.controller = controller; state.runPicker.loading = true;
    try { var data = await apiRequest(API.keywords, { query: { q: search, enabled: true, page: page, page_size: 50 }, signal: controller.signal }); if (state.runPicker.controller !== controller || state.runPicker.search !== search) return; var items = arrayFrom(data,['keywords','items','results']), meta = paginationMeta(data,items,page,50); state.runPicker.items = reset ? items : state.runPicker.items.concat(items); state.runPicker.pages = meta.pages; state.runPicker.total = meta.total; state.runPicker.page = page + 1; }
    catch (error) { if (error.name !== 'AbortError') throw error; }
    finally { if (state.runPicker.controller === controller) { state.runPicker.controller = null; state.runPicker.loading = false; } }
  }

  function renderRunKeywordPicker() {
    var list = byId('run-keyword-list');
    var items = state.runPicker.items;
    if (!items.length) {
      list.innerHTML = '<div class="table-empty" style="padding:50px 12px;text-align:center">没有匹配的关键词</div>';
    } else {
      list.innerHTML = items.map(function (keyword) {
        var id = String(pick(keyword, ['id', 'keyword_id'], ''));
        var name = pick(keyword, ['keyword', 'name'], '');
        var enabled = boolValue(pick(keyword, ['enabled', 'is_enabled'], true), true);
        var selected = state.runPicker.selected.has(id);
        return '<label class="picker-item">' +
          '<input type="checkbox" data-action="select-run-keyword" data-id="' + escapeHTML(id) + '"' + (selected ? ' checked' : '') + (enabled ? '' : ' disabled') + '>' +
          '<span class="picker-copy"><strong>' + escapeHTML(name) + '</strong><small>' + escapeHTML(keywordTypeLabels[pick(keyword, ['keyword_type', 'type'], 'general')] || pick(keyword, ['keyword_type', 'type'], 'general')) + ' · ' + (enabled ? '已启用' : '已停用') + '</small></span>' +
          (enabled ? '' : '<span class="type-badge">不可用</span>') +
        '</label>';
      }).join('') + (state.runPicker.page <= state.runPicker.pages ? '<button class="button secondary wide" type="button" data-action="load-more-run-keywords">加载更多（' + items.length + ' / ' + state.runPicker.total + '）</button>' : '');
    }
    byId('run-selected-label').textContent = '已选 ' + state.runPicker.selected.size + ' 项';
  }

  async function createRun(keywordIds, force, button) {
    if (!keywordIds.length) {
      toast('请至少选择一个关键词', 'error');
      return null;
    }
    if (button) setButtonLoading(button, true, '启动中');
    try {
      var result = await apiRequest(API.runs, {
        method: 'POST',
        body: { keyword_ids: keywordIds.map(function (id) { return /^\d+$/.test(String(id)) ? Number(id) : id; }), force: Boolean(force) }
      });
      toast(force ? '强制采集任务已创建' : '采集任务已创建', 'success');
      byId('run-dialog').close();
      state.keywords.selected.clear();
      updateKeywordBulkBar();
      state.loaded.runs = false;
      if (state.view !== 'runs') navigate('runs', true);
      else loadRuns({ force: true });
      return result;
    } catch (error) {
      toast(error.message || '任务创建失败', 'error');
      return null;
    } finally {
      if (button) setButtonLoading(button, false);
    }
  }

  async function submitRun(event) {
    event.preventDefault();
    var error = byId('run-form-error');
    error.hidden = true;
    var ids = Array.from(state.runPicker.selected);
    if (!ids.length) {
      error.textContent = '请至少选择一个关键词';
      error.hidden = false;
      return;
    }
    await createRun(ids, event.currentTarget.elements.force.checked, byId('run-submit'));
  }

  async function launchSelectedRun(force) {
    var ids = Array.from(state.keywords.selected);
    if (!ids.length) return;
    if (force) {
      var confirmed = await confirmAction('强制采集', '所选关键词将绕过冷却期立即执行。', '强制执行');
      if (!confirmed) return;
    }
    createRun(ids, force);
  }

  function openRunDetail(id) {
    navigate('runs/' + id, true);
  }

  function cancelRunSourceRequests() {
    Object.keys(state.runDetail.sourceControllers).forEach(function (key) { state.runDetail.sourceControllers[key].abort(); });
    state.runDetail.sourceControllers = {};
  }

  function cancelRunDetailRequests() {
    stopDetailPolling();
    if (state.runDetail.controller) state.runDetail.controller.abort();
    if (state.runDetail.itemsController) state.runDetail.itemsController.abort();
    cancelRunSourceRequests();
    if (state.runDetail.observer) state.runDetail.observer.disconnect();
    state.runDetail.controller = null;
    state.runDetail.itemsController = null;
    state.runDetail.observer = null;
    state.runDetail.loading = false;
  }

  async function loadRunPage(id) {
    if (!id) return;
    cancelRunDetailRequests();
    state.runDetail.id = String(id);
    var controller = new AbortController(); state.runDetail.controller = controller;
    showAlert('run-page-alert', ''); byId('run-page-summary').innerHTML = '<div class="table-loading"><span>' + icon('loader-circle', 'spin') + '正在加载</span></div>'; byId('run-page-items').innerHTML = '';
    try {
      var data = await apiRequest(API.runs + '/' + encodeURIComponent(id), { signal: controller.signal });
      if (state.runDetail.controller !== controller || state.view !== 'run-detail' || state.runDetail.id !== String(id)) return;
      state.runDetail.summary = pick(data, ['run', 'collection_run'], data); renderRunPageSummary(state.runDetail.summary); await loadRunItems(true); setupRunItemObserver();
      if (ACTIVE_STATUSES.includes(normalizedStatus(state.runDetail.summary))) startDetailPolling(id);
    } catch (error) { if (error.name !== 'AbortError') showAlert('run-page-alert', error.message || '任务详情加载失败'); }
  }

  function renderRunPageSummary(run) {
    var id = pick(run, ['id', 'run_id', 'batch_id'], state.runDetail.id), progress = runProgress(run);
    byId('run-page-title').textContent = '批次 #' + id;
    byId('run-page-summary').innerHTML = '<section class="panel run-summary-card"><div class="panel-heading"><div class="source-stack">' + statusBadge(normalizedStatus(run)) + '<span class="type-badge">' + escapeHTML(triggerLabels[pick(run, ['trigger', 'trigger_type'], 'scheduled')] || '任务') + '</span></div><span class="batch-id">BATCH / ' + escapeHTML(id) + '</span></div><div class="batch-progress-label"><span>' + progress.completed + ' / ' + progress.total + ' 关键词</span><span>' + progress.percent + '%</span></div><div class="progress-track"><div class="progress-bar" style="width:' + progress.percent + '%"></div></div><div class="batch-metrics"><div><strong>' + formatNumber(pick(run, ['new_count', 'created_count'], 0)) + '</strong><span>新增资源</span></div><div><strong>' + formatNumber(pick(run, ['duplicate_count'], 0)) + '</strong><span>重复资源</span></div><div><strong>' + escapeHTML(durationFrom(run)) + '</strong><span>耗时</span></div></div></section>';
  }

  async function loadRunItems(reset) {
    if (reset) {
      if (state.runDetail.itemsController) state.runDetail.itemsController.abort();
      cancelRunSourceRequests();
      state.runDetail.page = 1; state.runDetail.pages = 1; state.runDetail.loadedPages = 0; state.runDetail.items = []; state.runDetail.loading = false;
    } else if (state.runDetail.loading) return;
    if (!reset && state.runDetail.page > state.runDetail.pages) return;
    var runID = state.runDetail.id;
    var page = state.runDetail.page;
    var controller = new AbortController();
    state.runDetail.itemsController = controller;
    state.runDetail.loading = true; byId('run-items-sentinel').innerHTML = icon('loader-circle', 'spin') + ' 加载中';
    try {
      var data = await apiRequest(API.runs + '/' + encodeURIComponent(runID) + '/items', { query: Object.assign({ page: page, page_size: 30 }, state.runDetail.query), signal: controller.signal });
      if (state.runDetail.itemsController !== controller || state.view !== 'run-detail' || state.runDetail.id !== runID) return;
      var items = arrayFrom(data, ['items', 'run_items', 'keywords']), meta = paginationMeta(data, items, page, 30);
      state.runDetail.items = reset ? items : state.runDetail.items.concat(items); state.runDetail.pages = meta.pages; state.runDetail.loadedPages = Math.max(state.runDetail.loadedPages, page); state.runDetail.total = meta.total; state.runDetail.page = page + 1; renderRunPageItems();
    } catch (error) { if (error.name !== 'AbortError') showAlert('run-page-alert', error.message || '执行项加载失败'); }
    finally { if (state.runDetail.itemsController === controller) { state.runDetail.itemsController = null; state.runDetail.loading = false; byId('run-items-sentinel').textContent = state.runDetail.page <= state.runDetail.pages ? '继续向下滚动加载' : '已加载全部 ' + state.runDetail.total + ' 项'; } }
  }

  async function refreshLoadedRunItems() {
    if (state.view !== 'run-detail' || !state.runDetail.id) return;
    if (state.runDetail.itemsController) state.runDetail.itemsController.abort();
    cancelRunSourceRequests();
    var runID = state.runDetail.id;
    var requestedPages = Math.max(1, state.runDetail.loadedPages);
    var controller = new AbortController();
    state.runDetail.itemsController = controller;
    state.runDetail.loading = true;
    try {
      var first = await apiRequest(API.runs + '/' + encodeURIComponent(runID) + '/items', { query: Object.assign({ page: 1, page_size: 30 }, state.runDetail.query), signal: controller.signal });
      var firstItems = arrayFrom(first, ['items', 'run_items', 'keywords']);
      var firstMeta = paginationMeta(first, firstItems, 1, 30);
      var pageCount = Math.min(requestedPages, firstMeta.pages);
      var responses = [first];
      if (pageCount > 1) responses = responses.concat(await Promise.all(Array.from({ length: pageCount - 1 }, function (_, index) {
        return apiRequest(API.runs + '/' + encodeURIComponent(runID) + '/items', { query: Object.assign({ page: index + 2, page_size: 30 }, state.runDetail.query), signal: controller.signal });
      })));
      if (state.runDetail.itemsController !== controller || state.view !== 'run-detail' || state.runDetail.id !== runID) return;
      var refreshed = [];
      responses.forEach(function (data) { refreshed = refreshed.concat(arrayFrom(data, ['items', 'run_items', 'keywords'])); });
      var meta = paginationMeta(responses[0] || {}, refreshed, 1, 30);
      state.runDetail.items = refreshed; state.runDetail.total = meta.total; state.runDetail.pages = meta.pages; state.runDetail.loadedPages = pageCount; state.runDetail.page = pageCount + 1;
      renderRunPageItems();
    } catch (error) { if (error.name !== 'AbortError') showAlert('run-page-alert', error.message || '执行项刷新失败'); }
    finally { if (state.runDetail.itemsController === controller) { state.runDetail.itemsController = null; state.runDetail.loading = false; byId('run-items-sentinel').textContent = state.runDetail.page <= state.runDetail.pages ? '继续向下滚动加载' : '已加载全部 ' + state.runDetail.total + ' 项'; } }
  }

  function sourceCount(item, key) {
    var counts = pick(item, ['source_counts'], {});
    var fields = key === 'success' ? ['source_success', 'success_count', 'source_success_count'] : (key === 'empty' ? ['source_empty', 'empty_count', 'source_empty_count', 'success_empty_count'] : ['source_failed', 'failed_count', 'source_failed_count']);
    return numberValue(pick(item, fields, pick(counts, key === 'empty' ? ['empty', 'success_empty'] : [key], 0)));
  }

  function runKeywordLabel(item) {
    var keyword = pick(item, ['keyword', 'keyword_name', 'name'], '');
    if (keyword && typeof keyword === 'object') keyword = pick(keyword, ['keyword', 'name'], '');
    return keyword || '未命名关键词';
  }

  function renderRunPageItems() {
    byId('run-page-items').innerHTML = state.runDetail.items.map(function (item) {
      var id = pick(item, ['id', 'item_id'], ''), kw = runKeywordLabel(item);
      return '<details class="run-keyword-card" data-item-id="' + escapeHTML(id) + '"><summary><span class="run-keyword-main"><strong>' + escapeHTML(kw) + '</strong><span class="metric-tags"><span class="metric-tag success">' + icon('circle-check') + sourceCount(item,'success') + ' 成功</span><span class="metric-tag empty">' + icon('circle-minus') + sourceCount(item,'empty') + ' 无结果</span><span class="metric-tag failed">' + icon('circle-x') + sourceCount(item,'failed') + ' 失败</span><span class="metric-tag">' + icon('clock-3') + escapeHTML(durationFrom(item)) + '</span></span></span>' + statusBadge(normalizedStatus(item)) + icon('chevron-down','detail-chevron') + '</summary><div class="run-source-content" data-source-content="' + escapeHTML(id) + '"><span class="muted">展开后加载来源明细</span></div></details>';
    }).join('') || '<div class="table-empty">暂无执行项</div>'; refreshIcons(byId('run-page-items'));
  }

  async function loadRunItemSources(itemID) {
    var target = document.querySelector('[data-source-content="' + CSS.escape(String(itemID)) + '"]'); if (!target || target.dataset.loaded === 'true' || target.dataset.loading === 'true') return;
    var runID = state.runDetail.id;
    var controller = new AbortController(); state.runDetail.sourceControllers[itemID] = controller; target.dataset.loading = 'true'; if (!target._items || !target._items.length) target.innerHTML = '<span class="muted">加载来源中…</span>';
    try {
      var page = numberValue(target.dataset.nextPage, 1), data = await apiRequest(API.runs + '/' + encodeURIComponent(runID) + '/items/' + encodeURIComponent(itemID) + '/sources', { query: { page: page, page_size: 50 }, signal: controller.signal });
      if (state.runDetail.sourceControllers[itemID] !== controller || state.view !== 'run-detail' || state.runDetail.id !== runID || !target.isConnected) return;
      var sources = arrayFrom(data, ['sources','items']), meta = paginationMeta(data, sources, page, 50); target._items = (target._items || []).concat(sources); target.dataset.nextPage = String(page + 1);
      var groups = {plugin:[],tg:[],external:[]}; target._items.forEach(function(s){var t=String(pick(s,['source_type','type'],'external')).toLowerCase();groups[t==='plugin'?'plugin':(t==='tg'||t==='telegram'?'tg':'external')].push(s);});
      target.innerHTML = (Object.keys(groups).filter(function(k){return groups[k].length;}).map(function(k){return '<section class="source-group"><h4>'+({plugin:'插件',tg:'Telegram',external:'外部来源'}[k])+' <span>'+groups[k].length+'</span></h4>'+groups[k].map(renderRunSource).join('')+'</section>';}).join('') || '<span class="muted">暂无来源明细</span>') + (page < meta.pages ? '<button class="button secondary source-load-more" type="button" data-action="load-more-run-sources" data-item-id="'+escapeHTML(itemID)+'">加载更多来源</button>' : ''); target.dataset.loaded = page >= meta.pages ? 'true' : 'false'; refreshIcons(target);
    } catch(error){if(error.name!=='AbortError')target.innerHTML='<span class="danger-text">'+escapeHTML(error.message||'来源加载失败')+'</span>';}
    finally { if (state.runDetail.sourceControllers[itemID] === controller) { delete state.runDetail.sourceControllers[itemID]; target.dataset.loading = 'false'; } }
  }
  function renderRunSource(s){var st=normalizedStatus(s);if(st==='empty')st='success_empty';var key=pick(s,['name','source_name','channel','plugin','key','source_key'],'未知来源'),name=String(key).replace(/^(plugin|tg|telegram|external):/i,''),err=pick(s,['error','error_message'],''),statusIcon=st==='failed'?'circle-x':(st==='success_empty'?'circle-minus':(st==='success'?'circle-check':'circle-help'));return '<details class="run-source-row"><summary><span class="source-name-tag">'+escapeHTML(name)+'</span><span class="metric-tag '+escapeHTML(st)+'">'+icon(statusIcon)+escapeHTML(statusLabels[st]||st)+'</span><span class="metric-tag">'+icon('clock-3')+escapeHTML(durationFrom(s))+'</span><span class="metric-tag">'+formatNumber(pick(s,['result_count'],0))+' 结果</span><span class="metric-tag">+'+formatNumber(pick(s,['new_count'],0))+' 新增</span>'+icon('chevron-down','detail-chevron')+'</summary><p class="'+(err?'danger-text':'muted')+'">'+escapeHTML(err||('尝试 '+formatNumber(pick(s,['attempts','attempt_count'],1))+' 次 · 重复 '+formatNumber(pick(s,['duplicate_count'],0))))+'</p></details>';}
  function setupRunItemObserver(){if(state.runDetail.observer)state.runDetail.observer.disconnect();state.runDetail.observer=new IntersectionObserver(function(es){if(es.some(function(e){return e.isIntersecting;}))loadRunItems(false);},{rootMargin:'300px'});state.runDetail.observer.observe(byId('run-items-sentinel'));}

  function showPermissionState(scope, forbidden) {
    var permission = byId(scope + '-forbidden');
    if (permission) permission.hidden = !forbidden;
    if (scope === 'users') {
      byId('users-table-wrap').hidden = forbidden;
      byId('users-pagination').hidden = forbidden;
      byId('users-empty').hidden = true;
    } else if (scope === 'usage') {
      byId('usage-content').hidden = forbidden;
    }
    if (forbidden) refreshIcons(permission);
  }

  async function loadUsers(force) {
    if (state.loaded.users && !force) return;
    state.loaded.users = true;
    var serial = ++state.requestSerial.users;
    showPermissionState('users', false);
    showAlert('users-alert', '');
    byId('users-empty').hidden = true;
    byId('users-table-wrap').hidden = false;
    byId('users-pagination').hidden = false;
    tableLoading('users-body', 8);
    var query = Object.assign({}, state.users.query, {
      page: state.users.page,
      page_size: state.users.pageSize
    });
    var controller = replaceRequestController('users');
    try {
      var data = await apiRequest(API.users, { query: query, signal: controller.signal });
      if (state.requestControllers.users !== controller || serial !== state.requestSerial.users) return;
      var items = arrayFrom(data, ['users', 'items', 'results']);
      var meta = paginationMeta(data, items, state.users.page, state.users.pageSize);
      state.users.items = items;
      Object.assign(state.users, meta);
      renderUsers();
      updateUsageUserFilter(items);
      updateTimestamp();
    } catch (error) {
      if (error.name === 'AbortError') return;
      if (serial !== state.requestSerial.users) return;
      state.loaded.users = false;
      byId('users-body').innerHTML = '';
      if (error instanceof APIError && error.status === 403) {
        showPermissionState('users', true);
      } else {
        showAlert('users-alert', error.message || '用户加载失败');
      }
    } finally { finishRequestController('users', controller); }
  }

  function userStatus(user) {
    var enabled = boolValue(pick(user, ['enabled', 'is_enabled'], true), true);
    var expiresAt = toDate(pick(user, ['expires_at', 'expiry'], null));
    if (!enabled) return { key: 'disabled', label: '已停用' };
    if (expiresAt && expiresAt.getTime() <= Date.now()) return { key: 'expired', label: '已到期' };
    if (boolValue(pick(user, ['must_change_password'], false))) return { key: 'password', label: '待改密' };
    return { key: 'enabled', label: '已启用' };
  }

  function userStatusBadge(status) {
    var classes = {
      enabled: 'status-success',
      disabled: 'status-pending',
      expired: 'status-failed',
      password: 'status-unknown'
    };
    return '<span class="status-badge ' + (classes[status.key] || 'status-pending') + '">' + escapeHTML(status.label) + '</span>';
  }

  function userAPIKey(user) {
    var apiKey = pick(user, ['api_key', 'key'], {});
    if (!apiKey || typeof apiKey !== 'object') apiKey = {};
    var prefix = pick(apiKey, ['key_prefix', 'prefix'], pick(user, ['api_key_prefix', 'key_prefix'], ''));
    var revokedAt = pick(apiKey, ['revoked_at'], pick(user, ['api_key_revoked_at'], null));
    var hasKey = boolValue(pick(user, ['has_api_key'], Boolean(prefix)), Boolean(prefix));
    return {
      prefix: prefix,
      active: hasKey && !revokedAt,
      lastUsedAt: pick(apiKey, ['last_used_at'], pick(user, ['api_key_last_used_at'], null))
    };
  }

  function renderUsers() {
    var body = byId('users-body');
    var empty = byId('users-empty');
    if (!state.users.items.length) {
      body.innerHTML = '';
      empty.hidden = false;
      byId('users-pagination').innerHTML = '';
      refreshIcons(empty);
      return;
    }
    empty.hidden = true;
    body.innerHTML = state.users.items.map(function (user) {
      var id = String(pick(user, ['id', 'user_id'], ''));
      var username = pick(user, ['username', 'name'], '未命名用户');
      var role = String(pick(user, ['role'], 'user')).toLowerCase();
      var status = userStatus(user);
      var key = userAPIKey(user);
      var unlimited = boolValue(pick(user, ['rate_limit_disabled', 'unlimited'], false));
      var rps = numberValue(pick(user, ['rps_limit', 'rps'], 3));
      var rpm = numberValue(pick(user, ['rpm_limit', 'rpm'], 60));
      var expires = pick(user, ['expires_at', 'expiry'], null);
      var enabled = boolValue(pick(user, ['enabled', 'is_enabled'], true), true);
      return '<tr>' +
        '<td><div class="user-cell"><span class="avatar">' + escapeHTML(String(username).charAt(0).toUpperCase()) + '</span><span><strong>' + escapeHTML(username) + '</strong><small class="mono">ID ' + escapeHTML(id) + '</small></span></div></td>' +
        '<td><span class="type-badge role-' + escapeHTML(role) + '">' + (role === 'admin' ? '管理员' : '普通用户') + '</span></td>' +
        '<td>' + userStatusBadge(status) + '</td>' +
        '<td>' + (unlimited ? '<span class="type-badge">不限流</span>' : '<div class="limit-cell"><strong>' + rps + ' RPS</strong><small>' + rpm + ' RPM</small></div>') + '</td>' +
        '<td title="' + escapeHTML(formatDate(expires, true)) + '">' + (expires ? escapeHTML(formatDate(expires, false)) : '<span class="muted">永久</span>') + '</td>' +
        '<td><div class="key-state"><span class="mono">' + escapeHTML(key.prefix || '未创建') + '</span><small class="' + (key.active ? 'success-text' : 'muted') + '">' + (key.active ? '有效' : key.prefix ? '已吊销' : '无 Key') + '</small></div></td>' +
        '<td title="' + escapeHTML(formatDate(pick(user, ['last_login_at'], null), true)) + '">' + escapeHTML(relativeTime(pick(user, ['last_login_at'], null))) + '</td>' +
        '<td><div class="row-actions user-actions">' +
          '<button class="row-action" type="button" data-action="edit-user" data-id="' + escapeHTML(id) + '" aria-label="编辑 ' + escapeHTML(username) + '" title="编辑">' + icon('pencil') + '</button>' +
          '<button class="row-action" type="button" data-action="toggle-user" data-id="' + escapeHTML(id) + '" aria-label="' + (enabled ? '停用 ' : '启用 ') + escapeHTML(username) + '" title="' + (enabled ? '停用账号' : '启用账号') + '">' + icon(enabled ? 'user-round-x' : 'user-round-check') + '</button>' +
          '<button class="row-action" type="button" data-action="reset-user-password" data-id="' + escapeHTML(id) + '" aria-label="重置 ' + escapeHTML(username) + ' 的密码" title="重置密码">' + icon('lock-keyhole') + '</button>' +
          '<button class="row-action" type="button" data-action="reset-user-key" data-id="' + escapeHTML(id) + '" aria-label="重置 ' + escapeHTML(username) + ' 的 API Key" title="重置 API Key">' + icon('key-round') + '</button>' +
          (key.active ? '<button class="row-action danger" type="button" data-action="revoke-user-key" data-id="' + escapeHTML(id) + '" aria-label="吊销 ' + escapeHTML(username) + ' 的 API Key" title="吊销 API Key">' + icon('key-square') + '</button>' : '') +
          '<button class="row-action danger" type="button" data-action="delete-user" data-id="' + escapeHTML(id) + '" aria-label="删除 ' + escapeHTML(username) + '" title="删除用户">' + icon('trash-2') + '</button>' +
        '</div></td>' +
      '</tr>';
    }).join('');
    renderPagination('users-pagination', state.users.page, state.users.pages, 'users');
    refreshIcons(body);
  }

  function updateUserFilters() {
    var status = byId('user-status-filter').value;
    state.users.query = {
      q: byId('user-search').value.trim(),
      role: byId('user-role-filter').value,
      status: status,
      enabled: status === 'enabled' ? 'true' : status === 'disabled' ? 'false' : '',
      expired: status === 'expired' ? 'true' : ''
    };
    state.users.page = 1;
    state.loaded.users = false;
    loadUsers(true);
  }

  function toDateTimeLocal(value) {
    var date = toDate(value);
    if (!date) return '';
    var pad = function (part) { return String(part).padStart(2, '0'); };
    return date.getFullYear() + '-' + pad(date.getMonth() + 1) + '-' + pad(date.getDate()) + 'T' + pad(date.getHours()) + ':' + pad(date.getMinutes());
  }

  function openUserDialog(user) {
    var form = byId('user-form');
    form.reset();
    byId('user-form-error').hidden = true;
    byId('user-dialog-title').textContent = user ? '编辑用户' : '创建用户';
    form.elements.id.value = user ? pick(user, ['id', 'user_id'], '') : '';
    form.elements.username.value = user ? pick(user, ['username', 'name'], '') : '';
    form.elements.username.disabled = Boolean(user);
    form.elements.role.value = user ? pick(user, ['role'], 'user') : 'user';
    form.elements.expires_at.value = user ? toDateTimeLocal(pick(user, ['expires_at', 'expiry'], null)) : '';
    form.elements.rps_limit.value = user ? numberValue(pick(user, ['rps_limit', 'rps'], 3)) : 3;
    form.elements.rpm_limit.value = user ? numberValue(pick(user, ['rpm_limit', 'rpm'], 60)) : 60;
    form.elements.rate_limit_disabled.checked = user ? boolValue(pick(user, ['rate_limit_disabled', 'unlimited'], false)) : false;
    form.elements.enabled.checked = user ? boolValue(pick(user, ['enabled', 'is_enabled'], true), true) : true;
    syncUserLimitFields();
    byId('user-dialog').showModal();
    if (!user) window.setTimeout(function () { form.elements.username.focus(); }, 0);
  }

  function syncUserLimitFields() {
    var form = byId('user-form');
    var disabled = form.elements.rate_limit_disabled.checked;
    form.elements.rps_limit.disabled = disabled;
    form.elements.rpm_limit.disabled = disabled;
  }

  function userPayloadFromForm(form) {
    var expiresValue = form.elements.expires_at.value;
    var payload = {
      role: form.elements.role.value,
      enabled: form.elements.enabled.checked,
      expires_at: expiresValue ? new Date(expiresValue).toISOString() : null,
      rps_limit: numberValue(form.elements.rps_limit.value, 3),
      rpm_limit: numberValue(form.elements.rpm_limit.value, 60),
      rate_limit_disabled: form.elements.rate_limit_disabled.checked
    };
    if (!form.elements.id.value) payload.username = form.elements.username.value.trim();
    return payload;
  }

  async function saveUser(event) {
    event.preventDefault();
    var form = event.currentTarget;
    var id = form.elements.id.value;
    var payload = userPayloadFromForm(form);
    var error = byId('user-form-error');
    error.hidden = true;
    if (!id && !payload.username) {
      error.textContent = '用户名不能为空';
      error.hidden = false;
      return;
    }
    var button = byId('user-save');
    setButtonLoading(button, true, '保存中');
    try {
      var result = await apiRequest(id ? API.users + '/' + encodeURIComponent(id) : API.users, {
        method: id ? 'PUT' : 'POST',
        body: payload
      });
      byId('user-dialog').close();
      toast(id ? '用户已更新' : '用户已创建', 'success');
      if (!id) showCredentialDialog(result, '用户创建成功');
      state.loaded.users = false;
      await loadUsers(true);
    } catch (saveError) {
      error.textContent = saveError.message || '保存失败';
      error.hidden = false;
    } finally {
      setButtonLoading(button, false);
    }
  }

  function credentialValues(data) {
    var credentials = pick(data, ['credentials'], data || {});
    var user = pick(data, ['user'], {});
    return {
      username: pick(user, ['username'], pick(data, ['username'], '')),
      password: pick(credentials, ['temporary_password', 'temp_password', 'password'], pick(data, ['temporary_password', 'temp_password'], '')),
      apiKey: pick(credentials, ['api_key', 'key', 'plain_api_key'], pick(data, ['api_key', 'plain_api_key'], ''))
    };
  }

  function showCredentialDialog(data, title) {
    var values = credentialValues(data);
    var fields = [];
    if (values.username) fields.push({ label: '用户名', value: values.username, secret: false });
    if (values.password) fields.push({ label: '临时密码', value: values.password, secret: true });
    if (values.apiKey) fields.push({ label: 'API Key', value: values.apiKey, secret: true });
    if (!fields.length) return;
    byId('credential-title').textContent = title || '保存用户凭证';
    byId('credential-fields').innerHTML = fields.map(function (field) {
      return '<div class="credential-field"><span>' + escapeHTML(field.label) + '</span><div><code' + (field.secret ? ' class="credential-secret"' : '') + '>' + escapeHTML(field.value) + '</code><button class="row-action" type="button" data-action="copy-credential" data-value="' + escapeHTML(field.value) + '" aria-label="复制' + escapeHTML(field.label) + '" title="复制">' + icon('copy') + '</button></div></div>';
    }).join('');
    byId('credential-saved').checked = false;
    byId('credential-error').hidden = true;
    byId('credential-dialog').showModal();
    refreshIcons(byId('credential-fields'));
  }

  function attemptCloseCredentials() {
    if (!byId('credential-saved').checked) {
      byId('credential-error').textContent = '请确认已保存凭证后再关闭';
      byId('credential-error').hidden = false;
      return;
    }
    byId('credential-dialog').close();
  }

  function findUser(id) {
    return state.users.items.find(function (item) {
      return String(pick(item, ['id', 'user_id'], '')) === String(id);
    });
  }

  async function toggleUser(id) {
    var user = findUser(id);
    if (!user) return;
    var enabled = !boolValue(pick(user, ['enabled', 'is_enabled'], true), true);
    if (!enabled) {
      var confirmed = await confirmAction('停用用户', '停用后该用户的 JWT 与 API Key 将立即失效。', '停用');
      if (!confirmed) return;
    }
    try {
      await apiRequest(API.users + '/' + encodeURIComponent(id) + '/toggle', { method: 'POST', body: { enabled: enabled } });
      toast(enabled ? '用户已启用' : '用户已停用', 'success');
      state.loaded.users = false;
      loadUsers(true);
    } catch (error) {
      toast(error.message || '用户状态更新失败', 'error');
    }
  }

  async function resetUserPassword(id) {
    var user = findUser(id);
    if (!user) return;
    var confirmed = await confirmAction('重置密码', '将为“' + pick(user, ['username'], '') + '”生成临时密码，并使旧 JWT 失效。', '重置密码');
    if (!confirmed) return;
    try {
      var result = await apiRequest(API.users + '/' + encodeURIComponent(id) + '/reset-password', { method: 'POST' });
      showCredentialDialog(result, '临时密码已生成');
      state.loaded.users = false;
      loadUsers(true);
    } catch (error) {
      toast(error.message || '密码重置失败', 'error');
    }
  }

  async function resetUserKey(id) {
    var user = findUser(id);
    if (!user) return;
    var confirmed = await confirmAction('重置 API Key', '旧 Key 将立即失效，新 Key 只显示一次。', '重置 Key');
    if (!confirmed) return;
    try {
      var result = await apiRequest(API.users + '/' + encodeURIComponent(id) + '/reset-api-key', { method: 'POST' });
      showCredentialDialog(result, 'API Key 已重置');
      state.loaded.users = false;
      loadUsers(true);
    } catch (error) {
      toast(error.message || 'API Key 重置失败', 'error');
    }
  }

  async function revokeUserKey(id) {
    var user = findUser(id);
    if (!user) return;
    var confirmed = await confirmAction('吊销 API Key', '吊销后该用户只能通过网页登录搜索，直到重新生成 Key。', '吊销 Key');
    if (!confirmed) return;
    try {
      await apiRequest(API.users + '/' + encodeURIComponent(id) + '/revoke-api-key', { method: 'POST' });
      toast('API Key 已吊销', 'success');
      state.loaded.users = false;
      loadUsers(true);
    } catch (error) {
      toast(error.message || 'API Key 吊销失败', 'error');
    }
  }

  async function deleteUser(id) {
    var user = findUser(id);
    if (!user) return;
    var confirmed = await confirmAction('删除用户', '将删除“' + pick(user, ['username'], '') + '”。账号凭证会立即失效，调用日志按保留策略继续保存。', '删除');
    if (!confirmed) return;
    try {
      await apiRequest(API.users + '/' + encodeURIComponent(id), { method: 'DELETE' });
      toast('用户已删除', 'success');
      state.loaded.users = false;
      loadUsers(true);
    } catch (error) {
      toast(error.message || '用户删除失败', 'error');
    }
  }

  function updateUsageUserFilter(users) {
    var select = byId('usage-user-filter');
    if (!select) return;
    var selected = select.value;
    var known = new Map();
    state.users.items.concat(users || []).forEach(function (user) {
      var id = String(pick(user, ['id', 'user_id'], ''));
      if (id) known.set(id, pick(user, ['username', 'name'], '用户 #' + id));
    });
    select.innerHTML = '<option value="">全部用户</option>' + Array.from(known.entries()).map(function (entry) {
      return '<option value="' + escapeHTML(entry[0]) + '">' + escapeHTML(entry[1]) + '</option>';
    }).join('');
    if (known.has(selected)) select.value = selected;
  }

  async function loadUsage(force) {
    if (state.loaded.usage && !force) return;
    state.loaded.usage = true;
    var serial = ++state.requestSerial.usage;
    showPermissionState('usage', false);
    showAlert('usage-alert', '');
    byId('usage-content').hidden = false;
    byId('usage-stat-grid').innerHTML = '<article class="stat-card skeleton-card"></article>'.repeat(6);
    tableLoading('usage-logs-body', 9);
    var range = state.usage.range;
    if (state.requestControllers.usageLogs) state.requestControllers.usageLogs.abort();
    state.requestControllers.usageLogs = null;
    var controller = replaceRequestController('usage');
    try {
      var settled = await Promise.all([
        apiRequest(API.usageOverview, { query: { range: range }, signal: controller.signal }),
        apiRequest(API.usageTrends, { query: { range: range }, signal: controller.signal })
      ]);
      if (state.requestControllers.usage !== controller || serial !== state.requestSerial.usage) return;
      state.usage.overview = settled[0] || {};
      state.usage.trends = arrayFrom(settled[1], ['trends', 'items', 'points']);
      renderUsageOverview();
      await loadUsageLogs(true);
      updateTimestamp();
    } catch (error) {
      if (error.name === 'AbortError') return;
      if (serial !== state.requestSerial.usage) return;
      state.loaded.usage = false;
      if (error instanceof APIError && error.status === 403) {
        showPermissionState('usage', true);
      } else {
        showAlert('usage-alert', error.message || 'API 监控数据加载失败');
      }
    } finally { finishRequestController('usage', controller); }
  }

  function percentValue(value) {
    var numeric = numberValue(value);
    if (numeric > 0 && numeric <= 1) numeric *= 100;
    return Math.max(0, Math.min(100, numeric));
  }

  function formatPercent(value) {
    return (Math.round(percentValue(value) * 10) / 10) + '%';
  }

  function renderUsageOverview() {
    var data = state.usage.overview || {};
    var requestCount = numberValue(pick(data, ['request_count', 'total_requests', 'calls_total', 'total'], 0));
    var activeUsers = numberValue(pick(data, ['active_users', 'active_user_count', 'user_count'], 0));
    var successRate = pick(data, ['success_rate'], 0);
    var average = numberValue(pick(data, ['average_duration_ms', 'avg_duration_ms', 'average_latency_ms'], 0));
    var p95 = numberValue(pick(data, ['p95_duration_ms', 'p95_latency_ms'], 0));
    var limited = numberValue(pick(data, ['rate_limited_count', 'limited_count', 'status_429_count'], 0));
    var cards = [
      { label: '总调用量', value: formatNumber(requestCount), icon: 'waypoints', tone: 'blue', foot: state.usage.range === '24h' ? '最近 24 小时' : state.usage.range === '30d' ? '最近 30 天' : '最近 7 天' },
      { label: '活跃用户', value: formatNumber(activeUsers), icon: 'users', tone: 'violet', foot: '产生过调用的用户' },
      { label: '成功率', value: formatPercent(successRate), icon: 'circle-check-big', tone: 'green', foot: 'HTTP 2xx 请求' },
      { label: '平均耗时', value: Math.round(average) + ' ms', icon: 'timer', tone: 'amber', foot: '端到端响应时间' },
      { label: 'P95 耗时', value: Math.round(p95) + ' ms', icon: 'gauge', tone: 'violet', foot: '95% 请求低于此值' },
      { label: '限流次数', value: formatNumber(limited), icon: 'shield-alert', tone: limited ? 'amber' : 'green', foot: 'HTTP 429 响应' }
    ];
    byId('usage-stat-grid').innerHTML = cards.map(function (card) {
      return '<article class="stat-card"><div class="stat-head"><span>' + escapeHTML(card.label) + '</span><span class="stat-icon ' + card.tone + '">' + icon(card.icon) + '</span></div><div class="stat-value">' + escapeHTML(card.value) + '</div><div class="stat-foot">' + escapeHTML(card.foot) + '</div></article>';
    }).join('');
    renderUsageTrendChart(state.usage.trends);
    renderUsageStatusChart(pick(data, ['status_counts', 'status_distribution', 'statuses'], {}));
    var topUsers = arrayFrom(pick(data, ['top_users', 'user_ranking', 'users'], []), ['items', 'users']);
    renderUsageUsersChart(topUsers);
    updateUsageUserFilter(topUsers);
    refreshIcons(byId('usage-content'));
  }

  function normalizeUsageTrend(data) {
    if (Array.isArray(data)) {
      return data.map(function (item) {
        return {
          time: pick(item, ['time', 'date', 'bucket', 'label'], ''),
          requests: numberValue(pick(item, ['request_count', 'requests', 'total', 'count'], 0)),
          success: numberValue(pick(item, ['success_count', 'successful', 'success'], 0)),
          limited: numberValue(pick(item, ['rate_limited_count', 'limited_count', 'status_429_count'], 0))
        };
      });
    }
    return [];
  }

  function renderUsageTrendChart(data) {
    var chart = chartFor('usage-trend-chart');
    if (!chart) return;
    var points = normalizeUsageTrend(data);
    chart.setOption({
      animationDuration: 420,
      color: ['#1769e0', '#16835b', '#c23838'],
      tooltip: { trigger: 'axis', backgroundColor: '#17191d', borderWidth: 0, textStyle: { color: '#fff', fontSize: 11 } },
      grid: { left: 8, right: 10, top: 24, bottom: 4, containLabel: true },
      xAxis: { type: 'category', boundaryGap: false, data: points.map(function (point) { return String(point.time).replace('T', ' ').slice(5, 16); }), axisLine: { lineStyle: { color: '#dfe2e6' } }, axisTick: { show: false }, axisLabel: { color: '#8c939d', fontSize: 9, hideOverlap: true } },
      yAxis: { type: 'value', minInterval: 1, splitLine: { lineStyle: { color: '#eceef0', type: 'dashed' } }, axisLabel: { color: '#8c939d', fontSize: 9 } },
      series: [
        { name: '请求', type: 'line', smooth: 0.25, symbol: 'none', lineStyle: { width: 2 }, areaStyle: { color: 'rgba(23,105,224,0.10)' }, data: points.map(function (point) { return point.requests; }) },
        { name: '成功', type: 'line', smooth: 0.25, symbol: 'none', lineStyle: { width: 1.5 }, data: points.map(function (point) { return point.success; }) },
        { name: '限流', type: 'bar', barMaxWidth: 8, data: points.map(function (point) { return point.limited; }) }
      ]
    }, true);
  }

  function normalizeHTTPStatuses(data) {
    if (Array.isArray(data)) {
      return data.map(function (item) {
        return { code: String(pick(item, ['status_code', 'status', 'code'], '其他')), count: numberValue(pick(item, ['count', 'request_count', 'total'], 0)) };
      });
    }
    if (data && typeof data === 'object') {
      return Object.keys(data).map(function (key) { return { code: key, count: numberValue(data[key]) }; });
    }
    return [];
  }

  function httpStatusColor(code) {
    var value = Number(code);
    if (value >= 200 && value < 300) return '#16835b';
    if (value === 429) return '#a26108';
    if (value >= 400 && value < 500) return '#d28a24';
    if (value >= 500) return '#c23838';
    return '#9299a3';
  }

  function renderUsageStatusChart(data) {
    var chart = chartFor('usage-status-chart');
    var statuses = normalizeHTTPStatuses(data);
    if (chart) {
      chart.setOption({
        animationDuration: 420,
        tooltip: { trigger: 'item', formatter: 'HTTP {b}<br/>{c} ({d}%)', backgroundColor: '#17191d', borderWidth: 0, textStyle: { color: '#fff', fontSize: 11 } },
        series: [{ type: 'pie', radius: ['55%', '76%'], center: ['50%', '48%'], label: { show: false }, data: statuses.map(function (item) { return { name: item.code, value: item.count, itemStyle: { color: httpStatusColor(item.code) } }; }) }]
      }, true);
    }
    byId('usage-status-legend').innerHTML = statuses.slice(0, 6).map(function (item) {
      return '<span><i style="background:' + httpStatusColor(item.code) + '"></i>HTTP ' + escapeHTML(item.code) + ' ' + formatNumber(item.count) + '</span>';
    }).join('');
  }

  function renderUsageUsersChart(users) {
    var chart = chartFor('usage-users-chart');
    if (!chart) return;
    var normalized = users.map(function (user) {
      return {
        name: pick(user, ['username', 'name'], '用户 #' + pick(user, ['user_id', 'id'], '')),
        count: numberValue(pick(user, ['request_count', 'count', 'total', 'value'], 0))
      };
    }).sort(function (a, b) { return b.count - a.count; }).slice(0, 10).reverse();
    chart.setOption({
      animationDuration: 420,
      color: ['#1769e0'],
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, backgroundColor: '#17191d', borderWidth: 0, textStyle: { color: '#fff', fontSize: 11 } },
      grid: { left: 8, right: 16, top: 12, bottom: 5, containLabel: true },
      xAxis: { type: 'value', minInterval: 1, splitLine: { lineStyle: { color: '#eceef0', type: 'dashed' } }, axisLabel: { color: '#8c939d', fontSize: 9 } },
      yAxis: { type: 'category', data: normalized.map(function (user) { return String(user.name).slice(0, 18); }), axisLine: { show: false }, axisTick: { show: false }, axisLabel: { color: '#59616b', fontSize: 10 } },
      series: [{ type: 'bar', barMaxWidth: 13, itemStyle: { borderRadius: [0, 3, 3, 0] }, data: normalized.map(function (user) { return user.count; }) }]
    }, true);
  }

  async function loadUsageLogs(force) {
    if (!force && state.usage.logs.length) return;
    var serial = ++state.requestSerial.usageLogs;
    byId('usage-logs-empty').hidden = true;
    tableLoading('usage-logs-body', 9);
    var query = Object.assign({}, state.usage.query, {
      range: state.usage.range,
      page: state.usage.page,
      page_size: state.usage.pageSize
    });
    var controller = replaceRequestController('usageLogs');
    try {
      var data = await apiRequest(API.usageLogs, { query: query, signal: controller.signal });
      if (state.requestControllers.usageLogs !== controller || serial !== state.requestSerial.usageLogs) return;
      var items = arrayFrom(data, ['logs', 'items', 'results']);
      var meta = paginationMeta(data, items, state.usage.page, state.usage.pageSize);
      state.usage.logs = items;
      state.usage.total = meta.total;
      state.usage.page = meta.page;
      state.usage.pageSize = meta.pageSize;
      state.usage.pages = meta.pages;
      renderUsageLogs();
    } catch (error) {
      if (error.name === 'AbortError') return;
      if (serial !== state.requestSerial.usageLogs) return;
      byId('usage-logs-body').innerHTML = '';
      if (error instanceof APIError && error.status === 403) showPermissionState('usage', true);
      else showAlert('usage-alert', error.message || '调用日志加载失败');
    } finally { finishRequestController('usageLogs', controller); }
  }

  function renderUsageLogs() {
    var body = byId('usage-logs-body');
    var empty = byId('usage-logs-empty');
    byId('usage-log-total').textContent = formatNumber(state.usage.total) + ' 条记录';
    if (!state.usage.logs.length) {
      body.innerHTML = '';
      empty.hidden = false;
      byId('usage-logs-pagination').innerHTML = '';
      refreshIcons(empty);
      return;
    }
    empty.hidden = true;
    body.innerHTML = state.usage.logs.map(function (log) {
      var status = numberValue(pick(log, ['status_code', 'status'], 0));
      var username = pick(log, ['username', 'user_name'], '用户 #' + pick(log, ['user_id'], '—'));
      var authType = pick(log, ['auth_type'], 'web');
      var endpoint = pick(log, ['endpoint', 'path'], '/api/search');
      var method = pick(log, ['method'], 'POST');
      var keyword = pick(log, ['keyword', 'query'], '');
      var cache = String(pick(log, ['cache_status', 'cache'], 'unknown')).toLowerCase();
      return '<tr>' +
        '<td>' + escapeHTML(formatDate(pick(log, ['created_at', 'timestamp', 'time'], null), true)) + '</td>' +
        '<td><div class="keyword-name"><strong>' + escapeHTML(username) + '</strong><small>' + (authType === 'api_key' ? 'API Key' : '网页 JWT') + '</small></div></td>' +
        '<td><div class="request-cell"><span class="mono">' + escapeHTML(method) + '</span><small class="mono">' + escapeHTML(endpoint) + '</small></div></td>' +
        '<td><span class="log-keyword" title="' + escapeHTML(keyword) + '">' + escapeHTML(keyword || '—') + '</span></td>' +
        '<td><span class="http-status status-' + (status >= 200 && status < 300 ? 'ok' : status === 429 ? 'limited' : 'error') + '">' + status + '</span></td>' +
        '<td class="mono">' + formatNumber(pick(log, ['duration_ms'], 0)) + ' ms</td>' +
        '<td>' + formatNumber(pick(log, ['result_count'], 0)) + '</td>' +
        '<td><span class="type-badge cache-' + escapeHTML(cache) + '">' + escapeHTML(cache === 'hit' ? '命中' : cache === 'miss' ? '未命中' : cache === 'refresh' ? '刷新' : cache) + '</span></td>' +
        '<td class="mono">' + escapeHTML(pick(log, ['source_ip', 'ip'], '—')) + '</td>' +
      '</tr>';
    }).join('');
    renderPagination('usage-logs-pagination', state.usage.page, state.usage.pages, 'usageLogs');
  }

  function updateUsageFilters() {
    state.usage.query = {
      q: byId('usage-log-search').value.trim(),
      user_id: byId('usage-user-filter').value,
      auth_type: byId('usage-auth-filter').value,
      status: byId('usage-status-filter').value,
      cache_status: byId('usage-cache-filter').value
    };
    state.usage.page = 1;
    loadUsageLogs(true);
  }

  function changeUsageRange() {
    state.usage.range = byId('usage-range').value;
    state.usage.page = 1;
    state.loaded.usage = false;
    loadUsage(true);
  }

  async function loadSources(force) {
    if (state.loaded.sources && !force) return;
    state.loaded.sources = true;
    showAlert('sources-alert', '');
    try {
      var results = await Promise.all([
        apiRequest(API.sourceCatalog),
        apiRequest(API.sourceConfig)
      ]);
      state.sources.catalog = arrayFrom(results[0], ['items', 'plugins', 'catalog']);
      state.sources.config = pick(results[1], ['config'], results[1] || {});
      state.sources.version = numberValue(pick(results[1], ['version'], 0));
      state.sources.updatedAt = pick(results[1], ['updated_at'], null);
      state.sources.snapshot = pick(results[1], ['snapshot', 'runtime'], {});
      state.sources.dirty = false;
      renderSources();
      await loadSourceCredentials();
    } catch (error) {
      state.loaded.sources = false;
      showAlert('sources-alert', error.message || '搜索来源加载失败');
      renderSources();
    }
  }

  async function loadSourceCredentials() {
    var requestID = ++state.requestSerial.sources;
    var requestedTab = state.sources.tab;
    var target = byId('source-credentials');
    if (target) {
      target.innerHTML = '<div class="credential-loading">' + icon('loader-circle', 'spin') + '<span>正在加载插件账号</span></div>';
      refreshIcons(target);
    }
    try {
      var credentialAPI = requestedTab === 'admin' ? API.credentials : API.userCredentials;
      var hasAdminFilters = requestedTab === 'admin' && Boolean(state.sources.credentialQuery.plugin_key || state.sources.credentialQuery.status);
      var summaryRequest = hasAdminFilters ? apiRequest(API.credentials, { query: { page_size: 100 } }) : null;
      var result = await apiRequest(credentialAPI, {
        query: {
          plugin_key: state.sources.credentialQuery.plugin_key || '',
          status: state.sources.credentialQuery.status || '',
          page_size: 100
        }
      });
      var summaryResult = summaryRequest ? await summaryRequest : result;
      if (requestID !== state.requestSerial.sources || requestedTab !== state.sources.tab) return;
      state.sources.credentials = arrayFrom(result, ['items', 'credentials']);
      if (requestedTab === 'admin') {
        state.sources.adminCredentials = arrayFrom(summaryResult, ['items', 'credentials']);
        state.sources.sharedTotal = state.sources.credentials.filter(function (item) {
          return item.scope === 'public_shared' && item.status === 'active' && pick(item, ['owner_enabled', 'enabled'], true) !== false && !item.admin_suspended_at;
        }).length;
        byId('source-shared-total').textContent = String(state.sources.sharedTotal);
      }
      renderSourceCredentials();
      renderSourcePlugins(state.sources.config);
    } catch (error) {
      if (requestID !== state.requestSerial.sources || requestedTab !== state.sources.tab) return;
      state.sources.credentials = [];
      showAlert('sources-alert', error.message || '插件账号加载失败');
      renderSourceCredentials();
    }
  }

  function splitBulkValues(value) {
    return String(value || '').split(/[\n\r,，;；]+/).map(function (item) { return item.trim(); }).filter(Boolean);
  }

  function uniqueBulkValues(value, normalizer) {
    var seen = new Set();
    var result = [];
    splitBulkValues(value).forEach(function (item) {
      var normalized = normalizer ? normalizer(item) : item;
      var key = String(normalized || '').toLowerCase();
      if (!key || seen.has(key)) return;
      seen.add(key);
      result.push(normalized);
    });
    return result;
  }

  function normalizeSourceChannelKey(value) {
    var result = String(value || '').trim();
    result = result.replace(/^https?:\/\/(?:www\.)?(?:t\.me|telegram\.me)\//i, '');
    result = result.replace(/^(?:www\.)?(?:t\.me|telegram\.me)\//i, '');
    result = result.replace(/^s\//i, '');
    result = result.replace(/^@+/, '');
    result = result.split(/[?#]/)[0].replace(/^\/+|\/+$/g, '');
    if (result.indexOf('/') >= 0) result = result.split('/')[0];
    return result.toLowerCase();
  }

  function importSourceChannels() {
    var input = byId('source-channel-bulk');
    if (!input) return;
    var rawItems = splitBulkValues(input.value);
    if (!rawItems.length) {
      toast('请先输入 TG 频道', 'info');
      return;
    }
    if (!state.sources.config) state.sources.config = { channels: [], plugins: {} };
    state.sources.config = collectSourceDraft();
    var channels = Array.isArray(state.sources.config.channels) ? state.sources.config.channels : [];
    var existing = new Set(channels.map(function (channel) { return normalizeSourceChannelKey(channel.key); }).filter(Boolean));
    var added = 0;
    var skipped = 0;
    rawItems.forEach(function (item) {
      var key = normalizeSourceChannelKey(item);
      if (!key || existing.has(key)) {
        skipped += 1;
        return;
      }
      existing.add(key);
      channels.push({ key: key, display_name: key, enabled: true, order: channels.length });
      added += 1;
    });
    state.sources.config.channels = channels;
    input.value = '';
    renderSources();
    toast('已导入 ' + added + ' 个频道，跳过 ' + skipped + ' 个重复或无效项', added ? 'success' : 'info');
  }

  function pluginConfigFieldHTML(descriptor, settings) {
    var key = descriptor.key || descriptor.name;
    var config = settings.config || {};
    var allowed = Array.isArray(descriptor.allowed_config_keys) ? descriptor.allowed_config_keys : [];
    var fields = [];
    if (allowed.indexOf('base_url') >= 0) {
      fields.push('<label class="plugin-config-field"><span>服务地址</span><input class="source-input" type="url" inputmode="url" data-source-plugin-config="' + escapeHTML(key) + '" data-config-key="base_url" value="' + escapeHTML(config.base_url || '') + '" placeholder="https://example.com"><small>仅支持 HTTPS，变更后相关账号需要重新认证。</small></label>');
    }
    if (allowed.indexOf('blocked_pan_types') >= 0) {
      var blocked = Array.isArray(config.blocked_pan_types) ? config.blocked_pan_types.join('\n') : String(config.blocked_pan_types || '');
      fields.push('<label class="plugin-config-field"><span>屏蔽网盘类型</span><textarea class="source-input" rows="2" data-source-plugin-config="' + escapeHTML(key) + '" data-config-key="blocked_pan_types" placeholder="baidu, aliyun">' + escapeHTML(blocked) + '</textarea><small>支持逗号或换行分隔，留空表示不过滤。</small></label>');
    }
    return fields.length ? '<details class="plugin-config-disclosure"><summary>' + icon('settings-2') + '<span>运行参数</span><small>' + fields.length + ' 项</small>' + icon('chevron-down') + '</summary><div class="plugin-config-fields">' + fields.join('') + '</div></details>' : '';
  }

  function sourcePluginCredentialStats(pluginKey) {
    var credentials = state.sources.adminCredentials || [];
    var matching = credentials.filter(function (item) { return String(item.plugin_key) === String(pluginKey); });
    var active = matching.filter(function (item) {
      return item.status === 'active' && pick(item, ['owner_enabled', 'enabled'], true) !== false && !item.admin_suspended_at;
    }).length;
    return { total: matching.length, active: active };
  }

  function renderSourcePlugins(config) {
    config = config || state.sources.config || { plugins: {} };
    var plugins = config.plugins || {};
    var queryInput = byId('source-plugin-search');
    var query = String(queryInput ? queryInput.value : state.sources.pluginSearch || '').trim().toLowerCase();
    state.sources.pluginSearch = query;
    var catalog = (state.sources.catalog || []).slice().sort(function (left, right) {
      var accountDelta = Number(boolValue(right.requires_account, false)) - Number(boolValue(left.requires_account, false));
      if (accountDelta) return accountDelta;
      return String(left.display_name || left.key || '').localeCompare(String(right.display_name || right.key || ''), 'zh-CN');
    });
    var enabledCount = catalog.filter(function (descriptor) {
      var key = descriptor.key || descriptor.name;
      return boolValue((plugins[key] || {}).enabled, false);
    }).length;
    if (byId('source-plugin-count')) byId('source-plugin-count').textContent = String(enabledCount);
    var visible = catalog.filter(function (descriptor) {
      var key = descriptor.key || descriptor.name;
      return !query || String(key).toLowerCase().indexOf(query) >= 0 || String(descriptor.display_name || '').toLowerCase().indexOf(query) >= 0;
    });
    var target = byId('source-plugins');
    var scrollTop = target ? target.scrollTop : 0;
    var activePluginKey = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.sourcePlugin : '';
    var openConfigKeys = new Set(Array.from(document.querySelectorAll('#source-plugins .plugin-config-disclosure[open]')).map(function (node) { return node.dataset.pluginKey; }));
    var summary = '<div class="selection-tools plugin-count-summary" role="status" aria-live="polite"><span>显示 ' + visible.length + ' / ' + catalog.length + ' 个插件</span><strong>已启用 ' + enabledCount + ' 个</strong></div>';
    var cards = visible.map(function (descriptor) {
      var key = descriptor.key || descriptor.name;
      var settings = plugins[key] || {};
      var enabled = boolValue(settings.enabled, false);
      var runtimeEnabled = boolValue(pick(config, ['async_plugins_enabled'], false), false);
      var requirement = descriptor.requires_account ? (descriptor.login_type === 'qr' ? '需要扫码账号' : '需要账号登录') : '无需账号';
      var description = descriptor.description || requirement;
      var credentialStats = sourcePluginCredentialStats(key);
      var accountReady = !descriptor.requires_account || credentialStats.active > 0;
      var running = enabled && runtimeEnabled && accountReady;
      var statusClass = running ? 'is-running' : (enabled && runtimeEnabled && !accountReady ? 'is-needs-account' : (enabled ? 'is-paused' : 'is-disabled'));
      var statusIcon = running ? 'circle-check' : (statusClass === 'is-needs-account' ? 'circle-alert' : (enabled ? 'pause-circle' : 'circle-off'));
      var statusText = running ? '运行中' : (statusClass === 'is-needs-account' ? '等待账号' : (enabled ? '总开关已暂停' : '未启用'));
      var credentialChip = '';
      var credentialAction = '';
      if (descriptor.requires_account) {
        credentialChip = credentialStats.active
          ? '<span class="plugin-account-chip is-ready">' + icon('badge-check') + '可用账号 ' + credentialStats.active + '</span>'
          : '<span class="plugin-account-chip is-missing">' + icon('circle-alert') + (credentialStats.total ? '账号需重新登录' : '账号未配置') + '</span>';
        credentialAction = '<button class="button secondary plugin-configure-account" type="button" data-action="configure-source-plugin" data-plugin-key="' + escapeHTML(key) + '">' + icon(descriptor.login_type === 'qr' ? 'scan-line' : 'key-round') + '<span>' + (credentialStats.total ? '添加账号' : '配置账号') + '</span></button>';
      }
      return '<article class="source-card plugin-source-card ' + statusClass + '" data-plugin-key="' + escapeHTML(key) + '">' +
        '<div class="plugin-source-main"><div class="plugin-source-title-row"><strong>' + escapeHTML(descriptor.display_name || key) + '</strong><span class="type-badge">' + escapeHTML(key) + '</span></div><small>' + escapeHTML(description) + '</small><div class="plugin-source-chips"><span class="plugin-state-chip">' + icon(statusIcon) + escapeHTML(statusText) + '</span>' + credentialChip + '<span class="summary-chip">' + escapeHTML(requirement) + '</span></div></div>' +
        '<div class="plugin-card-actions">' + credentialAction + '<label class="switch-row plugin-enable-switch"><input type="checkbox" data-source-plugin="' + escapeHTML(key) + '" aria-label="启用 ' + escapeHTML(descriptor.display_name || key) + ' 插件" ' + (enabled ? 'checked' : '') + '><span>启用</span></label></div>' +
        pluginConfigFieldHTML(descriptor, settings).replace('class="plugin-config-disclosure"', 'class="plugin-config-disclosure" data-plugin-key="' + escapeHTML(key) + '"' + (openConfigKeys.has(String(key)) ? ' open' : '')) + '</article>';
    }).join('');
    target.innerHTML = summary + (cards || '<div class="empty-state compact-empty"><p>没有匹配的内置插件。</p></div>');
    target.scrollTop = scrollTop;
    if (activePluginKey) {
      var activeToggle = Array.from(target.querySelectorAll('[data-source-plugin]')).find(function (input) { return input.dataset.sourcePlugin === activePluginKey; });
      if (activeToggle) activeToggle.focus({ preventScroll: true });
    }
    refreshIcons(target);
  }

  function renderSources() {
    var config = state.sources.config || { channels: [], plugins: {}, async_plugins_enabled: false };
    byId('source-version').textContent = state.sources.version ? 'v' + state.sources.version : '—';
    byId('source-updated-at').textContent = formatDate(state.sources.updatedAt, true);
    byId('source-snapshot-status').textContent = pick(state.sources.snapshot, ['status'], state.sources.version ? '运行中' : '未加载');
    byId('source-plugin-master').checked = boolValue(pick(config, ['async_plugins_enabled'], false), false);
    var channels = arrayFrom(config, ['channels']);
    if (byId('source-channel-count')) byId('source-channel-count').textContent = String(channels.length);
    byId('source-channels').innerHTML = channels.length ? channels.map(function (channel, index) {
      return '<article class="source-card"><label class="switch-row"><input type="checkbox" data-source-channel-enabled="' + index + '" ' + (boolValue(channel.enabled, true) ? 'checked' : '') + '><span>启用</span></label><input class="source-input" data-source-channel-key="' + index + '" value="' + escapeHTML(channel.key || '') + '" aria-label="频道标识"><input class="source-input" data-source-channel-name="' + index + '" value="' + escapeHTML(channel.display_name || '') + '" placeholder="显示名称" aria-label="频道显示名称"><button class="row-action danger" type="button" data-action="remove-channel" data-index="' + index + '" aria-label="删除频道">' + icon('trash-2') + '</button></article>';
    }).join('') : '<div class="empty-state compact-empty"><p>暂无 TG 频道，点击“新增”开始配置。</p></div>';
    renderSourcePlugins(config);
    switchSourceConfigTab(state.sources.configTab);
    var filter = byId('credential-plugin-filter');
    var selectedFilter = filter.value;
    filter.innerHTML = '<option value="">全部插件</option>' + state.sources.catalog.filter(function (descriptor) {
      return boolValue(descriptor.requires_account, false);
    }).map(function (descriptor) {
      var key = descriptor.key || descriptor.name;
      return '<option value="' + escapeHTML(key) + '">' + escapeHTML(descriptor.display_name || key) + '</option>';
    }).join('');
    filter.value = selectedFilter;
    byId('source-shared-total').textContent = String(state.sources.sharedTotal || 0);
    renderSourceCredentials();
    refreshIcons(byId('view-sources'));
  }

  function renderSourceCredentials() {
    var target = byId('source-credentials');
    var query = String(state.sources.credentialQuery.user || '').trim().toLowerCase();
    var items = (state.sources.credentials || []).filter(function (item) {
      if (!query) return true;
      var metadata = item.public_metadata || {};
      return [item.owner_username, item.owner_user_id, item.display_name, item.plugin_key, metadata.account_hint, metadata.masked_identifier]
        .some(function (value) { return String(value || '').toLowerCase().indexOf(query) >= 0; });
    });
    if (byId('source-account-count')) byId('source-account-count').textContent = String((state.sources.credentials || []).length);
    target.innerHTML = items.length ? items.map(function (item) {
      var metadata = item.public_metadata || {};
      var suspended = Boolean(item.admin_suspended_at || item.admin_suspended || item.status === 'admin_suspended');
      var enabled = pick(item, ['owner_enabled', 'enabled'], true) !== false;
      var status = suspended ? 'admin_suspended' : (!enabled ? 'disabled' : (item.status || 'unknown'));
      var owner = state.sources.tab === 'users' ? (item.owner_username || '用户 #' + (item.owner_user_id || '—')) : (item.scope === 'public_shared' ? '公开共享' : '管理员私有');
      var id = credentialPublicID(item);
      var searchScope = credentialSearchScopeSummary(item);
      var actions = state.sources.tab === 'admin'
        ? '<button class="credential-action" type="button" data-action="relogin-plugin-credential" data-id="' + escapeHTML(id) + '">' + icon('refresh-cw') + '<span>重新登录</span></button>' +
          ((item.plugin_key === 'qqpd' || item.plugin_key === 'weibo') ? '<button class="credential-action" type="button" data-action="edit-plugin-credential-search-scope" data-id="' + escapeHTML(id) + '">' + icon('list-filter') + '<span>搜索范围</span></button>' : '') +
          '<button class="credential-action" type="button" data-action="change-plugin-credential-scope" data-id="' + escapeHTML(id) + '">' + icon('users') + '<span>' + (item.scope === 'public_shared' ? '转为私有' : '设为共享') + '</span></button>' +
          '<button class="credential-action" type="button" data-action="toggle-plugin-credential" data-id="' + escapeHTML(id) + '">' + icon(enabled ? 'pause' : 'play') + '<span>' + (enabled ? '停用' : '启用') + '</span></button>' +
          '<button class="credential-action danger" type="button" data-action="delete-plugin-credential" data-id="' + escapeHTML(id) + '">' + icon('trash-2') + '<span>删除</span></button>'
        : '<button class="credential-action" type="button" data-action="suspend-user-credential" data-id="' + escapeHTML(id) + '">' + icon(suspended ? 'play' : 'pause') + '<span>' + (suspended ? '恢复' : '暂停') + '</span></button>' +
          '<button class="credential-action danger" type="button" data-action="delete-user-credential" data-id="' + escapeHTML(id) + '">' + icon('trash-2') + '<span>删除</span></button>';
      return '<article class="credential-card"><div class="credential-identity"><strong>' + escapeHTML(item.display_name || metadata.account_hint || item.plugin_key) + '</strong><small>' + escapeHTML(owner + ' · ' + item.plugin_key) + '</small></div>' + statusBadge(status) + '<div class="credential-meta"><span>可见范围 ' + escapeHTML(item.scope === 'public_shared' ? '公开共享' : (item.scope === 'admin_private' ? '管理员私有' : '用户私有')) + '</span>' + (searchScope ? '<span>搜索范围 ' + escapeHTML(searchScope) + '</span>' : '') + '<span>到期 ' + escapeHTML(formatDate(item.expires_at, true)) + '</span><span>最近成功 ' + escapeHTML(formatDate(item.last_success_at, true)) + '</span><span>' + escapeHTML(item.last_error_code || '运行正常') + '</span></div><div class="credential-card-actions">' + actions + '</div></article>';
    }).join('') : '<div class="empty-state compact-empty"><p>当前视图暂无插件账号。</p></div>';
    refreshIcons(target);
  }

  function credentialPublicID(item) {
    return String(pick(item, ['public_id', 'credential_id', 'id'], ''));
  }

  function findSourceCredential(id) {
    return (state.sources.credentials || []).find(function (item) { return credentialPublicID(item) === String(id); });
  }

  function credentialMetadataValues(pluginKey, metadata) {
    metadata = metadata || {};
    var raw = pluginKey === 'qqpd' ? metadata.channels : (pluginKey === 'weibo' ? metadata.user_ids : []);
    if (Array.isArray(raw)) return raw.map(function (value) { return String(value).trim(); }).filter(Boolean);
    return splitBulkValues(raw);
  }

  function credentialSearchScopeSummary(item) {
    var values = credentialMetadataValues(item.plugin_key, item.public_metadata || {});
    if (!values.length) return (item.plugin_key === 'qqpd' || item.plugin_key === 'weibo') ? '未配置' : '';
    var preview = values.slice(0, 2).join('、');
    return values.length > 2 ? preview + ' 等 ' + values.length + ' 项' : preview;
  }

  function collectSourceDraft() {
    var current = state.sources.config || {};
    var channels = [];
    document.querySelectorAll('[data-source-channel-key]').forEach(function (input) {
      var index = input.dataset.sourceChannelKey;
      channels.push({ key: input.value.trim(), display_name: document.querySelector('[data-source-channel-name="' + index + '"]').value.trim(), enabled: document.querySelector('[data-source-channel-enabled="' + index + '"]').checked, order: channels.length });
    });
    var plugins = Object.assign({}, current.plugins || {});
    document.querySelectorAll('[data-source-plugin]').forEach(function (input, index) {
      var existing = plugins[input.dataset.sourcePlugin] || {};
      plugins[input.dataset.sourcePlugin] = Object.assign({}, existing, { enabled: input.checked, order: numberValue(existing.order, index) });
    });
    document.querySelectorAll('[data-source-plugin-config]').forEach(function (input) {
      var pluginKey = input.dataset.sourcePluginConfig;
      var configKey = input.dataset.configKey;
      var existing = plugins[pluginKey] || {};
      var runtimeConfig = Object.assign({}, existing.config || {});
      if (configKey === 'blocked_pan_types') {
        runtimeConfig[configKey] = uniqueBulkValues(input.value, function (value) { return String(value).trim().toLowerCase(); });
      } else {
        var configValue = String(input.value || '').trim();
        if (configValue) runtimeConfig[configKey] = configValue;
        else delete runtimeConfig[configKey];
      }
      plugins[pluginKey] = Object.assign({}, existing, { config: runtimeConfig });
    });
    return { schema_version: 1, async_plugins_enabled: byId('source-plugin-master').checked, channels: channels, plugins: plugins };
  }

  async function validateSources() {
    try {
      await apiRequest(API.sourceValidate, { method: 'POST', body: { config: collectSourceDraft() } });
      toast('配置校验通过', 'success');
    } catch (error) { showAlert('sources-alert', error.message || '配置校验失败'); }
  }

  function setSourceEditorBusy(busy) {
    state.sources.saving = busy;
    document.querySelectorAll('#view-sources button, #view-sources input, #view-sources textarea, #view-sources select').forEach(function (control) {
      control.disabled = busy;
    });
  }

  async function saveSources(options) {
    options = options || {};
    if (state.sources.saving) {
      toast('来源配置正在保存，请稍候', 'info');
      return false;
    }
    setSourceEditorBusy(true);
    try {
      var draft = options.config || collectSourceDraft();
      await apiRequest(API.sourceConfig, { method: 'PUT', body: { expected_version: state.sources.version, config: draft } });
      state.loaded.sources = false;
      toast(options.successMessage || '来源配置已热更新', 'success');
      await loadSources(true);
      return true;
    } catch (error) {
      showAlert('sources-alert', error.status === 409 ? '配置已被其他管理员更新，请重新加载后再保存。' : (error.message || '来源配置保存失败，旧配置仍在运行。'));
      if (options.rollbackOnError) await loadSources(true);
      return false;
    } finally {
      setSourceEditorBusy(false);
    }
  }

  async function setAllSourcePlugins(enabled) {
    if (!enabled) {
      var confirmed = await confirmAction('停用全部插件', '所有内置插件会立即停止参与搜索，已保存的账号和运行参数不会被删除。', '全部停用');
      if (!confirmed) return;
    }
    var draft = collectSourceDraft();
    draft.async_plugins_enabled = enabled;
    draft.plugins = draft.plugins || {};
    (state.sources.catalog || []).forEach(function (descriptor, index) {
      var key = descriptor.key || descriptor.name;
      var existing = draft.plugins[key] || {};
      draft.plugins[key] = Object.assign({}, existing, { enabled: enabled, order: numberValue(existing.order, index) });
    });
    state.sources.config = draft;
    renderSources();
    await saveSources({ config: draft, successMessage: enabled ? '全部插件已启动' : '全部插件已停用', rollbackOnError: true });
  }

  async function ensureSourcePluginEnabled(pluginKey) {
    var draft = collectSourceDraft();
    var existing = (draft.plugins || {})[pluginKey] || {};
    if (draft.async_plugins_enabled && boolValue(existing.enabled, false)) return true;
    draft.plugins = draft.plugins || {};
    draft.async_plugins_enabled = true;
    draft.plugins[pluginKey] = Object.assign({}, existing, { enabled: true });
    state.sources.config = draft;
    renderSources();
    return saveSources({ config: draft, successMessage: '插件已启用，可以继续配置账号' });
  }

  async function configureSourcePlugin(pluginKey) {
    var descriptor = findPluginDescriptor(pluginKey);
    if (!descriptor) {
      toast('未找到该账号型插件', 'error');
      return;
    }
    if (state.sources.tab !== 'admin') switchCredentialTab('admin');
    openPluginCredentialDialog(null, 'login', pluginKey);
  }

  function addSourceChannel() {
    if (!state.sources.config) state.sources.config = { channels: [], plugins: {} };
    state.sources.config = collectSourceDraft();
    if (!Array.isArray(state.sources.config.channels)) state.sources.config.channels = [];
    state.sources.config.channels.push({ key: '', display_name: '', enabled: true, order: state.sources.config.channels.length });
    renderSources();
  }

  function removeSourceChannel(index) {
    if (state.sources.config) state.sources.config = collectSourceDraft();
    if (state.sources.config && Array.isArray(state.sources.config.channels)) {
      state.sources.config.channels.splice(index, 1);
      renderSources();
    }
  }

  function switchCredentialTab(tab) {
    state.sources.tab = tab === 'users' ? 'users' : 'admin';
    document.querySelectorAll('[data-credential-tab]').forEach(function (button) {
      var active = button.dataset.credentialTab === state.sources.tab;
      button.classList.toggle('active', active);
      button.setAttribute('aria-selected', active ? 'true' : 'false');
    });
    byId('new-plugin-credential').hidden = state.sources.tab !== 'admin';
    byId('credential-user-filter-wrap').hidden = state.sources.tab !== 'users';
    state.sources.credentialQuery.user = '';
    byId('credential-user-filter').value = '';
    loadSourceCredentials();
  }

  function switchSourceConfigTab(tab) {
    var allowed = ['tg', 'plugins', 'accounts'];
    state.sources.configTab = allowed.indexOf(tab) >= 0 ? tab : 'tg';
    document.querySelectorAll('[data-source-config-tab]').forEach(function (button) {
      var active = button.dataset.sourceConfigTab === state.sources.configTab;
      button.classList.toggle('active', active);
      button.setAttribute('aria-selected', active ? 'true' : 'false');
      button.tabIndex = active ? 0 : -1;
    });
    document.querySelectorAll('[data-source-config-panel]').forEach(function (panel) {
      panel.hidden = panel.dataset.sourceConfigPanel !== state.sources.configTab;
    });
  }

  function accountPluginDescriptors() {
    return (state.sources.catalog || []).filter(function (descriptor) {
      return boolValue(descriptor.requires_account, false);
    });
  }

  function findPluginDescriptor(key) {
    return accountPluginDescriptors().find(function (descriptor) {
      return String(descriptor.key || descriptor.name) === String(key);
    });
  }

  function pluginCredentialMetadataFieldHTML(pluginKey, metadata) {
    if (pluginKey !== 'qqpd' && pluginKey !== 'weibo') return '';
    var values = credentialMetadataValues(pluginKey, metadata || {});
    var fieldName = pluginKey === 'qqpd' ? 'metadata_channels' : 'metadata_user_ids';
    var label = pluginKey === 'qqpd' ? 'QQ 频道 ID' : '微博用户 ID';
    var placeholder = pluginKey === 'qqpd' ? 'pd97631607\nkuake12345' : '1234567890\n9876543210';
    return '<label class="field plugin-metadata-field"><span>' + label + '</span><textarea class="source-input" name="' + fieldName + '" rows="5" placeholder="' + placeholder + '">' + escapeHTML(values.join('\n')) + '</textarea><small>支持逗号、中文逗号、分号或换行分隔，重复项会自动去除。</small></label>';
  }

  function collectPluginCredentialMetadata(form, pluginKey) {
    if (pluginKey === 'qqpd') {
      var channelField = form.elements.metadata_channels;
      return { channels: uniqueBulkValues(channelField ? channelField.value : '', normalizeSourceChannelKey) };
    }
    if (pluginKey === 'weibo') {
      var userField = form.elements.metadata_user_ids;
      return { user_ids: uniqueBulkValues(userField ? userField.value : '') };
    }
    return {};
  }

  function renderPluginCredentialLoginFields() {
    var form = byId('plugin-credential-form');
    var pluginKey = form.elements.plugin_key.value;
    var descriptor = findPluginDescriptor(pluginKey);
    var loginType = descriptor && descriptor.login_type === 'qr' ? 'qr' : 'password';
    var target = byId('plugin-credential-login-fields');
    var metadata = state.sources.credentialEditing ? state.sources.credentialEditing.public_metadata || {} : {};
    var metadataField = pluginCredentialMetadataFieldHTML(pluginKey, metadata);
    var accountHelp = descriptor && descriptor.description ? '<div class="inline-alert info">' + escapeHTML(descriptor.description) + '</div>' : '';
    if (state.sources.credentialEditMode === 'metadata') {
      target.innerHTML = accountHelp + (metadataField || '<div class="inline-alert info">该插件没有独立搜索范围设置。</div>');
    } else if (loginType === 'qr') {
      target.innerHTML = accountHelp + '<div class="qr-login-hint">' + icon('qr-code') + '<div><strong>扫码登录</strong><span>提交后将生成一次性二维码，登录状态会自动更新。</span></div></div>' + metadataField;
    } else {
      target.innerHTML = accountHelp + '<label class="field"><span>登录账号</span><input name="username" type="text" autocomplete="username" required></label>' +
        '<label class="field"><span>登录密码</span><input name="password" type="password" autocomplete="current-password" required></label>' + metadataField;
    }
    renderPluginCredentialContext(pluginKey, descriptor);
    refreshIcons(target);
  }

  function renderPluginCredentialContext(pluginKey, descriptor) {
    descriptor = descriptor || findPluginDescriptor(pluginKey);
    var selectedName = descriptor ? (descriptor.display_name || pluginKey) : pluginKey;
    var context = byId('plugin-credential-context');
    if (!context) return;
    context.innerHTML = '<span class="plugin-credential-context-icon">' + icon(descriptor && descriptor.login_type === 'qr' ? 'scan-line' : 'shield-check') + '</span><div><strong>' + escapeHTML(selectedName) + '</strong><span>' + escapeHTML(descriptor && descriptor.login_type === 'qr' ? '使用客户端扫码授权；提交登录时会自动启用插件运行环境。' : '登录信息由后端加密保存；提交登录时会自动启用插件运行环境。') + '</span></div>';
    refreshIcons(context);
  }

  function openPluginCredentialDialog(credential, mode, pluginKey) {
    var descriptors = accountPluginDescriptors();
    if (!descriptors.length) {
      toast('当前没有可配置的账号型插件', 'error');
      return;
    }
    state.sources.credentialEditing = credential || null;
    state.sources.credentialEditMode = mode === 'metadata' ? 'metadata' : 'login';
    var form = byId('plugin-credential-form');
    form.reset();
    form.elements.credential_id.value = credential ? credentialPublicID(credential) : '';
    form.elements.plugin_key.innerHTML = descriptors.map(function (descriptor) {
      var key = descriptor.key || descriptor.name;
      return '<option value="' + escapeHTML(key) + '">' + escapeHTML(descriptor.display_name || key) + '</option>';
    }).join('');
    var selectedPluginKey = credential ? credential.plugin_key : (pluginKey || descriptors[0].key || descriptors[0].name);
    form.elements.plugin_key.value = selectedPluginKey;
    if (credential) {
      form.elements.plugin_key.value = credential.plugin_key;
      form.elements.plugin_key.disabled = true;
      form.elements.scope.value = credential.scope || 'admin_private';
      form.elements.scope.disabled = true;
      form.elements.display_name.value = credential.display_name || '';
    } else {
      form.elements.plugin_key.disabled = Boolean(pluginKey);
      form.elements.scope.disabled = false;
    }
    form.elements.display_name.disabled = state.sources.credentialEditMode === 'metadata';
    var selectedDescriptor = findPluginDescriptor(selectedPluginKey);
    var selectedName = selectedDescriptor ? (selectedDescriptor.display_name || selectedPluginKey) : selectedPluginKey;
    byId('plugin-credential-dialog-title').textContent = state.sources.credentialEditMode === 'metadata' ? '编辑账号搜索范围' : (credential ? '重新登录插件账号' : (pluginKey ? '配置 ' + selectedName : '新增插件账号'));
    byId('plugin-credential-submit').querySelector('span').textContent = state.sources.credentialEditMode === 'metadata' ? '保存搜索范围' : (credential ? '重新登录' : '登录并保存');
    byId('plugin-credential-form-error').hidden = true;
    renderPluginCredentialLoginFields();
    byId('plugin-credential-dialog').showModal();
    refreshIcons(byId('plugin-credential-dialog'));
  }

  function setPluginCredentialFormBusy(busy) {
    var button = byId('plugin-credential-submit');
    button.disabled = busy;
    button.classList.toggle('loading', busy);
  }

  async function submitPluginCredential(event) {
    event.preventDefault();
    var form = event.currentTarget;
    var descriptor = findPluginDescriptor(form.elements.plugin_key.value);
    var editing = state.sources.credentialEditing;
    var payload = {
      plugin_key: form.elements.plugin_key.value,
      scope: form.elements.scope.value,
      display_name: form.elements.display_name.value.trim()
    };
    payload.metadata = collectPluginCredentialMetadata(form, payload.plugin_key);
    var errorNode = byId('plugin-credential-form-error');
    errorNode.hidden = true;
    setPluginCredentialFormBusy(true);
    try {
      if (state.sources.credentialEditMode === 'metadata') {
        if (!editing) throw new APIError('未找到需要更新的插件账号', 404, null);
        await apiRequest(API.credentials + '/' + encodeURIComponent(credentialPublicID(editing)), {
          method: 'PATCH',
          body: { metadata: payload.metadata }
        });
        byId('plugin-credential-dialog').close();
        toast('账号搜索范围已更新', 'success');
        await loadSourceCredentials();
      } else if (!await ensureSourcePluginEnabled(payload.plugin_key)) {
        throw new APIError('插件启用失败，请稍后重试', 500, null);
      } else if (descriptor && descriptor.login_type === 'qr') {
        await startPluginQRFlow(payload, editing);
        byId('plugin-credential-dialog').close();
      } else {
        payload.username = form.elements.username.value.trim();
        payload.password = form.elements.password.value;
        if (!payload.username || !payload.password) throw new APIError('请输入登录账号和密码', 400, null);
        await apiRequest(editing ? API.credentials + '/' + encodeURIComponent(credentialPublicID(editing)) + '/relogin' : API.credentials, {
          method: 'POST',
          body: payload
        });
        byId('plugin-credential-dialog').close();
        toast(editing ? '账号重新登录成功' : '插件账号已添加', 'success');
        await loadSourceCredentials();
      }
    } catch (error) {
      errorNode.textContent = error.message || '插件账号保存失败';
      errorNode.hidden = false;
    } finally {
      setPluginCredentialFormBusy(false);
    }
  }

  function pluginQRImageURL(flow) {
    var value = String(pick(flow, ['qr_code_url', 'qr_code_data', 'qr_code'], '') || '').trim();
    if (/^data:image\/(png|jpeg|gif|webp|svg\+xml);/i.test(value) || /^https:\/\//i.test(value)) return value;
    if (/^[a-zA-Z0-9+/=\r\n]+$/.test(value) && value.length > 100) return 'data:image/png;base64,' + value.replace(/\s/g, '');
    return '';
  }

  function normalizePluginLoginFlow(value) {
    return pick(value, ['flow', 'login_flow'], value || {});
  }

  async function startPluginQRFlow(payload, editing) {
    closePluginQRFlow(false);
    var flow = normalizePluginLoginFlow(await apiRequest(API.credentialLoginFlows, {
      method: 'POST',
      body: Object.assign({}, payload, editing ? { credential_id: credentialPublicID(editing) } : {})
    }));
    state.sources.loginFlow = { data: flow, payload: payload, editing: editing || null };
    renderPluginQRFlow();
    byId('plugin-qr-dialog').showModal();
    schedulePluginQRFlowPoll();
  }

  function renderPluginQRFlow() {
    if (!state.sources.loginFlow) return;
    var flow = state.sources.loginFlow.data || {};
    var status = String(flow.status || 'pending');
    var imageURL = pluginQRImageURL(flow);
    byId('plugin-qr-image').innerHTML = imageURL ? '<img src="' + escapeHTML(imageURL) + '" alt="插件账号登录二维码">' : '<span>正在生成二维码…</span>';
    byId('plugin-qr-status').textContent = status === 'scanned' ? '已扫码，请在手机上确认' : (status === 'success' ? '登录成功' : (status === 'expired' ? '二维码已过期' : (status === 'failed' ? '登录失败' : '等待扫码')));
    byId('plugin-qr-message').textContent = flow.message || '请使用对应客户端扫码，并在手机上确认登录。';
    byId('plugin-qr-retry').hidden = status !== 'expired' && status !== 'failed';
    refreshIcons(byId('plugin-qr-dialog'));
  }

  function schedulePluginQRFlowPoll() {
    if (!state.sources.loginFlow) return;
    clearTimeout(state.sources.loginFlowTimer);
    state.sources.loginFlowTimer = window.setTimeout(pollPluginQRFlow, 1200);
  }

  async function pollPluginQRFlow() {
    if (!state.sources.loginFlow || !byId('plugin-qr-dialog').open) return;
    var current = state.sources.loginFlow;
    var flowID = String(pick(current.data, ['public_id', 'flow_id', 'id'], ''));
    if (!flowID) return;
    try {
      current.data = normalizePluginLoginFlow(await apiRequest(API.credentialLoginFlows + '/' + encodeURIComponent(flowID)));
      renderPluginQRFlow();
      if (current.data.status === 'success') {
        toast(current.editing ? '账号重新登录成功' : '插件账号已添加', 'success');
        window.setTimeout(function () { closePluginQRFlow(); }, 600);
        await loadSourceCredentials();
        return;
      }
      if (current.data.status === 'failed' || current.data.status === 'expired') return;
    } catch (error) {
      byId('plugin-qr-status').textContent = '状态更新失败';
      byId('plugin-qr-message').textContent = error.message || '请稍后重试';
      byId('plugin-qr-retry').hidden = false;
      return;
    }
    schedulePluginQRFlowPoll();
  }

  function closePluginQRFlow(clearState) {
    clearTimeout(state.sources.loginFlowTimer);
    state.sources.loginFlowTimer = null;
    if (byId('plugin-qr-dialog').open) byId('plugin-qr-dialog').close();
    if (clearState !== false) state.sources.loginFlow = null;
  }

  async function retryPluginQRFlow() {
    if (!state.sources.loginFlow) return;
    var current = state.sources.loginFlow;
    try {
      await startPluginQRFlow(current.payload, current.editing);
    } catch (error) {
      byId('plugin-qr-status').textContent = '二维码生成失败';
      byId('plugin-qr-message').textContent = error.message || '请稍后重试';
    }
  }

  async function changePluginCredentialScope(id) {
    var credential = findSourceCredential(id);
    if (!credential) return;
    var nextScope = credential.scope === 'public_shared' ? 'admin_private' : 'public_shared';
    var confirmed = await confirmAction('更改账号可见范围', nextScope === 'public_shared' ? '设为共享后，所有用户搜索均可使用此账号。确认继续？' : '转为私有后，普通用户不再使用此账号。确认继续？', '确认更改');
    if (!confirmed) return;
    await mutateSourceCredential(API.credentials + '/' + encodeURIComponent(id), { scope: nextScope }, '账号范围已更新');
  }

  async function togglePluginCredential(id) {
    var credential = findSourceCredential(id);
    if (!credential) return;
    var enabled = pick(credential, ['owner_enabled', 'enabled'], true) !== false;
    await mutateSourceCredential(API.credentials + '/' + encodeURIComponent(id), { enabled: !enabled }, enabled ? '账号已停用' : '账号已启用');
  }

  async function suspendUserCredential(id) {
    var credential = findSourceCredential(id);
    if (!credential) return;
    var suspended = Boolean(credential.admin_suspended_at || credential.admin_suspended || credential.status === 'admin_suspended');
    await mutateSourceCredential(API.userCredentials + '/' + encodeURIComponent(id), { suspended: !suspended }, suspended ? '用户账号已恢复' : '用户账号已暂停');
  }

  async function mutateSourceCredential(path, body, message) {
    try {
      await apiRequest(path, { method: 'PATCH', body: body });
      toast(message, 'success');
      await loadSourceCredentials();
    } catch (error) {
      toast(error.message || '账号更新失败', 'error');
    }
  }

  async function deleteSourceCredential(id, userOwned) {
    var confirmed = await confirmAction('删除插件账号', userOwned ? '删除后用户需要重新登录才能继续使用此账号。管理员无法恢复该凭证。' : '删除后该账号无法恢复，关联搜索将立即停止使用它。', '删除账号');
    if (!confirmed) return;
    try {
      await apiRequest((userOwned ? API.userCredentials : API.credentials) + '/' + encodeURIComponent(id), { method: 'DELETE' });
      toast('插件账号已删除', 'success');
      await loadSourceCredentials();
    } catch (error) {
      toast(error.message || '插件账号删除失败', 'error');
    }
  }

  function updateCredentialFilters() {
    state.sources.credentialQuery.plugin_key = byId('credential-plugin-filter').value;
    state.sources.credentialQuery.status = byId('credential-status-filter').value;
    state.sources.credentialQuery.user = byId('credential-user-filter').value;
    if (state.sources.tab === 'users' && state.sources.credentialQuery.user) renderSourceCredentials();
    else loadSourceCredentials();
  }

  function startDetailPolling(id) {
    if (state.detailPollTimer) return;
    state.detailPollTimer = window.setInterval(function () {
      if (document.visibilityState === 'visible' && state.view === 'run-detail') refreshRunPageSummary(id);
    }, 4000);
  }

  async function refreshRunPageSummary(id) {
    if (state.runDetail.pollController) return;
    var controller = new AbortController();
    state.runDetail.pollController = controller;
    try {
      var previousStatus = normalizedStatus(state.runDetail.summary || {});
      var data = await apiRequest(API.runs + '/' + encodeURIComponent(id), { signal: controller.signal });
      if (state.runDetail.pollController !== controller || state.view !== 'run-detail' || state.runDetail.id !== String(id)) return;
      state.runDetail.summary = pick(data, ['run','collection_run'], data);
      renderRunPageSummary(state.runDetail.summary);
      var currentStatus = normalizedStatus(state.runDetail.summary);
      if (!ACTIVE_STATUSES.includes(currentStatus)) {
        stopDetailPolling();
        if (ACTIVE_STATUSES.includes(previousStatus)) await refreshLoadedRunItems();
      }
    } catch (error) {}
    finally { if (state.runDetail.pollController === controller) state.runDetail.pollController = null; }
  }

  function stopDetailPolling() {
    if (state.detailPollTimer) {
      clearInterval(state.detailPollTimer);
      state.detailPollTimer = null;
    }
    if (state.runDetail.pollController) state.runDetail.pollController.abort();
    state.runDetail.pollController = null;
  }

  function handleActionClick(event) {
    var viewLink = event.target.closest('[data-view]');
    if (viewLink) {
      event.preventDefault();
      navigate(viewLink.dataset.view);
      return;
    }
    var keywordTab = event.target.closest('[data-keyword-tab]');
    if (keywordTab) {
      switchKeywordTab(keywordTab.dataset.keywordTab);
      return;
    }
    var keywordMode = event.target.closest('[data-keyword-mode]');
    if (keywordMode && !keywordMode.disabled) {
      setKeywordDialogMode(keywordMode.dataset.keywordMode, false);
      return;
    }
    var credentialTab = event.target.closest('[data-credential-tab]');
    if (credentialTab) {
      switchCredentialTab(credentialTab.dataset.credentialTab);
      return;
    }
    var sourceConfigTab = event.target.closest('[data-source-config-tab]');
    if (sourceConfigTab) {
      switchSourceConfigTab(sourceConfigTab.dataset.sourceConfigTab);
      return;
    }
    var actionElement = event.target.closest('[data-action]');
    if (!actionElement) return;
    var action = actionElement.dataset.action;
    if (action === 'open-menu') openMenu();
    else if (action === 'close-menu') closeMenu();
    else if (action === 'logout') logout();
    else if (action === 'refresh-view') refreshCurrentView();
    else if (action === 'close-dialog') actionElement.closest('dialog').close();
    else if (action === 'reset-resource-filters') {
      byId('resource-filters').reset();
      readResourceFilters();
    } else if (action === 'copy-resource') copyText(actionElement.dataset.url, '链接');
    else if (action === 'view-resource') openResourceDetail(actionElement.dataset.id);
    else if (action === 'load-more-resource-related') loadResourceRelated(actionElement.closest('[data-resource-related]'));
    else if (action === 'load-more-run-keywords') loadRunPickerKeywords(false).then(renderRunKeywordPicker);
    else if (action === 'load-more-run-sources') loadRunItemSources(actionElement.dataset.itemId);
    else if (action === 'new-keyword') openKeywordDialog(null, state.keywords.tab === 'api' ? 'api' : 'manual');
    else if (action === 'edit-keyword') {
      var keyword = state.keywords.items.find(function (item) {
        return String(pick(item, ['id', 'keyword_id'], '')) === String(actionElement.dataset.id);
      });
      if (keyword) openKeywordDialog(keyword);
    } else if (action === 'toggle-keyword') toggleKeyword(actionElement.dataset.id, actionElement);
    else if (action === 'delete-keyword') deleteKeyword(actionElement.dataset.id);
    else if (action === 'switch-api-editor') switchAPIEditorMode(actionElement.dataset.editorTarget, actionElement.dataset.editorMode);
    else if (action === 'add-api-kv') addAPIRow(actionElement.dataset.target);
    else if (action === 'remove-api-kv') actionElement.closest('.api-kv-row').remove();
    else if (action === 'test-keyword-api') testKeywordAPI();
    else if (action === 'save-and-sync-keyword-api') saveKeywordAPISource(byId('keyword-form'), true);
    else if (action === 'select-json-path') selectKeywordAPIPath(actionElement.dataset.path);
    else if (action === 'edit-keyword-api') openKeywordAPISource(actionElement.dataset.id);
    else if (action === 'sync-keyword-api') syncKeywordAPISource(actionElement.dataset.id);
    else if (action === 'view-keyword-sync-history') viewKeywordSyncHistory(actionElement.dataset.id);
    else if (action === 'view-keyword-sync-run') openKeywordSyncRunDetail(actionElement.dataset.id);
    else if (action === 'load-more-sync-iterations') loadKeywordSyncIterations(false, pick(state.keywordSyncRuns.currentDetail, ['request_summary'], {}));
    else if (action === 'reset-keyword-sync-filters') resetKeywordSyncFilters();
    else if (action === 'copy-keyword-api') copyKeywordAPISource(actionElement.dataset.id, actionElement);
    else if (action === 'delete-keyword-api') deleteKeywordAPISource(actionElement.dataset.id);
    else if (action === 'run-keyword') createRun([actionElement.dataset.id], false);
    else if (action === 'launch-selected-run') launchSelectedRun(false);
    else if (action === 'launch-selected-force-run') launchSelectedRun(true);
    else if (action === 'open-run-dialog') openRunDialog();
    else if (action === 'select-all-run-keywords') {
      var enabledIds = state.runPicker.items.filter(function (item) {
        return boolValue(pick(item, ['enabled', 'is_enabled'], true), true);
      }).map(function (item) { return String(pick(item, ['id', 'keyword_id'], '')); });
      var everySelected = enabledIds.length > 0 && enabledIds.every(function (id) { return state.runPicker.selected.has(id); });
      state.runPicker.selected = everySelected ? new Set() : new Set(enabledIds);
      renderRunKeywordPicker();
    } else if (action === 'view-run') { event.preventDefault(); openRunDetail(actionElement.dataset.id); }
    else if (action === 'new-user') openUserDialog(null);
    else if (action === 'edit-user') {
      var user = findUser(actionElement.dataset.id);
      if (user) openUserDialog(user);
    } else if (action === 'toggle-user') toggleUser(actionElement.dataset.id);
    else if (action === 'reset-user-password') resetUserPassword(actionElement.dataset.id);
    else if (action === 'reset-user-key') resetUserKey(actionElement.dataset.id);
    else if (action === 'revoke-user-key') revokeUserKey(actionElement.dataset.id);
    else if (action === 'delete-user') deleteUser(actionElement.dataset.id);
    else if (action === 'copy-credential') copyText(actionElement.dataset.value, '凭证');
    else if (action === 'close-credentials') attemptCloseCredentials();
    else if (action === 'cancel-confirm') settleConfirm(false);
    else if (action === 'validate-sources') validateSources();
    else if (action === 'save-sources') saveSources();
    else if (action === 'add-channel') addSourceChannel();
    else if (action === 'import-source-channels' || action === 'bulk-import-channels' || action === 'bulk-add-channels' || action === 'import-channels') importSourceChannels();
    else if (action === 'remove-channel') removeSourceChannel(numberValue(actionElement.dataset.index, -1));
    else if (action === 'enable-all-plugins') setAllSourcePlugins(true);
    else if (action === 'disable-all-plugins') setAllSourcePlugins(false);
    else if (action === 'configure-source-plugin') configureSourcePlugin(actionElement.dataset.pluginKey);
    else if (action === 'new-plugin-credential') openPluginCredentialDialog(null);
    else if (action === 'relogin-plugin-credential') {
      var sourceCredential = findSourceCredential(actionElement.dataset.id);
      if (sourceCredential) openPluginCredentialDialog(sourceCredential);
    }
    else if (action === 'edit-plugin-credential-search-scope') {
      var searchScopeCredential = findSourceCredential(actionElement.dataset.id);
      if (searchScopeCredential) openPluginCredentialDialog(searchScopeCredential, 'metadata');
    }
    else if (action === 'change-plugin-credential-scope') changePluginCredentialScope(actionElement.dataset.id);
    else if (action === 'toggle-plugin-credential') togglePluginCredential(actionElement.dataset.id);
    else if (action === 'delete-plugin-credential') deleteSourceCredential(actionElement.dataset.id, false);
    else if (action === 'suspend-user-credential') suspendUserCredential(actionElement.dataset.id);
    else if (action === 'delete-user-credential') deleteSourceCredential(actionElement.dataset.id, true);
    else if (action === 'close-plugin-qr') closePluginQRFlow();
    else if (action === 'retry-plugin-qr') retryPluginQRFlow();
  }

  function handleChange(event) {
    var target = event.target;
    if (target.matches('[data-source-plugin]')) {
      state.sources.config = collectSourceDraft();
      renderSourcePlugins(state.sources.config);
    }
    else if (target.id === 'source-plugin-master') {
      state.sources.config = collectSourceDraft();
      renderSourcePlugins(state.sources.config);
    }
    else if (target.matches('#resource-filters select, #resource-filters input[type="date"]')) readResourceFilters();
    else if (target.id === 'keyword-enabled-filter' || target.id === 'keyword-type-filter') updateKeywordFilters();
    else if (target.id === 'keyword-sync-source-filter' || target.id === 'keyword-sync-status-filter' || target.id === 'keyword-sync-trigger-filter' || target.id === 'keyword-sync-from-filter' || target.id === 'keyword-sync-to-filter') updateKeywordSyncFilters();
    else if (target.id === 'api-body-type') syncAPIBodyEditor();
    else if (target.id === 'api-iteration-enabled' || target.id === 'api-iteration-unlimited' || target.id === 'api-iteration-location') updateAPIIterationPreview();
    else if (target.id === 'run-status-filter' || target.id === 'run-trigger-filter') updateRunFilters();
    else if (target.id === 'user-role-filter' || target.id === 'user-status-filter') updateUserFilters();
    else if (target.id === 'usage-range') changeUsageRange();
    else if (target.id === 'credential-plugin-filter' || target.id === 'credential-status-filter') updateCredentialFilters();
    else if (target.matches('#plugin-credential-form [name="plugin_key"]')) renderPluginCredentialLoginFields();
    else if (target.id === 'usage-user-filter' || target.id === 'usage-auth-filter' || target.id === 'usage-status-filter' || target.id === 'usage-cache-filter') updateUsageFilters();
    else if (target.matches('#user-form [name="rate_limit_disabled"]')) syncUserLimitFields();
    else if (target.id === 'select-all-keywords') {
      state.keywords.items.forEach(function (item) {
        var id = String(pick(item, ['id', 'keyword_id'], ''));
        if (target.checked) state.keywords.selected.add(id);
        else state.keywords.selected.delete(id);
      });
      renderKeywords();
    } else if (target.dataset.action === 'select-keyword') {
      if (target.checked) state.keywords.selected.add(target.dataset.id);
      else state.keywords.selected.delete(target.dataset.id);
      updateKeywordBulkBar();
    } else if (target.dataset.action === 'select-run-keyword') {
      if (target.checked) state.runPicker.selected.add(target.dataset.id);
      else state.runPicker.selected.delete(target.dataset.id);
      byId('run-selected-label').textContent = '已选 ' + state.runPicker.selected.size + ' 项';
    }
  }

  function handlePagination(event) {
    var button = event.target.closest('[data-page-scope]');
    if (!button || button.disabled) return;
    var page = numberValue(button.dataset.page, 1);
    var scope = button.dataset.pageScope;
    var pageState = scope === 'usageLogs' ? state.usage : state[scope];
    if (!pageState || page < 1 || page > pageState.pages) return;
    pageState.page = page;
    if (scope !== 'usageLogs') state.loaded[scope] = false;
    if (scope === 'runs') loadRuns({ force: true });
    else if (scope === 'resources') loadResources(true);
    else if (scope === 'keywords') loadKeywords(true);
    else if (scope === 'keywordSyncRuns') {
      state.keywordSyncRuns.loaded = false;
      loadKeywordSyncRuns({ force: true });
    }
    else if (scope === 'users') loadUsers(true);
    else if (scope === 'usageLogs') {
      state.usage.page = page;
      loadUsageLogs(true);
    }
    window.scrollTo({ top: 0, behavior: 'smooth' });
  }

  function setupEvents() {
    byId('login-form').addEventListener('submit', submitLogin);
    byId('keyword-form').addEventListener('submit', saveKeyword);
    byId('run-form').addEventListener('submit', submitRun);
    byId('user-form').addEventListener('submit', saveUser);
    byId('plugin-credential-form').addEventListener('submit', submitPluginCredential);
    byId('confirm-submit').addEventListener('click', function () { settleConfirm(true); });
    document.addEventListener('click', handleActionClick);
    document.addEventListener('click', handlePagination);
    document.addEventListener('change', handleChange);
    byId('resource-filters').addEventListener('submit', function (event) {
      event.preventDefault();
      readResourceFilters();
    });
    byId('keyword-sync-filters').addEventListener('submit', function (event) {
      event.preventDefault();
      updateKeywordSyncFilters();
    });
    byId('resource-filters').querySelector('[name="q"]').addEventListener('input', debounce(readResourceFilters, 350));
    byId('keyword-search').addEventListener('input', debounce(updateKeywordFilters, 350));
    byId('api-response-path').addEventListener('input', debounce(updateKeywordAPIExtractPreview, 120));
    ['api-iteration-start', 'api-iteration-step', 'api-iteration-count', 'api-iteration-delay', 'api-iteration-no-keyword-stop-count', 'api-iteration-random-delay-min', 'api-iteration-random-delay-max'].forEach(function (id) {
      byId(id).addEventListener('input', updateAPIIterationPreview);
    });
    ['query', 'header', 'form'].forEach(function (target) {
      byId(apiEditorDefinitions[target].json).addEventListener('blur', function () { validateAPIObjectEditor(target); });
      byId(apiEditorDefinitions[target].json).addEventListener('input', function () { showAPIEditorError(target, ''); });
    });
    byId('user-search').addEventListener('input', debounce(updateUserFilters, 350));
    byId('usage-log-search').addEventListener('input', debounce(updateUsageFilters, 350));
    byId('credential-user-filter').addEventListener('input', debounce(function (event) {
      state.sources.credentialQuery.user = event.target.value;
      renderSourceCredentials();
    }, 220));
    var pluginSearch = byId('source-plugin-search');
    if (pluginSearch) pluginSearch.addEventListener('input', debounce(function (event) {
      state.sources.config = collectSourceDraft();
      state.sources.pluginSearch = event.target.value;
      renderSourcePlugins(state.sources.config);
    }, 180));
    var channelBulk = byId('source-channel-bulk');
    if (channelBulk) channelBulk.addEventListener('keydown', function (event) {
      if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        importSourceChannels();
      }
    });
    byId('plugin-qr-dialog').addEventListener('close', function () {
      clearTimeout(state.sources.loginFlowTimer);
      state.sources.loginFlowTimer = null;
      state.sources.loginFlow = null;
    });
    byId('run-keyword-search').addEventListener('input', function (event) {
      state.runPicker.search = event.target.value.trim();
      clearTimeout(state.runPicker.searchTimer); state.runPicker.searchTimer = window.setTimeout(function(){loadRunPickerKeywords(true).then(renderRunKeywordPicker).catch(function(error){if(error.name!=='AbortError')toast(error.message||'关键词加载失败','error');});},300);
    });
    byId('run-keyword-list').addEventListener('scroll', function(event){if(event.target.scrollTop+event.target.clientHeight>=event.target.scrollHeight-100)loadRunPickerKeywords(false).then(renderRunKeywordPicker);});
    byId('run-item-search').addEventListener('input', debounce(function (event) { state.runDetail.query.q = event.target.value.trim(); loadRunItems(true); }, 300));
    byId('run-item-status').addEventListener('change', function (event) { state.runDetail.query.status = event.target.value; loadRunItems(true); });
    byId('run-page-items').addEventListener('toggle', function (event) { if (event.target.matches('.run-keyword-card') && event.target.open) loadRunItemSources(event.target.dataset.itemId); }, true);
    byId('resource-detail').addEventListener('toggle', function (event) { if (event.target.matches('[data-resource-related]') && event.target.open) loadResourceRelated(event.target); }, true);
    window.addEventListener('hashchange', function () { navigate(location.hash.slice(1) || 'overview'); });
    window.addEventListener('resize', debounce(resizeCharts, 120));
    document.addEventListener('visibilitychange', function () {
      if (document.visibilityState !== 'visible') {
        stopOverviewRefresh(true);
        stopKeywordSourcePolling();
        stopKeywordSyncDetailPolling();
        return;
      }
      if (state.view === 'overview') loadOverview(true, { background: true });
      if (document.visibilityState === 'visible' && state.view === 'runs') loadRuns({ silent: true, force: true });
      if (document.visibilityState === 'visible' && state.view === 'keywords' && state.keywords.tab === 'api' && keywordSourcesHaveActiveRun()) loadKeywordSources(true, { silent: true });
      if (document.visibilityState === 'visible' && state.view === 'keywords' && state.keywords.tab === 'history' && keywordSyncHistoryHasActiveRun()) loadKeywordSyncRuns({ force: true, silent: true });
      if (document.visibilityState === 'visible' && byId('keyword-sync-detail-dialog').open && state.keywordSyncRuns.detailID) openKeywordSyncRunDetail(state.keywordSyncRuns.detailID, true);
    });
    document.addEventListener('keydown', function (event) {
      if (event.key === 'Enter') {
        var row = event.target.closest('tr[data-action="view-run"]');
        if (row) openRunDetail(row.dataset.id);
      }
    });
  }

  function initialize() {
    byId('today-label').textContent = new Intl.DateTimeFormat('zh-CN', {
      year: 'numeric',
      month: 'long',
      day: 'numeric',
      weekday: 'short'
    }).format(new Date());
    setupEvents();
    setupDialogBehavior();
    refreshIcons();
    restoreSession();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initialize);
  } else {
    initialize();
  }
}());
