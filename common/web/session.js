// Shows who is signed in, in every masthead, and checks that the server is
// as new as this page. The frontends are served from disk at every request
// but the server only changes when it is rebuilt and restarted, so after a
// deploy the two can disagree. Raise apiVersion together with APIVersion in
// internal/app/app.go.
const apiVersion = 2;

fetch('/api/session')
  .then((r) => (r.ok ? r.json() : null))
  .then((s) => {
    if (!s) return;
    const node = document.querySelector('[data-user]');
    if (node && s.user) node.textContent = s.user;
    if (node && s.build) node.title = `Server build ${s.build}`;
    if ((s.api || 1) < apiVersion) {
      const bar = document.createElement('div');
      bar.className = 'stale-server';
      bar.setAttribute('role', 'alert');
      bar.textContent = `This page is newer than the server it talks to (server build ${s.build || 'from before build stamps'}). ` +
        'Some features will fail until the server is rebuilt and restarted.';
      document.body.prepend(bar);
    }
  })
  .catch(() => {});
