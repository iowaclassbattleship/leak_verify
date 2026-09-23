import { $, el, api, setupDropzone, verifyCard, fmtSize } from './common.js';

let wired = false;

export function initVerify() {
  if (wired) return;
  wired = true;
  setupDropzone($('#dropzone'), $('#verify-input'), verifyFiles, $('#verify'));
}

// showVerify refreshes the context line each time the tab is opened. A
// recovered file is checked against every copy in the log, whichever
// document is loaded on the Tag tab.
export async function showVerify() {
  const box = $('#verify-context');
  try {
    const st = await api('/api/state');
    const n = (st.marks || []).length;
    const docs = new Set((st.log || []).map((e) => e.source || e.fileName)).size;
    box.replaceChildren(
      el('span', { class: 'muted', text: 'Checking against the issuance log: ' }),
      el('b', { text: `${n.toLocaleString()} ${n === 1 ? 'copy' : 'copies'}` }),
      docs > 0 ? el('span', { class: 'muted', text: ` of ${docs.toLocaleString()} ${docs === 1 ? 'document' : 'documents'}` }) : null,
      ...(n === 0 ? [' ', el('a', { class: 'warn-inline', href: '#tag', text: 'No copies issued yet' })] : []),
    );
  } catch (err) {
    box.replaceChildren(el('span', { class: 'muted', text: err.message }));
  }
}

export async function verifyFiles(files) {
  // Each drop replaces the last result. Several files dropped together still
  // all get a card.
  $('#verify-results').replaceChildren();
  for (const file of files) await verifyFile(file);
}

export async function verifyFile(file) {
  const card = el('article', { class: 'verify-card pending' },
    el('div', { class: 'vc-head' }, el('b', { text: file.name }), el('span', { class: 'muted small', text: fmtSize(file.size) })),
    el('div', { class: 'vc-status spinner', text: 'Checking' }));
  $('#verify-results').append(card);

  let res;
  let note = '';
  try {
    try {
      res = await detect(file);
    } catch (err) {
      // A proxy in front of the application may cap uploads below the 25 MB
      // the application accepts. A capture survives downscaling and JPEG, so
      // shrink it and try once more rather than giving up.
      if (err.status !== 413 || !/^image\/(png|jpeg)$/.test(file.type)) throw err;
      const smaller = await shrinkImage(file, 900 * 1024);
      if (!smaller) throw err;
      res = await detect(smaller);
      note = `Too large for the upload limit, so it was checked as a ${fmtSize(smaller.size)} JPEG.`;
    }
  } catch (err) {
    card.className = 'verify-card error';
    card.replaceChildren(
      el('div', { class: 'vc-head' }, el('b', { text: file.name }), el('span', { class: 'muted small', text: fmtSize(file.size) })),
      el('div', { class: 'vc-status' }, el('span', { class: 'big', text: 'Could not read this file' })),
      el('p', { class: 'muted', text: err.status === 413
        ? `The web server in front of Custodial refused ${fmtSize(file.size)} before the application saw it. Its upload limit needs raising to 25 MB (client_max_body_size in nginx).`
        : err.message }));
    return;
  }
  verifyCard(card, file.name, fmtSize(file.size), res.report, res.issuance);
  if (note) card.querySelector('.vc-head').after(el('p', { class: 'muted small', text: note }));
}

function detect(file) {
  const form = new FormData();
  form.append('file', file);
  return api('/api/detect', { form });
}

// shrinkImage re-encodes an image as JPEG, scaling it down until it fits in
// limit bytes. It gives up below 40% of the original size, which is as far as
// the detectors are known to hold.
async function shrinkImage(file, limit) {
  let bitmap;
  try {
    bitmap = await createImageBitmap(file);
  } catch {
    return null;
  }
  const canvas = document.createElement('canvas');
  for (const [scale, quality] of [[1, 0.92], [0.8, 0.9], [0.64, 0.88], [0.5, 0.85], [0.4, 0.8]]) {
    canvas.width = Math.round(bitmap.width * scale);
    canvas.height = Math.round(bitmap.height * scale);
    const ctx = canvas.getContext('2d');
    ctx.fillStyle = '#fff';
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
    const blob = await new Promise((resolve) => canvas.toBlob(resolve, 'image/jpeg', quality));
    if (blob && blob.size <= limit) {
      return new File([blob], file.name.replace(/\.\w+$/, '') + '.jpg', { type: 'image/jpeg' });
    }
  }
  return null;
}
