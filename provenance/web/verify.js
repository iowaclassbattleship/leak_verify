import { $, el, api, toast } from './common.js';

// Verify checks a recovered file against the copy the Mark tab is configured
// to produce. Each carrier reports separately, then the verdict pools them.

const LABEL = { attributed: 'Match', inconclusive: 'Inconclusive', absent: 'Not found' };

function tag(status) {
  return el('span', { class: 'tag ' + status, text: LABEL[status] || status });
}

function renderReport(name, size, rep) {
  const v = rep.verdict;
  const headline = v.status === 'attributed' ? 'Marked copy identified'
    : v.status === 'inconclusive' ? 'Possible mark' : 'No mark found';

  return el('div', { class: 'result ' + v.status },
    el('div', { class: 'head' },
      el('b', { text: name }),
      el('span', { text: `${rep.rows.toLocaleString()} rows, ${rep.columns.length} columns, ${Math.round(size / 1024)} KB` })),
    el('div', { class: 'verdict', text: headline }),
    v.markId ? el('div', {}, 'Matches ', el('b', { text: v.markId })) : null,
    el('p', { class: 'small dim', text: v.detail }),
    el('p', { class: 'small dim', text: `${rep.resolvedRows.toLocaleString()} of ${rep.rows.toLocaleString()} rows could be matched back to a source row.` }),
    el('div', { class: 'tablewrap' },
      el('table', {},
        el('thead', {}, el('tr', {},
          el('th', { text: 'Carrier' }), el('th', { text: 'Stage' }),
          el('th', { text: 'Result' }), el('th', { text: 'Detail' }))),
        el('tbody', {}, rep.results.map((r) =>
          el('tr', {},
            el('td', { text: r.name }),
            el('td', { class: 'dim', text: r.stage }),
            el('td', {}, tag(r.status)),
            el('td', { class: 'wrap' }, r.detail,
              r.confidence ? el('div', { class: 'dim', text: r.confidence }) : null)))))));
}

async function check(file) {
  const box = $('#verify-results');
  box.replaceChildren(el('div', { class: 'empty spinner', text: 'Checking ' }));
  const form = new FormData();
  form.append('file', file);
  try {
    const res = await api('/api/detect', { form });
    box.replaceChildren(renderReport(res.name, res.size, res.report));
  } catch (err) {
    box.replaceChildren(el('div', { class: 'result error' },
      el('div', { class: 'verdict', text: 'Could not check the file' }),
      el('p', { class: 'small dim', text: err.message })));
    toast(err.message, true);
  }
}

export async function showVerify() {
  // Read-only: /api/preview would re-register the copy and wipe the marking.
  try {
    const st = await api('/api/state');
    const on = (st.issued || []).flatMap((i) =>
      Object.entries(i.techniques || {}).filter(([k, v]) => v === true && k !== 'params').map(([k]) => k));
    $('#verify-context').textContent = on.length
      ? `Checking against ${st.source}, ${st.rows.toLocaleString()} rows, marked with ${on.join(', ')}.`
      : `Checking against ${st.source}, ${st.rows.toLocaleString()} rows. No measures are set on the Mark tab yet.`;
  } catch (err) {
    $('#verify-context').textContent = err.message;
  }
}

export function initVerify() {
  const zone = $('#verify-drop');
  $('#verify-file').addEventListener('change', (e) => {
    const f = e.target.files[0];
    e.target.value = '';
    if (f) check(f);
  });
  for (const ev of ['dragenter', 'dragover']) {
    zone.addEventListener(ev, (e) => { e.preventDefault(); zone.classList.add('over'); });
  }
  for (const ev of ['dragleave', 'drop']) {
    zone.addEventListener(ev, () => zone.classList.remove('over'));
  }
  zone.addEventListener('drop', (e) => {
    e.preventDefault();
    const f = e.dataTransfer.files[0];
    if (f) check(f);
  });
}
