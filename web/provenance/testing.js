import { $, el, api, toast, busy, renderTable, renderReport, renderMatrix } from '../shared/common.js';

let state = null;

export function initTesting() {
  for (const id of ['#test-sample', '#test-shuffle', '#test-round', '#test-trunc']) {
    $(id).addEventListener('input', describe);
  }
  $('#test-drop').addEventListener('change', describe);
  $('#test-sample').addEventListener('input', () => ($('#test-sample-out').value = $('#test-sample').value + '%'));

  $('#test-run').addEventListener('click', (e) => busy(e.target, async () => {
    const mark = $('#test-source').value;
    if (!mark) return toast('Issue at least one copy first', true);
    const res = await api('/api/tab/leak', { json: { mark, attack: attack() } });
    $('#test-result').hidden = false;
    $('#test-download').href = `/api/tab/leak?id=${res.id}`;
    renderTable($('#test-preview'), res.preview.columns, res.preview.rows);
    renderReport($('#test-report'), res.report, res.truth);
  }));

  $('#test-matrix').addEventListener('click', (e) => busy(e.target, async () => {
    const mark = $('#test-matrix-source').value;
    if (!mark) return toast('Issue at least one copy first', true);
    renderMatrix($('#test-matrix-out'), await api('/api/tab/matrix', { json: { mark } }));
  }));
}

export async function showTesting() {
  state = await api('/api/tab/state');
  const marks = state.marks || [];
  for (const sel of ['#test-source', '#test-matrix-source']) {
    const cur = $(sel).value;
    $(sel).replaceChildren(...marks.map((m) => el('option', { value: m.markId, text: `${m.label} (${m.markId})` })));
    if (marks.some((m) => m.markId === cur)) $(sel).value = cur;
  }
  const cols = [...state.preview.columns];
  if (state.schema.dummy && !cols.includes(state.schema.dummy) && state.issued?.some((i) => i.techniques.dummy)) {
    cols.push(state.schema.dummy);
  }
  const checked = new Set([...document.querySelectorAll('#test-drop input:checked')].map((i) => i.value));
  $('#test-drop').replaceChildren(...cols.map((c) => el('label', {},
    el('input', { type: 'checkbox', value: c, checked: checked.has(c) }), el('code', { text: c }))));
  describe();
}

function attack() {
  return {
    dropColumns: [...document.querySelectorAll('#test-drop input:checked')].map((i) => i.value),
    samplePct: Number($('#test-sample').value),
    roundDigits: Number($('#test-round').value),
    truncateTimestamps: $('#test-trunc').checked,
    shuffle: $('#test-shuffle').checked,
  };
}

function describe() {
  const a = attack();
  const parts = [];
  if (a.dropColumns.length) parts.push(`drop ${a.dropColumns.join(', ')}`);
  if (a.samplePct < 100) parts.push(`keep ${a.samplePct}% of rows`);
  if (a.roundDigits > 0) parts.push(`round off ${a.roundDigits} decimals`);
  if (a.truncateTimestamps) parts.push('truncate timestamps');
  if (a.shuffle) parts.push('shuffle rows');
  $('#test-desc').textContent = parts.length ? parts.join(', ') : 'unchanged copy';
}
