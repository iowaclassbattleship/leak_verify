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

export function renderTable(container, columns, rows, { empty = 'No rows.' } = {}) {
  container.replaceChildren();
  if (!rows || rows.length === 0) {
    container.append(el('div', { class: 'empty', text: empty }));
    return;
  }
  container.append(el('table', {},
    el('thead', {}, el('tr', {}, columns.map((c) => el('th', { text: c })))),
    el('tbody', {}, rows.map((r) => el('tr', {}, r.map((v) => el('td', { text: v })))))));
}

const LABEL = { attributed: 'Attributed', inconclusive: 'Inconclusive', absent: 'Not found', 'n/a': 'n/a', wrong: 'Wrong recipient' };

function effectiveStatus(result, truth) {
  if (result.status === 'attributed' && truth && result.markId && result.markId !== truth.markId) return 'wrong';
  return result.status;
}

export function statusTag(status) {
  return el('span', { class: 'tag ' + (status === 'n/a' ? 'na' : status), text: LABEL[status] || status });
}

function decisionBlock(d) {
  if (!d || d.status === 'absent') return null;
  const ranking = (d.ranking || []).slice(0, 3);
  return el('div', { class: 'small dim' },
    `Decoded ${d.decodedId}${d.decodedInLog ? '' : ' (not in the log)'}`,
    ranking.length ? el('ol', {}, ranking.map((r) =>
      el('li', {}, `${r.recipient}: ${Math.round(r.agreement * 100)}% match, ${r.errors} bit errors`))) : null);
}

// renderCards appends one card per technique and returns the container.
export function renderCards(container, results, truth) {
  container.append(el('div', { class: 'cards' }, results.map((r) => el('div', { class: 'card' },
    el('div', { class: 'top' },
      el('div', {}, r.stage ? el('div', { class: 'stage', text: r.stage }) : null, el('b', { text: r.name })),
      statusTag(effectiveStatus(r, truth))),
    r.status === 'attributed' ? el('p', {}, r.recipient, ' ', el('span', { class: 'mono dim', text: r.markId })) : null,
    r.confidence ? el('p', { class: 'small', text: r.confidence }) : null,
    el('p', { text: r.detail }),
    decisionBlock(r.decision)))));
  return container;
}

// renderReport shows the verdict and every technique, with the simulated
// source when the caller knows it.
export function renderReport(container, report, truth) {
  container.replaceChildren();
  const v = report.verdict;
  const status = effectiveStatus(v, truth);
  const correct = truth && v.status === 'attributed' && v.markId === truth.markId;
  container.append(el('div', { class: 'result ' + status },
    el('div', { class: 'head' },
      el('span', { text: report.rows ? `${report.rows.toLocaleString()} rows` : '' }),
      truth ? el('span', {}, 'simulated source: ', el('b', { text: truth.recipient }), ' ', statusTag(correct ? 'attributed' : status)) : null),
    el('div', { class: 'verdict' },
      v.status === 'attributed' ? `Traced to ${v.recipient}` : v.status === 'inconclusive' ? 'Inconclusive' : 'No mark found'),
    el('p', { class: 'small dim', text: [v.confidence, v.detail].filter(Boolean).join('. ') }),
    renderCards(el('div'), report.results, truth)));
}

// verifyCard renders the result for one dropped file.
export function verifyCard(card, name, size, rep) {
  const v = rep.verdict;
  const status = v.status === 'attributed' ? 'attributed' : v.status === 'inconclusive' ? 'inconclusive' : 'absent';
  card.className = 'result ' + status;
  const surviving = rep.results.filter((r) => r.status === 'attributed');
  const summary = status === 'attributed'
    ? el('div', {},
      el('div', {}, 'Issued to ', el('b', { text: v.recipient }), ' ', el('span', { class: 'mono dim', text: v.markId })),
      el('p', { class: 'small dim', text: [v.confidence, surviving.length ? 'Read from ' + surviving.map((r) => r.name).join(', ') : ''].filter(Boolean).join('. ') }))
    : el('p', {
      class: 'small dim', text: status === 'inconclusive'
        ? 'Signal found, but not enough to name a recipient.'
        : 'No mark survives in this file. Either it was not issued here, or every technique was destroyed.',
    });

  card.replaceChildren(
    el('div', { class: 'head' }, el('b', { text: name }), el('span', { text: [size, new Date().toLocaleTimeString()].filter(Boolean).join(' · ') })),
    el('div', { class: 'verdict', text: status === 'attributed' ? 'Marked' : status === 'inconclusive' ? 'Possible mark' : 'No mark found' }),
    summary,
    el('div', { class: 'chips' }, rep.results.map((r) => el('span', { class: 'chip', title: r.detail }, statusTag(r.status), r.name))),
    el('details', {}, el('summary', { text: 'Technique detail' }), renderCards(el('div'), rep.results, null)));
}

const SYM = { attributed: '✓', wrong: '✗', inconclusive: '~', absent: '·', 'n/a': 'n/a' };

export function renderMatrix(container, data) {
  container.replaceChildren();
  const symCell = (c) => {
    const st = c.status === 'attributed' ? (c.correct ? 'attributed' : 'wrong') : c.status;
    return el('td', { title: (c.who ? `${c.who}\n` : '') + (c.detail || '') },
      el('span', { class: 'sym ' + (st === 'n/a' ? 'na' : st), text: SYM[st] || st }));
  };
  container.append(
    el('p', { class: 'small dim' }, 'Each attack applied to the copy issued to ', el('b', { text: data.recipient }), '. Hover a cell for detail.'),
    el('div', { class: 'legend' },
      el('span', {}, el('b', { style: 'color:var(--accent)', text: '✓' }), ' correct recipient'),
      el('span', {}, el('b', { style: 'color:var(--bad)', text: '✗' }), ' wrong recipient'),
      el('span', {}, el('b', { style: 'color:var(--warn)', text: '~' }), ' inconclusive'),
      el('span', {}, el('b', { class: 'dim', text: '·' }), ' not found')),
    el('div', { class: 'tablewrap' },
      el('table', { class: 'matrix' },
        el('thead', {}, el('tr', {}, el('th', { text: 'Attack' }), data.techniques.map((t) => el('th', { text: t })), el('th', { text: 'Overall' }))),
        el('tbody', {}, data.rows.map((row) => el('tr', {},
          el('td', { title: row.description }, row.attack),
          row.cells.map((c) => symCell(c)),
          symCell(row.verdict)))))));
}

export function fmtSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
