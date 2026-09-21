import { $, $$, el, api, toast, busy } from './common.js';

// The marking tab shows the head of the loaded table and re-renders it every
// time a measure is toggled, so the cost of each measure is visible rather
// than described. Nothing is shown until a table is loaded.

const measures = () => Object.fromEntries($$('#mark-measures input').map((i) => [i.name, i.checked]));
const params = () => Object.fromEntries($$('#mark-measures [data-param]').map((i) => [i.dataset.param, Number(i.value)]));

let loaded = false;

function setLoaded(on) {
  loaded = on;
  for (const i of $$('#mark-measures input, #mark-measures select')) i.disabled = !on;
  $('#mark-download').hidden = !on;
  $('#mark-lede').hidden = !on;
  $('#mark-actions').hidden = !on;
  if (!on) {
    $('#mark-source').textContent = 'no table loaded';
    $('#mark-preview').replaceChildren(el('div', { class: 'empty', text: '' }));
    $('#mark-impact').replaceChildren();
  }
}

async function refresh() {
  if (!loaded) return;
  const box = $('#mark-preview');
  try {
    const { preview, markId, source, total } = await api('/api/preview', { json: { techniques: { ...measures(), params: params() }, rows: 10 } });
    $('#mark-source').textContent = `${source}, ${total.toLocaleString()} rows, marked as ${markId}`;
    renderTable(box, preview);
    renderImpact($('#mark-impact'), preview.impact);
    const q = Object.entries(measures()).filter(([, v]) => v).map(([k]) => `${k}=1`)
      .concat(Object.entries(params()).map(([k, v]) => `${k}=${v}`));
    $('#mark-copy').href = '/api/marked.csv' + (q.length ? '?' + q.join('&') : '');
  } catch (err) {
    box.replaceChildren(el('div', { class: 'empty', text: err.message }));
    toast(err.message, true);
  }
}

function renderTable(box, preview) {
  const head = el('tr', {}, preview.columns.map((c) =>
    el('th', { class: c.added ? 'added' : null },
      el('div', { text: c.name }),
      c.role ? el('div', { class: 'role', text: c.role }) : null)));

  const body = preview.rows.map((r) =>
    el('tr', { class: [r.canary ? 'canary' : '', r.withheld ? 'withheld' : '', r.moved ? 'moved' : ''].filter(Boolean).join(' ') || null,
      title: r.withheld ? 'withheld from this copy' : r.moved ? 'position swapped with its neighbour' : null },
      r.cells.map((cell) =>
        el('td', { class: cell.kind || null, title: cell.before ? `was ${cell.before}` : null }, cell.value))));

  box.replaceChildren(
    el('table', {}, el('thead', {}, head), el('tbody', {}, body)),
    el('div', { class: 'legend' },
      el('span', {}, el('i', { class: 'key changed' }), ' changed value'),
      el('span', {}, el('i', { class: 'key noise' }), ' noise'),
      el('span', {}, el('i', { class: 'key format' }), ' rewritten, same value'),
      el('span', {}, el('i', { class: 'key redact' }), ' redacted'),
      el('span', {}, el('i', { class: 'key added' }), ' added column'),
      el('span', {}, el('i', { class: 'key canary' }), ' synthetic row'),
      el('span', {}, el('i', { class: 'key withheld' }), ' withheld row'),
      el('span', {}, el('i', { class: 'key moved' }), ' reordered')));
}

function renderImpact(box, im) {
  // Tolerate fields a running server is too old to send: the frontend reloads
  // on refresh, the backend only on restart.
  const n = (v) => (v ?? 0).toLocaleString();
  const stat = (label, value, cls) => el('span', { class: 'stat' }, el('b', { class: cls, text: value }), ' ', label);
  box.replaceChildren(
    el('div', { class: 'stats' },
      stat('rows in the copy', n(im.rows)),
      stat('rows added', n(im.rowsAdded), im.rowsAdded ? 'hit' : null),
      stat('rows withheld', n(im.rowsWithheld), im.rowsWithheld ? 'hit' : null),
      stat('columns added', im.columnsAdded ?? 0, im.columnsAdded ? 'hit' : null),
      stat(`of ${n(im.cellsTotal)} cells changed`, `${n(im.cellsChanged)} (${(im.cellsPercent ?? 0).toFixed(2)}%)`, im.cellsChanged ? 'hit' : null),
      stat('cells noised', n(im.cellsNoised), im.cellsNoised ? 'hit' : null),
      stat('cells rewritten', n(im.cellsReformatted), im.cellsReformatted ? 'hit' : null),
      stat('values redacted', n(im.cellsRedacted), im.cellsRedacted ? 'hit' : null),
      stat('pairs reordered', n(im.pairsSwapped), im.pairsSwapped ? 'hit' : null)),
    el('ul', { class: 'notes-list' }, (im.notes || []).map((n) => el('li', { text: n }))));
}

async function load(fn) {
  try {
    await fn();
    setLoaded(true);
    await refresh();
  } catch (err) {
    toast(err.message, true);
  }
}

export function initMark() {
  setLoaded(false);
  for (const input of $$('#mark-measures input, #mark-measures select')) input.addEventListener('change', refresh);
  $('#mark-example').addEventListener('click', (e) =>
    busy(e.target, () => load(() => api('/api/source?sample=1', { method: 'POST' }))));
  $('#mark-file').addEventListener('change', (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (!file) return;
    const form = new FormData();
    form.append('file', file);
    load(() => api('/api/source', { form }));
  });
}
