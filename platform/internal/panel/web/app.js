// 篝火面板前端：单文件、无构建步骤。
'use strict';

const state = {
  tab: 'overview',
  clusters: [],
  active: '',
  mods: [],
  detail: null,
  catalog: { groups: [], playstyles: [], presets: [] },
  wizard: null,
  worldgenShard: 'Master',
};

// ---------- 基础工具 ----------
async function api(path, opts = {}) {
  const res = await fetch(path, {
    method: opts.body ? 'POST' : 'GET',
    headers: opts.body ? { 'Content-Type': 'application/json' } : {},
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = { error: text }; }
  if (!res.ok || (data && data.error)) throw new Error((data && data.error) || res.statusText);
  return data;
}
const el = (id) => document.getElementById(id);
const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const fmtBytes = (n) => {
  n = Number(n) || 0;
  if (n >= 1073741824) return (n / 1073741824).toFixed(2) + ' GB';
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
  if (n >= 1024) return (n / 1024).toFixed(0) + ' KB';
  return n + ' B';
};
const fmtDate = (ts) => {
  if (!ts) return '';
  const d = new Date(Number(ts) * 1000);
  const p = (x) => String(x).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
};
const modeName = (m) => ({ survival: '生存', endless: '无尽', wilderness: '荒野', relaxed: '轻松', lightsout: '暗无天日' }[m] || m || '生存');

function toast(msg, isErr) {
  const box = document.createElement('div');
  box.className = 'toast' + (isErr ? ' err' : '');
  box.textContent = msg;
  document.body.appendChild(box);
  setTimeout(() => box.remove(), isErr ? 6000 : 3000);
}

function setAgent(stateText, cls) {
  const p = el('agentState');
  p.textContent = stateText;
  p.className = 'pill ' + (cls || '');
}

// 轮询任务日志
function watchJob(job, title) {
  const modal = el('modal');
  el('modalTitle').textContent = title || ('任务 ' + job.id);
  el('modalLog').textContent = (job.log || []).join('\n');
  modal.classList.remove('hidden');
  const timer = setInterval(async () => {
    try {
      const j = await api('/api/jobs/' + job.id);
      el('modalLog').textContent = (j.log || []).join('\n');
      el('modalLog').scrollTop = el('modalLog').scrollHeight;
      if (j.state !== 'running') {
        clearInterval(timer);
        el('modalTitle').textContent = (j.state === 'done' ? '完成 · ' : '失败 · ') + (title || '');
        if (j.error) el('modalLog').textContent += '\n\n错误: ' + j.error;
        if (j.state === 'done') toast('操作完成');
        else toast(j.error || '操作失败', true);
        refreshAfterJob();
      }
    } catch (e) { clearInterval(timer); toast(e.message, true); }
  }, 900);
}

function refreshAfterJob() {
  const map = { overview: loadOverview, worlds: loadWorlds, mods: loadMods, worldgen: loadWorldgen, logs: loadLogs };
  (map[state.tab] || loadOverview)();
  loadClusterSwitch();
}

// ---------- 世界切换 ----------
async function loadClusterSwitch() {
  try {
    const data = await api('/api/clusters');
    state.clusters = data.clusters || [];
    state.active = data.active || (state.clusters[0] || {}).name || '';
    const sel = el('clusterSwitch');
    sel.innerHTML = state.clusters.map((c) =>
      `<option value="${esc(c.name)}"${c.name === state.active ? ' selected' : ''}>${esc(c.title || c.name)}${c.running ? ' · 运行中' : ''}</option>`).join('');
    setAgent('Agent 在线', 'ok');
  } catch (e) {
    setAgent('Agent 离线', 'err');
  }
}

el('clusterSwitch').addEventListener('change', async (ev) => {
  const name = ev.target.value;
  try {
    await api('/api/clusters/activate', { body: { name } });
    state.active = name;
    toast('已切换到世界 ' + name);
    refreshAfterJob();
  } catch (e) { toast(e.message, true); }
});

// ---------- 概览 ----------
async function loadOverview() {
  const page = el('page-overview');
  try {
    const st = await api('/api/overview');
    const shards = (st.shards || []).map((s) => `
      <div class="card shard ${s.active ? 'on' : 'off'}">
        <div class="shard-name">${esc(s.name)} · ${esc(s.service)}</div>
        <div class="shard-state">${s.active ? '运行中' : '已停止'}</div>
        <div class="muted">端口 ${esc(s.port)} · 重启 ${esc(s.restarts)} 次</div>
      </div>`).join('');
    const memPct = st.mem_total_kb ? Math.round((st.mem_used_kb / st.mem_total_kb) * 100) : 0;
    const diskPct = st.disk_total_kb ? Math.round((st.disk_used_kb / st.disk_total_kb) * 100) : 0;
    page.innerHTML = `
      <div class="grid2">${shards}</div>
      <div class="grid4" style="margin-top:14px">
        <div class="card"><div class="label">内存</div>
          <div class="bar"><i style="width:${memPct}%"></i></div>
          <div class="muted">${(st.mem_used_kb / 1048576).toFixed(1)} GB / ${(st.mem_total_kb / 1048576).toFixed(1)} GB（${memPct}%）· swap ${(st.swap_total_kb / 1048576).toFixed(1)} GB</div></div>
        <div class="card"><div class="label">磁盘</div>
          <div class="bar"><i style="width:${diskPct}%"></i></div>
          <div class="muted">${(st.disk_used_kb / 1048576).toFixed(1)} GB / ${(st.disk_total_kb / 1048576).toFixed(1)} GB（${diskPct}%）</div></div>
        <div class="card"><div class="label">模组</div><div class="big">${st.mods_enabled} / ${st.mods_installed}</div><div class="muted">已启用 / 已安装</div></div>
        <div class="card"><div class="label">玩家</div><div class="big">${st.players_online ? '有人在线' : '空服暂停'}</div>
          <div class="muted">世界 ${esc(st.cluster)} · build ${esc(st.game_version)} · Agent ${esc(st.agent_version)}</div></div>
      </div>
      <div class="row" style="margin-top:14px">
        <button class="btn" id="btnRefreshOverview">刷新</button>
        <button class="btn danger" id="btnRestartAll">重启当前世界</button>
      </div>`;
    el('btnRefreshOverview').onclick = loadOverview;
    el('btnRestartAll').onclick = async () => {
      const job = await api('/api/service/restart', { body: { cluster: state.active } });
      watchJob(job, '重启世界');
    };
  } catch (e) {
    page.innerHTML = `<div class="card err">读取概览失败：${esc(e.message)}</div>`;
  }
}

// ---------- 世界列表 ----------
async function loadWorlds() {
  const page = el('page-worlds');
  try {
    const data = await api('/api/clusters');
    state.clusters = data.clusters || [];
    state.active = data.active || state.active;
    const rows = state.clusters.map((c) => `
      <div class="card world ${c.active ? 'active' : ''}">
        <div class="world-icon">${c.caves ? '🏔️' : '🌲'}</div>
        <div class="world-main">
          <div class="world-title">${esc(c.title || c.name)} ${c.active ? '<span class="tag">当前</span>' : ''}</div>
          <div class="muted">目录 ${esc(c.name)} · ${esc(c.game_mode_name)} · 最多 ${c.max_players} 人 · ${c.caves ? '森林和洞穴' : '仅森林'}${c.pvp ? ' · PvP' : ''}${c.password ? ' · 有密码' : ' · 无密码'}</div>
          <div class="muted">端口 ${c.master_port}${c.caves ? '/' + c.caves_port : ''} · Steam ${c.steam_port || '-'}/${c.auth_port || '-'}</div>
          <div class="muted">${c.shards.map((s) => `${esc(s.name)}:${s.active ? '运行' : '停止'}`).join(' · ')}</div>
        </div>
        <div class="world-actions">
          ${c.running
            ? `<button class="btn" data-stop="${esc(c.name)}">停止</button>`
            : `<button class="btn primary" data-start="${esc(c.name)}">启动</button>`}
          ${c.active ? '' : `<button class="btn" data-activate="${esc(c.name)}">设为当前</button>`}
          <button class="btn danger" data-remove="${esc(c.name)}">删除</button>
        </div>
      </div>`).join('');
    page.innerHTML = `
      <div class="row"><button class="btn primary big" id="btnNewWorld">＋ 创建新世界</button>
        <span class="muted">同一时间只运行一个世界（2C4G 内存只够一份）；启动新世界会自动停掉其它世界。</span></div>
      <div class="list" style="margin-top:14px">${rows || '<div class="card muted">还没有世界</div>'}</div>`;
    el('btnNewWorld').onclick = openWizard;
    page.querySelectorAll('[data-start]').forEach((b) => b.onclick = async () => {
      const job = await api('/api/clusters/start', { body: { name: b.dataset.start } });
      watchJob(job, '启动 ' + b.dataset.start);
    });
    page.querySelectorAll('[data-stop]').forEach((b) => b.onclick = async () => {
      const job = await api('/api/clusters/stop', { body: { name: b.dataset.stop } });
      watchJob(job, '停止 ' + b.dataset.stop);
    });
    page.querySelectorAll('[data-activate]').forEach((b) => b.onclick = async () => {
      try {
        await api('/api/clusters/activate', { body: { name: b.dataset.activate } });
        toast('已切换到 ' + b.dataset.activate);
        state.active = b.dataset.activate;
        loadClusterSwitch(); loadWorlds();
      } catch (e) { toast(e.message, true); }
    });
    page.querySelectorAll('[data-remove]').forEach((b) => b.onclick = async () => {
      if (!confirm('删除世界 ' + b.dataset.remove + '？会先备份存档到 /home/dst/backups/。')) return;
      const job = await api('/api/clusters/remove', { body: { name: b.dataset.remove } });
      watchJob(job, '删除 ' + b.dataset.remove);
    });
  } catch (e) {
    page.innerHTML = `<div class="card err">读取世界列表失败：${esc(e.message)}</div>`;
  }
}

// ---------- 创建世界向导 ----------
async function openWizard() {
  if (!state.catalog.groups || state.catalog.groups.length === 0) {
    try {
      const wg = await api('/api/worldgen?shard=Master');
      state.catalog = wg.catalog || { groups: [] };
    } catch (e) { /* 目录拿不到也能创建，只是没有世界规则可调 */ }
  }
  state.wizard = {
    step: 'settings',
    name: 'MyWorld',
    game_mode: 'survival',
    description: '',
    password: '',
    max_players: 6,
    pvp: false,
    caves: true,
    master_preset: 'SURVIVAL_TOGETHER',
    caves_preset: 'DST_CAVE',
    master_overrides: {},
    caves_overrides: {},
    modes: [['relaxed', '轻松'], ['endless', '无尽'], ['survival', '生存'], ['wilderness', '荒野'], ['lightsout', '暗无天日']],
  };
  renderWizard();
}

function renderWizard() {
  const w = state.wizard;
  const drawer = el('drawer');
  const body = el('drawerBody');
  const steps = [['settings', '设置'], ['master', '地上'], ['caves', '洞穴'], ['create', '创建']];
  const tabs = steps.map(([k, label]) =>
    `<button class="wt ${w.step === k ? 'on' : ''}" data-wstep="${k}">${label}</button>`).join('');
  let inner = '';
  if (w.step === 'settings') inner = wizardSettings(w);
  else if (w.step === 'master') inner = wizardWorldSettings(w, 'forest', 'master_overrides', w.master_preset, 'master_preset');
  else if (w.step === 'caves') inner = wizardWorldSettings(w, 'cave', 'caves_overrides', w.caves_preset, 'caves_preset');
  else inner = wizardConfirm(w);
  body.innerHTML = `
    <div class="drawer-head"><h2>创建新世界</h2><button class="btn" data-close>关闭</button></div>
    <div class="wtabs">${tabs}</div>
    <div class="wizard-body">${inner}</div>
    <div class="wizard-foot">
      <button class="btn" data-close>返回</button>
      <span class="spacer"></span>
      ${w.step === 'create' ? '<button class="btn primary" id="btnDoCreate">创建世界</button>'
        : '<button class="btn primary" id="btnNextStep">下一步</button>'}
    </div>`;
  drawer.classList.remove('hidden');
  bindWizard();
}

function wizardSettings(w) {
  return `
    <div class="form">
      <label>世界名称（也用作服务器目录名）<input id="w-name" value="${esc(w.name)}" maxlength="20"></label>
      <label>描述<input id="w-desc" value="${esc(w.description)}" placeholder="朋友在列表里看到的说明"></label>
      <label>游戏风格
        <select id="w-mode">${w.modes.map(([k, n]) => `<option value="${k}"${w.game_mode === k ? ' selected' : ''}>${n}</option>`).join('')}</select>
      </label>
      <label>最多玩家<input id="w-players" type="number" min="2" max="16" value="${w.max_players}"></label>
      <label class="inline"><input id="w-pvp" type="checkbox"${w.pvp ? ' checked' : ''}> 开启 PvP</label>
      <label class="inline"><input id="w-caves" type="checkbox"${w.caves ? ' checked' : ''}> 同时开洞穴分片</label>
      <label>密码（可留空）<input id="w-pass" value="${esc(w.password)}"></label>
      <p class="muted">游戏风格会决定默认的世界规则（轻松/无尽/荒野/暗无天日各有预设），下一步可以逐项覆盖。</p>
    </div>`;
}

// ---------- 世界设置目录：先按分片、再按阶段区分 ----------
// 分片区分（agent 已按游戏 customize.lua 的 world 字段解析成 scope/shards）：
//   forest → 只在“地上世界”页出现
//   cave   → 只在“洞穴”页出现
//   both   → 两页都出现，各写各的分片配置文件
//   global → 全局规则：由地上（森林）分片主控，运行时自动同步到洞穴，所以只在地上页显示
// 阶段区分：generation = 世界生成（生成后永久锁定）；rules = 世界规则（运行时可改）
function settingAppliesToWorld(s, world) {
  if (s.shards && s.shards.length) return s.shards.includes(world);
  const ws = s.worlds || [];
  if (ws.length === 0) return world === 'forest';
  return ws.includes(world);
}

function settingSections(catalog, world) {
  const out = { generation: [], rules: [], global: [] };
  for (const g of (catalog.groups || [])) {
    const items = (g.settings || []).filter((s) => settingAppliesToWorld(s, world));
    if (!items.length) continue;
    if (g.kind === 'worldgen') { out.generation.push({ name: g.name, items }); continue; }
    const own = [], glob = [];
    for (const s of items) (s.scope === 'global' ? glob : own).push(s);
    if (own.length) out.rules.push({ name: g.name, items: own });
    if (glob.length) out.global.push({ name: g.name, items: glob });
  }
  return out;
}

function settingFieldHtml(s, cur, attrs) {
  if ((s.choices || []).length) {
    return `<select ${attrs}><option value=""${cur === '' ? ' selected' : ''}>默认</option>${
      (s.choices || []).map((c) => `<option value="${esc(c.value)}"${cur === c.value ? ' selected' : ''}>${esc(c.label)}</option>`).join('')}</select>`;
  }
  return `<input ${attrs} value="${esc(cur)}" placeholder="默认">`;
}

function settingGroupsHtml(groupList, overrides, attrsOf) {
  let html = '';
  for (const g of groupList) {
    html += `<div class="wgroup"><h4>${esc(g.name)}</h4><div class="wgrid">`;
    for (const s of g.items) {
      const cur = (overrides || {})[s.key] || '';
      const tag = s.scope === 'global' ? ' <span class="tag">全局</span>' : '';
      html += `<label class="witem" title="${esc(s.hover || '')}"><span>${esc(s.label)}${tag}</span>${settingFieldHtml(s, cur, attrsOf(s))}</label>`;
    }
    html += '</div></div>';
  }
  return html;
}

function settingPanelsHtml(sec, overrides, attrsOf) {
  const panels = [];
  if (sec.generation.length) {
    panels.push(`<div class="card"><h3>世界生成设置 <span class="muted">· 生成后锁定</span></h3>
      <p class="muted">只在创建 / 重新生成世界时读取一次；生成之后永久锁定，游戏内改不了（要改只能重新生成）。</p>
      ${settingGroupsHtml(sec.generation, overrides, attrsOf)}</div>`);
  }
  if (sec.rules.length) {
    panels.push(`<div class="card"><h3>世界规则设置 <span class="muted">· 运行时可改</span></h3>
      <p class="muted">运行时规则，保存后由游戏在读档时套用；不涉及地形，改完不需要重新生成世界。</p>
      ${settingGroupsHtml(sec.rules, overrides, attrsOf)}</div>`);
  }
  if (sec.global.length) {
    panels.push(`<div class="card"><h3>全局规则 <span class="muted">· 森林主控 · 自动同步到洞穴</span></h3>
      <p class="muted">这一组是跨分片的全局规则（季节/昼夜/死亡复活/节日/坎普斯等）：只有森林（地上）分片能设置，运行时会自动同步给洞穴，所以只在这里出现 —— 改这里等于两片一起改。</p>
      ${settingGroupsHtml(sec.global, overrides, attrsOf)}</div>`);
  }
  if (!panels.length) panels.push('<div class="card"><div class="muted">这个分片没有可调项。</div></div>');
  return panels.join('');
}

function wizardWorldSettings(w, world, ovKey, preset, presetKey) {
  const isCave = world === 'cave';
  // 预设按分片区分：地上页只有地上预设，洞穴页只有洞穴预设（cave 标记由 agent 下发）。
  const presetList = (state.catalog.presets || []).filter((p) => (isCave ? !!p.cave : !p.cave));
  const sec = settingSections(state.catalog, world);
  const overrides = w[ovKey] || {};
  const head = `<div class="card"><div class="row"><label class="muted">${isCave ? '洞穴预设' : '地上预设'}
      <select id="w-preset">${presetList.map((p) => `<option value="${esc(p.id)}"${p.id === preset ? ' selected' : ''}>${esc(p.name)}</option>`).join('')}</select></label>
      <span class="muted">${isCave ? '只影响洞穴分片' : '只影响地上世界'}</span></div>
    <p class="muted">预设只填充${isCave ? '洞穴这一片' : '地上这一片'}的默认参数；下面可以逐项覆盖，不会影响另一个分片。</p></div>`;
  return head + settingPanelsHtml(sec, overrides, (s) => `data-ov="${esc(ovKey)}" data-key="${esc(s.key)}"`);
}

function wizardConfirm(w) {
  const cavesText = w.caves ? '<span class="ok">开</span>' : '不开';
  return `<div class="form">
    <p>即将创建世界 <b>${esc(w.name)}</b>：</p>
    <ul class="muted">
      <li>游戏风格：${esc(modeName(w.game_mode))}（预设 ${esc(w.master_preset)}${w.caves ? ' / 洞穴 ' + esc(w.caves_preset) : ''}）</li>
      <li>人数：${w.max_players}，PvP：${w.pvp ? '是' : '否'}，密码：${w.password ? '有' : '无'}</li>
      <li>洞穴分片：${cavesText}</li>
      <li>世界规则覆盖：地上 ${Object.keys(w.master_overrides).length} 项${w.caves ? '，洞穴 ' + Object.keys(w.caves_overrides).length + ' 项' : ''}</li>
      <li>会把当前启用中的模组同步到新世界（新世界第一次启动会生成世界，约 1–3 分钟）。</li>
    </ul></div>`;
}

function bindWizard() {
  const w = state.wizard;
  const body = el('drawerBody');
  body.querySelectorAll('[data-wstep]').forEach((b) => b.onclick = () => { grabWizardInputs(); w.step = b.dataset.wstep; renderWizard(); });
  body.querySelectorAll('[data-close]').forEach((b) => b.onclick = () => el('drawer').classList.add('hidden'));
  body.querySelectorAll('[data-ov]').forEach((sel) => sel.onchange = () => {
    const store = w[sel.dataset.ov] || (w[sel.dataset.ov] = {});
    if (sel.value) store[sel.dataset.key] = sel.value; else delete store[sel.dataset.key];
  });
  const next = el('btnNextStep');
  if (next) next.onclick = () => {
    grabWizardInputs();
    const order = ['settings', 'master', 'caves', 'create'];
    const i = order.indexOf(w.step);
    w.step = order[Math.min(i + 1, order.length - 1)];
    renderWizard();
  };
  const create = el('btnDoCreate');
  if (create) create.onclick = doCreateWorld;
}

function grabWizardInputs() {
  const w = state.wizard;
  const v = (id) => { const e = el(id); return e ? e.value : undefined; };
  const b = (id) => { const e = el(id); return e ? e.checked : undefined; };
  if (v('w-name') !== undefined) w.name = v('w-name').trim();
  if (v('w-desc') !== undefined) w.description = v('w-desc');
  if (v('w-mode') !== undefined) w.game_mode = v('w-mode');
  if (v('w-players') !== undefined) w.max_players = Number(v('w-players')) || 6;
  if (b('w-pvp') !== undefined) w.pvp = b('w-pvp');
  if (b('w-caves') !== undefined) w.caves = b('w-caves');
  if (v('w-pass') !== undefined) w.password = v('w-pass');
  if (v('w-preset') !== undefined) {
    if (w.step === 'master') w.master_preset = v('w-preset');
    if (w.step === 'caves') w.caves_preset = v('w-preset');
  }
}

async function doCreateWorld() {
  const w = state.wizard;
  grabWizardInputs();
  const btn = el('btnDoCreate');
  if (btn) btn.disabled = true;
  try {
    const job = await api('/api/clusters/create', {
      body: {
        name: w.name, game_mode: w.game_mode, description: w.description, password: w.password,
        max_players: w.max_players, pvp: w.pvp, caves: w.caves,
        master_preset: w.master_preset, caves_preset: w.caves_preset,
        master_overrides: w.master_overrides || {}, caves_overrides: w.caves_overrides || {},
        start: true, copy_mods: true,
      },
    });
    el('drawer').classList.add('hidden');
    watchJob(job, '创建世界 ' + w.name);
  } catch (e) {
    toast(e.message, true);
    if (btn) btn.disabled = false;
  }
}

// ---------- 模组页 ----------
async function loadMods() {
  const page = el('page-mods');
  try {
    const data = await api('/api/mods?cluster=' + encodeURIComponent(state.active));
    state.mods = data.mods || [];
    const cards = state.mods.map((m) => `
      <div class="card mod">
        <div class="mod-thumb">${m.preview_url
          ? `<img loading="lazy" src="/api/image?url=${encodeURIComponent(m.preview_url)}" alt="">`
          : '<span class="muted">无图</span>'}</div>
        <div class="mod-main">
          <div class="mod-title">${esc(m.name || m.workshop_title || ('workshop-' + m.id))}
            ${m.needs_update ? '<span class="tag warn">可更新</span>' : ''}</div>
          <div class="muted">workshop-${esc(m.id)}${m.version ? ' · v' + esc(m.version) : ''}${m.all_clients ? ' · 朋友必装' : ''}${m.client_only ? ' · 纯客户端' : ''}</div>
          <div class="muted">${fmtBytes(m.size_bytes)}${m.updated_at ? ' · 本地 ' + fmtDate(m.updated_at) : ''}${m.subscriptions ? ' · 订阅 ' + m.subscriptions : ''}</div>
          <div class="mod-desc muted">${esc((m.description || '').slice(0, 160))}</div>
        </div>
        <div class="mod-actions">
          <label class="switch"><input type="checkbox" data-enable="${esc(m.id)}"${m.enabled ? ' checked' : ''}><span>启用</span></label>
          <button class="btn" data-detail="${esc(m.id)}">详情</button>
          ${(m.options || []).length ? `<button class="btn" data-config="${esc(m.id)}">设置（${m.options.length}）</button>` : ''}
          <button class="btn" data-one="${esc(m.id)}">更新</button>
          <button class="btn danger" data-remove="${esc(m.id)}">移除</button>
        </div>
      </div>`).join('');
    page.innerHTML = `
      <div class="row">
        <button class="btn primary" id="btnUpdateAll">一键更新全部模组</button>
        <button class="btn" id="btnReloadMods">刷新列表</button>
        <span class="muted">共 ${state.mods.length} 个模组 · 世界 ${esc(state.active)}</span>
      </div>
      <div class="card" style="margin-top:12px">
        <h3>从 Steam 创意工坊添加</h3>
        <div class="row">
          <input id="wsInput" class="grow" placeholder="粘贴创意工坊链接或 ID，例如 https://steamcommunity.com/sharedfiles/filedetails/?id=378160973">
          <button class="btn primary" id="btnWsLookup">查询并预览</button>
        </div>
        <div id="wsResult" class="list"></div>
      </div>
      <div class="list" style="margin-top:6px">${cards}</div>`;
    el('btnUpdateAll').onclick = doUpdateAll;
    el('btnReloadMods').onclick = loadMods;
    el('btnWsLookup').onclick = wsLookup;
    page.querySelectorAll('[data-enable]').forEach((cb) => cb.onchange = async () => {
      try {
        const job = await api('/api/mods/enable', { body: { cluster: state.active, id: cb.dataset.enable, enabled: cb.checked } });
        watchJob(job, (cb.checked ? '启用 ' : '停用 ') + cb.dataset.enable);
      } catch (e) { toast(e.message, true); cb.checked = !cb.checked; }
    });
    page.querySelectorAll('[data-detail]').forEach((b) => b.onclick = () => openModDetail(b.dataset.detail));
    page.querySelectorAll('[data-config]').forEach((b) => b.onclick = () => openConfig(b.dataset.config));
    page.querySelectorAll('[data-one]').forEach((b) => b.onclick = async () => {
      try {
        const job = await api('/api/mods/updateOne', { body: { cluster: state.active, id: b.dataset.one, restart: true } });
        watchJob(job, '更新模组 ' + b.dataset.one);
      } catch (e) { toast(e.message, true); }
    });
    page.querySelectorAll('[data-remove]').forEach((b) => b.onclick = async () => {
      if (!confirm('移除模组 ' + b.dataset.remove + '？')) return;
      const job = await api('/api/mods/remove', { body: { cluster: state.active, id: b.dataset.remove } });
      watchJob(job, '移除模组');
    });
  } catch (e) {
    page.innerHTML = `<div class="card err">读取模组失败：${esc(e.message)}</div>`;
  }
}

async function doUpdateAll() {
  try {
    const job = await api('/api/mods/update', { body: { cluster: state.active, restart: true } });
    watchJob(job, '一键更新全部模组');
  } catch (e) { toast(e.message, true); }
}

async function wsLookup() {
  const input = el('wsInput').value.trim();
  if (!input) return;
  const box = el('wsResult');
  box.innerHTML = '<div class="muted">查询中（服务器访问 Steam，首次可能要几秒）…</div>';
  try {
    const data = await api('/api/workshop/lookup', { body: { input } });
    const items = data.items || [];
    box.innerHTML = items.map((it) => `
      <div class="card ws">
        <div class="mod-thumb">${it.preview_url ? `<img src="/api/image?url=${encodeURIComponent(it.preview_url)}" alt="">` : ''}</div>
        <div class="mod-main">
          <div class="mod-title">${esc(it.title)}</div>
          <div class="muted">workshop-${esc(it.id)} · ${fmtBytes(it.size_bytes)} · 订阅 ${it.subscriptions || 0}${it.collection ? ' · 合集' : ''}</div>
          <div class="muted mod-desc">${esc((it.description || '').slice(0, 200))}</div>
        </div>
        <div class="mod-actions">
          <button class="btn primary" data-add="${esc(it.id)}" data-kind="${it.collection ? 'collection' : 'mod'}">${it.collection ? '添加整个合集并安装' : '添加并安装'}</button>
        </div>
      </div>`).join('') || '<div class="muted">没有结果</div>';
    box.querySelectorAll('[data-add]').forEach((b) => b.onclick = async () => {
      const id = b.dataset.add;
      const kind = b.dataset.kind;
      try {
        const body = { cluster: state.active, ids: [], collections: [], config: {} };
        if (kind === 'collection') body.collections = [id]; else body.ids = [id];
        const job = await api('/api/mods/install', { body });
        watchJob(job, '安装模组');
      } catch (e) { toast(e.message, true); }
    });
  } catch (e) {
    box.innerHTML = `<div class="card err">${esc(e.message)}</div>`;
  }
}

async function openModDetail(id) {
  const drawer = el('drawer');
  const body = el('drawerBody');
  body.innerHTML = '<div class="muted" style="padding:20px">读取模组详情…</div>';
  drawer.classList.remove('hidden');
  try {
    const d = await api('/api/mods/detail', { body: { cluster: state.active, id } });
    const opts = (d.options || []).map((o) => {
      const cur = (d.current || {})[o.name] || o.default || '';
      const choices = o.choices && o.choices.length
        ? `<select data-opt="${esc(o.name)}">${o.choices.map((c) => `<option value="${esc(c.value)}"${c.value === cur ? ' selected' : ''}>${esc(c.label)}</option>`).join('')}</select>`
        : `<input data-opt="${esc(o.name)}" value="${esc(cur)}">`;
      return `<label class="witem"><span title="${esc(o.hover || '')}">${esc(o.label || o.name)}</span>${choices}</label>`;
    }).join('');
    body.innerHTML = `
      <div class="drawer-head"><h2>${esc(d.name || d.workshop_title)}</h2><button class="btn" data-close>关闭</button></div>
      <div class="detail">
        <div class="detail-head">
          <div class="detail-img">${d.preview_url ? `<img src="/api/image?url=${encodeURIComponent(d.preview_url)}" alt="">` : ''}</div>
          <div>
            <div class="muted">workshop-${esc(d.id)}${d.version ? ' · v' + esc(d.version) : ''}</div>
            <div class="muted">${esc(d.author || d.creator || '')}${d.workshop_updated_at ? ' · 工坊更新 ' + fmtDate(d.workshop_updated_at) : ''}</div>
            <div class="muted">${fmtBytes(d.size_bytes)} · 订阅 ${d.subscriptions || 0}${d.all_clients ? ' · 朋友必装' : ''}${d.client_only ? ' · 纯客户端' : ''}</div>
            <div class="row" style="margin-top:8px">
              <button class="btn" id="dUpdate">更新这个模组</button>
              <a class="btn" href="${esc(d.catalog_url || '#')}" target="_blank" rel="noreferrer">在工坊页面打开</a>
            </div>
          </div>
        </div>
        <pre class="detail-desc">${esc(d.description || '（这个模组没有提供文字介绍）')}</pre>
        ${(d.tags || []).length ? `<div class="muted">标签：${d.tags.map(esc).join('、')}</div>` : ''}
        ${(d.shard_states || []).length ? `<div class="muted">分片状态：${d.shard_states.map((s) => `${esc(s.shard)}=${s.enabled ? '启用' : '停用'}`).join(' · ')}</div>` : ''}
        ${opts ? `<h3 style="margin-top:18px">模组设置</h3><div class="wgrid">${opts}</div>
          <div class="row" style="margin-top:10px"><button class="btn primary" id="dSave">保存设置并重启</button></div>` : ''}
      </div>`;
    body.querySelectorAll('[data-close]').forEach((b) => b.onclick = () => drawer.classList.add('hidden'));
    const upd = el('dUpdate');
    if (upd) upd.onclick = async () => {
      drawer.classList.add('hidden');
      const job = await api('/api/mods/updateOne', { body: { cluster: state.active, id, restart: true } });
      watchJob(job, '更新模组 ' + id);
    };
    const save = el('dSave');
    if (save) save.onclick = async () => {
      const options = {};
      body.querySelectorAll('[data-opt]').forEach((inp) => {
        if (inp.tagName === 'SELECT' && inp.value === '') return;
        options[inp.dataset.opt] = inp.value;
      });
      try {
        const job = await api('/api/mods/config', { body: { cluster: state.active, id, options } });
        drawer.classList.add('hidden');
        watchJob(job, '保存模组设置');
      } catch (e) { toast(e.message, true); }
    };
  } catch (e) {
    body.innerHTML = `<div class="drawer-head"><h2>模组详情</h2><button class="btn" data-close>关闭</button></div><div class="card err">${esc(e.message)}</div>`;
    body.querySelectorAll('[data-close]').forEach((b) => b.onclick = () => el('drawer').classList.add('hidden'));
  }
}

async function openConfig(id) {
  const m = state.mods.find((x) => x.id === id);
  if (!m) return;
  const drawer = el('drawer');
  const body = el('drawerBody');
  const opts = (m.options || []).map((o) => {
    const cur = (m.current || {})[o.name] || o.default || '';
    const choices = o.choices && o.choices.length
      ? `<select data-opt="${esc(o.name)}">${o.choices.map((c) => `<option value="${esc(c.value)}"${c.value === String(cur) ? ' selected' : ''}>${esc(c.label)}</option>`).join('')}</select>`
      : `<input data-opt="${esc(o.name)}" value="${esc(cur)}">`;
    return `<label class="witem"><span title="${esc(o.hover || '')}">${esc(o.label || o.name)}</span>${choices}</label>`;
  }).join('');
  body.innerHTML = `
    <div class="drawer-head"><h2>${esc(m.name)} · 设置</h2><button class="btn" data-close>关闭</button></div>
    <div class="wgrid">${opts}</div>
    <div class="row" style="margin-top:12px"><button class="btn primary" id="cfgSave">保存并重启</button></div>`;
  drawer.classList.remove('hidden');
  body.querySelectorAll('[data-close]').forEach((b) => b.onclick = () => drawer.classList.add('hidden'));
  el('cfgSave').onclick = async () => {
    const options = {};
    body.querySelectorAll('[data-opt]').forEach((inp) => { options[inp.dataset.opt] = inp.value; });
    try {
      const job = await api('/api/mods/config', { body: { cluster: state.active, id, options } });
      drawer.classList.add('hidden');
      watchJob(job, '保存模组设置');
    } catch (e) { toast(e.message, true); }
  };
}

// ---------- 世界生成页 ----------
async function loadWorldgen() {
  const page = el('page-worldgen');
  try {
    let wg = await api('/api/worldgen?cluster=' + encodeURIComponent(state.active) + '&shard=' + state.worldgenShard);
    if (!wg.caves_enabled && state.worldgenShard === 'Caves') { state.worldgenShard = 'Master'; return loadWorldgen(); }
    state.catalog = wg.catalog || { groups: [] };
    const shardTabs = ['Master'];
    if (wg.caves_enabled) shardTabs.push('Caves');
    const tabs = shardTabs.map((s) => `<button class="wt ${state.worldgenShard === s ? 'on' : ''}" data-shard="${s}">${s === 'Master' ? '地上世界' : '洞穴'}</button>`).join('');
    const world = wg.world_type === 'cave' ? 'cave' : 'forest';
    // 地上页与洞穴页各用各的设置集：森林专属项/全局主控项不会出现在洞穴页，
    // 洞穴专属项也不会出现在地上页。页面里再分“世界生成（锁定）/ 世界规则（可改）”。
    const sec = settingSections(wg.catalog, world);
    const panels = settingPanelsHtml(sec, wg.overrides || {}, (s) => `data-wg="${esc(s.key)}"`);
    let snaps = { keep: 2, snapshots: [] };
    try { snaps = await api('/api/worldgen/snapshots?cluster=' + encodeURIComponent(state.active)); } catch (e) { /* 历史存档读取失败不影响主页面 */ }
    // 预设按“是否洞穴专用”过滤：cave 标记由 agent 下发，不再靠 id 里猜 CAVE。
    const presetList = (wg.catalog.presets || []).filter((p) => wg.world_type === 'cave' ? !!p.cave : !p.cave);
    const presetOptions = `<option value=""${wg.preset === '' ? ' selected' : ''}>（未设置 → 保存时自动用 ${esc(wg.expected_preset)}）</option>` +
      presetList.map((p) => `<option value="${esc(p.id)}"${p.id === wg.preset ? ' selected' : ''}>${esc(p.name)}</option>`).join('');
    const bad = !wg.preset_valid || !wg.save_type_match;
    const warn = bad ? `<div class="card err" style="margin-top:12px">
        <b>⚠ 这个分片现在生成不出正确类型的世界</b>
        <p class="muted">${esc(wg.preset_hint || '')}</p>
        <div class="row"><button class="btn primary" id="wgFix">改为 ${esc(wg.expected_preset)} 并保存</button>
          <span class="muted">应为 ${esc(wg.world_type)} 世界 · 磁盘上存档：${esc(wg.save_world_type || '无')}</span></div>
      </div>` : '';
    const shardLabel = state.worldgenShard === 'Master' ? '地上世界' : '洞穴';
    const mine = (snaps.snapshots || []).filter((s) => s.shard === state.worldgenShard);
    const snapRows = mine.length ? mine.map((s) => `<tr>
        <td>${fmtDate(s.modified)}</td>
        <td>${s.world_type === 'cave' ? '洞穴' : (s.world_type === 'forest' ? '地上' : (esc(s.world_type) || '—'))}</td>
        <td>${esc(s.session_id || '—')}</td>
        <td>${fmtBytes(s.size_bytes)}</td>
        <td><button class="btn" data-restore="${esc(s.name)}">回档到此</button></td>
      </tr>`).join('')
      : `<tr><td colspan="5" class="muted">还没有历史存档（每次重新生成${shardLabel}时会自动保留）</td></tr>`;
    const snapCard = `<div class="card" style="margin-top:12px">
      <div class="row"><h3 style="margin:0">历史存档 · 回档（${shardLabel}）</h3><span class="spacer"></span>
        <span class="muted">每个分片保留最近 ${snaps.keep || 2} 份</span>
        <button class="btn" id="wgSnapNow">立即创建快照</button></div>
      <p class="muted">重新生成${shardLabel}之前，旧存档会自动保留成快照；回档会停掉两个分片、把选中的快照换回存档区，再按「先地上、后洞穴」的顺序重启（约 3–5 分钟），当前存档会先保留成快照，不会丢。</p>
      <table class="tbl"><thead><tr><th>时间</th><th>世界</th><th>会话</th><th>大小</th><th></th></tr></thead>
      <tbody>${snapRows}</tbody></table>
    </div>`;
    page.innerHTML = `
      <div class="row">${tabs}<span class="spacer"></span>
        <span class="muted">世界 ${esc(state.active)}${wg.world_seed ? ' · seed ' + esc(wg.world_seed) : ''}${wg.has_override_file ? ' · 有配置文件' : ' · 无配置文件'}${wg.save_world_type ? ' · 存档 ' + esc(wg.save_world_type) : ''}</span></div>
      ${warn}
      <div class="card" style="margin-top:12px">
        <div class="row"><label class="muted">${state.worldgenShard === 'Master' ? '地上预设' : '洞穴预设'}
          <select id="wgPreset">${presetOptions}</select></label>
          <span class="muted">${state.worldgenShard === 'Master' ? '只写进地上世界分片' : '只写进洞穴分片'}</span>
          <span class="spacer"></span>
          <button class="btn primary" id="wgSave">保存设置</button>
          <button class="btn danger" id="wgRegenShard">重新生成${shardLabel}</button>
          ${wg.caves_enabled ? '<button class="btn danger" id="wgRegenAll">重新生成全部（地上+洞穴）</button>' : ''}</div>
        <p class="muted">地上页与洞穴页的参数是分开的：这里保存只会写进${shardLabel}的配置，另一个分片不受影响。世界生成项只在“重新生成${shardLabel}”时生效；只重新生成一个分片时，另一个分片的存档不会被改动。</p>
        ${state.worldgenShard === 'Master' ? `<div class="row" style="margin-top:10px">
          <span class="muted">多枝树的初始数量写在地图房间定义里，游戏没有密度开关（「多枝树再生」只控制砍掉后还长不长）。想让现在的世界立刻变干净，可以一次性清掉地图上所有多枝树。</span>
          <span class="spacer"></span>
          <button class="btn" id="wgCleanTwiggy">清理多枝树（一次性）</button>
        </div>` : ''}
      </div>
      ${snapCard}
      ${panels}`;
    page.querySelectorAll('[data-shard]').forEach((b) => b.onclick = () => { state.worldgenShard = b.dataset.shard; loadWorldgen(); });
    const saveWg = async (preset) => {
      const overrides = {};
      page.querySelectorAll('[data-wg]').forEach((s) => { if (s.value) overrides[s.dataset.wg] = s.value; });
      const res = await api('/api/worldgen/save', { body: { cluster: state.active, shard: state.worldgenShard, preset, overrides } });
      const dropped = (res && res.dropped) || [];
      if (dropped.length) {
        toast(`已保存；${dropped.length} 项不属于${shardLabel}，已忽略：${dropped.join('、')}`, true);
      } else {
        toast('设置已保存（重新生成' + shardLabel + '后生效）');
      }
      loadWorldgen();
    };
    el('wgSave').onclick = async () => { try { await saveWg(el('wgPreset').value); } catch (e) { toast(e.message, true); } };
    if (el('wgSnapNow')) el('wgSnapNow').onclick = async () => {
      try {
        const job = await api('/api/worldgen/snapshot', { body: { cluster: state.active } });
        watchJob(job, '创建存档快照');
      } catch (e) { toast(e.message, true); }
    };
    page.querySelectorAll('[data-restore]').forEach((b) => b.onclick = async () => {
      const name = b.dataset.restore;
      if (!confirm(`回档会把${shardLabel}的当前存档换成快照 ${name}（当前存档会先保留成快照，不会丢），然后重启两个分片。继续？`)) return;
      try {
        const job = await api('/api/worldgen/restore', { body: { cluster: state.active, shard: state.worldgenShard, name } });
        watchJob(job, '回档到 ' + name);
      } catch (e) { toast(e.message, true); }
    });
    if (el('wgCleanTwiggy')) el('wgCleanTwiggy').onclick = async () => {
      if (!confirm('会删除当前地上世界地图上所有多枝树（玩家自己种的也会被删掉），无法撤销。执行完会自动存档。继续？')) return;
      const btn = el('wgCleanTwiggy');
      const old = btn.textContent;
      btn.disabled = true; btn.textContent = '执行中…';
      try {
        const res = await api('/api/console/exec', { body: { cluster: state.active, shard: 'Master', command: 'c_removeall("twiggytree")' } });
        const out = ((res && res.lines) || []).join(' · ').trim();
        const m = /removed\s+(\d+)/.exec(out.replace(/\s+/g, ' '));
        if (m) {
          toast(`已清掉 ${m[1]} 棵多枝树，正在存档`);
          try { await api('/api/console/exec', { body: { cluster: state.active, shard: 'Master', command: 'c_save()' } }); } catch (e2) { /* 存档失败不影响清理结果 */ }
        } else {
          toast('命令已执行；分片输出：' + (out || '（无输出）'), true);
        }
      } catch (e) { toast(e.message, true); }
      btn.disabled = false; btn.textContent = old;
    };
    if (el('wgFix')) el('wgFix').onclick = async () => { try { await saveWg(wg.expected_preset); } catch (e) { toast(e.message, true); } };
    el('wgRegenShard').onclick = async () => {
      if (!confirm(`重新生成${shardLabel}会移除该分片当前的存档（旧存档会改名保留在服务器上），另一个分片不受影响。继续？`)) return;
      try {
        const job = await api('/api/worldgen/regenerate', { body: { cluster: state.active, shard: state.worldgenShard } });
        watchJob(job, '重新生成' + shardLabel);
      } catch (e) { toast(e.message, true); }
    };
    if (el('wgRegenAll')) el('wgRegenAll').onclick = async () => {
      if (!confirm('重新生成全部会同时移除地上和洞穴的当前存档（旧存档会改名保留在服务器上）。继续？')) return;
      try {
        const job = await api('/api/worldgen/regenerate', { body: { cluster: state.active, shard: '' } });
        watchJob(job, '重新生成世界（地上+洞穴）');
      } catch (e) { toast(e.message, true); }
    };
  } catch (e) {
    page.innerHTML = `<div class="card err">读取世界生成失败：${esc(e.message)}</div>`;
  }
}

// ---------- 日志 ----------
async function loadLogs() {
  const page = el('page-logs');
  try {
    const data = await api('/api/logs?cluster=' + encodeURIComponent(state.active) + '&lines=200');
    page.innerHTML = `<div class="row"><h3>${esc(data.service)}</h3><button class="btn" id="btnReloadLogs">刷新</button></div>
      <pre class="logs">${esc((data.lines || []).join('\n'))}</pre>`;
    el('btnReloadLogs').onclick = loadLogs;
  } catch (e) {
    page.innerHTML = `<div class="card err">读取日志失败：${esc(e.message)}</div>`;
  }
}

// ---------- 路由 ----------
const loaders = { overview: loadOverview, worlds: loadWorlds, mods: loadMods, worldgen: loadWorldgen, logs: loadLogs };

function activateTab(name) {
  if (!loaders[name]) name = 'overview';
  state.tab = name;
  document.querySelectorAll('.tabs a').forEach((a) => a.classList.toggle('on', a.dataset.tab === name));
  document.querySelectorAll('.page').forEach((p) => p.classList.toggle('hidden', p.id !== 'page-' + name));
  if (location.hash !== '#' + name) history.replaceState(null, '', '#' + name);
  loaders[name]();
}

window.addEventListener('hashchange', () => activateTab(location.hash.replace('#', '')));
document.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => el('drawer').classList.add('hidden')));
document.querySelectorAll('[data-close-modal]').forEach((b) => b.addEventListener('click', () => el('modal').classList.add('hidden')));

(async function boot() {
  await loadClusterSwitch();
  // 深链接：?shard=Caves 直接打开洞穴分片的世界生成；?mod=.. 模组详情；?newworld=1 创建向导
  const q = new URLSearchParams(location.search);
  const shard = q.get('shard');
  if (shard === 'Caves' || shard === 'Master') state.worldgenShard = shard;
  activateTab(location.hash.replace('#', '') || 'overview');
  if (q.get('mod')) {
    setTimeout(() => openModDetail(q.get('mod')), 300);
  } else if (q.get('newworld')) {
    setTimeout(async () => {
      await openWizard();
      const step = q.get('wstep');
      if (step && state.wizard) { state.wizard.step = step; renderWizard(); }
    }, 300);
  }
})();
