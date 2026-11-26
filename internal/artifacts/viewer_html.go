package artifacts

const viewerPageHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s — ClaudGravity Artifact</title>
<style>
  *{box-sizing:border-box}
  html,body{margin:0;height:100%%;background:#0d1117;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;display:flex;flex-direction:column}
  .bar{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:8px 14px;background:#161b22;border-bottom:1px solid #30363d;flex-shrink:0}
  .meta{display:flex;align-items:center;gap:8px;min-width:0}
  .title{font-weight:600;font-size:13px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .badge{font-size:11px;padding:2px 6px;border-radius:4px;background:#21262d;color:#8b949e;font-family:monospace}
  .live-pill{display:inline-flex;align-items:center;gap:5px;font-size:11px;padding:2px 8px;border-radius:999px;background:#16261e;color:#3fb950;border:1px solid #234d2c}
  .live-pill.busy{background:#2a1f0f;color:#d29922;border-color:#5a3f15}
  .live-pill.off{background:#21262d;color:#8b949e;border-color:#30363d}
  .live-pill .dot{width:7px;height:7px;border-radius:50%%;background:currentColor}
  .live-pill.busy .dot{animation:pulse 1s infinite}
  @keyframes pulse{0%%,100%%{opacity:1}50%%{opacity:.35}}
  .actions{display:flex;align-items:center;gap:6px;flex-shrink:0}
  .btn{background:#21262d;color:#e6edf3;border:1px solid #30363d;border-radius:6px;padding:5px 10px;font-size:12px;cursor:pointer;text-decoration:none}
  .btn:hover{background:#30363d}
  .btn.active{border-color:#58a6ff;color:#58a6ff}
  .main{flex:1;display:flex;min-height:0;position:relative}
  iframe{flex:1;width:100%%;height:100%%;border:0;background:#fff}
  .drawer{width:340px;max-width:90vw;background:#161b22;border-left:1px solid #30363d;display:none;flex-direction:column;flex-shrink:0}
  .drawer.open{display:flex}
  .drawer-head{padding:10px 14px;border-bottom:1px solid #30363d;display:flex;align-items:center;justify-content:space-between;font-weight:600;font-size:13px}
  .threads{flex:1;overflow:auto;padding:10px 12px;display:flex;flex-direction:column;gap:10px}
  .thread{background:#0d1117;border:1px solid #30363d;border-radius:8px;padding:9px 10px;display:flex;flex-direction:column;gap:6px}
  .thread.resolved{opacity:.65}
  .msg{font-size:12px;line-height:1.45;word-break:break-word}
  .msg .who{font-weight:600;color:#58a6ff;margin-right:4px}
  .msg.agent .who{color:#a371f7}
  .thread-foot{display:flex;justify-content:space-between;align-items:center;gap:6px;margin-top:2px}
  .mini{font-size:11px;padding:3px 7px;border-radius:5px;border:1px solid #30363d;background:#21262d;color:#c9d1d9;cursor:pointer}
  .mini:hover{background:#30363d}
  .composer{padding:10px 12px;border-top:1px solid #30363d;display:flex;flex-direction:column;gap:8px}
  .composer textarea{width:100%%;min-height:66px;resize:vertical;background:#0d1117;color:#e6edf3;border:1px solid #30363d;border-radius:6px;padding:8px;font:inherit;font-size:12px}
  .composer-row{display:flex;align-items:center;justify-content:space-between;gap:8px;font-size:12px;color:#8b949e}
  .toast{position:fixed;bottom:16px;right:16px;background:#1f2937;color:#f3f4f6;border:1px solid #374151;padding:8px 12px;border-radius:8px;font-size:12px;opacity:0;pointer-events:none;transition:opacity .2s;z-index:20}
  .toast.show{opacity:1}
</style>
</head>
<body>
<header class="bar">
  <div class="meta">
    <span class="title" id="artTitle">%s</span>
    <span class="badge" id="verBadge">v%s</span>
    <span class="badge">%s</span>
    <span class="live-pill off" id="liveBadge"><span class="dot"></span><span id="liveText">Подключение…</span></span>
  </div>
  <div class="actions">
    <button class="btn" id="commentsBtn" onclick="toggleDrawer()">💬 Комментарии <span id="commentsCount"></span></button>
    <button class="btn" onclick="copyLink()">Копировать URL</button>
    <button class="btn" onclick="downloadHTML()">Скачать HTML</button>
    <a class="btn" href="/dashboard">Дашборд</a>
  </div>
</header>
<div class="main">
  <iframe id="artifactFrame" sandbox="allow-scripts allow-forms allow-downloads allow-modals allow-popups"></iframe>
  <aside class="drawer" id="commentsDrawer">
    <div class="drawer-head">
      <span>Комментарии к артефакту</span>
      <button class="mini" onclick="toggleDrawer()">✕</button>
    </div>
    <div class="threads" id="threadsList"></div>
    <form class="composer" onsubmit="return submitComment(event)">
      <textarea id="commentInput" placeholder="Оставьте комментарий или попросите Клода внести правку…"></textarea>
      <div class="composer-row">
        <label><input type="checkbox" id="askClaudeBox" checked> Отправить Клоду</label>
        <button class="btn" type="submit">Отправить</button>
      </div>
    </form>
  </aside>
</div>
<div class="toast" id="toast"></div>
<script>
let docHtml = %s;
let rawHtml = %s;
const slug = %s;
const basePath = %s;
const frame = document.getElementById('artifactFrame');
let hotSnapshot = null;

window.addEventListener('message', function(ev) {
  if (ev && ev.data && ev.data.__cgHotSnapshot !== undefined) {
    hotSnapshot = ev.data.__cgHotSnapshot;
  }
});
frame.addEventListener('load', function() {
  if (hotSnapshot !== null && frame.contentWindow) {
    try { frame.contentWindow.postMessage({__cgHotInit: hotSnapshot}, '*'); } catch (_) {}
  }
});
frame.srcdoc = docHtml;

function showToast(txt) {
  const el = document.getElementById('toast');
  el.textContent = txt;
  el.classList.add('show');
  clearTimeout(window.__toastTimer);
  window.__toastTimer = setTimeout(() => el.classList.remove('show'), 2500);
}
function setLiveState(mode, label) {
  const pill = document.getElementById('liveBadge');
  pill.className = 'live-pill' + (mode ? ' ' + mode : '');
  document.getElementById('liveText').textContent = label;
}
function toggleDrawer() {
  const d = document.getElementById('commentsDrawer');
  const b = document.getElementById('commentsBtn');
  const open = !d.classList.contains('open');
  d.classList.toggle('open', open);
  b.classList.toggle('active', open);
}
function esc(s) {
  return String(s || '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
let currentThreads = [];
function renderThreads(list) {
  currentThreads = Array.isArray(list) ? list : [];
  const openCount = currentThreads.filter(t => !t.resolved_at).length;
  document.getElementById('commentsCount').textContent = openCount > 0 ? '(' + openCount + ')' : '';
  const box = document.getElementById('threadsList');
  if (currentThreads.length === 0) {
    box.innerHTML = '<div style="font-size:12px;color:#8b949e">Пока нет комментариев. Напишите пожелание ниже — если Клод подключён в live, он увидит его сразу.</div>';
    return;
  }
  box.innerHTML = currentThreads.map(t => {
    const res = !!t.resolved_at;
    const msgs = (t.comments || []).map(c => {
      const isAgent = c.author && c.author.role === 'assistant';
      return '<div class="msg ' + (isAgent ? 'agent' : '') + '"><span class="who">' + (isAgent ? 'Claude:' : 'Вы:') + '</span>' + esc(c.text) + '</div>';
    }).join('');
    const btn = res ? '<span style="font-size:11px;color:#3fb950">✓ Решено</span>' : '<button class="mini" onclick="resolveThread(\'' + esc(t.id) + '\')">Решить</button>';
    return '<div class="thread ' + (res ? 'resolved' : '') + '">' + msgs + '<div class="thread-foot"><span>' + btn + '</span>' + (!res ? '<button class="mini" onclick="replyThread(\'' + esc(t.id) + '\')">Ответить</button>' : '') + '</div></div>';
  }).join('');
}
async function submitComment(ev, tid) {
  if (ev) ev.preventDefault();
  const inp = document.getElementById('commentInput');
  const text = inp.value.trim();
  if (!text) return false;
  const ask = document.getElementById('askClaudeBox').checked;
  const res = await fetch(basePath + '/comments', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({text: text, ask_claude: ask, thread_id: tid || ''})
  });
  if (res.ok) {
    inp.value = '';
    showToast(ask ? 'Отправлено Клоду' : 'Комментарий сохранён');
  }
  return false;
}
async function replyThread(tid) {
  const txt = prompt('Ответ в тред:');
  if (!txt || !txt.trim()) return;
  const ask = document.getElementById('askClaudeBox').checked;
  await fetch(basePath + '/comments', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({text: txt.trim(), ask_claude: ask, thread_id: tid})
  });
}
async function resolveThread(tid) {
  await fetch(basePath + '/comments', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({action: 'resolve', thread_id: tid})
  });
}
function connectLive() {
  const es = new EventSource(basePath + '/events');
  es.onopen = () => setLiveState('', 'Live');
  es.onerror = () => setLiveState('off', 'Переподключение…');
  es.addEventListener('state', ev => {
    const d = JSON.parse(ev.data);
    document.getElementById('verBadge').textContent = 'v' + d.version;
    document.getElementById('artTitle').textContent = d.title;
    setLiveState(d.active ? 'busy' : '', d.active ? 'Claude редактирует…' : 'Live');
    renderThreads(d.threads);
  });
  es.addEventListener('activity', ev => {
    const d = JSON.parse(ev.data);
    setLiveState(d.active ? 'busy' : '', d.active ? 'Claude редактирует…' : 'Live');
  });
  es.addEventListener('version', ev => {
    const d = JSON.parse(ev.data);
    docHtml = d.shimmed_html;
    rawHtml = d.raw_html;
    document.getElementById('verBadge').textContent = 'v' + d.version;
    document.getElementById('artTitle').textContent = d.title;
    document.title = d.title + ' — ClaudGravity Artifact';
    frame.srcdoc = docHtml;
    showToast('Обновлено до v' + d.version);
  });
  es.addEventListener('retitle', ev => {
    const d = JSON.parse(ev.data);
    document.getElementById('artTitle').textContent = d.title;
    document.title = d.title + ' — ClaudGravity Artifact';
  });
  es.addEventListener('comments', ev => {
    const d = JSON.parse(ev.data);
    renderThreads(d.threads);
  });
  es.addEventListener('deleted', () => {
    es.close();
    setLiveState('off', 'Удалён');
    showToast('Артефакт был удалён');
  });
}
connectLive();
function copyLink(){
  if (navigator.clipboard) {
    navigator.clipboard.writeText(location.href);
    showToast('Ссылка скопирована');
  }
}
function downloadHTML(){
  const b = new Blob([rawHtml], {type:'text/html;charset=utf-8'});
  const u = URL.createObjectURL(b);
  const a = document.createElement('a');
  a.href = u; a.download = slug + '.html'; a.click();
  setTimeout(() => URL.revokeObjectURL(u), 1000);
}
</script>
</body>
</html>`
