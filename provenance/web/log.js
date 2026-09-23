import { $, el, api } from './common.js';
import { MEASURE } from './verify.js';

// The issuance log, filtered by who is looking. A viewer sees the copies
// issued to their own part of the hierarchy, and only Data Governance sees
// the mark IDs that tie a recovered file to a recipient.

let viewer = 'dpo';

function unitName(units, id) {
  return (units.find((u) => u.id === id) || {}).name || id;
}

function renderLog(st) {
  if (!Array.isArray(st.viewers)) {
    // A page newer than the server: the log needs a server restart.
    $('#log-scope').textContent = 'The server does not provide an issuance log yet. It is running an older build than this page; restart it after deploying.';
    $('#log-table').replaceChildren();
    return;
  }
  const sel = $('#log-viewer');
  if (!sel.options.length) {
    for (const v of st.viewers) sel.append(el('option', { value: v.id, text: v.name }));
    sel.value = viewer;
  }
  const parts = [`${st.viewer.name} sees copies issued to ${unitName(st.units, st.viewer.unit)}${st.viewer.unit === 'gov' ? ', which is everyone' : ''}.`];
  if (st.hidden) parts.push(`${st.hidden.toLocaleString('en')} outside that scope ${st.hidden === 1 ? 'is' : 'are'} hidden.`);
  if (st.viewer.unit !== 'gov') parts.push('Mark IDs are restricted to Data Governance.');
  $('#log-scope').textContent = parts.join(' ');

  const box = $('#log-table');
  if (!st.log || !st.log.length) {
    box.replaceChildren(el('div', { class: 'empty', text: 'Nothing issued in this scope yet.' }));
    return;
  }
  box.replaceChildren(el('div', { class: 'tablewrap scroll-hint' }, el('table', {},
    el('thead', {}, el('tr', {}, ['Mark', 'Recipient', 'Table', 'Issued', 'Measures', 'File'].map((h) => el('th', { text: h })))),
    el('tbody', {}, [...st.log].reverse().map((e) => el('tr', {},
      el('td', {}, e.markId === 'restricted' ? el('span', { class: 'dim', text: 'restricted' }) : el('b', { class: 'mono', text: e.markId })),
      el('td', {}, el('b', { text: e.recipient }),
        el('span', { class: 'sub', text: [e.purpose, unitName(st.units, e.unit)].filter(Boolean).join(' · ') })),
      el('td', { class: 'wrap', text: e.source }),
      el('td', { text: new Date(e.issuedAt).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' }) }),
      el('td', { class: 'wrap dim', text: Object.keys(MEASURE).filter((k) => e.techniques?.[k]).map((k) => MEASURE[k]).join(', ') }),
      el('td', { class: 'wrap mono dim', text: e.fileName })))))));
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
