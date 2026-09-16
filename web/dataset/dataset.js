import { $, el, api, toast, busy, renderTable, renderReport, renderMatrix, fmtTime } from '../shared/common.js';

const SAMPLE_RECIPIENTS = [
  ['Northwind Analytics', 'Churn-model vendor'],
  ['Blau & Partner Marketing', 'Campaign agency'],
  ['Internal Risk (Credit)', 'Internal team'],
  ['Tessin Audit SA', 'External auditor'],
  ['Datenwerk Research', 'Academic partner'],
];

let state = null;

function initDataset() {
  $('#tab-regen').addEventListener('click', (e) => busy(e.target, async () => {
    if (state?.issued?.length && !confirm('Generating a new table clears the issuance log. Continue?')) return;
    state = await api('/api/tab/dataset', { json: { rows: Number($('#tab-rows').value) } });
    render();
    $('#tab-report').replaceChildren(el('p', { class: 'muted', text: 'Nothing analysed yet.' }));
    $('#tab-leak-result').hidden = true;
    $('#tab-matrix-out').replaceChildren();
    toast(`Generated ${state.rows} rows`);
  }));

  $('#tab-issue-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const f = e.target;
    busy(f.querySelector('button[type=submit]'), async () => {
      const is = await api('/api/tab/issue', { json: {
        recipient: f.recipient.value, org: f.org.value,
        techniques: { canary: f.canary.checked, lowBit: f.lowBit.checked, dummy: f.dummy.checked },
      } });
      f.recipient.value = ''; f.org.value = '';
      toast(`Issued ${is.markId} to ${is.recipient}`);
      await refresh();
    });
  });

  $('#tab-issue-sample').addEventListener('click', (e) => busy(e.target, async () => {
    const f = $('#tab-issue-form');
    for (const [recipient, org] of SAMPLE_RECIPIENTS) {
      await api('/api/tab/issue', { json: { recipient, org, techniques: { canary: f.canary.checked, lowBit: f.lowBit.checked, dummy: f.dummy.checked } } });
    }
    toast(`Issued ${SAMPLE_RECIPIENTS.length} copies`);
    await refresh();
  }));

  $('#tab-sample').addEventListener('input', () => ($('#tab-sample-out').value = $('#tab-sample').value + '%'));
  $('#tab-source').addEventListener('change', renderDropColumns);
  for (const id of ['#tab-sample', '#tab-shuffle', '#tab-round-coords', '#tab-round-risk', '#tab-trunc', '#tab-source']) {
    $(id).addEventListener('input', describeAttack);
  }
  $('#tab-drop').addEventListener('change', describeAttack);

  $('#tab-leak').addEventListener('click', (e) => busy(e.target, async () => {
    const mark = $('#tab-source').value;
    if (!mark) return toast('Issue at least one copy first', true);
    const res = await api('/api/tab/leak', { json: { mark, attack: currentAttack() } });
    $('#tab-leak-result').hidden = false;
    $('#tab-leak-download').href = `/api/tab/leak?id=${res.id}`;
    $('#tab-leak-desc').textContent = `${res.rows} rows, ${res.columns.length} columns`;
    renderTable($('#tab-leak-preview'), res.preview.columns, res.preview.rows, { highlight: ['latitude', 'longitude', 'opened_at', 'risk_score', 'branch_ref'] });
    renderReport($('#tab-report'), res.report, res.truth);
    $('#tab-report').scrollIntoView({ behavior: 'smooth', block: 'start' });
  }));

  $('#tab-upload').addEventListener('change', async (e) => {
    const file = e.target.files[0];
    if (!file) return;
    const form = new FormData();
    form.append('file', file);
    try {
      const res = await api('/api/tab/detect', { form });
      renderReport($('#tab-report'), res.report, null);
      toast(`Analysed ${res.name}: ${res.rows} rows`);
    } catch (err) {
      toast(err.message, true);
    }
    e.target.value = '';
  });

  $('#tab-matrix').addEventListener('click', (e) => busy(e.target, async () => {
    const mark = $('#tab-matrix-source').value;
    if (!mark) return toast('Issue at least one copy first', true);
    renderMatrix($('#tab-matrix-out'), await api('/api/tab/matrix', { json: { mark } }));
  }));

  refresh();
}

async function refresh() {
  state = await api('/api/tab/state');
  render();
}

function render() {
  $('#tab-rows').value = state.rows;
  $('#tab-summary').textContent = `${state.rows} rows · ${state.columns.length} columns`;
  renderTable($('#tab-preview'), state.preview.columns, state.preview.rows, { highlight: state.tolerant.map((f) => f.name) });
  $('#tab-tolerant').replaceChildren(...state.tolerant.map((f) => el('li', {}, el('code', { text: f.name }), el('span', { text: f.tolerance }))));

  const issued = state.issued || [];
  if (!issued.length) {
    renderTable($('#tab-log'), [], [], { empty: 'No copies issued yet.' });
  } else {
    const tbody = el('tbody', {}, issued.map((is) => el('tr', {},
      el('td', { class: 'mono', text: is.markId }),
      el('td', { text: is.recipient }),
      el('td', { text: is.org }),
      el('td', {}, badge('canary', is.techniques.canary), badge('low bits', is.techniques.lowBit), badge('dummy col', is.techniques.dummy)),
      el('td', { class: 'num-cell', text: is.rows }),
      el('td', { class: 'num-cell', title: `${is.markedCells} cells carry a bit. Only those whose value had to change were modified.`, text: `${is.changedCells} / ${is.markedCells}` }),
      el('td', { class: 'num-cell', title: (is.canaries || []).map((c) => `${c.name}, ${c.city} <${c.email}>`).join('\n'), text: (is.canaries || []).length }),
      el('td', { text: fmtTime(is.issuedAt) }),
      el('td', {}, el('a', { href: `/api/tab/copy?mark=${is.markId}`, text: 'CSV' })))));
    $('#tab-log').replaceChildren(el('table', {},
      el('thead', {}, el('tr', {}, ['Mark ID', 'Recipient', 'Organisation', 'Techniques', 'Rows', 'Bits changed', 'Canaries', 'Issued', ''].map((h) => el('th', { text: h })))),
      tbody));
  }

  for (const sel of ['#tab-source', '#tab-matrix-source']) {
    const cur = $(sel).value;
    $(sel).replaceChildren(...issued.map((is) => el('option', { value: is.markId, text: `${is.recipient} (${is.markId})` })));
    if (issued.some((is) => is.markId === cur)) $(sel).value = cur;
    else if (issued.length) $(sel).value = issued[issued.length - 1].markId;
  }
  renderDropColumns();
}

function badge(label, on) {
  return el('span', { class: 'badge' + (on ? ' on' : ''), text: on ? label : `no ${label}` });
}

function renderDropColumns() {
  const is = (state.issued || []).find((x) => x.markId === $('#tab-source').value);
  const cols = [...state.columns];
  if (is?.techniques.dummy) cols.splice(cols.indexOf('city') + 1, 0, 'branch_ref');
  const checked = new Set([...document.querySelectorAll('#tab-drop input:checked')].map((i) => i.value));
  $('#tab-drop').replaceChildren(...cols.map((c) => el('label', {},
    el('input', { type: 'checkbox', value: c, checked: checked.has(c) }), el('code', { text: c }))));
  describeAttack();
}

function currentAttack() {
  return {
    dropColumns: [...document.querySelectorAll('#tab-drop input:checked')].map((i) => i.value),
    samplePct: Number($('#tab-sample').value),
    roundCoords: Number($('#tab-round-coords').value),
    roundRisk: Number($('#tab-round-risk').value),
    truncateTimestamps: $('#tab-trunc').checked,
    shuffle: $('#tab-shuffle').checked,
  };
}

function describeAttack() {
  const a = currentAttack();
  const parts = [];
  if (a.dropColumns.length) parts.push(`drop ${a.dropColumns.join(', ')}`);
  if (a.samplePct < 100) parts.push(`keep ${a.samplePct}% of rows`);
  if (a.roundCoords >= 0) parts.push(`round lat/lon to ${a.roundCoords} dp`);
  if (a.roundRisk >= 0) parts.push(`round risk to ${a.roundRisk} dp`);
  if (a.truncateTimestamps) parts.push('truncate timestamps');
  if (a.shuffle) parts.push('shuffled');
  $('#tab-leak-desc').textContent = parts.length ? parts.join(', ') : 'Unchanged copy';
}

initDataset();
