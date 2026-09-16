import { $, $$, el, api, toast, busy, setupDropzone } from '../shared/common.js';

// Three-step flow: data and column roles, recipients, download.

const EXAMPLES = [
  ['Northwind Analytics', 'Churn model vendor'],
  ['Tessin Audit SA', 'External auditor'],
  ['Datenwerk Research', 'Academic partner'],
];

let state = null;     // /api/tab/state
let roles = {};       // column name -> id | tolerant | plain
let recipients = [];  // [{name, org}]
let batch = [];
let dataReady = false;
let step = 1;
let reached = 1;

export function initTag() {
  setupDropzone($('#tab-drop'), $('#tab-file'), (files) => loadTable(files[0]), $('#tab-upload'));
  $('#tab-sample').addEventListener('click', () => loadTable(null));
  $('#tab-replace').addEventListener('click', () => {
    dataReady = false;
    reached = 1;
    render1();
    renderStepper();
  });
  $('#tab-next-1').addEventListener('click', (e) => busy(e.target, saveSchema));

  $('#tab-add').addEventListener('submit', (e) => {
    e.preventDefault();
    const f = e.target;
    recipients.push({ name: f.name.value.trim(), org: f.org.value.trim() });
    f.name.value = '';
    f.org.value = '';
    renderRecipients();
    f.name.focus();
  });
  $('#tab-examples').addEventListener('click', () => {
    for (const [name, org] of EXAMPLES) {
      if (!recipients.some((r) => r.name === name)) recipients.push({ name, org });
    }
    renderRecipients();
  });
  $('#tab-techniques').addEventListener('change', renderTechniques);
  $('#tab-back-2').addEventListener('click', () => go(1));
  $('#tab-create').addEventListener('click', (e) => busy(e.target, createCopies));

  $('#tab-back-3').addEventListener('click', () => go(2));
  $('#tab-restart').addEventListener('click', () => {
    dataReady = false;
    batch = [];
    recipients = [];
    reached = 1;
    go(1);
  });

  for (const b of $$('#tab-stepper button')) {
    b.addEventListener('click', () => {
      const n = Number(b.dataset.step);
      if (n <= reached) go(n);
    });
  }
  start();
}

async function start() {
  state = await api('/api/tab/state');
  renderTechniques();
  go(1);
}

function go(n) {
  step = n;
  reached = Math.max(reached, n);
  for (let i = 1; i <= 3; i++) $(`#tab-step-${i}`).hidden = i !== n;
  if (n === 1) render1();
  if (n === 2) renderRecipients();
  if (n === 3) renderCopies();
  renderStepper();
  $('#tag .wizard').scrollIntoView({ block: 'start', behavior: 'smooth' });
}

function renderStepper() {
  for (const b of $$('#tab-stepper button')) {
    const n = Number(b.dataset.step);
    b.parentElement.className = n === step ? 'active' : n <= reached ? 'done' : 'locked';
    b.disabled = n > reached;
    b.querySelector('.dot').textContent = b.parentElement.className === 'done' ? '✓' : n;
  }
}

// ---- Step 1: data and column roles ----

async function loadTable(file) {
  if (batch.length && !confirm('Replace the current table? Copies already downloaded stay tied to it.')) return;
  const zone = $('#tab-drop');
  zone.classList.add('busy');
  $('#tab-drop-title').textContent = file ? `Reading ${file.name}` : 'Loading sample data';
  try {
    if (file) {
      const form = new FormData();
      form.append('file', file);
      state = await api('/api/tab/source', { form });
    } else {
      state = await api('/api/tab/source?sample=1', { method: 'POST' });
    }
    dataReady = true;
    batch = [];
    reached = 1;
    render1();
    renderStepper();
  } catch (err) {
    toast(err.message, true);
  } finally {
    zone.classList.remove('busy');
    $('#tab-drop-title').textContent = 'Drop your CSV here';
  }
}

function render1() {
  $('#tab-upload').hidden = dataReady;
  $('#tab-data').hidden = !dataReady;
  $('#tab-next-1').disabled = !dataReady;
  if (!dataReady) return;
  $('#tab-data-title').textContent = state.source;
  $('#tab-data-meta').textContent = `${state.rows.toLocaleString()} rows, ${state.columns.length} columns`;

  roles = {};
  for (const c of state.columns) roles[c.name] = 'plain';
  if (state.schema.key) roles[state.schema.key] = 'id';
  for (const m of state.schema.match || []) roles[m] = 'id';
  for (const f of state.schema.tolerant || []) roles[f.name] = 'tolerant';
  renderRoles();
}

function renderRoles() {
  const rows = state.columns.map((c) => {
    const select = el('select', { onchange: (e) => { roles[c.name] = e.target.value; renderRolesNote(); } },
      el('option', { value: 'plain', text: 'Not used' }),
      el('option', { value: 'id', text: 'Identifier' }),
      el('option', { value: 'tolerant', text: 'Tolerant', disabled: !c.tolerance }));
    select.value = roles[c.name];
    return el('tr', {},
      el('td', {}, el('b', { text: c.name })),
      el('td', { class: 'muted', text: c.kind }),
      el('td', { class: 'mono muted', text: c.sample }),
      el('td', { class: 'muted', text: c.unique >= 1 ? 'all distinct' : `${Math.floor(c.unique * 100)}% distinct` }),
      el('td', { class: 'muted', text: c.tolerance ? `± ${c.tolerance}` : '' }),
      el('td', {}, select));
  });
  $('#tab-roles').replaceChildren(el('table', {},
    el('thead', {}, el('tr', {}, ['Column', 'Type', 'Example', 'Values', 'Smallest change', 'Role'].map((h) => el('th', { text: h })))),
    el('tbody', {}, rows)));
  renderRolesNote();
}

function renderRolesNote() {
  const ids = Object.keys(roles).filter((k) => roles[k] === 'id');
  const tol = Object.keys(roles).filter((k) => roles[k] === 'tolerant');
  const notes = [];
  if (ids.length === 0) notes.push('No identifier: rows will be recognised by the columns that marking leaves alone, which fails if any of them is dropped.');
  if (tol.length === 0) notes.push('No tolerant column: the low-order-bit mark cannot be used, leaving canary rows and the dummy column.');
  $('#tab-roles-note').hidden = notes.length === 0;
  $('#tab-roles-note').textContent = notes.join(' ');
}

async function saveSchema() {
  const ids = state.columns.map((c) => c.name).filter((n) => roles[n] === 'id');
  const tolerant = (state.schema.tolerant || []).filter((f) => roles[f.name] === 'tolerant');
  for (const c of state.columns) {
    if (roles[c.name] === 'tolerant' && !tolerant.some((f) => f.name === c.name)) {
      tolerant.push({ name: c.name, decimals: c.decimals, timestamp: c.kind === 'timestamp', tolerance: c.tolerance });
    }
  }
  const schema = { key: ids[0] || '', match: ids.slice(1), tolerant, dummy: state.schema.dummy };
  await api('/api/tab/schema', { json: schema });
  state = await api('/api/tab/state');
  go(2);
}

// ---- Step 2: recipients ----

function initials(name) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((p) => p[0].toUpperCase()).join('');
}

function renderRecipients() {
  $('#tab-recipients').replaceChildren(...recipients.map((r, i) => el('div', { class: 'recip selected' },
    el('span', { class: 'avatar', text: initials(r.name) }),
    el('span', { class: 'who' }, el('b', { text: r.name }), el('small', { class: 'muted', text: r.org })),
    el('button', { class: 'linklike', onclick: () => { recipients.splice(i, 1); renderRecipients(); }, text: 'Remove' }))));
  $('#tab-recipients-empty').hidden = recipients.length > 0;
  $('#tab-count').textContent = recipients.length ? `${recipients.length} selected` : '';
  const any = Object.values(techniques()).some(Boolean);
  $('#tab-create').disabled = recipients.length === 0 || !any;
  $('#tab-create').textContent = recipients.length ? `Create ${recipients.length} cop${recipients.length === 1 ? 'y' : 'ies'}` : 'Create copies';
}

function techniques() {
  return Object.fromEntries($$('#tab-techniques input').map((i) => [i.name, i.checked]));
}

function renderTechniques() {
  const on = Object.values(techniques()).filter(Boolean).length;
  $('#tab-tech-summary').textContent = `· ${on} of 3 active`;
  if (state) renderRecipients();
}

async function createCopies() {
  batch = await api('/api/tab/issue', { json: { recipients, techniques: techniques() } });
  state = await api('/api/tab/state');
  reached = 3;
  go(3);
}

// ---- Step 3: share ----

function renderCopies() {
  const n = batch.length;
  $('#tab-ready-title').textContent = `${n} cop${n === 1 ? 'y' : 'ies'} ready`;
  $('#tab-copies').replaceChildren(...batch.map((c) => el('div', { class: 'copy' },
    el('span', { class: 'avatar', text: initials(c.recipient) }),
    el('span', { class: 'who' }, el('b', { text: c.recipient }), el('small', { class: 'muted', text: c.org })),
    el('span', { class: 'copy-file' },
      el('span', { class: 'fname', text: c.fileName }),
      el('small', { class: 'muted' }, `${c.rows.toLocaleString()} rows, tag `, el('span', { class: 'mono', text: c.markId }))),
    el('a', { class: 'button secondary-btn', href: `/api/tab/copy?mark=${c.markId}`, download: c.fileName, text: 'Download' }))));
  $('#tab-zip').href = `/api/tab/bundle?marks=${batch.map((c) => c.markId).join(',')}`;
  $('#tab-zip').hidden = n < 2;
}
