/**
 * Chaturbate top-channels autopilot — Cloudflare Worker
 * -----------------------------------------------------
 * Every 15 minutes (Cron Trigger) this worker scans Chaturbate's public
 * "female" and "couple" room listings, and keeps a healthy BUFFER of the
 * most-viewed PUBLIC rooms (above a viewer threshold) ready in the shared
 * Supabase `channel_assignments` pool (status = "unassigned"). Go DVR nodes
 * claim "unassigned" rows when CHANNEL_POOL_MODE = pooled.
 *
 * Behaviour:
 *   - Never removes channels.
 *   - Keeps ~`target_buffer` unassigned channels ready per category (auto
 *     refills as nodes claim/record them). Configurable without redeploy.
 *   - Manual removals stick: channels the worker has auto-added before are
 *     remembered in `pool_autopilot` and are NOT re-added if they later
 *     disappear from the pool.
 *   - Only PUBLIC rooms with num_users > min_viewers qualify.
 *   - Cloudflare/custom anti-bot challenge: a headless Chromium (Browser
 *     Rendering binding) loads Chaturbate, lets the challenge clear, then
 *     fetches the roomlist from inside the browser. The freshly minted
 *     clearance cookie is cached in Workers KV and reused on the next run to
 *     reduce browser launches / rate limits.
 *
 * Config (Supabase `app_settings` key `autopilot_config`, self-seeded):
 *   { min_viewers, target_buffer, categories:[{key,label}], stale_minutes }
 *
 * Env / secrets (see wrangler.toml + `wrangler secret put`):
 *   SUPABASE_URL       (var)    Supabase project URL
 *   SUPABASE_ANON_KEY  (secret) Supabase anon key (RLS allows all ops here)
 *   CB_DOMAIN          (var)    https://chaturbate.com
 *   CB_USER_AGENT      (var)    Chrome UA used to talk to Chaturbate
 *   CB_COOKIES         (secret) chaturbate cookies (optional seed)
 *   CB_ALERT_WEBHOOK   (secret, optional) webhook fired on scan failure / stale
 *   CB_SUCCESS_WEBHOOK (secret, optional) webhook fired when channels are added
 *   BROWSER            (binding) Cloudflare Browser Rendering binding
 *   AUTOPILOT          (binding) Workers KV namespace for cached session
 */

import puppeteer from '@cloudflare/puppeteer';

// ---- Defaults (used until autopilot_config exists in app_settings) ----------
const DEFAULT_CONFIG = {
  min_viewers: 5000,
  target_buffer: 10,
  categories: [
    { key: 'f', label: 'female', target_buffer: 1 },
    { key: 'c', label: 'couple', target_buffer: 2 },
  ],
  stale_minutes: 25,
};
const DEFAULT_UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 ' +
  '(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36';
const SESSION_TTL = 1800; // 30 min — cf_clearance reuse window
const CONFIG_KEY = 'autopilot_config';
const SCANS_KEY = 'autopilot_scans';
const HEARTBEAT_KEY = 'autopilot_heartbeat';
// ---- End config ------------------------------------------------------------

function jsonResponse(obj, status = 200) {
  return new Response(JSON.stringify(obj, null, 2), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

class BlockedError extends Error {}

// ---- app_settings helpers ---------------------------------------------------
function sbHeaders(env, extra = {}) {
  return {
    apikey: env.SUPABASE_ANON_KEY,
    Authorization: `Bearer ${env.SUPABASE_ANON_KEY}`,
    'Content-Type': 'application/json',
    ...extra,
  };
}

async function sbGet(env, path) {
  const resp = await fetch(`${env.SUPABASE_URL}/rest/v1/${path}`, {
    headers: sbHeaders(env),
  });
  if (!resp.ok) throw new Error(`GET ${path} -> HTTP ${resp.status} ${await resp.text()}`);
  return resp.json();
}

async function sbPost(env, path, body, extra = {}) {
  const resp = await fetch(`${env.SUPABASE_URL}/rest/v1/${path}`, {
    method: 'POST',
    headers: sbHeaders(env, extra),
    body: JSON.stringify(body),
  });
  if (!resp.ok) throw new Error(`POST ${path} -> HTTP ${resp.status} ${await resp.text()}`);
  return resp;
}

// POST an array of rows in a single request (keeps subrequest count low).
async function sbPostMany(env, path, rows, extra = {}) {
  if (!rows || rows.length === 0) return;
  await sbPost(env, path, rows, extra);
}

async function getSetting(env, key) {
  const rows = await sbGet(env, `app_settings?key=eq.${encodeURIComponent(key)}&select=value&limit=1`);
  if (Array.isArray(rows) && rows[0]) return rows[0].value;
  return null;
}

async function setSetting(env, key, value) {
  await sbPost(
    env,
    'app_settings?on_conflict=key',
    { key, value },
    { Prefer: 'resolution=merge-duplicates' }
  );
}

async function loadConfig(env) {
  let cfg = null;
  try {
    cfg = await getSetting(env, CONFIG_KEY);
  } catch (e) {
    console.error('loadConfig failed, using defaults:', e.message);
  }
  if (!cfg || typeof cfg !== 'object' || !Array.isArray(cfg.categories)) {
    cfg = JSON.parse(JSON.stringify(DEFAULT_CONFIG));
    try {
      await setSetting(env, CONFIG_KEY, cfg); // self-seed so it's editable
    } catch (e) {
      console.error('self-seed config failed:', e.message);
    }
  }
  cfg.min_viewers = Number(cfg.min_viewers) || DEFAULT_CONFIG.min_viewers;
  cfg.target_buffer = Number(cfg.target_buffer) || DEFAULT_CONFIG.target_buffer;
  cfg.stale_minutes = Number(cfg.stale_minutes) || DEFAULT_CONFIG.stale_minutes;
  if (!Array.isArray(cfg.categories) || cfg.categories.length === 0) {
    cfg.categories = DEFAULT_CONFIG.categories;
  }
  // Per-category buffer size (fall back to the global default).
  for (const cat of cfg.categories) {
    cat.target_buffer = Number(cat.target_buffer) || cfg.target_buffer || 10;
  }
  return cfg;
}

// ---- Batched Supabase pool helpers ------------------------------------------
function inList(usernames) {
  return 'in.(' + usernames.map((u) => encodeURIComponent(u)).join(',') + ')';
}

async function batchInPool(env, usernames) {
  if (usernames.length === 0) return new Set();
  const rows = await sbGet(
    env,
    `channel_assignments?username=${inList(usernames)}&site=eq.chaturbate&select=username`
  );
  return new Set((rows || []).map((r) => r.username));
}

async function batchAutoAdded(env, usernames) {
  if (usernames.length === 0) return new Set();
  const rows = await sbGet(env, `pool_autopilot?username=${inList(usernames)}&select=username`);
  return new Set((rows || []).map((r) => r.username));
}

async function batchInChannels(env, usernames) {
  if (usernames.length === 0) return new Set();
  const rows = await sbGet(env, `channels?username=${inList(usernames)}&select=username`);
  return new Set((rows || []).map((r) => r.username));
}

/**
 * Count unassigned chaturbate rows that the worker itself added, grouped by
 * gender (gender lives on `pool_autopilot`). This drives the per-category
 * buffer: deficit = target_buffer - autoUnassigned[gender].
 */
async function countAutoUnassigned(env) {
  const unassigned = await sbGet(
    env,
    `channel_assignments?site=eq.chaturbate&status=eq.unassigned&select=username`
  );
  const autopilot = await sbGet(env, `pool_autopilot?select=username,gender`);
  const genderByUser = {};
  for (const a of autopilot || []) {
    if (a.username && a.gender) genderByUser[a.username] = a.gender;
  }
  const autoByGender = {};
  for (const row of unassigned || []) {
    const g = genderByUser[row.username];
    if (g) autoByGender[g] = (autoByGender[g] || 0) + 1;
  }
  return autoByGender;
}

async function logMany(env, entries) {
  if (!entries.length) return;
  try {
    await sbPost(env, 'channel_logs', entries);
  } catch (e) {
    console.error('logMany failed:', e.message);
  }
}

// ---- Chaturbate fetch -------------------------------------------------------
function topRooms(data, minViewers) {
  const rooms = Array.isArray(data?.rooms) ? data.rooms : [];
  return rooms
    .filter((r) => (r.current_show || r.room_status) === 'public' && (r.num_users ?? 0) > minViewers)
    .sort((a, b) => (b.num_users ?? 0) - (a.num_users ?? 0));
}

function roomListUrl(genderKey, env) {
  return (
    `${env.CB_DOMAIN}/api/ts/roomlist/room-list/` +
    `?enable_recommendations=false&genders=${genderKey}&limit=100`
  );
}

/** Fetch one category's roomlist from inside the (already-challenged) page. */
async function fetchRoomList(page, genderKey, env) {
  const apiUrl = roomListUrl(genderKey, env);
  return await page.evaluate(async (url) => {
    for (let attempt = 0; attempt < 15; attempt++) {
      try {
        const r = await fetch(url, {
          headers: {
            Accept: 'application/json, text/javascript, */*; q=0.01',
            'X-Requested-With': 'XMLHttpRequest',
          },
        });
        const text = await r.text();
        if (r.ok && text.trim().startsWith('{')) return JSON.parse(text);
      } catch (_) {
        /* retry */
      }
      await new Promise((res) => setTimeout(res, 2000));
    }
    throw new Error('challenge not cleared / no JSON after retries');
  }, apiUrl);
}

// ---- KV session cache -------------------------------------------------------
async function loadCachedSession(env) {
  try {
    if (!env.AUTOPILOT) return null;
    return await env.AUTOPILOT.get('cb_session', { type: 'json' });
  } catch (_) {
    return null;
  }
}

async function saveCachedSession(env, cookies) {
  try {
    if (!env.AUTOPILOT || !cookies) return;
    await env.AUTOPILOT.put('cb_session', JSON.stringify({ cookies, saved_at: Date.now() }), {
      expirationTtl: SESSION_TTL,
    });
  } catch (e) {
    console.error('saveCachedSession failed:', e.message);
  }
}

// ---- Heartbeat / stale / metrics -------------------------------------------
async function writeHeartbeat(env, method) {
  try {
    await setSetting(env, HEARTBEAT_KEY, {
      last_success_at: new Date().toISOString(),
      method,
    });
  } catch (e) {
    console.error('writeHeartbeat failed:', e.message);
  }
}

async function checkStale(env, cfg) {
  try {
    const hb = await getSetting(env, HEARTBEAT_KEY);
    if (!hb || !hb.last_success_at) return;
    const ageMin = (Date.now() - new Date(hb.last_success_at).getTime()) / 60000;
    if (ageMin > cfg.stale_minutes) {
      await alert(
        env,
        `autopilot heartbeat stale: last successful scan ${ageMin.toFixed(0)} min ago ` +
          `(threshold ${cfg.stale_minutes} min)`
      );
    }
  } catch (e) {
    console.error('checkStale failed:', e.message);
  }
}

async function recordScan(env, scanRow) {
  try {
    const prev = (await getSetting(env, SCANS_KEY)) || [];
    const arr = Array.isArray(prev) ? prev : [];
    arr.unshift(scanRow);
    if (arr.length > 50) arr.length = 50;
    await setSetting(env, SCANS_KEY, arr);
  } catch (e) {
    console.error('recordScan failed:', e.message);
  }
}

async function alert(env, message) {
  if (!env.CB_ALERT_WEBHOOK) return;
  try {
    await fetch(env.CB_ALERT_WEBHOOK, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: message, level: 'error', source: 'chaturbate-autopilot' }),
    });
  } catch (e) {
    console.error('alert webhook failed:', e.message);
  }
}

async function successAlert(env, added) {
  if (!env.CB_SUCCESS_WEBHOOK || !added || added.length === 0) return;
  try {
    await fetch(env.CB_SUCCESS_WEBHOOK, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: 'autopilot added channels', added, source: 'chaturbate-autopilot' }),
    });
  } catch (e) {
    console.error('success webhook failed:', e.message);
  }
}

// ---- Main scan --------------------------------------------------------------
async function runScan(env, dryrun = false) {
  const cfg = await loadConfig(env);
  await checkStale(env, cfg); // alert if previous successful scan was too long ago

  const result = {
    v: 3,
    config: { min_viewers: cfg.min_viewers, target_buffer: cfg.target_buffer },
    method: env.BROWSER ? 'browser' : 'direct',
    female: [],
    couple: [],
    added: [],
    skipped: [],
    blocked: false,
    errors: [],
  };

  const startedAt = Date.now();
  let browser = null;
  if (env.BROWSER) {
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        browser = await puppeteer.launch(env.BROWSER);
        break;
      } catch (e) {
        const msg = `browser launch attempt ${attempt + 1} failed: ${e.message}`;
        result.errors.push(msg);
        await logMany(env, [{ username: '__autopilot__', log_level: 'error', message: msg }]);
        if (attempt < 2) await new Promise((r) => setTimeout(r, 8000 * (attempt + 1)));
      }
    }
    if (!browser) {
      result.errors.push('all browser launch attempts failed; direct fetch fallback will be challenged');
    }
  }

  try {
    const autoByGender = await countAutoUnassigned(env).catch((e) => {
      result.errors.push(`countAutoUnassigned failed: ${e.message}`);
      return {};
    });

    // Gather candidates per category (capped at the per-category deficit).
    const candidates = []; // {category, label, username, num_users}
    let page = null;
    if (browser) {
      page = await browser.newPage();
      await page.setUserAgent(env.CB_USER_AGENT || DEFAULT_UA);

      // Free-plan Workers allow only 50 external subrequests/invocation. The
      // Chaturbate homepage pulls dozens of images/scripts/fonts, so block
      // everything except the document, challenge scripts, and our XHR/fetch.
      await page.setRequestInterception(true);
      page.on('request', (req) => {
        const t = req.resourceType();
        if (t === 'document' || t === 'script' || t === 'xhr' || t === 'fetch') {
          req.continue();
        } else {
          req.abort().catch(() => {});
        }
      });

      const cached = await loadCachedSession(env);
      if (cached?.cookies?.length) {
        try {
          await page.setCookie(...cached.cookies);
        } catch (_) {
          /* ignore bad cookies, page will re-solve */
        }
      }
      await page
        .goto(env.CB_DOMAIN + '/', { waitUntil: 'domcontentloaded', timeout: 30000 })
        .catch(() => {});
    }

    for (const cat of cfg.categories) {
      const deficit = Math.max(0, cat.target_buffer - (autoByGender[cat.label] || 0));
      let top = [];
      try {
        const data = browser
          ? await fetchRoomList(page, cat.key, env)
          : await chaturbateFetchDirect(roomListUrl(cat.key, env), env);
        top = topRooms(data, cfg.min_viewers);
      } catch (e) {
        result.blocked = result.blocked || e instanceof BlockedError;
        const msg = `autopilot scan failed for ${cat.label}: ${e.message}`;
        result.errors.push(msg);
        await logMany(env, [{ username: '__autopilot__', log_level: 'error', message: msg }]);
        await alert(env, msg);
        continue;
      }

      const capped = top.slice(0, deficit);
      result[cat.label] = capped.map((r) => ({ username: r.username, num_users: r.num_users }));
      for (const r of capped) {
        candidates.push({ category: cat.label, username: r.username, num_users: r.num_users });
      }
    }

    // Batch dedup: one query per source for ALL candidates at once.
    const usernames = candidates.map((c) => c.username);
    const [inPoolSet, autoAddedSet, channelsSet] = await Promise.all([
      batchInPool(env, usernames),
      batchAutoAdded(env, usernames),
      batchInChannels(env, usernames),
    ]).catch((e) => {
      result.errors.push(`batch dedup failed: ${e.message}`);
      return [new Set(), new Set(), new Set()];
    });

    const addRows = []; // channel_assignments
    const autoRows = []; // pool_autopilot
    const logRows = [];
    for (const c of candidates) {
      let reason = null;
      if (inPoolSet.has(c.username)) reason = 'already in pool / claimed by a node';
      else if (autoAddedSet.has(c.username)) reason = 'previously auto-added then removed (blocklist)';
      else if (channelsSet.has(c.username)) reason = 'exists in isolated channels table';

      if (reason) {
        result.skipped.push({ category: c.category, username: c.username, reason });
        continue;
      }

      if (dryrun) {
        result.added.push({ category: c.category, username: c.username, num_users: c.num_users, dryrun: true });
        continue;
      }

      addRows.push({
        username: c.username,
        site: 'chaturbate',
        status: 'unassigned',
        resolution: 2160,
        framerate: 60,
      });
      autoRows.push({
        username: c.username,
        gender: c.category,
        viewers: c.num_users,
        updated_at: new Date().toISOString(),
      });
      logRows.push({
        username: c.username,
        log_level: 'info',
        message: `autopilot: added ${c.category} ${c.username} (${c.num_users} viewers)`,
      });
      result.added.push({ category: c.category, username: c.username, num_users: c.num_users });
    }

    if (!dryrun) {
      try {
        await sbPostMany(
          env,
          'channel_assignments?on_conflict=username,site',
          addRows,
          { Prefer: 'resolution=ignore-duplicates' }
        );
        await sbPostMany(
          env,
          'pool_autopilot?on_conflict=username',
          autoRows,
          { Prefer: 'resolution=merge-duplicates' }
        );
        await logMany(env, logRows);
      } catch (e) {
        result.errors.push(`batch write failed: ${e.message}`);
      }
    }

    // Cache the freshly minted session for the next run.
    if (page) {
      try {
        const cookies = await page.cookies();
        const cb = cookies.filter((c) => (c.domain || '').includes('chaturbate'));
        if (cb.length) await saveCachedSession(env, cb);
      } catch (e) {
        console.error('cookie extract failed:', e.message);
      }
    }

    const success = !result.blocked && result.errors.length === 0;
    if (success && !dryrun) {
      await writeHeartbeat(env, result.method);
      await successAlert(env, result.added);
    }
  } finally {
    if (browser) {
      try {
        await browser.close();
      } catch (_) {
        /* ignore */
      }
    }
  }

  result.duration_ms = Date.now() - startedAt;
  await recordScan(env, {
    ran_at: new Date().toISOString(),
    method: result.method,
    min_viewers: cfg.min_viewers,
    target_buffer: cfg.target_buffer,
    found: { female: result.female.length, couple: result.couple.length },
    added: result.added,
    skipped: result.skipped,
    blocked: result.blocked,
    errors: result.errors,
    duration_ms: result.duration_ms,
  });

  return result;
}

// Direct fetch (fallback only; will normally be challenged without a browser).
async function chaturbateFetchDirect(url, env) {
  const headers = {
    'User-Agent': env.CB_USER_AGENT || DEFAULT_UA,
    Referer: env.CB_DOMAIN + '/',
    Origin: env.CB_DOMAIN,
    Accept: 'application/json, text/javascript, */*; q=0.01',
    'Accept-Language': 'en-US,en;q=0.9',
  };
  if (env.CB_COOKIES) headers['Cookie'] = env.CB_COOKIES;
  const resp = await fetch(url, { headers, redirect: 'follow' });
  const ct = resp.headers.get('content-type') || '';
  if (resp.status >= 400 || ct.includes('text/html')) {
    let snippet = '';
    try {
      snippet = (await resp.text()).slice(0, 200);
    } catch (_) {
      /* ignore */
    }
    throw new BlockedError(`HTTP ${resp.status} (${ct || 'unknown'}): ${snippet}`);
  }
  return resp.json();
}

// ---- Health check (separate cron) ------------------------------------------
async function healthCheck(env) {
  const cfg = await loadConfig(env).catch(() => DEFAULT_CONFIG);
  await checkStale(env, cfg);
}

export default {
  async scheduled(event, env, ctx) {
    if (event.cron === '*/20 * * * *') {
      ctx.waitUntil(healthCheck(env));
      return;
    }
    ctx.waitUntil(runScan(env).then((r) => console.log('autopilot scan complete', JSON.stringify(r))));
  },

  async fetch(request, env, _ctx) {
    const url = new URL(request.url);
    const dryrun =
      url.searchParams.get('dryrun') === '1' || url.searchParams.get('dryrun') === 'true';
    if (url.pathname === '/health') {
      const hb = await getSetting(env, HEARTBEAT_KEY).catch(() => null);
      const ageMin = hb?.last_success_at
        ? Math.round((Date.now() - new Date(hb.last_success_at).getTime()) / 60000)
        : null;
      return jsonResponse({ ok: !!hb, last_success_at: hb?.last_success_at || null, age_min: ageMin });
    }
    try {
      const result = await runScan(env, dryrun);
      return jsonResponse(result);
    } catch (e) {
      return jsonResponse({ error: e.message }, 500);
    }
  },
};
