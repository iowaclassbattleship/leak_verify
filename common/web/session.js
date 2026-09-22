// Shows who is signed in, in every masthead. The email is fetched rather than
// rendered into the page, so the frontends stay static files.
fetch('/api/session')
  .then((r) => (r.ok ? r.json() : null))
  .then((s) => {
    const node = document.querySelector('[data-user]');
    if (node && s && s.user) node.textContent = s.user;
  })
  .catch(() => {});
