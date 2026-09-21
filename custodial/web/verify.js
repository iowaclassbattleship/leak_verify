import { $, el, api, setupDropzone, verifyCard, fmtSize } from './common.js';

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
    const st = await api('/api/state');
    const n = (st.marks || []).length;
    $('#verify-hint').textContent = st.document.kind === 'pdf'
      ? 'PDF, PNG or JPEG, up to 25 MB'
      : `${st.document.kindName}, a PDF export or screenshot of it, or text copied out of it, up to 25 MB`;
    box.replaceChildren(
      el('span', { class: 'muted', text: 'Checking against: ' }),
      el('b', { text: st.document.title }),
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
    res = await api('/api/detect', { form });
  } catch (err) {
    card.className = 'verify-card error';
    card.replaceChildren(
      el('div', { class: 'vc-head' }, el('b', { text: file.name }), el('span', { class: 'muted small', text: fmtSize(file.size) })),
      el('div', { class: 'vc-status' }, el('span', { class: 'big', text: 'Could not read this file' })),
      el('p', { class: 'muted', text: err.message }));
    return;
  }
  verifyCard(card, file.name, fmtSize(file.size), res.report);
}
