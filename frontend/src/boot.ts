// bootDone lifts the loading screen index.html opens with: it fades out, then leaves the page. The
// app calls it once it knows what to show (the dashboard or the sign-in); later calls do nothing.
export function bootDone(): void {
  const el = document.getElementById('boot');
  if (!el || el.classList.contains('gone')) return;
  el.classList.add('gone');
  el.removeAttribute('role');
  const remove = () => el.remove();
  el.addEventListener('transitionend', remove, { once: true });
  // In case no transition runs (reduced motion, a hidden tab).
  window.setTimeout(remove, 600);
}

// onStatusPage reports whether this address shows the public status page: /status, or wherever the
// server marked the page as the status page (its own domain, or the path the owner chose).
export function onStatusPage(path: string): boolean {
  return path === '/status' || document.documentElement.dataset.page === 'status';
}
