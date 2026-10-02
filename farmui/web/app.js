"use strict";

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const state = {
  system: null,
  avds: [],
  macros: [],
  apks: [],
  goldens: [],
  jobs: [],
  cards: new Map(),
  statRefs: null,
  chipRefs: null,
  drawer: { name: null, timer: null, lastW: 720, lastH: 1280 },
  screenTimer: null,
  source: null,
  recorder: { active: false, steps: [], lastTap: 0, device: null },
  // selected macro id per device name, persisted in localStorage
  macroChoice: JSON.parse(localStorage.getItem("farmui.macroChoice") || "{}"),
  // live-view state: global switch + per-device opt-out (persisted)
  live: JSON.parse(localStorage.getItem("farmui.live") || '{"global":true,"disabled":{}}'),
  visible: new Set(),
  observer: null,
  regions: [],
  netProfiles: {},
  proxies: [],
};

function saveLive() {
  localStorage.setItem("farmui.live", JSON.stringify(state.live));
}

// liveEnabled reports whether a device should stream frames.
function liveEnabled(name) {
  return !!state.live.global && !state.live.disabled[name];
}

function saveChoices() {
  localStorage.setItem("farmui.macroChoice", JSON.stringify(state.macroChoice));
}

/* ---------------- API ---------------- */
async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!res.ok) {
    const msg = (data && data.error) ? data.error : (typeof data === "string" ? data : res.statusText);
    throw new Error(msg);
  }
  return data;
}

/* ---------------- Formatting ---------------- */
function toast(message, kind = "info", ms = 3200) {
  const node = document.createElement("div");
  node.className = `toast glass ${kind}`;
  node.textContent = message;
  $("#toasts").appendChild(node);
  setTimeout(() => {
    node.style.transition = "opacity .3s, transform .3s";
    node.style.opacity = "0";
    node.style.transform = "translateY(10px)";
    setTimeout(() => node.remove(), 300);
  }, ms);
}

function fmtBytes(n) {
  if (!n || n <= 0) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return `${(n / Math.pow(1024, i)).toFixed(i ? 1 : 0)} ${u[i]}`;
}
const fmtPct = (v) => `${(v || 0).toFixed(1)}%`;

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function sparkline(history, key, color) {
  if (!history || history.length < 2) return "";
  const vals = history.map((p) => p[key] || 0);
  const max = Math.max(...vals, 1);
  const w = 90, h = 28;
  const step = w / (vals.length - 1);
  const points = vals.map((v, i) => `${(i * step).toFixed(1)},${(h - (v / max) * (h - 4) - 2).toFixed(1)}`).join(" ");
  return `<svg class="spark" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}">
    <polyline fill="none" stroke="${color}" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" points="${points}" opacity="0.9"/></svg>`;
}

/* ---------------- Static chrome (built once) ---------------- */
function buildStatic() {
  const chipDefs = [
    ["arch", "Arquitectura"], ["gpu", "GPU"], ["windowed", "Ventana"],
    ["ram", "RAM host"], ["cpus", "CPUs"],
  ];
  $("#system-chips").innerHTML = chipDefs
    .map(([k, label]) => `<span class="chip"><span class="dot"></span>${label} <b data-chip="${k}">—</b></span>`)
    .join("");
  state.chipRefs = {};
  chipDefs.forEach(([k]) => { state.chipRefs[k] = $(`[data-chip="${k}"]`); });

  const statDefs = [
    ["avds", "AVDs"], ["running", "En ejecución"], ["cpu", "CPU total"], ["mem", "RAM total"],
  ];
  $("#stats").innerHTML = statDefs
    .map(([k, label]) => `<div class="stat glass"><div class="label">${label}</div>
      <div class="value" data-stat="${k}">—</div><div class="spark-slot" data-spark="${k}"></div></div>`)
    .join("");
  state.statRefs = {};
  statDefs.forEach(([k]) => {
    state.statRefs[k] = { value: $(`[data-stat="${k}"]`), spark: $(`[data-spark="${k}"]`) };
  });
}

function updateChips() {
  const s = state.system;
  if (!s) return;
  const r = state.chipRefs;
  r.arch.textContent = s.hostAbi;
  r.gpu.textContent = s.gpu + (s.gpu === "host" ? " ⚡Metal" : "");
  r.windowed.textContent = s.windowed ? "activada" : "headless";
  r.ram.textContent = fmtBytes(s.totalMem);
  r.cpus.textContent = s.cpuCount;
}

function updateStats() {
  const avds = state.avds;
  const running = avds.filter((a) => a.running);
  const booted = running.filter((a) => a.booted).length;
  const totalCPU = running.reduce((acc, a) => acc + ((a.metrics && a.metrics.cpu) || 0), 0);
  const totalMem = running.reduce((acc, a) => acc + ((a.metrics && a.metrics.memBytes) || 0), 0);
  const biggest = running.map((a) => a.metrics).filter(Boolean)
    .sort((x, y) => (y.history || []).length - (x.history || []).length)[0];

  const r = state.statRefs;
  r.avds.value.textContent = avds.length;
  r.running.value.innerHTML = `${running.length} <small>/ ${booted} listos</small>`;
  r.cpu.value.textContent = fmtPct(totalCPU);
  r.mem.value.textContent = fmtBytes(totalMem);
  r.cpu.spark.innerHTML = biggest ? sparkline(biggest.history, "cpu", "#3ddc84") : "";
  r.mem.spark.innerHTML = biggest ? sparkline(biggest.history, "mem", "#22d3ee") : "";
}

/* ---------------- Grid ---------------- */
function renderGrid() {
  const grid = $("#grid");
  const avds = state.avds;
  const running = avds.filter((a) => a.running).length;

  $("#devices-count").textContent = avds.length
    ? `${avds.length} dispositivo${avds.length > 1 ? "s" : ""} · ${running} activo${running === 1 ? "" : "s"}`
    : "";
  $("#empty").classList.toggle("hidden", avds.length > 0);

  const seen = new Set();
  for (const avd of avds) {
    seen.add(avd.name);
    let card = state.cards.get(avd.name);
    if (!card) {
      card = createCard(avd);
      state.cards.set(avd.name, card);
      grid.appendChild(card.el);
    }
    updateCard(card, avd);
  }
  for (const [name, card] of state.cards) {
    if (!seen.has(name)) { card.el.remove(); state.cards.delete(name); }
  }
}

function createCard(avd) {
  const el = document.createElement("article");
  el.className = "card glass";
  el.dataset.name = avd.name;
  el.innerHTML = `
    <div class="card-head">
      <div class="card-title"><h3></h3><span class="sub"></span></div>
      <div class="card-head-actions">
        <button class="icon-btn act-eye" title="Vista en vivo">👁</button>
        <span class="pill"><span class="dot"></span><span class="pill-text"></span></span>
      </div>
    </div>
    <div class="screen-wrap">
      <div class="screen-off"><span class="big">🖥️</span><span>Detenido</span></div>
      <img alt="Pantalla" style="display:none" />
      <button class="icon-btn shot-btn hidden" title="Capturar ahora (bajo demanda)">📸</button>
    </div>
    <div class="metrics">
      <div class="metric"><div class="k">CPU</div><div class="v"><span class="cpu">—</span></div><div class="bar"><span class="cpu-bar" style="width:0%"></span></div></div>
      <div class="metric"><div class="k">RAM</div><div class="v"><span class="mem">—</span> <small class="mempct"></small></div><div class="bar"><span class="mem-bar" style="width:0%"></span></div></div>
      <div class="metric net" style="grid-column:1/-1"><div class="k">RED (↓ / ↑)</div><div class="v"><span class="net-rx">—</span> <small>↓</small> <span class="net-tx">—</span> <small>↑</small></div></div>
    </div>
    <div class="card-actions">
      <button class="btn primary act-start">▶ Arrancar</button>
      <button class="btn ghost act-stop hidden">■ Detener</button>
      <button class="btn ghost act-console" disabled>⌨ Consola</button>
      <button class="btn ghost act-opt" title="Aplicar perfil ligero y desactivar animaciones">⚡ Optimizar</button>
      <button class="btn danger act-del">🗑</button>
    </div>
    <div class="macro-row">
      <select class="macro-select" title="Macro asignada a este dispositivo" disabled>
        <option value="">— sin macro —</option>
      </select>
      <button class="btn macro-run" disabled title="Ejecutar en loop hasta detener">🧩 Ejecutar</button>
    </div>`;
  const refs = {
    title: $("h3", el), sub: $(".sub", el), pill: $(".pill", el), pillText: $(".pill-text", el),
    img: $("img", el), screenOff: $(".screen-off", el), shot: $(".shot-btn", el),
    eye: $(".act-eye", el),
    cpu: $(".cpu", el), mem: $(".mem", el), mempct: $(".mempct", el),
    cpuBar: $(".cpu-bar", el), memBar: $(".mem-bar", el),
    netRx: $(".net-rx", el), netTx: $(".net-tx", el),
    start: $(".act-start", el), stop: $(".act-stop", el), console: $(".act-console", el),
    opt: $(".act-opt", el), del: $(".act-del", el),
    select: $(".macro-select", el), run: $(".macro-run", el),
  };
  refs.start.addEventListener("click", () => doStart(avd.name, refs.start));
  refs.stop.addEventListener("click", () => doStop(avd.name, refs.stop));
  refs.del.addEventListener("click", () => doDelete(avd.name));
  refs.console.addEventListener("click", () => openDrawer(avd.name));
  refs.opt.addEventListener("click", () => doOptimize(avd.name, refs.opt));
  refs.img.addEventListener("click", (e) => tapFromEvent(avd.name, refs.img, e));
  refs.shot.addEventListener("click", (e) => { e.stopPropagation(); captureCardFrame(avd.name, true); });
  refs.eye.addEventListener("click", () => toggleLiveDevice(avd.name));
  refs.select.addEventListener("change", () => {
    state.macroChoice[avd.name] = refs.select.value;
    saveChoices();
  });
  refs.run.addEventListener("click", () => toggleMacro(avd.name, refs.run));
  observeCard(el);
  return { el, refs };
}

function updateCard(card, avd) {
  const { refs } = card;
  refs.title.textContent = avd.name;
  refs.sub.textContent = avd.serial ? `${avd.serial} · :${avd.port}` : (avd.image || "sin arrancar");
  card.el.classList.toggle("running", !!avd.running);

  let status = "stopped", label = "detenido";
  if (avd.running && avd.booted) { status = "ready"; label = "listo"; }
  else if (avd.running) { status = "booting"; label = "arrancando"; }
  refs.pill.className = `pill ${status}`;
  refs.pillText.textContent = label;

  const booted = avd.running && avd.booted;
  const streaming = booted && liveEnabled(avd.name);
  refs.img.style.display = streaming ? "block" : "none";
  refs.screenOff.style.display = streaming ? "none" : "grid";
  if (!streaming) {
    let label = "Detenido";
    if (avd.running && !avd.booted) label = "Arrancando Android…";
    else if (booted) label = "📺 Vista en vivo apagada";
    refs.screenOff.querySelector("span:last-child").textContent = label;
    if (!booted) refs.img.removeAttribute("src");
  }
  refs.shot.classList.toggle("hidden", !(booted && !liveEnabled(avd.name)));
  refs.eye.textContent = liveEnabled(avd.name) ? "👁" : "🚫";
  refs.eye.classList.toggle("off", !liveEnabled(avd.name));
  refs.eye.disabled = !avd.running;

  const m = avd.metrics;
  if (m && avd.running) {
    refs.cpu.textContent = fmtPct(m.cpu);
    refs.mem.textContent = fmtBytes(m.memBytes);
    refs.mempct.textContent = `${(m.memPct || 0).toFixed(1)}%`;
    refs.cpuBar.style.width = `${Math.min(100, m.cpu / Math.max(navigator.hardwareConcurrency || 4, 1))}%`;
    refs.memBar.style.width = `${Math.min(100, m.memPct || 0)}%`;
    refs.netRx.textContent = `${fmtBytes(m.rxRate || 0)}/s`;
    refs.netTx.textContent = `${fmtBytes(m.txRate || 0)}/s`;
  } else {
    refs.cpu.textContent = "—"; refs.mem.textContent = "—"; refs.mempct.textContent = "";
    refs.cpuBar.style.width = "0%"; refs.memBar.style.width = "0%";
    refs.netRx.textContent = "—"; refs.netTx.textContent = "—";
  }

  refs.start.classList.toggle("hidden", !!avd.running);
  refs.stop.classList.toggle("hidden", !avd.running);
  refs.console.disabled = !booted;

  // Macro selector + run toggle, bound to this device
  const options = ['<option value="">— sin macro —</option>']
    .concat(state.macros.map((m) => `<option value="${m.id}">${escapeHtml(m.name)}</option>`))
    .join("");
  if (refs.select.dataset.opts !== options) {
    refs.select.innerHTML = options;
    refs.select.dataset.opts = options;
  }
  const choice = state.macroChoice[avd.name] || "";
  if (refs.select.value !== choice) refs.select.value = choice;
  refs.select.disabled = !booted;

  const running = !!avd.macroId;
  refs.run.disabled = !booted && !running;
  refs.run.classList.toggle("macro-on", running);
  refs.run.textContent = running ? "⏹ Detener" : "🧩 Ejecutar";
  const active = macroById(avd.macroId);
  refs.run.title = running ? `Ejecutando "${active ? active.name : avd.macroId}" en loop` : "Ejecutar en loop hasta detener";
}

/* ---------------- Actions ---------------- */
async function doStart(name, btn) {
  const prev = btn.innerHTML;
  btn.disabled = true;
  btn.innerHTML = `<span class="spinner"></span> Arrancando`;
  try {
    const r = await api("POST", `/api/avds/${encodeURIComponent(name)}/start`, { port: 0 });
    toast(`▶ ${name} arrancando en ${r.serial}`, "ok");
  } catch (e) {
    toast(`Error al arrancar ${name}: ${e.message}`, "err", 5000);
  } finally {
    btn.innerHTML = prev; btn.disabled = false;
  }
}

async function doStop(name, btn) {
  const prev = btn.innerHTML;
  btn.disabled = true;
  btn.innerHTML = `<span class="spinner"></span> Deteniendo`;
  try {
    await api("POST", `/api/avds/${encodeURIComponent(name)}/stop`);
    toast(`■ ${name} detenido`, "info");
  } catch (e) {
    toast(`Error al detener ${name}: ${e.message}`, "err", 5000);
  } finally {
    btn.innerHTML = prev; btn.disabled = false;
  }
}

async function doOptimize(name, btn) {
  try {
    await api("POST", `/api/avds/${encodeURIComponent(name)}/optimize`, { lite: true });
    toast(`⚡ ${name} optimizado (reinicie para aplicar el perfil de hardware)`, "ok", 4500);
  } catch (e) {
    toast(`Error al optimizar ${name}: ${e.message}`, "err", 5000);
  }
}

async function doDelete(name) {
  if (!confirm(`¿Destruir el AVD "${name}"? Esta acción borra sus datos.`)) return;
  try {
    await api("DELETE", `/api/avds/${encodeURIComponent(name)}`);
    toast(`🗑 ${name} eliminado`, "info");
  } catch (e) {
    toast(`Error al eliminar ${name}: ${e.message}`, "err", 5000);
  }
}

/* ---------------- Create ---------------- */
function openCreate() {
  $("#create-overlay").classList.remove("hidden");
  setTimeout(() => $("#create-name").focus(), 50);
}
async function submitCreate() {
  const name = $("#create-name").value.trim();
  const image = $("#create-image").value.trim();
  const device = $("#create-device").value.trim();
  const lite = $("#create-lite").checked;
  if (!name) { toast("El nombre es obligatorio", "err"); return; }
  const btn = $("#create-submit");
  btn.disabled = true;
  const prev = btn.textContent;
  btn.innerHTML = `<span class="spinner"></span> Creando…`;
  try {
    await api("POST", "/api/avds", { name, image, device, lite });
    toast(`✅ ${name} creado${lite ? " (modo ligero)" : ""}`, "ok");
    $("#create-overlay").classList.add("hidden");
    $("#create-name").value = "";
  } catch (e) {
    toast(`Error al crear: ${e.message}`, "err", 6000);
  } finally {
    btn.disabled = false; btn.textContent = prev;
  }
}

/* ---------------- Settings ---------------- */
function openSettings() {
  const s = state.system;
  if (s) {
    $("#settings-gpu").value = s.gpu || "host";
    $("#settings-windowed").checked = !!s.windowed;
    updateGpuHint();
  }
  $("#settings-overlay").classList.remove("hidden");
}
function updateGpuHint() {
  const gpu = $("#settings-gpu").value;
  const hints = {
    host: "Metal nativo (MoltenVK) — máxima aceleración gráfica en Apple Silicon.",
    auto: "El emulador elige el mejor backend disponible.",
    angle_indirect: "Traduce GLES a Metal vía ANGLE. Útil si 'host' da problemas.",
    swiftshader_indirect: "Render por software. Mucho más CPU. Solo para compatibilidad.",
  };
  $("#gpu-hint").textContent = hints[gpu] || "";
}
async function saveSettings() {
  const payload = { gpu: $("#settings-gpu").value, windowed: $("#settings-windowed").checked };
  try {
    await api("PUT", "/api/settings", payload);
    toast("⚙️ Ajustes aplicados", "ok");
    $("#settings-overlay").classList.add("hidden");
  } catch (e) {
    toast(`Error al guardar ajustes: ${e.message}`, "err");
  }
}

/* ---------------- Macros ---------------- */
function macroById(id) { return state.macros.find((m) => m.id === id); }

async function toggleMacro(name, btn) {
  const avd = state.avds.find((a) => a.name === name);
  if (avd && avd.macroId) {
    try {
      await api("POST", `/api/instances/${encodeURIComponent(name)}/macro/stop`);
      toast(`⏹ Macro detenida en ${name}`, "info");
    } catch (e) { toast(`Error al detener macro: ${e.message}`, "err"); }
    return;
  }
  const choice = state.macroChoice[name] || "";
  if (!choice) {
    toast("Elige una macro en el selector del contenedor.", "info", 4200);
    return;
  }
  try {
    await api("POST", `/api/instances/${encodeURIComponent(name)}/macro/start`, { id: choice, loop: true });
    const m = macroById(choice);
    toast(`🧩 Ejecutando "${m ? m.name : choice}" en loop sobre ${name}`, "ok");
  } catch (e) {
    toast(`Error al ejecutar macro: ${e.message}`, "err");
  }
}

function openMacroPicker() { openLibrary(); }

function openLibrary() {
  renderLibrary();
  $("#library-overlay").classList.remove("hidden");
}

function renderLibrary() {
  const list = $("#library-list");
  if (!state.macros.length) {
    list.innerHTML = `<p class="muted">Aún no hay macros. Abre la consola (⌨) de un dispositivo y pulsa <b>⏺ Grabar</b>.</p>`;
    return;
  }
  list.innerHTML = state.macros.map((m) => {
    const chips = (m.steps || []).map((s, i) =>
      `<span class="step-chip ${s.type}">${i + 1}·${s.type}${s.type === "text" ? ` "${escapeHtml(s.text || "")}"` : ""}</span>`
    ).join("");
    return `<div class="macro-item" data-id="${m.id}">
      <div class="mi-head">
        <span class="mi-name">${escapeHtml(m.name)}</span>
        <span class="mi-meta">${(m.steps || []).length} pasos${m.appId ? ` · ${escapeHtml(m.appId)}` : ""}</span>
      </div>
      <div class="mi-steps">${chips || '<span class="mi-meta">sin pasos</span>'}</div>
      <div class="mi-actions">
        <button class="btn ghost mi-assign">📌 Asignar…</button>
        <button class="btn ghost mi-del">🗑 Eliminar</button>
      </div>
    </div>`;
  }).join("");

  $$(".macro-item", list).forEach((item) => {
    $(".mi-del", item).addEventListener("click", async () => {
      try {
        await api("DELETE", `/api/macros/${item.dataset.id}`);
        toast("Macro eliminada", "info");
        renderLibrary();
      } catch (e) { toast(`Error: ${e.message}`, "err"); }
    });
    $(".mi-assign", item).addEventListener("click", () => assignMacro(item.dataset.id));
  });
}

async function assignMacro(id) {
  const running = state.avds.filter((a) => a.running && a.booted);
  if (!running.length) { toast("No hay dispositivos activos para asignar.", "err"); return; }
  const device = running.length === 1
    ? running[0].name
    : prompt(`Asignar a qué dispositivo:\n${running.map((a) => a.name).join(", ")}`, running[0].name);
  if (!device) return;
  if (!running.some((a) => a.name === device)) { toast(`"${device}" no está activo.`, "err"); return; }
  state.macroChoice[device] = id;
  saveChoices();
  toast(`📌 Macro asignada a ${device}`, "ok");
  renderGrid();
  $("#library-overlay").classList.add("hidden");
}

/* ---------------- Bake ---------------- */
function openBake() { renderBakeLists(); $("#bake-overlay").classList.remove("hidden"); }

function renderBakeLists() {
  renderAPKList();
  renderJobsList();
  renderGoldensList();
}

function renderAPKList() {
  const el = $("#apk-list");
  if (!state.apks.length) {
    el.innerHTML = `<p class="muted">Sin APKs subidos aún.</p>`;
    return;
  }
  el.innerHTML = state.apks.map((a) => `
    <div class="apk-item" data-id="${a.id}">
      <input type="checkbox" checked />
      <span class="apk-name" title="${escapeHtml(a.name)}">${escapeHtml(a.name)}</span>
      <span class="apk-size">${fmtBytes(a.sizeBytes)}</span>
      <button class="icon-btn apk-del" title="Eliminar">🗑</button>
    </div>`).join("");
  $$(".apk-item", el).forEach((item) => {
    $(".apk-del", item).addEventListener("click", async () => {
      try { await api("DELETE", `/api/apks/${item.dataset.id}`); toast("APK eliminado", "info"); renderAPKList(); }
      catch (e) { toast(`Error: ${e.message}`, "err"); }
    });
  });
}

function renderJobsList() {
  const el = $("#jobs-list");
  if (!state.jobs.length) {
    el.innerHTML = `<p class="muted">Sin jobs de bake.</p>`;
    return;
  }
  el.innerHTML = state.jobs.map((j) => `
    <div class="job-item">
      <span class="job-state ${j.state}">${j.state}</span>
      <strong>${escapeHtml(j.name)}</strong>
      <span class="job-msg" title="${escapeHtml(j.message)}">${escapeHtml(j.message)}</span>
    </div>`).join("");
}

function renderGoldensList() {
  const el = $("#goldens-list");
  if (!state.goldens.length) {
    el.innerHTML = `<p class="muted">Sin goldens horneados aún.</p>`;
    return;
  }
  el.innerHTML = state.goldens.map((g) => `
    <div class="golden-item" data-name="${escapeHtml(g.name)}">
      <span class="golden-name">${escapeHtml(g.name)}</span>
      <span class="golden-meta">${fmtBytes(g.sizeBytes)} · base ${escapeHtml(g.baseName || "?")}</span>
      <button class="btn primary golden-launch">🚀 Lanzar clon</button>
      <button class="icon-btn golden-del" title="Eliminar">🗑</button>
    </div>`).join("");
  $$(".golden-item", el).forEach((item) => {
    $(".golden-launch", item).addEventListener("click", () => launchGolden(item.dataset.name));
    $(".golden-del", item).addEventListener("click", () => deleteGolden(item.dataset.name));
  });
}

async function deleteGolden(name) {
  if (!confirm(`¿Eliminar el golden "${name}"? Se borra la imagen horneada (no los AVDs clonados).`)) return;
  try {
    await api("DELETE", `/api/goldens/${encodeURIComponent(name)}`);
    toast(`🗑 Golden "${name}" eliminado`, "info");
  } catch (e) {
    toast(`Error al eliminar golden: ${e.message}`, "err", 5000);
  }
}

async function uploadAPKs(files) {
  for (const file of files) {
    if (!/\.apk$/i.test(file.name)) { toast(`"${file.name}" no es un .apk`, "err"); continue; }
    const fd = new FormData();
    fd.append("apk", file);
    try {
      await fetch("/api/apks", { method: "POST", body: fd });
      toast(`📦 "${file.name}" subido`, "ok", 2500);
    } catch (e) {
      toast(`Error subiendo "${file.name}"`, "err");
    }
  }
}

async function submitBake() {
  const name = $("#bake-name").value.trim();
  if (!name) { toast("Pon nombre al golden", "err"); return; }
  const apkIds = $$("#apk-list .apk-item input:checked").map((i) => i.closest(".apk-item").dataset.id);
  const payload = {
    name,
    baseName: $("#bake-base").value.trim(),
    image: $("#bake-image").value.trim(),
    device: $("#bake-device").value.trim(),
    apkIds,
  };
  try {
    await api("POST", "/api/bake", payload);
    toast("🔥 Bake iniciado. Sigue el progreso en 'Jobs'.", "ok", 4000);
  } catch (e) {
    toast(`Error al iniciar bake: ${e.message}`, "err", 5000);
  }
}

async function launchGolden(goldenName) {
  const cloneName = prompt(`Nombre del clon a partir de "${goldenName}":`, `${goldenName}-c1`);
  if (!cloneName) return;
  try {
    const r = await api("POST", `/api/goldens/${encodeURIComponent(goldenName)}/launch`, { cloneName, writable: true });
    toast(`🚀 Clon "${cloneName}" lanzado en ${r.serial}`, "ok");
  } catch (e) {
    toast(`Error al lanzar clon: ${e.message}`, "err", 5000);
  }
}

/* ---------------- Apps (adb install/list/launch/uninstall) ---------------- */
async function refreshApps() {
  const device = state.drawer.name;
  if (!device) return;
  // populate APK selector
  const sel = $("#apps-apk");
  const prev = sel.value;
  sel.innerHTML = state.apks.length
    ? state.apks.map((a) => `<option value="${a.id}">${escapeHtml(a.name)} (${fmtBytes(a.sizeBytes)})</option>`).join("")
    : `<option value="">— sin APKs subidos —</option>`;
  if (prev && state.apks.some((a) => a.id === prev)) sel.value = prev;
  // installed packages
  const list = $("#apps-list");
  list.innerHTML = `<div class="app-row"><span class="app-name">Cargando…</span></div>`;
  try {
    const pkgs = await api("GET", `/api/instances/${encodeURIComponent(device)}/packages`);
    if (!pkgs.length) {
      list.innerHTML = `<div class="app-row"><span class="app-name muted">Sin apps de terceros instaladas.</span></div>`;
      return;
    }
    list.innerHTML = pkgs.map((p) => `
      <div class="app-row" data-pkg="${escapeHtml(p.package)}">
        <span class="app-name" title="${escapeHtml(p.package)}">${escapeHtml(p.package)}</span>
        <button class="icon-btn app-launch" title="Abrir">▶</button>
        <button class="icon-btn app-uninstall" title="Desinstalar">🗑</button>
      </div>`).join("");
    $$(".app-row", list).forEach((row) => {
      const pkg = row.dataset.pkg;
      $(".app-launch", row).addEventListener("click", async () => {
        try { await api("POST", `/api/instances/${encodeURIComponent(device)}/packages/${encodeURIComponent(pkg)}/launch`); toast(`▶ ${pkg}`, "ok", 1500); }
        catch (e) { toast(`No se pudo abrir: ${e.message}`, "err"); }
      });
      $(".app-uninstall", row).addEventListener("click", async () => {
        if (!confirm(`¿Desinstalar "${pkg}" de ${device}?`)) return;
        try { await api("DELETE", `/api/instances/${encodeURIComponent(device)}/packages/${encodeURIComponent(pkg)}`); toast(`🗑 ${pkg} desinstalado`, "info"); refreshApps(); }
        catch (e) { toast(`Error: ${e.message}`, "err"); }
      });
    });
  } catch (e) {
    list.innerHTML = `<div class="app-row"><span class="app-name muted">Error: ${escapeHtml(e.message)}</span></div>`;
  }
}

async function installSelectedAPK() {
  const device = state.drawer.name;
  const id = $("#apps-apk").value;
  if (!device) return;
  if (!id) { toast("Sube un APK primero (📦 Bake → sección APKs).", "info"); return; }
  const btn = $("#apps-install");
  const prev = btn.textContent;
  btn.disabled = true;
  btn.innerHTML = `<span class="spinner"></span> Instalando…`;
  try {
    const r = await api("POST", `/api/instances/${encodeURIComponent(device)}/apks/${encodeURIComponent(id)}/install`);
    toast(`✅ Instalado (${r.output || "ok"})`, "ok");
    refreshApps();
  } catch (e) {
    toast(`Error al instalar: ${e.message}`, "err", 6000);
  } finally {
    btn.disabled = false;
    btn.textContent = prev;
  }
}

/* ---------------- Region / Network ---------------- */
function populateRegions() {
  const sel = $("#region-state");
  if (!sel || sel.dataset.ready) return;
  sel.innerHTML = state.regions
    .map((r) => `<option value="${escapeHtml(r.code)}">${escapeHtml(r.name)} — ${escapeHtml(r.capital)}</option>`)
    .join("");
  sel.dataset.ready = "1";
}

function updateRegionCurrent() {
  const el = $("#region-current");
  if (!el) return;
  const p = state.netProfiles[state.drawer.name];
  if (!p) { el.textContent = ""; return; }
  const region = state.regions.find((r) => r.code === p.state);
  const parts = [];
  if (region) parts.push(region.name);
  if (p.proxy) parts.push(`proxy ${p.proxy}`);
  el.textContent = parts.join(" · ");
  $("#region-proxy").value = p.proxy || "";
}

async function applyRegion() {
  const device = state.drawer.name;
  const code = $("#region-state").value;
  if (!device || !code) return;
  const btn = $("#region-apply");
  const prev = btn.textContent;
  btn.disabled = true; btn.innerHTML = `<span class="spinner"></span>`;
  try {
    const r = await api("POST", `/api/instances/${encodeURIComponent(device)}/region`, { state: code });
    toast(`🌎 ${device} → ${r.region.name} (${r.region.timezone})`, "ok");
  } catch (e) {
    toast(`Error al aplicar región: ${e.message}`, "err", 5000);
  } finally {
    btn.disabled = false; btn.textContent = prev;
  }
}

async function applyProxy() {
  const device = state.drawer.name;
  const proxy = $("#region-proxy").value.trim();
  if (!device) return;
  try {
    await api("POST", `/api/instances/${encodeURIComponent(device)}/proxy`, { proxy });
    toast(proxy ? `🔌 Proxy aplicado: ${proxy}` : "🔌 Proxy quitado", "ok");
  } catch (e) {
    toast(`Error al aplicar proxy: ${e.message}`, "err", 5000);
  }
}

function populateProxyList() {
  const sel = $("#proxy-list");
  if (!sel) return;
  const prev = sel.value;
  sel.innerHTML = state.proxies.length
    ? state.proxies.map((p) => `<option value="${p.id}">${escapeHtml(p.label)} · ${escapeHtml(p.host)}:${p.port}</option>`).join("")
    : `<option value="">— libreta vacía —</option>`;
  if (prev && state.proxies.some((p) => p.id === prev)) sel.value = prev;
}

function proxyUse() {
  const p = state.proxies.find((x) => x.id === $("#proxy-list").value);
  if (!p) { toast("Libreta vacía. Guarda un proxy con 💾.", "info"); return; }
  $("#region-proxy").value = `${p.host}:${p.port}`;
  toast("Proxy cargado en el campo. Pulsa Aplicar.", "info", 2000);
}

async function proxySave() {
  const proxy = $("#region-proxy").value.trim();
  if (!proxy) { toast("Escribe host:puerto para guardar", "err"); return; }
  try {
    const p = await api("POST", "/api/proxies", { label: proxy, proxy });
    toast(`💾 Guardado: ${p.label}`, "ok");
  } catch (e) {
    toast(`Error al guardar: ${e.message}`, "err", 5000);
  }
}

async function proxyDelete() {
  const id = $("#proxy-list").value;
  if (!id) return;
  try {
    await api("DELETE", `/api/proxies/${encodeURIComponent(id)}`);
    toast("🗑 Proxy borrado de la libreta", "info");
  } catch (e) {
    toast(`Error: ${e.message}`, "err");
  }
}

/* ---------------- Recorder ---------------- */
function recToggle() {
  const r = state.recorder;
  if (!r.active) {
    r.active = true;
    r.device = state.drawer.name;
    if (r.steps.length === 0) r.steps = [];
    $("#rec-dot").classList.add("live");
    $("#rec-state").textContent = "Grabando…";
    $("#rec-toggle").textContent = "⏹ Detener";
    $("#rec-toggle").classList.add("danger");
    $("#drawer-screen").classList.add("recording");
    syncCanvasSize();
    toast("⏺ Grabación iniciada. Traza sobre el lienzo.", "ok", 2600);
  } else {
    r.active = false;
    $("#rec-dot").classList.remove("live");
    $("#rec-state").textContent = "Grabación detenida";
    $("#rec-toggle").textContent = "⏺ Grabar";
    $("#rec-toggle").classList.remove("danger");
    $("#drawer-screen").classList.remove("recording");
    toast(`⏹ ${r.steps.length} pasos capturados`, "info");
  }
  updateRecorder();
}

function recStep(step) {
  const r = state.recorder;
  if (!r.active) return;
  const now = Date.now();
  const prev = r.steps[r.steps.length - 1];
  if (prev) prev.delayMs = Math.min(4000, now - r.lastTap);
  r.steps.push({ ...step, delayMs: 0 });
  r.lastTap = now;
  updateRecorder();
}

function updateRecorder() {
  const r = state.recorder;
  $("#rec-count").textContent = `${r.steps.length} paso${r.steps.length === 1 ? "" : "s"}`;
  $("#rec-undo").disabled = !r.steps.length;
  $("#rec-clear").disabled = !r.steps.length;
  $("#rec-save").disabled = !r.steps.length;
  $("#rec-list").innerHTML = r.steps
    .slice(-8)
    .map((s, i) => `<div class="rec-step">${r.steps.length - Math.min(8, r.steps.length) + i + 1}. <b>${s.type}</b> ${stepDetail(s)}</div>`)
    .join("");
  if (!r.steps.length) $("#rec-list").innerHTML = `<div class="rec-step">Sin pasos aún.</div>`;
  redrawTraces();
}

function stepDetail(s) {
  switch (s.type) {
    case "tap": return `(${(s.x * 100).toFixed(0)}%, ${(s.y * 100).toFixed(0)}%)`;
    case "swipe": return `→ (${(s.x2 * 100).toFixed(0)}%, ${(s.y2 * 100).toFixed(0)}%)`;
    case "text": return `"${escapeHtml(s.text)}"`;
    case "key": return `keyevent ${s.key}`;
    default: return "";
  }
}

function recUndo() {
  state.recorder.steps.pop();
  updateRecorder();
}
function recClear() {
  state.recorder.steps = [];
  updateRecorder();
}

function openMacroSave() {
  const steps = state.recorder.steps;
  if (!steps.length) { toast("Nada que guardar", "err"); return; }
  $("#macro-summary").textContent = `${steps.length} pasos grabados en ${state.recorder.device || "el dispositivo"}.`;
  $("#macro-preview").innerHTML = steps
    .map((s, i) => `<div class="rec-step">${i + 1}. <b>${s.type}</b> ${stepDetail(s)}</div>`)
    .join("");
  $("#macro-name").value = "";
  $("#macro-overlay").classList.remove("hidden");
  setTimeout(() => $("#macro-name").focus(), 50);
}

async function saveMacro() {
  const name = $("#macro-name").value.trim();
  const appId = $("#macro-app").value.trim();
  if (!name) { toast("Ponle un nombre a la macro", "err"); return; }
  try {
    await api("POST", "/api/macros", { name, appId, steps: state.recorder.steps });
    toast(`💾 Macro "${name}" guardada`, "ok");
    $("#macro-overlay").classList.add("hidden");
    state.recorder.steps = [];
    updateRecorder();
    renderLibrary();
  } catch (e) {
    toast(`Error al guardar: ${e.message}`, "err", 5000);
  }
}

/* ---------------- Canvas tracing (record on screen) ---------------- */
function canvas() { return $("#drawer-canvas"); }
function canvasCtx() { return canvas().getContext("2d"); }

function syncCanvasSize() {
  const img = $("#drawer-img");
  const c = canvas();
  if (!img.naturalWidth) return;
  const rect = img.getBoundingClientRect();
  c.width = Math.round(rect.width);
  c.height = Math.round(rect.height);
  c.style.width = `${rect.width}px`;
  c.style.height = `${rect.height}px`;
  redrawTraces();
}

function redrawTraces() {
  const c = canvas();
  const ctx = canvasCtx();
  ctx.clearRect(0, 0, c.width, c.height);
  for (const s of state.recorder.steps) {
    const x = (s.x ?? s.x) * c.width, y = (s.y ?? s.y) * c.height;
    if (s.type === "tap") {
      ctx.beginPath();
      ctx.arc(x, y, 14, 0, Math.PI * 2);
      ctx.fillStyle = "rgba(61,220,132,0.35)";
      ctx.fill();
      ctx.strokeStyle = "#3ddc84";
      ctx.lineWidth = 2;
      ctx.stroke();
      ctx.beginPath();
      ctx.arc(x, y, 3, 0, Math.PI * 2);
      ctx.fillStyle = "#3ddc84";
      ctx.fill();
    } else if (s.type === "swipe") {
      const x2 = s.x2 * c.width, y2 = s.y2 * c.height;
      ctx.beginPath();
      ctx.moveTo(x, y);
      ctx.lineTo(x2, y2);
      ctx.strokeStyle = "#22d3ee";
      ctx.lineWidth = 3;
      ctx.lineCap = "round";
      ctx.stroke();
      ctx.beginPath();
      ctx.arc(x2, y2, 5, 0, Math.PI * 2);
      ctx.fillStyle = "#22d3ee";
      ctx.fill();
    }
  }
}

function bindCanvasInput() {
  const c = canvas();
  let start = null;
  let drawing = null;

  c.addEventListener("pointerdown", (ev) => {
    if (!state.recorder.active || !state.drawer.name) return;
    const rect = c.getBoundingClientRect();
    start = { x: (ev.clientX - rect.left) / rect.width, y: (ev.clientY - rect.top) / rect.height, t: Date.now() };
    drawing = { x2: start.x, y2: start.y };
    c.setPointerCapture(ev.pointerId);
  });

  c.addEventListener("pointermove", (ev) => {
    if (!start) return;
    const rect = c.getBoundingClientRect();
    drawing.x2 = (ev.clientX - rect.left) / rect.width;
    drawing.y2 = (ev.clientY - rect.top) / rect.height;
    drawLive(start, drawing);
  });

  c.addEventListener("pointerup", (ev) => {
    if (!start || !state.drawer.name) { start = null; drawing = null; return; }
    const name = state.drawer.name;
    const rect = c.getBoundingClientRect();
    const end = { x: (ev.clientX - rect.left) / rect.width, y: (ev.clientY - rect.top) / rect.height };
    const dist = Math.hypot(end.x - start.x, end.y - start.y);
    const img = $("#drawer-img");
    if (dist < 0.045) {
      // tap
      api("POST", `/api/instances/${encodeURIComponent(name)}/input`, {
        type: "tap", x: Math.round(start.x * img.naturalWidth), y: Math.round(start.y * img.naturalHeight),
      }).catch((e) => toast(`Tap falló: ${e.message}`, "err"));
      recStep({ type: "tap", x: start.x, y: start.y });
    } else {
      const duration = Math.min(1200, Math.max(150, Date.now() - start.t));
      api("POST", `/api/instances/${encodeURIComponent(name)}/input`, {
        type: "swipe",
        x: Math.round(start.x * img.naturalWidth), y: Math.round(start.y * img.naturalHeight),
        x2: Math.round(end.x * img.naturalWidth), y2: Math.round(end.y * img.naturalHeight),
        duration,
      }).catch((e) => toast(`Swipe falló: ${e.message}`, "err"));
      recStep({ type: "swipe", x: start.x, y: start.y, x2: end.x, y2: end.y, duration });
    }
    start = null;
    drawing = null;
  });

  c.addEventListener("pointercancel", () => { start = null; drawing = null; });
  window.addEventListener("resize", () => { syncCanvasSize(); });
}

function drawLive(start, end) {
  const c = canvas();
  const ctx = canvasCtx();
  redrawTraces();
  const x = start.x * c.width, y = start.y * c.height;
  const x2 = end.x2 * c.width, y2 = end.y2 * c.height;
  ctx.beginPath();
  ctx.moveTo(x, y);
  ctx.lineTo(x2, y2);
  ctx.strokeStyle = "rgba(34,211,238,0.85)";
  ctx.lineWidth = 3;
  ctx.lineCap = "round";
  ctx.setLineDash([6, 6]);
  ctx.stroke();
  ctx.setLineDash([]);
}

/* ---------------- Drawer ---------------- */
function openDrawer(name) {
  state.drawer.name = name;
  $("#drawer-overlay").classList.remove("hidden");
  $("#drawer").classList.remove("hidden");
  $("#drawer-title").textContent = name;
  $("#drawer-out").textContent = "Esperando comando…";
  $("#drawer-text").value = "";
  $("#drawer-cmd").value = "";
  $("#drawer-loading").classList.remove("hidden");
  $("#drawer-img").removeAttribute("src");
  $("#drawer-screen").classList.toggle("recording", state.recorder.active);
  startDrawerScreens();
  syncCanvasSize();
  refreshApps();
  populateRegions();
  populateProxyList();
  updateRegionCurrent();
}
function closeDrawer() {
  state.drawer.name = null;
  clearInterval(state.drawer.timer);
  state.drawer.timer = null;
  $("#drawer-overlay").classList.add("hidden");
  $("#drawer").classList.add("hidden");
}
function fetchDrawerFrame(force) {
  const name = state.drawer.name;
  if (!name) return;
  const url = `/api/instances/${encodeURIComponent(name)}/screenshot?t=${Date.now()}${force ? "&force=1" : ""}`;
  const probe = new Image();
  probe.onload = () => {
    $("#drawer-img").src = url;
    $("#drawer-loading").classList.add("hidden");
    syncCanvasSize();
  };
  probe.src = url;
}

function startDrawerScreens() {
  clearInterval(state.drawer.timer);
  const name = state.drawer.name;
  if (!name) return;
  const streaming = liveEnabled(name);
  $("#drawer-capture").classList.toggle("hidden", streaming);
  if (streaming) {
    fetchDrawerFrame(false);
    state.drawer.timer = setInterval(() => {
      if (state.drawer.name && liveEnabled(state.drawer.name)) fetchDrawerFrame(false);
    }, 1600);
  } else {
    fetchDrawerFrame(true);
  }
}

function tapFromEvent(name, img, ev) {
  if (!img.naturalWidth) return;
  const rect = img.getBoundingClientRect();
  const nx = (ev.clientX - rect.left) / rect.width;
  const ny = (ev.clientY - rect.top) / rect.height;
  const x = Math.round(nx * img.naturalWidth);
  const y = Math.round(ny * img.naturalHeight);
  api("POST", `/api/instances/${encodeURIComponent(name)}/input`, { type: "tap", x, y })
    .catch((e) => toast(`Tap falló: ${e.message}`, "err"));
  if (state.recorder.active && state.recorder.device === name) {
    recStep({ type: "tap", x: nx, y: ny });
  }
}

// Swipe recording via pointer drag on the drawer screen.
function bindSwipeRecording() {
  const img = $("#drawer-img");
  let start = null;
  img.addEventListener("pointerdown", (ev) => {
    if (!state.recorder.active || !img.naturalWidth) return;
    const rect = img.getBoundingClientRect();
    start = {
      t: Date.now(),
      x: (ev.clientX - rect.left) / rect.width,
      y: (ev.clientY - rect.top) / rect.height,
    };
    img.setPointerCapture(ev.pointerId);
  });
  img.addEventListener("pointerup", (ev) => {
    if (!start || !state.recorder.active || !state.drawer.name) { start = null; return; }
    const rect = img.getBoundingClientRect();
    const x2 = (ev.clientX - rect.left) / rect.width;
    const y2 = (ev.clientY - rect.top) / rect.height;
    const dist = Math.hypot(x2 - start.x, y2 - start.y);
    if (dist > 0.05) {
      const duration = Math.min(1200, Math.max(150, Date.now() - start.t));
      recStep({ type: "swipe", x: start.x, y: start.y, x2, y2, duration });
    }
    start = null;
  });
  img.addEventListener("pointercancel", () => { start = null; });
}

function sendKey(code) {
  const name = state.drawer.name;
  if (!name) return;
  api("POST", `/api/instances/${encodeURIComponent(name)}/input`, { type: "key", key: Number(code) })
    .catch((e) => toast(`Tecla falló: ${e.message}`, "err"));
  if (state.recorder.active && state.recorder.device === name) {
    recStep({ type: "key", key: Number(code) });
  }
}
function sendText() {
  const name = state.drawer.name;
  const text = $("#drawer-text").value;
  if (!name || !text) return;
  api("POST", `/api/instances/${encodeURIComponent(name)}/input`, { type: "text", text })
    .then(() => { $("#drawer-text").value = ""; toast("Texto enviado", "ok", 1500); })
    .catch((e) => toast(`Texto falló: ${e.message}`, "err"));
  if (state.recorder.active && state.recorder.device === name) {
    recStep({ type: "text", text });
  }
}
async function runShell() {
  const name = state.drawer.name;
  const command = $("#drawer-cmd").value.trim();
  if (!name || !command) return;
  const out = $("#drawer-out");
  out.textContent = `$ ${command}\n…`;
  try {
    const res = await api("POST", `/api/instances/${encodeURIComponent(name)}/shell`, { command });
    out.textContent = `$ ${command}\n${res.output || ""}`;
  } catch (e) {
    out.textContent = `$ ${command}\n${e.message || "error"}`;
  }
}

/* ---------------- Screenshots ---------------- */
// observeCard tracks whether a card is visible in the viewport so we only
// capture frames for what the user is actually looking at.
function observeCard(el) {
  if (!state.observer) {
    state.observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        const name = entry.target.dataset.name;
        if (!name) continue;
        if (entry.isIntersecting) {
          state.visible.add(name);
          if (liveEnabled(name)) captureCardFrame(name, false);
        } else {
          state.visible.delete(name);
        }
      }
    }, { threshold: 0.1 });
  }
  state.observer.observe(el);
}

function captureCardFrame(name, force) {
  if (name === state.drawer.name) return; // the drawer owns this device's stream
  const card = state.cards.get(name);
  if (!card) return;
  const url = `/api/instances/${encodeURIComponent(name)}/screenshot?t=${Date.now()}${force ? "&force=1" : ""}`;
  const probe = new Image();
  probe.onload = () => { card.refs.img.src = url; };
  probe.src = url;
}

function toggleLiveDevice(name) {
  if (state.live.disabled[name]) delete state.live.disabled[name];
  else state.live.disabled[name] = true;
  saveLive();
  const card = state.cards.get(name);
  const avd = state.avds.find((a) => a.name === name);
  if (card && avd) updateCard(card, avd);
  if (state.drawer.name === name) startDrawerScreens();
  toast(liveEnabled(name) ? `👁 Vista en vivo activada: ${name}` : `🚫 Vista en vivo apagada: ${name}`, "info", 1800);
}

function toggleLiveGlobal() {
  state.live.global = !state.live.global;
  saveLive();
  updateLiveToggle();
  for (const avd of state.avds) {
    const card = state.cards.get(avd.name);
    if (card) updateCard(card, avd);
  }
  if (state.drawer.name) startDrawerScreens();
  startScreenLoop();
  if (state.live.global) {
    for (const avd of state.avds) {
      if (avd.running && avd.booted && state.visible.has(avd.name)) captureCardFrame(avd.name, false);
    }
  }
  toast(state.live.global ? "📺 Vistas en vivo activadas" : "📺 Vistas en vivo apagadas (control y métricas siguen)", "info", 2600);
}

function updateLiveToggle() {
  const b = $("#live-toggle");
  if (!b) return;
  b.textContent = state.live.global ? "📺 Vistas: ON" : "📺 Vistas: OFF";
  b.classList.toggle("off", !state.live.global);
}

function startScreenLoop() {
  clearInterval(state.screenTimer);
  state.screenTimer = setInterval(() => {
    if (document.hidden || !state.live.global) return;
    for (const avd of state.avds) {
      if (!avd.running || !avd.booted) continue;
      if (!liveEnabled(avd.name)) continue;
      if (!state.visible.has(avd.name)) continue;
      captureCardFrame(avd.name, false);
    }
  }, 1800);
}

/* ---------------- State transport (SSE, on demand) ---------------- */
function applySnapshot(data) {
  if (!data) return;
  if (data.system) { state.system = data.system; updateChips(); }
  if (Array.isArray(data.macros)) state.macros = data.macros;
  if (Array.isArray(data.apks)) state.apks = data.apks;
  if (Array.isArray(data.goldens)) state.goldens = data.goldens;
  if (Array.isArray(data.jobs)) state.jobs = data.jobs;
  if (data.netProfiles) state.netProfiles = data.netProfiles;
  if (Array.isArray(data.proxies)) { state.proxies = data.proxies; populateProxyList(); }
  if (Array.isArray(data.avds)) { state.avds = data.avds; renderGrid(); updateStats(); }
  renderBakeLists();
  if (state.drawer.name) updateRegionCurrent();
  setOnline(true);
}

function setOnline(ok) {
  const el = $("#server-status");
  el.classList.toggle("online", ok);
  el.classList.toggle("offline", !ok);
  el.textContent = ok
    ? `en línea · ${state.system ? state.system.emulator : "emulator"}`
    : "sin conexión";
}

function connectSSE() {
  if (state.source) state.source.close();
  const src = new EventSource("/api/events");
  state.source = src;
  src.onopen = () => setOnline(true);
  src.onmessage = (ev) => {
    try { applySnapshot(JSON.parse(ev.data)); } catch { /* ignore */ }
  };
  src.onerror = () => setOnline(false);
}

async function bootstrap() {
  try {
    const [system, avds, macros, apks, goldens, regions, proxies] = await Promise.all([
      api("GET", "/api/system"),
      api("GET", "/api/avds"),
      api("GET", "/api/macros"),
      api("GET", "/api/apks"),
      api("GET", "/api/goldens"),
      api("GET", "/api/regions"),
      api("GET", "/api/proxies"),
    ]);
    state.system = system;
    state.avds = avds;
    state.macros = macros || [];
    state.apks = apks || [];
    state.goldens = goldens || [];
    state.regions = regions || [];
    state.proxies = proxies || [];
    state.jobs = [];
    $("#image-list").innerHTML = (system.images || []).map((i) => `<option value="${escapeHtml(i)}"></option>`).join("");
    updateChips();
    renderGrid();
    updateStats();
    renderBakeLists();
    populateRegions();
    populateProxyList();
  } catch {
    setOnline(false);
  }
}

/* ---------------- Bind ---------------- */
function bind() {
  $("#open-create").addEventListener("click", openCreate);
  $("#empty-create").addEventListener("click", openCreate);
  $("#create-submit").addEventListener("click", submitCreate);
  $("#open-settings").addEventListener("click", openSettings);
  $("#settings-save").addEventListener("click", saveSettings);
  $("#settings-gpu").addEventListener("change", updateGpuHint);

  $("#open-library").addEventListener("click", () => { $("#library-sub").textContent = "Ejecuta una macro sobre un dispositivo en ejecución."; openLibrary(); });
  $("#live-toggle").addEventListener("click", toggleLiveGlobal);
  $("#drawer-capture").addEventListener("click", () => fetchDrawerFrame(true));
  $("#open-bake").addEventListener("click", openBake);
  $("#bake-submit").addEventListener("click", submitBake);
  const apkFile = $("#apk-file");
  apkFile.addEventListener("change", () => { if (apkFile.files.length) uploadAPKs(apkFile.files); apkFile.value = ""; });
  const drop = $("#apk-drop");
  drop.addEventListener("click", () => apkFile.click());
  drop.addEventListener("dragover", (e) => { e.preventDefault(); drop.classList.add("over"); });
  drop.addEventListener("dragleave", () => drop.classList.remove("over"));
  drop.addEventListener("drop", (e) => { e.preventDefault(); drop.classList.remove("over"); if (e.dataTransfer.files.length) uploadAPKs(e.dataTransfer.files); });

  $("#rec-toggle").addEventListener("click", recToggle);
  $("#rec-undo").addEventListener("click", recUndo);
  $("#rec-clear").addEventListener("click", recClear);
  $("#rec-save").addEventListener("click", openMacroSave);
  $("#macro-confirm").addEventListener("click", saveMacro);
  bindSwipeRecording();
  bindCanvasInput();

  $$("[data-close]").forEach((b) => b.addEventListener("click", () => $(`#${b.dataset.close}`).classList.add("hidden")));
  $$(".overlay").forEach((o) => o.addEventListener("click", (e) => { if (e.target === o) o.classList.add("hidden"); }));

  $("#drawer-close").addEventListener("click", closeDrawer);
  $("#drawer-overlay").addEventListener("click", closeDrawer);
  $$("[data-key]").forEach((b) => b.addEventListener("click", () => sendKey(b.dataset.key)));
  $("#drawer-send-text").addEventListener("click", sendText);
  $("#drawer-text").addEventListener("keydown", (e) => { if (e.key === "Enter") sendText(); });
  $("#apps-refresh").addEventListener("click", refreshApps);
  $("#apps-install").addEventListener("click", installSelectedAPK);
  $("#region-apply").addEventListener("click", applyRegion);
  $("#proxy-apply").addEventListener("click", applyProxy);
  $("#proxy-use").addEventListener("click", proxyUse);
  $("#proxy-save").addEventListener("click", proxySave);
  $("#proxy-del").addEventListener("click", proxyDelete);
  $("#drawer-run").addEventListener("click", runShell);
  $("#drawer-cmd").addEventListener("keydown", (e) => { if (e.key === "Enter") runShell(); });
  $("#drawer-img").addEventListener("click", (e) => { if (state.drawer.name) tapFromEvent(state.drawer.name, $("#drawer-img"), e); });

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { closeDrawer(); $$(".overlay").forEach((o) => o.classList.add("hidden")); }
  });
  document.addEventListener("visibilitychange", () => { if (!document.hidden) setOnline(!!state.source); });
}

async function main() {
  buildStatic();
  bind();
  updateLiveToggle();
  await bootstrap();
  connectSSE();
  startScreenLoop();
}

main();
