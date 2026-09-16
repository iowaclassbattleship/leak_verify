import { $, el, api, pill, renderCards, setupDropzone } from '../shared/common.js';

let wired = false;

export function initVerify() {
  if (wired) return;
  wired = true;
  setupDropzone($('#dropzone'), $('#verify-input'), verifyFiles, $('#verify'));
}

// showVerify refreshes the context line each time the tab is opened.
export async function showVerify() {
  const box = $('#verify-context');
  try {
    const st = await api('/api/doc/state');
    const n = (st.marks || []).length;
    box.replaceChildren(
      el('span', { class: 'muted', text: 'Checking against' }),
      el('b', { text: st.master.title }),
      el('span', { class: 'muted', text: `· ${n} cop${n === 1 ? 'y' : 'ies'} issued` }),
      ...(n === 0 ? [el('a', { class: 'warn-inline', href: '#tag', text: 'No copies issued yet' })] : []),
    );
  } catch (err) {
    box.replaceChildren(el('span', { class: 'muted', text: err.message }));
  }
}

export async function verifyFiles(files) {
  for (const file of files) await verifyFile(file);
}

export async function verifyFile(file) {
  const card = el('article', { class: 'verify-card pending' },
    el('div', { class: 'vc-head' }, el('b', { text: file.name }), el('span', { class: 'muted small', text: fmtSize(file.size) })),
    el('div', { class: 'vc-status spinner', text: 'Checking' }));
  $('#verify-results').prepend(card);

  const form = new FormData();
  form.append('file', file);
  let res;
  try {
    res = await api('/api/doc/detect', { form });
  } catch (err) {
    card.className = 'verify-card error';
    card.replaceChildren(
      el('div', { class: 'vc-head' }, el('b', { text: file.name }), el('span', { class: 'muted small', text: fmtSize(file.size) })),
      el('div', { class: 'vc-status' }, el('span', { class: 'big', text: 'Could not read this file' })),
      el('p', { class: 'muted', text: err.message }));
    return;
  }
  renderResult(card, file, res);
}

const HEADLINE = {
  attributed: 'Tagged',
  inconclusive: 'Possible tag',
  absent: 'No tag found',
};

function renderResult(card, file, res) {
  const rep = res.report;
  const v = rep.verdict;
  const status = v.status === 'attributed' ? 'attributed' : v.status === 'inconclusive' ? 'inconclusive' : 'absent';
  card.className = 'verify-card ' + status;

  const surviving = rep.results.filter((r) => r.status === 'attributed');
  let summary;
  if (status === 'attributed') {
    summary = el('div', {},
      el('div', { class: 'who' }, 'Issued to ', el('b', { text: v.recipient }), ' ', el('span', { class: 'mono', text: v.markId })),
      el('div', { class: 'muted small', text: [v.confidence, surviving.length ? 'Read from ' + surviving.map((r) => r.name).join(', ') : ''].filter(Boolean).join('. ') }));
  } else if (status === 'inconclusive') {
    summary = el('div', { class: 'muted', text: 'Signal found, but not enough to name a recipient.' });
  } else {
    summary = el('div', { class: 'muted', text: 'No tag survives in this file. Either it was not issued here, or every layer was destroyed.' });
  }

  card.replaceChildren(
    el('div', { class: 'vc-head' },
      el('b', { text: file.name }),
      el('span', { class: 'muted small', text: `${rep.input} · ${fmtSize(file.size)} · ${new Date().toLocaleTimeString()}` })),
    el('div', { class: 'vc-status' }, el('span', { class: 'big', text: HEADLINE[status] }), summary),
    el('div', { class: 'layer-chips' }, rep.results.map((r) => el('span', { class: 'chip', title: r.detail }, pill(r.status), ' ', r.name))),
    el('details', {},
      el('summary', { text: 'Layer detail' }),
      renderCards(el('div'), rep.results, null)));
}

function fmtSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
