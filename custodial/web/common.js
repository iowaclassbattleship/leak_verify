export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) {
    if (c === undefined || c === null || c === false) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

// setText writes to an element that the markup is free to leave out, for
// captions and hints that carry no state.
export function setText(sel, text) {
  const node = $(sel);
  if (node) node.textContent = text;
}

export async function api(path, { method = 'GET', json, form } = {}) {
  const opts = { method, headers: {} };
  if (json !== undefined) {
    opts.method = method === 'GET' ? 'POST' : method;
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(json);
  } else if (form) {
    opts.method = 'POST';
    opts.body = form;
  }
  const res = await fetch(path, opts);
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `${res.status} ${res.statusText}`);
  return body;
}

let toastTimer;
export function toast(msg, isError = false) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'toast' + (isError ? ' error' : '');
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), isError ? 6000 : 2800);
}

export async function busy(button, fn) {
  const label = button.textContent;
  button.disabled = true;
  button.classList.add('spinner');
  try {
    return await fn();
  } catch (err) {
    toast(err.message, true);
    console.error(err);
  } finally {
    button.disabled = false;
    button.classList.remove('spinner');
    button.textContent = label;
  }
}

export function renderTable(container, columns, rows, { highlight = [], empty = 'No rows.' } = {}) {
  container.replaceChildren();
  if (!rows || rows.length === 0) {
    container.append(el('div', { class: 'empty', text: empty }));
    return;
  }
  const hl = new Set(highlight);
  const table = el('table', {},
    el('thead', {}, el('tr', {}, columns.map((c) => el('th', { class: hl.has(c) ? 'hl' : null, text: c })))),
    el('tbody', {}, rows.map((r) => el('tr', {}, r.map((v, i) => el('td', { class: hl.has(columns[i]) ? 'hl' : null, text: v }))))));
  container.append(table);
}

const LABEL = { attributed: 'Attributed', inconclusive: 'Inconclusive', absent: 'Not found', 'n/a': 'Not applicable', wrong: 'Wrong recipient' };

export function effectiveStatus(result, truth) {
  if (result.status === 'attributed' && truth && result.markId && result.markId !== truth.markId) return 'wrong';
  return result.status;
}

export function pill(status) {
  return el('span', { class: 'pill ' + (status === 'n/a' ? 'na' : status), text: LABEL[status] || status });
}

function decisionBlock(d) {
  if (!d || d.status === 'absent') return null;
  const ranking = (d.ranking || []).slice(0, 3);
  return el('div', { class: 'small muted' },
    `Decoded mark ${d.decodedId}${d.decodedInLog ? '' : ' (not in the log)'}. Closest matches:`,
    ranking.length ? el('ol', { class: 'rank' }, ranking.map((r) =>
      el('li', {}, `${r.recipient}: ${Math.round(r.agreement * 100)}% match, ${r.errors} bit errors`))) : null);
}

export function renderReport(container, report, truth) {
  container.replaceChildren();
  const v = report.verdict;
  const vs = effectiveStatus(v, truth);
  const correct = truth && v.status === 'attributed' && v.markId === truth.markId;
  container.append(el('div', { class: 'verdict ' + vs },
    el('div', {},
      el('div', { class: 'small muted', text: (report.input ? report.input + ' · ' : '') + (v.name || 'Overall verdict') }),
      el('div', { class: 'big' },
        v.status === 'attributed' ? `Traced to ${v.recipient}` :
          v.status === 'inconclusive' ? 'Inconclusive' : 'No mark found'),
      el('div', { class: 'small', text: [v.confidence, v.detail].filter(Boolean).join('. ') })),
    truth ? el('div', { class: 'truth' },
      el('div', { class: 'muted small', text: 'Simulated leak source' }),
      el('div', {}, `${truth.recipient} · `, el('span', { class: 'mono', text: truth.markId })),
      el('div', {}, v.status === 'attributed' ? pill(correct ? 'attributed' : 'wrong') : pill(v.status))) : null));

  renderCards(container, report.results, truth);
}

// renderCards appends one evidence card per technique/layer and returns the container.
export function renderCards(container, results, truth) {
  container.append(el('div', { class: 'cards' }, results.map((r) => {
    const s = effectiveStatus(r, truth);
    return el('div', { class: 'card' },
      el('div', { class: 'top' },
        el('div', {}, r.stage ? el('div', { class: 'stage', text: r.stage }) : null, el('b', { text: r.name })),
        pill(s)),
      r.status === 'attributed' ? el('div', { class: 'who' }, r.recipient, ' ', el('span', { class: 'mono muted', text: r.markId })) : null,
      r.confidence ? el('div', { class: 'conf', text: r.confidence }) : null,
      el('div', { class: 'detail', text: r.detail }),
      decisionBlock(r.decision),
      r.survives ? el('div', { class: 'survives', text: r.survives }) : null);
  })));
  return container;
}

const SYM = { attributed: '✓', wrong: '✗', inconclusive: '~', absent: '·', 'n/a': 'n/a' };

export function renderMatrix(container, data) {
  container.replaceChildren();
  const symCell = (c, extra = '') => {
    const st = c.status === 'attributed' ? (c.correct ? 'attributed' : 'wrong') : c.status;
    const title = (c.who ? `${c.who}\n` : '') + (c.detail || '');
    return el('td', { class: extra, title }, el('span', { class: 'sym ' + (st === 'n/a' ? 'na' : st), text: SYM[st] || st }));
  };
  container.append(
    el('p', { class: 'muted' }, 'Each attack applied to the copy issued to ', el('b', { text: data.recipient }), '. Hover a cell for detail.'),
    el('div', { class: 'legend' },
      el('span', {}, el('b', { class: 'sym', style: 'color:var(--ok)', text: '✓' }), ' correct recipient'),
      el('span', {}, el('b', { style: 'color:var(--bad)', text: '✗' }), ' wrong recipient'),
      el('span', {}, el('b', { style: 'color:var(--warn)', text: '~' }), ' inconclusive'),
      el('span', {}, el('b', { style: 'color:#98a2b3', text: '·' }), ' not found')),
    el('div', { class: 'table-wrap' },
      el('table', { class: 'matrix' },
        el('thead', {}, el('tr', {}, el('th', { text: 'Attack' }), data.techniques.map((t) => el('th', { text: t })), el('th', { class: 'verdict-col', text: 'Overall' }))),
        el('tbody', {}, data.rows.map((row) => el('tr', {},
          el('td', { title: row.description }, row.attack),
          row.cells.map((c) => symCell(c)),
          symCell(row.verdict, 'verdict-col')))))));
}

export function fmtTime(iso) {
  const d = new Date(iso);
  return d.toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'medium' });
}

let pageDropGuard = false;

// setupDropzone wires a file-drop label: click or keyboard opens the picker,
// and files dropped anywhere on `area` are delivered to onFiles.
export function setupDropzone(zone, input, onFiles, area = zone) {
  input.addEventListener('change', () => {
    if (input.files.length) onFiles([...input.files]);
    input.value = '';
  });
  zone.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      input.click();
    }
  });
  let depth = 0;
  area.addEventListener('dragenter', (e) => {
    e.preventDefault();
    depth++;
    zone.classList.add('over');
  });
  area.addEventListener('dragover', (e) => e.preventDefault());
  area.addEventListener('dragleave', () => {
    if (--depth <= 0) {
      depth = 0;
      zone.classList.remove('over');
    }
  });
  area.addEventListener('drop', (e) => {
    e.preventDefault();
    depth = 0;
    zone.classList.remove('over');
    if (e.dataTransfer.files.length) onFiles([...e.dataTransfer.files]);
  });
  if (!pageDropGuard) {
    // Keep the browser from navigating to a file dropped just outside the area.
    pageDropGuard = true;
    window.addEventListener('dragover', (e) => e.preventDefault());
    window.addEventListener('drop', (e) => e.preventDefault());
  }
}

const HEADLINE = {
  attributed: 'Tagged',
  inconclusive: 'Possible tag',
  absent: 'No tag found',
};

export function verifyCard(card, name, size, rep) {
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
      el('b', { text: name }),
      el('span', { class: 'muted small', text: [rep.input, size, new Date().toLocaleTimeString()].filter(Boolean).join(' · ') })),
    el('div', { class: 'vc-status' }, el('span', { class: 'big', text: HEADLINE[status] }), summary),
    el('div', { class: 'layer-chips' }, rep.results.map((r) => el('span', { class: 'chip', title: r.detail }, pill(r.status), ' ', r.name))),
    el('details', {},
      el('summary', { text: 'Layer detail' }),
      renderCards(el('div'), rep.results, null)));
}

export function fmtSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
