import { $$ } from './common.js';
import { initMark } from './mark.js';
import { initVerify, showVerify } from './verify.js';

const tabs = { mark: initMark, verify: initVerify };
const inited = {};

function show() {
  const tab = location.hash.slice(1) in tabs ? location.hash.slice(1) : 'mark';
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
