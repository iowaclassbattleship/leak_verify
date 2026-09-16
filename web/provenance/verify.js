import { $, el, api, setupDropzone, verifyCard, fmtSize } from '../shared/common.js';

let wired = false;

export function initVerify() {
  if (wired) return;
  wired = true;
  setupDropzone($('#verify-drop'), $('#verify-input'), verifyFiles, $('#verify'));
}

// showVerify refreshes the context line each time the tab is opened.
export async function showVerify() {
  const box = $('#verify-context');
  try {
    const st = await api('/api/tab/state');
    const n = (st.marks || []).length;
    box.replaceChildren(
      el('span', { class: 'muted', text: 'Checking against' }),
      el('b', { text: st.source }),
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
  const head = () => el('div', { class: 'vc-head' },
    el('b', { text: file.name }), el('span', { class: 'muted small', text: fmtSize(file.size) }));
  const card = el('article', { class: 'verify-card pending' }, head(), el('div', { class: 'vc-status spinner', text: 'Checking' }));
  $('#verify-results').prepend(card);
  const form = new FormData();
  form.append('file', file);
  try {
    const res = await api('/api/tab/detect', { form });
    verifyCard(card, file.name, `${res.rows.toLocaleString()} rows, ${res.columns.length} columns`, res.report);
  } catch (err) {
    card.className = 'verify-card error';
    card.replaceChildren(head(),
      el('div', { class: 'vc-status' }, el('span', { class: 'big', text: 'Could not read this file' })),
      el('p', { class: 'muted', text: err.message }));
  }
}
