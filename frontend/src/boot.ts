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
