import { $, el, api, toast, countNoun, setupDropzone } from './common.js';

// Verify checks a recovered file against the issuance log. Each carrier
// reports separately, then the verdict pools them.

const LABEL = { attributed: 'Match', inconclusive: 'Inconclusive', absent: 'Not found' };

// MEASURE names each technique the way the Mark tab does.
export const MEASURE = {
  canary: 'canary rows', lowBit: 'low-order bits', dummy: 'dummy column', allocate: 'allocation',
  order: 'tuple ordering', format: 'free choices', noise: 'noise', redact: 'redaction',
};

function tag(status) {
  return el('span', { class: 'tag ' + status, text: LABEL[status] || status });
}

// whoCard names a recipient: who, what for, and when they got the data.
function whoCard(markId, label, who) {
  if (!who) {
    return el('div', { class: 'who-card' }, el('b', { text: label || 'Unassigned copy from the Mark tab' }), ' ',
      el('span', { class: 'mono dim', text: markId }),
      label ? null : el('div', { class: 'small dim', text: 'Not issued to anyone yet. Issue copies to name a recipient.' }));
  }
  return el('div', { class: 'who-card' },
    el('b', { text: who.recipient }), ' ', el('span', { class: 'mono dim', text: markId }),
    el('div', { class: 'small purpose' }, [
      who.purpose ? `Issued for ${who.purpose}` : 'Issued',
      `on ${new Date(who.issuedAt).toLocaleDateString(undefined, { dateStyle: 'medium' })}`,
      who.fileName ? `as ${who.fileName}` : '',
    ].filter(Boolean).join(' ')));
}

function renderReport(name, size, res) {
  const rep = res.report;
  const v = rep.verdict;
  const who = res.issuances || {};
  let headline, summary;
  switch (v.status) {
    case 'attributed':
      headline = 'Marked copy identified';
      summary = [whoCard(v.markId, who[v.markId] ? null : v.recipient, who[v.markId]),
        el('p', { class: 'small dim', text: v.detail })];
      break;
    case 'merged':
      headline = 'Merged copies detected';
      summary = [
        el('p', { text: 'Rows from more than one recipient’s copy are in this file. The copies were merged, or the recipients shared data.' }),
        el('ul', { class: 'candidates' }, (v.candidates || []).map((c) => el('li', {},
          whoCard(c.markId, who[c.markId] ? null : c.recipient, who[c.markId]),
          el('span', { class: 'small dim', text: `Found by ${c.layers.join(', ')}` })))),
      ];
      break;
    case 'inconclusive':
      headline = 'Possible mark';
      summary = [el('p', { class: 'small dim', text: v.detail })];
      break;
    default:
      headline = rep.matchesSource ? 'No mark: this matches the unmarked source' : 'No mark found';
      summary = [el('p', { class: 'small dim', text: v.detail })];
  }

  const from = res.table ? `Matched against the copies of ${res.table}.` : '';
  return el('div', { class: 'result ' + v.status },
    el('div', { class: 'head' },
      el('b', { text: name }),
      el('span', { text: `${countNoun(rep.rows, 'row', 'rows')}, ${countNoun(rep.columns.length, 'column', 'columns')}, ${Math.max(1, Math.round(size / 1024)).toLocaleString('en')} KB` })),
    el('div', { class: 'verdict', text: headline }),
    ...summary,
    from && (v.status === 'attributed' || v.status === 'merged') ? el('p', { class: 'small dim', text: from }) : null,
    el('p', { class: 'small dim', text: `${rep.resolvedRows.toLocaleString('en')} of ${countNoun(rep.rows, 'row', 'rows')} could be matched back to a source row.` }),
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
    box.replaceChildren(renderReport(res.name, res.size, res));
  } catch (err) {
    box.replaceChildren(el('div', { class: 'result error' },
      el('div', { class: 'verdict', text: 'Could not check the file' }),
      el('p', { class: 'small dim', text: err.message })));
    toast(err.message, true);
  }
}

// showVerify describes what a file will be checked against: every copy on
// the issuance log, whichever table the Mark tab has loaded, and says which
// tables cannot be checked because they are not loaded in this session.
export async function showVerify() {
  try {
    const st = await api('/api/state');
    const log = st.log || [];
    const held = new Set(st.held || []);
    const tables = [...new Set(log.map((e) => e.source))];
    const checkable = tables.filter((t) => held.has(t));
    const missing = tables.filter((t) => !held.has(t));
    const copies = log.filter((e) => held.has(e.source)).length;
    const used = new Set(log.flatMap((e) => Object.keys(MEASURE).filter((k) => e.techniques?.[k])));
    const measures = Object.keys(MEASURE).filter((k) => used.has(k)).map((k) => MEASURE[k]);
    const parts = [];
    if (!log.length) {
      parts.push('Nothing has been issued yet. Issue copies on the Mark tab, then check a recovered file here.');
    } else if (copies) {
      parts.push(`Checking against the issuance log: ${countNoun(copies, 'copy', 'copies')} of ${checkable.join(', ')}, marked with ${measures.join(', ') || 'no measures'}.`);
    }
    if (missing.length) {
      parts.push(`${missing.join(', ')} ${missing.length === 1 ? 'is' : 'are'} not loaded in this session, so ${missing.length === 1 ? 'its copies' : 'their copies'} cannot be checked. Load ${missing.length === 1 ? 'it' : 'them'} on the Mark tab first.`);
    }
    $('#verify-context').textContent = parts.join(' ');
  } catch (err) {
    $('#verify-context').textContent = err.message;
  }
}

export function initVerify() {
  setupDropzone($('#verify-drop'), $('#verify-file'), (files) => check(files[0]), $('#verify'));
}
