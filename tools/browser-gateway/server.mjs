import http from 'node:http';
import { mkdir } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';

const host = process.env.BROWSER_GATEWAY_HOST || '0.0.0.0';
const port = Number(process.env.BROWSER_GATEWAY_PORT || 18789);
const enginePackage = process.env.BROWSER_ENGINE_PACKAGE || 'cloakbrowser';
const headless = process.env.BROWSER_HEADLESS !== 'false';
const dataDir = process.env.BROWSER_GATEWAY_DATA_DIR || './data';
const defaultSession = process.env.BROWSER_DEFAULT_SESSION || 'default';
const executablePath = process.env.BROWSER_EXECUTABLE_PATH || '';
const warmWaitMs = Number(process.env.BROWSER_WARM_WAIT_MS || 20000);
const extraArgs = (process.env.BROWSER_EXTRA_ARGS || '')
  .split(/\s+/)
  .map((arg) => arg.trim())
  .filter(Boolean);

const __dirname = dirname(fileURLToPath(import.meta.url));
const resolvedDataDir = resolve(__dirname, dataDir);
const sessions = new Map();

let engine;
let browser;

async function loadEngine() {
  try {
    engine = await import(enginePackage);
  } catch (error) {
    if (enginePackage !== 'playwright') {
      console.warn(`failed to load ${enginePackage}, falling back to playwright: ${error.message}`);
      engine = await import('playwright');
      return;
    }
    throw error;
  }
}

async function launchBrowser() {
  await loadEngine();
  const launchOptions = {
    headless,
    args: ['--no-sandbox', '--disable-dev-shm-usage', ...extraArgs],
  };
  if (executablePath) {
    launchOptions.executablePath = executablePath;
    process.env.CLOAKBROWSER_BINARY_PATH = executablePath;
  }
  if (typeof engine.launch === 'function') {
    browser = await engine.launch(launchOptions);
    return;
  }
  const chromium = engine.chromium || engine.default?.chromium;
  if (!chromium) throw new Error(`browser engine ${enginePackage} does not expose chromium`);
  browser = await chromium.launch(launchOptions);
}

function jsonResponse(response, status, body) {
  response.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
  });
  response.end(JSON.stringify(body));
}

async function readJSON(request) {
  let data = '';
  for await (const chunk of request) {
    data += chunk;
    if (data.length > 1024 * 1024) throw new Error('request body too large');
  }
  return data ? JSON.parse(data) : {};
}

function targetURL(input) {
  const url = new URL(String(input.url || ''));
  const query = input.query && typeof input.query === 'object' ? input.query : {};
  Object.entries(query).forEach(([key, value]) => {
    if (value !== undefined && value !== null) url.searchParams.set(key, String(value));
  });
  return url;
}

function cookieHeader(headers) {
  if (!headers || typeof headers !== 'object') return '';
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === 'cookie') return String(value || '');
  }
  return '';
}

function filteredHeaders(headers) {
  const result = {};
  if (!headers || typeof headers !== 'object') return result;
  for (const [key, value] of Object.entries(headers)) {
    const lower = key.toLowerCase();
    if (lower === 'cookie' || lower === 'host' || lower === 'content-length') continue;
    result[key] = String(value ?? '');
  }
  return result;
}

function parseCookies(raw, url) {
  return String(raw || '').split(';').map((part) => {
    const trimmed = part.trim();
    if (!trimmed) return null;
    const index = trimmed.indexOf('=');
    if (index < 0) return null;
    return {
      name: trimmed.slice(0, index).trim(),
      value: trimmed.slice(index + 1).trim(),
      url: `${url.protocol}//${url.host}`,
    };
  }).filter(Boolean);
}

function browserProxy(raw) {
  const value = String(raw || process.env.BROWSER_PROXY_URL || '').trim();
  if (!value) return null;
  const parsed = new URL(value);
  let protocol = parsed.protocol.replace(':', '').toLowerCase();
  if (protocol === 'socks5h') protocol = 'socks5';
  if (!['http', 'https', 'socks4', 'socks5'].includes(protocol)) {
    throw new Error(`unsupported browser proxy protocol: ${protocol}`);
  }
  const proxy = { server: `${protocol}://${parsed.hostname}${parsed.port ? `:${parsed.port}` : ''}` };
  if (parsed.username) proxy.username = decodeURIComponent(parsed.username);
  if (parsed.password) proxy.password = decodeURIComponent(parsed.password);
  return proxy;
}

function requestBody(input) {
  const bodyType = String(input.body_type || 'none').toLowerCase();
  if (bodyType === 'none') return undefined;
  if (bodyType === 'form') {
    const params = new URLSearchParams();
    Object.entries(input.form || {}).forEach(([key, value]) => params.set(key, String(value ?? '')));
    return params.toString();
  }
  return input.body === undefined || input.body === null ? '' : String(input.body);
}

async function contextFor(sessionName, proxyURL) {
  const name = String(sessionName || defaultSession).replace(/[^a-zA-Z0-9._-]/g, '_') || defaultSession;
  const proxy = browserProxy(proxyURL);
  const proxyKey = proxy ? createHash('sha1').update(proxy.server).digest('hex').slice(0, 10) : 'direct';
  const key = `${name}-${proxyKey}`;
  if (sessions.has(key)) return sessions.get(key);
  await mkdir(resolvedDataDir, { recursive: true });
  const statePath = resolve(resolvedDataDir, `${name}.json`);
  const contextOptions = {
    storageState: statePath,
    ignoreHTTPSErrors: true,
    ...(proxy ? { proxy } : {}),
  };
  const context = await browser.newContext(contextOptions).catch(async () => {
    const { storageState, ...withoutStorage } = contextOptions;
    return browser.newContext(withoutStorage);
  });
  const page = await context.newPage();
  const record = { name, context, page, statePath };
  sessions.set(key, record);
  return record;
}

async function warmOrigin(page, url, headers) {
  const referer = Object.entries(headers || {}).find(([key]) => key.toLowerCase() === 'referer')?.[1];
  const warmURL = referer || `${url.protocol}//${url.host}/`;
  await page.goto(warmURL, { waitUntil: 'domcontentloaded', timeout: 30000 }).catch((error) => {
    console.warn(`warm origin continued after navigation issue: ${error.message}`);
  });
  await page.waitForTimeout(warmWaitMs);
  await page.evaluate(() => window.stop()).catch(() => {});
}

async function browserNavigate(page, payload) {
  await page.setExtraHTTPHeaders(payload.headers || {});
  const response = await page.goto(payload.url, { waitUntil: 'domcontentloaded', timeout: 60000 }).catch((error) => {
    console.warn(`api navigation continued after issue: ${error.message}`);
    return null;
  });
  await page.evaluate(() => window.stop()).catch(() => {});
  const body = await page.evaluate(() => document.body ? document.body.innerText : '');
  return {
    status_code: response ? response.status() : 0,
    content_type: response ? (response.headers()['content-type'] || '') : '',
    body,
  };
}

async function browserEvaluateFetch(page, payload) {
  return page.evaluate(async (requestPayload) => {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 60000);
    try {
      const options = {
        method: requestPayload.method,
        headers: requestPayload.headers,
        credentials: 'same-origin',
        signal: controller.signal,
      };
      if (requestPayload.body !== undefined && requestPayload.method !== 'GET') options.body = requestPayload.body;
      const response = await fetch(requestPayload.url, options);
      return {
        status_code: response.status,
        content_type: response.headers.get('content-type') || '',
        body: await response.text(),
      };
    } finally {
      clearTimeout(timeout);
    }
  }, payload);
}

async function browserFetch(input) {
  if (!browser) await launchBrowser();
  const url = targetURL(input);
  const headers = filteredHeaders(input.headers);
  const session = await contextFor(input.session, input.proxy_url);
  const cookies = parseCookies(cookieHeader(input.headers), url);
  if (cookies.length) await session.context.addCookies(cookies);
  if (!session.page.url().startsWith(`${url.protocol}//${url.host}`)) {
    await warmOrigin(session.page, url, headers);
  }
  const fetchPayload = {
    url: url.toString(),
    method: String(input.method || 'GET').toUpperCase(),
    headers,
    body: requestBody(input),
  };
  let result = fetchPayload.method === 'GET'
    ? await browserNavigate(session.page, fetchPayload)
    : await browserEvaluateFetch(session.page, fetchPayload);
  try {
    const parsed = JSON.parse(result.body);
    if (parsed && parsed.refresh === 1) {
      await warmOrigin(session.page, url, headers);
      result = fetchPayload.method === 'GET'
        ? await browserNavigate(session.page, fetchPayload)
        : await browserEvaluateFetch(session.page, fetchPayload);
    }
  } catch {
    // Non-JSON upstream bodies are passed through to PanSou for normal decoding errors.
  }
  await session.context.storageState({ path: session.statePath }).catch(() => {});
  return result;
}

const server = http.createServer(async (request, response) => {
  try {
    if (request.method === 'GET' && request.url === '/healthz') {
      jsonResponse(response, 200, { ok: true, engine: enginePackage });
      return;
    }
    if (request.method !== 'POST' || request.url !== '/fetch') {
      jsonResponse(response, 404, { error: 'not found' });
      return;
    }
    const input = await readJSON(request);
    const result = await browserFetch(input);
    jsonResponse(response, 200, result);
  } catch (error) {
    jsonResponse(response, 500, { error: error && error.message ? error.message : String(error) });
  }
});

process.on('SIGTERM', async () => {
  server.close();
  if (browser) await browser.close();
  process.exit(0);
});

server.listen(port, host, async () => {
  console.log(`PanSou browser gateway listening on http://${host}:${port} (${enginePackage})`);
});
