import { $, el, api } from './common.js';

// The issuance log, filtered by who is looking. A viewer sees entries for
// recipients in their own part of the hierarchy, and only the Security Office
// sees the mark IDs that tie a recovered copy to a person.

let viewer = 'cso';

function unitName(units, id) {
  return (units.find((u) => u.id === id) || {}).name || id;
}

function renderLog(st) {
  const sel = $('#log-viewer');
  if (!sel.options.length) {
    for (const v of st.viewers) sel.append(el('option', { value: v.id, text: `${v.name}` }));
    sel.value = viewer;
  }

  const scope = unitName(st.units, st.viewer.unit);
  const parts = [`${st.viewer.name} sees issuances to ${scope} and everything below it.`];
  if (st.hidden) parts.push(`${st.hidden} outside that scope ${st.hidden === 1 ? 'is' : 'are'} hidden.`);
  if (st.viewer.unit !== 'sec') parts.push('Mark IDs are restricted to the Security Office.');
  $('#log-scope').textContent = parts.join(' ');

  const box = $('#log-table');
  if (!st.log || !st.log.length) {
    box.replaceChildren(el('div', { class: 'empty', text: 'Nothing issued in this scope yet.' }));
    return;
  }
  const docName = (e) => e.document || (e.source || '').replace(/, \d+ pages?$/, '') || '';
  box.replaceChildren(el('div', { class: 'tablewrap scroll-hint' }, el('table', { class: 'log' },
    el('thead', {}, el('tr', {},
      ['Mark', 'Recipient', 'Document', 'Issued', 'Layers', 'File', ''].map((h) => el('th', { text: h })))),
    el('tbody', {}, st.log.map((e) => el('tr', {},
      el('td', {}, e.markId === 'restricted'
        ? el('span', { class: 'muted', text: 'restricted' })
        : el('b', { class: 'mono', text: e.markId })),
      el('td', {}, el('b', { text: e.name }), el('small', { class: 'muted sub', text: `${e.role} · ${unitName(st.units, e.unit)}` })),
      el('td', { class: 'wrap', text: docName(e) }),
      el('td', {}, new Date(e.issuedAt).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' }),
        e.issuedBy ? el('small', { class: 'muted sub', text: `by ${e.issuedBy}` }) : null),
      el('td', { class: 'muted wrap', text: Object.entries(e.layers || {}).filter(([, v]) => v).map(([k]) => k).join(', ') }),
      el('td', { class: 'muted wrap mono', text: e.fileName }),
      el('td', {}, e.markId === 'restricted'
        ? null
        : el('button', {
            class: 'recall', type: 'button', title: `Recall ${e.markId}`, 'aria-label': `Recall ${e.markId}`, text: '\u00d7',
            onclick: () => recall(e),
          }))))))));
}

async function recall(e) {
  if (!confirm(`Recall ${e.markId}, issued to ${e.name}?\n\nThe copy they already have is unaffected. What goes is the record of who holds it, so a recovered file will no longer be traced to them.`)) return;
  try {
    await api('/api/recall', { json: { mark: e.markId } });
    await showLog();
  } catch (err) {
    $('#log-scope').textContent = err.message;
  }
}

export async function showLog() {
  try {
    renderLog(await api(`/api/state?viewer=${encodeURIComponent(viewer)}`));
  } catch (err) {
    $('#log-scope').textContent = err.message;
  }
}

export function initLog() {
  $('#log-viewer').addEventListener('change', (e) => {
    viewer = e.target.value;
    showLog();
  });
}
