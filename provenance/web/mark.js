import { $, $$, el, api, toast, busy, apiURL, countNoun } from './common.js';
import { initIssue, setIssueVisible, issuedCount } from './issue.js';

// The marking tab shows the head of the loaded table and re-renders it every
// time a measure is toggled, so the cost of each measure is visible rather
// than described. Nothing is shown until a table is loaded.

const measures = () => Object.fromEntries($$('#mark-measures input').map((i) => [i.name, i.checked]));

let loaded = false;
let state = null;    // /api/state: columns, schema, format
let lastFile = null; // kept so the file can be read again with another format

function setLoaded(on) {
  loaded = on;
  $('#mark-download').hidden = !on;
  $('#mark-lede').hidden = !on;
  $('#mark-actions').hidden = !on;
  $('#mark-reload').disabled = !on || !lastFile;
  setIssueVisible(on);
  if (!on) {
    for (const i of $$('#mark-measures input')) i.disabled = true;
    $('#mark-source').textContent = 'no table loaded';
    $('#mark-preview').replaceChildren(el('div', { class: 'empty', text: '' }));
    $('#mark-impact').replaceChildren();
  }
}

// ---- which measures the table can carry ----

// eligibility says, per measure, why it cannot be applied to this table, or
// '' when it can. A measure with nowhere to go is disabled up front rather
// than reporting "nothing was changed" after the fact.
function eligibility() {
  const sc = state.schema;
  const kinds = Object.fromEntries(state.columns.map((c) => [c.name, c.kind]));
  const tolerant = (sc.tolerant || []).length > 0;
  return {
    canary: sc.key || (sc.match || []).length ? '' : 'No column identifies a row. Set one column to identifier.',
    lowBit: tolerant ? '' : 'No column is tolerant. Set a decimal column to tolerant in the table header.',
    noise: tolerant ? '' : 'No column is tolerant. Set a decimal column to tolerant in the table header.',
    redact: (sc.redact || []).length ? '' : 'No column is set to redact. Set a name or contact column to redact in the table header.',
    format: Object.values(kinds).includes('decimal') ? '' : 'The table has no decimal numbers to rewrite.',
    allocate: '', order: '', dummy: '',
  };
}

function renderEligibility() {
  const why = eligibility();
  for (const input of $$('#mark-measures input')) {
    const reason = why[input.name] || '';
    const label = input.closest('label');
    input.disabled = !loaded || !!reason;
    if (reason) input.checked = false;
    label.classList.toggle('unavailable', !!reason);
    label.querySelector('.why')?.remove();
    if (reason && loaded) label.append(el('span', { class: 'why', text: reason }));
  }
}

// ---- column roles ----

const ROLE_LABEL = { identifier: 'identifier', tolerant: 'tolerant', redact: 'redact', match: 'also identifies', '': 'untouched' };

// rolesFor lists the roles a column may take, from what profiling found.
function rolesFor(col) {
  const out = ['', 'identifier'];
  if ((col.kind === 'decimal' && col.decimals >= 1) || (col.kind === 'timestamp' && col.decimals >= 1)) out.push('tolerant');
  if (col.kind === 'text') out.push('redact');
  return out;
}

function currentRole(name) {
  const sc = state.schema;
  if (sc.key === name) return 'identifier';
  if ((sc.tolerant || []).some((f) => f.name === name)) return 'tolerant';
  if ((sc.redact || []).includes(name)) return 'redact';
  if ((sc.match || []).includes(name)) return 'match';
  return '';
}

function precisionLabel(col, decimals) {
  if (col.kind === 'timestamp') return decimals >= 3 ? `±${+(10 ** (3 - decimals)).toFixed(6)} ms` : `±${+(10 ** -decimals).toFixed(6)} s`;
  return '±0.' + '0'.repeat(decimals - 1) + '1';
}

function roleCell(col) {
  const role = currentRole(col.name);
  const options = rolesFor(col);
  if (role === 'match' && !options.includes('match')) options.push('match');
  const select = el('select', { class: 'role-select', 'aria-label': `Role of ${col.name}` },
    options.map((r) => el('option', { value: r, text: ROLE_LABEL[r], selected: r === role })));
  select.addEventListener('change', () => setRole(col, select.value));
  let precision = null;
  if (role === 'tolerant') {
    const field = state.schema.tolerant.find((f) => f.name === col.name);
    const p = el('select', { class: 'role-select', title: 'How much a value may change', 'aria-label': `How much ${col.name} may change` },
      Array.from({ length: col.decimals }, (_, i) => i + 1).map((d) =>
        el('option', { value: d, text: precisionLabel(col, d), selected: d === field.decimals })));
    p.addEventListener('change', () => setRole(col, 'tolerant', Number(p.value)));
    precision = p;
  }
  return [select, precision];
}

async function setRole(col, role, decimals) {
  const sc = structuredClone(state.schema);
  const name = col.name;
  if (issuedCount() > 0 && !confirm('Copies have already been issued from this table with the current roles. Changing a role re-marks every copy from now on, and the copies already handed out will no longer verify.\n\nChange it anyway?')) {
    renderTableFromLast();
    return;
  }
  if (sc.key === name) sc.key = '';
  sc.tolerant = (sc.tolerant || []).filter((f) => f.name !== name);
  sc.redact = (sc.redact || []).filter((n) => n !== name);
  sc.match = (sc.match || []).filter((n) => n !== name);
  if (role === 'identifier') {
    if (sc.key) sc.match = [...sc.match, sc.key].filter((n) => state.columns.find((c) => c.name === n)?.unique >= 0.98);
    sc.key = name;
  } else if (role === 'tolerant') {
    const d = decimals || col.decimals;
    sc.tolerant.push({ name, decimals: d, timestamp: col.kind === 'timestamp', tolerance: precisionLabel(col, d) });
  } else if (role === 'redact') {
    sc.redact.push(name);
  } else if (role === 'match') {
    sc.match.push(name);
  }
  try {
    const res = await api('/api/schema', { json: sc });
    state.schema = res.schema;
    renderEligibility();
    await refresh();
  } catch (err) {
    toast(err.message, true);
    renderTableFromLast();
  }
}

// ---- preview ----

let lastPreview = null;

function renderTableFromLast() {
  if (lastPreview) renderTable($('#mark-preview'), lastPreview);
}

async function refresh() {
  if (!loaded) return;
  const box = $('#mark-preview');
  try {
    const { preview, markId, source, total } = await api('/api/preview', { json: { techniques: measures(), rows: 10 } });
    $('#mark-source').textContent = `${source}, ${countNoun(total, 'row', 'rows')}, marked as ${markId}`;
    lastPreview = preview;
    renderTable(box, preview);
    renderImpact($('#mark-impact'), preview.impact);
    const on = Object.entries(measures()).filter(([, v]) => v).map(([k]) => `${k}=1`);
    $('#mark-copy').href = apiURL('/api/marked.csv') + (on.length ? '?' + on.join('&') : '');
  } catch (err) {
    box.replaceChildren(el('div', { class: 'empty', text: err.message }));
    toast(err.message, true);
  }
}

function renderTable(box, preview) {
  const byName = Object.fromEntries(state.columns.map((c) => [c.name, c]));
  const head = el('tr', {}, preview.columns.map((c) =>
    el('th', { class: c.added ? 'added' : null },
      el('div', { text: c.name }),
      c.added ? el('div', { class: 'role', text: 'added' }) : byName[c.name] ? roleCell(byName[c.name]) : null)));

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
  const n = (v) => (v ?? 0).toLocaleString('en');
  const stat = (count, one, many, cls) => el('span', { class: 'stat' },
    el('b', { class: count ? cls : null, text: n(count) }), ' ', (count ?? 0) === 1 ? one : many);
  box.replaceChildren(
    el('div', { class: 'stats' },
      stat(im.rows, 'row in the copy', 'rows in the copy'),
      stat(im.rowsAdded, 'row added', 'rows added', 'hit'),
      stat(im.rowsWithheld, 'row withheld', 'rows withheld', 'hit'),
      stat(im.columnsAdded, 'column added', 'columns added', 'hit'),
      el('span', { class: 'stat' },
        el('b', { class: im.cellsChanged ? 'hit' : null, text: `${n(im.cellsChanged)} (${(im.cellsPercent ?? 0).toFixed(2)}%)` }),
        ` of ${n(im.cellsTotal)} cells changed`),
      stat(im.cellsNoised, 'cell noised', 'cells noised', 'hit'),
      stat(im.cellsReformatted, 'cell rewritten', 'cells rewritten', 'hit'),
      stat(im.cellsRedacted, 'value redacted', 'values redacted', 'hit'),
      stat(im.pairsSwapped, 'pair reordered', 'pairs reordered', 'hit')),
    el('ul', { class: 'notes-list' }, (im.notes || []).map((n) => el('li', { text: n }))));
}

// ---- loading ----

const DELIM_NAME = { ',': 'comma', ';': 'semicolon', '\t': 'tab', '|': 'pipe' };

function formatQuery() {
  const f = $('#mark-format');
  const q = new URLSearchParams();
  if (f.querySelector('[name=delimiter]').value) q.set('delimiter', f.querySelector('[name=delimiter]').value);
  if (f.querySelector('[name=decimal]').value === 'comma') q.set('decimal', 'comma');
  if (f.querySelector('[name=decimal]').value === 'point' && !q.has('delimiter')) q.set('delimiter', ',');
  return q.toString() ? '?' + q : '';
}

function showFormat(fmt) {
  $('#mark-format-read').textContent = fmt
    ? `· read as ${DELIM_NAME[fmt.delimiter] || 'comma'}-separated, decimal ${fmt.decimalComma ? 'comma' : 'point'}${fmt.bom ? ', with a byte order mark' : ''}`
    : '';
}

async function load(fn) {
  const error = $('#mark-error');
  error.hidden = true;
  try {
    await fn();
    state = await api('/api/state');
    showFormat(state.format);
    setLoaded(true);
    renderEligibility();
    await refresh();
  } catch (err) {
    error.textContent = err.message;
    error.hidden = false;
  }
}

function loadFile(file) {
  lastFile = file;
  const form = new FormData();
  form.append('file', file);
  return load(() => api('/api/source' + formatQuery(), { form }));
}

export function initMark() {
  initIssue(measures);
  setLoaded(false);
  for (const input of $$('#mark-measures input')) input.addEventListener('change', refresh);
  $('#mark-example').addEventListener('click', (e) => {
    lastFile = null;
    busy(e.target, () => load(() => api('/api/source?sample=1', { method: 'POST' })));
  });
  $('#mark-file').addEventListener('change', (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (file) loadFile(file);
  });
  $('#mark-reload').addEventListener('click', () => lastFile && loadFile(lastFile));
}
