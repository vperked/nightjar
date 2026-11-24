import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { forgetPolls } from '../src/hooks/usePoll';

// Each test starts with no poll answers remembered from the one before.
afterEach(forgetPolls);

// jsdom has no PointerEvent; MouseEvent carries the coordinates the charts read.
if (typeof window.PointerEvent === 'undefined') {
  (window as unknown as { PointerEvent: typeof MouseEvent }).PointerEvent = MouseEvent;
}
