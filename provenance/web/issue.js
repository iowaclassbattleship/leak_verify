import { $, el, api, apiURL, busy, toast } from './common.js';

// Issuing gives each recipient their own copy of the table, marked with
// whatever measures are selected above. The issuance log is what turns a
// decoded mark back into a name.

let people = [];

function renderPeople() {
  $('#issue-empty').hidden = people.length > 0;
  $('#issue-create').disabled = people.length === 0;
  $('#issue-create').textContent = people.length > 1 ? `Issue ${people.length} copies` : 'Issue copy';
  $('#issue-list').replaceChildren(...people.map((p, i) =>
    el('div', { class: 'item' },
      el('span', { class: 'who' }, el('b', { text: p.name }), p.org ? el('small', { text: p.org }) : null),
      el('button', { class: 'link', type: 'button', text: 'remove', onclick: () => { people.splice(i, 1); renderPeople(); } }))));
}

// The list is the whole issuance log, not just what was issued a moment ago,
// so it still shows what this table has been handed to after a reload.
async function refreshCopies() {
  const box = $('#issue-copies');
  let copies = [];
  try {
    const st = await api('/api/state');
    copies = (st.issued || []).filter((c) => c.recipient !== markTabCopy);
  } catch {
    return;
  }
  if (copies.length === 0) {
    box.replaceChildren();
    return;
  }
  box.replaceChildren(
    el('h4', { text: copies.length > 1 ? `${copies.length} copies issued` : 'Copy issued' }),
    ...copies.map((c) => el('div', { class: 'copy' },
      el('span', { class: 'who' }, el('b', { text: c.recipient }), c.org ? el('small', { class: 'muted', text: c.org }) : null),
      el('span', { class: 'fname', text: c.fileName }),
      el('span', { class: 'muted small' }, 'mark ', el('span', { class: 'mono', text: c.markId })),
      el('a', { class: 'button', href: apiURL(`/api/copy?mark=${c.markId}`), download: c.fileName, text: 'Download' }),
      el('button', {
        class: 'recall', type: 'button', title: `Recall ${c.markId}`, text: '\u00d7',
        onclick: () => recall(c),
      }))),
    copies.length > 1
      ? el('p', {}, el('a', {
          class: 'button', text: 'Download all (zip)',
          href: apiURL(`/api/bundle?marks=${copies.map((c) => c.markId).join(',')}`),
        }))
      : null);
}

const markTabCopy = 'the copy from the Mark tab';

async function recall(c) {
  if (!confirm(`Recall ${c.markId}, issued to ${c.recipient}?\n\nThe copy they already have is unaffected. What goes is the record of who holds it, so a recovered file will no longer be traced to them.`)) return;
  try {
    await api('/api/recall', { json: { mark: c.markId } });
    await refreshCopies();
  } catch (err) {
    toast(err.message, true);
  }
}

export function setIssueVisible(on) {
  $('#mark-issue').hidden = !on;
  if (!on) {
    people = [];
    $('#issue-copies').replaceChildren();
    renderPeople();
    return;
  }
  refreshCopies();
}

// measures() lives in mark.js; it is passed in so both panels always agree.
export function initIssue(measures) {
  renderPeople();
  $('#issue-add').addEventListener('submit', (e) => {
    e.preventDefault();
    // form.elements, because form.name is the form's own name attribute.
    const f = e.target.elements;
    const name = f.name.value.trim();
    if (!name) return;
    people.push({ name, org: f.org.value.trim() });
    e.target.reset();
    f.name.focus();
    renderPeople();
  });
  $('#issue-create').addEventListener('click', async (e) => {
    // busy() restores the button's label and enabled state when it finishes,
    // so the list is re-rendered after it returns, not inside it.
    const copies = await busy(e.target, () =>
      api('/api/issue', { json: { recipients: people, techniques: measures() } }));
    if (!copies) return;
    people = [];
    renderPeople();
    await refreshCopies();
  });
}
