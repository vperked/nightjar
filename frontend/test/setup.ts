import '@testing-library/jest-dom/vitest';

// jsdom has no PointerEvent; MouseEvent carries the coordinates the charts read.
if (typeof window.PointerEvent === 'undefined') {
  (window as unknown as { PointerEvent: typeof MouseEvent }).PointerEvent = MouseEvent;
}
