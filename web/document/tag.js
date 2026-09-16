import { $, $$, el, api, toast, busy, setupDropzone } from '../shared/common.js';

// Three-step share flow: document, recipients, download.

let state = null;  // /api/doc/state
let roster = null; // [{name, role, unit, selected}]
let batch = [];    // copies created in step 2, shown in step 3
let docReady = false;
let step = 1;
let reached = 1;   // furthest step the user may jump to

export function initTag() {
  setupDropzone($('#tag-drop'), $('#tag-file'), (files) => loadDocument(files[0]), $('#tag-upload'));
  $('#tag-sample').addEventListener('click', () => loadDocument(null));
  $('#tag-replace').addEventListener('click', () => {
    docReady = false;
    reached = 1;
    renderStep1();
    renderStepper();
  });
  $('#tag-next-1').addEventListener('click', () => go(2));

  $('#tag-all').addEventListener('change', (e) => {
    for (const r of roster) r.selected = e.target.checked;
    renderRecipients();
  });
  $('#tag-add').addEventListener('submit', (e) => {
    e.preventDefault();
    const f = e.target;
    roster.push({ name: f.name.value.trim(), role: f.role.value.trim() || 'Recipient', unit: f.unit.value, selected: true });
    f.name.value = '';
    f.role.value = '';
    renderRecipients();
    f.name.focus();
  });
  $('#doc-layers').addEventListener('change', renderLayerSummary);
  $('#tag-back-2').addEventListener('click', () => go(1));
  $('#tag-create').addEventListener('click', (e) => busy(e.target, createCopies));

  $('#tag-back-3').addEventListener('click', () => go(2));
  $('#tag-restart').addEventListener('click', () => {
    docReady = false;
    batch = [];
    for (const r of roster) r.selected = false;
    reached = 1;
    go(1);
  });

  for (const b of $$('#tag-stepper button')) {
    b.addEventListener('click', () => {
      const n = Number(b.dataset.step);
      if (n <= reached) go(n);
    });
  }

  start();
}

async function start() {
  state = await api('/api/doc/state');
  roster = state.roster.map((r) => ({ ...r, selected: false }));
  $('#tag-add').unit.replaceChildren(...state.units.filter((u) => u.id !== 'sec').map((u) => el('option', { value: u.id, text: u.name })));
  $('#tag-add').unit.value = 'ext';
  for (const s of $$('.param-shift')) s.textContent = state.params.shiftPt;
  renderLayerSummary();
  go(1);
}

function go(n) {
  step = n;
  reached = Math.max(reached, n);
  for (let i = 1; i <= 3; i++) $(`#tag-step-${i}`).hidden = i !== n;
  if (n === 1) renderStep1();
  if (n === 2) renderRecipients();
  if (n === 3) renderCopies();
  renderStepper();
  $('.wizard').scrollIntoView({ block: 'start', behavior: 'smooth' });
}

function renderStepper() {
  for (const b of $$('#tag-stepper button')) {
    const n = Number(b.dataset.step);
    const li = b.parentElement;
    li.className = n === step ? 'active' : n <= reached ? 'done' : 'locked';
    b.disabled = n > reached;
    b.querySelector('.dot').textContent = li.className === 'done' ? '✓' : n;
  }
}

// ---- Step 1: document ----

async function loadDocument(file) {
  if (batch.length && !confirm('Replace the current document? Copies already downloaded stay tied to it.')) return;
  const zone = $('#tag-drop');
  zone.classList.add('busy');
  $('#tag-drop-title').textContent = file ? `Reading ${file.name}` : 'Loading sample';
  $('#tag-drop-hint').textContent = 'Extracting text and laying out pages';
  try {
    if (file) {
      const form = new FormData();
      form.append('file', file);
      state = await api('/api/doc/source', { form });
    } else {
      state = await api('/api/doc/source?sample=1', { method: 'POST' });
    }
    docReady = true;
    batch = [];
    reached = 1;
    renderStep1();
    renderStepper();
  } catch (err) {
    toast(err.message, true);
  } finally {
    zone.classList.remove('busy');
    $('#tag-drop-title').textContent = 'Drop your PDF here';
    $('#tag-drop-hint').textContent = 'PDF or text file, up to 25 MB';
  }
}

function renderStep1() {
  $('#tag-upload').hidden = docReady;
  $('#tag-doc').hidden = !docReady;
  $('#tag-next-1').disabled = !docReady;
  if (!docReady) return;
  const m = state.master;
  $('#tag-doc-title').textContent = m.title;
  $('#tag-doc-meta').textContent = `${m.pages} page${m.pages === 1 ? '' : 's'} · ${m.words.toLocaleString()} words · ${m.source}`;
  const notes = m.warnings || [];
  $('#tag-doc-notes').hidden = notes.length === 0;
  $('#tag-doc-notes').replaceChildren(...notes.map((w) => el('li', { text: w })));
}

// ---- Step 2: recipients ----

function unitName(id) {
  return state.units.find((u) => u.id === id)?.name || id;
}

function initials(name) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((p) => p[0].toUpperCase()).join('');
}

function renderRecipients() {
  const list = $('#tag-recipients');
  list.replaceChildren(...roster.map((r, i) => el('label', { class: 'recip' + (r.selected ? ' selected' : '') },
    el('input', { type: 'checkbox', checked: r.selected, onchange: (e) => { roster[i].selected = e.target.checked; renderRecipients(); } }),
    el('span', { class: 'avatar', text: initials(r.name) }),
    el('span', { class: 'who' }, el('b', { text: r.name }), el('small', { class: 'muted', text: r.role })),
    el('span', { class: 'badge', text: unitName(r.unit) }))));
  const n = roster.filter((r) => r.selected).length;
  $('#tag-count').textContent = `${n} of ${roster.length} selected`;
  $('#tag-all').checked = n === roster.length;
  $('#tag-all').indeterminate = n > 0 && n < roster.length;
  const layers = currentLayers();
  const anyLayer = Object.values(layers).some(Boolean);
  $('#tag-create').disabled = n === 0 || !anyLayer;
  $('#tag-create').textContent = n === 0 ? 'Create copies' : `Create ${n} cop${n === 1 ? 'y' : 'ies'}`;
}

function currentLayers() {
  return Object.fromEntries($$('#doc-layers input').map((i) => [i.name, i.checked]));
}

function renderLayerSummary() {
  const on = Object.values(currentLayers()).filter(Boolean).length;
  $('#tag-layer-summary').textContent = `· ${on} of 4 active`;
  if (roster) renderRecipients();
}

async function createCopies() {
  const recipients = roster.filter((r) => r.selected).map(({ name, role, unit }) => ({ name, role, unit }));
  batch = await api('/api/doc/issue', { json: { recipients, layers: currentLayers() } });
  reached = 3;
  go(3);
}

// ---- Step 3: share ----

function renderCopies() {
  const n = batch.length;
  $('#tag-ready-title').textContent = `${n} cop${n === 1 ? 'y' : 'ies'} ready`;
  $('#tag-copies').replaceChildren(...batch.map((c) => el('div', { class: 'copy' },
    el('span', { class: 'avatar', text: initials(c.name) }),
    el('span', { class: 'who' },
      el('b', { text: c.name }),
      el('small', { class: 'muted', text: `${c.role} · ${unitName(c.unit)}` })),
    el('span', { class: 'copy-file' },
      el('span', { class: 'fname', text: c.fileName }),
      el('small', { class: 'muted' }, `${Math.round(c.sizeBytes / 1024)} KB, tag `, el('span', { class: 'mono', text: c.markId }))),
    el('a', { class: 'button secondary-btn', href: `/api/doc/copy?mark=${c.markId}&download=1`, download: c.fileName, text: 'Download' }))));
  $('#tag-zip').href = `/api/doc/bundle?marks=${batch.map((c) => c.markId).join(',')}`;
  $('#tag-zip').hidden = n < 2;
}
