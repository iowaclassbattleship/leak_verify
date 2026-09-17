import { $$ } from './common.js';
import { initTag } from './tag.js';
import { initVerify, showVerify } from './verify.js';

const tabs = { tag: initTag, verify: initVerify };
const inited = {};

function show() {
  const tab = location.hash.slice(1) in tabs ? location.hash.slice(1) : 'tag';
  for (const s of $$('.module')) s.hidden = s.id !== tab;
  for (const a of $$('.tabs a')) a.classList.toggle('active', a.dataset.tab === tab);
  if (!inited[tab]) {
    inited[tab] = true;
    tabs[tab]();
  }
  if (tab === 'verify') showVerify();
}

window.addEventListener('hashchange', show);
show();
