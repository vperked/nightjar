import { act, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest';
import { api, ApiError, writesPending } from '../src/api/client';
import App from '../src/App';
import { scopeCSS } from '../src/customCode';
import { useNewBuild } from '../src/freshness';
import { usePoll } from '../src/hooks/usePoll';
import { useArrivals } from '../src/hooks/useArrivals';
import { State } from '../src/components/ui';

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const ok = (body: unknown = {}) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });

describe('api client changes', () => {
  it('sends a change with nothing to say as JSON, which is all the server takes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => ok());
    await api.sendReport();
    await api.repairCheck('web');
    await api.cleanupScan();
    await api.cleanupKeep('k1', true);
    await api.cleanupKeep('k1', false);
    const sent = fetchMock.mock.calls.map(([u, i]) => [String(u), i?.method, (i?.headers as Record<string, string>)['Content-Type'], i?.body]);
    expect(sent).toEqual([
      ['/api/report/send', 'POST', 'application/json', '{}'],
      ['/api/checks/web/repair', 'POST', 'application/json', '{}'],
      ['/api/cleanup/scan', 'POST', 'application/json', '{}'],
      ['/api/cleanup/k1/keep', 'PUT', 'application/json', '{}'],
      ['/api/cleanup/k1/keep', 'DELETE', undefined, undefined],
    ]);
  });

  it('says a request took too long instead of passing on the bare abort', async () => {
    vi.useFakeTimers();
    vi.spyOn(globalThis, 'fetch').mockImplementation(
      (_u, init) => new Promise((_, reject) => init?.signal?.addEventListener('abort', () => reject(new DOMException('signal is aborted without reason', 'AbortError')))),
    );
    const done = api.network().catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(5000);
    const err = await done;
    expect(err).toBeInstanceOf(ApiError);
    expect((err as Error).message).toBe('No answer from Nightjar within 5 seconds');
  });

  it('waits for long actions as long as the server lets them run', async () => {
    vi.useFakeTimers();
    let signal: AbortSignal | undefined;
    vi.spyOn(globalThis, 'fetch').mockImplementation((_u, init) => {
      signal = init?.signal ?? undefined;
      return new Promise((_, reject) => signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))));
    });
    const done = api.runHostSSH('h1', 'reinstall-agent').catch((e: unknown) => e);
    expect(writesPending()).toBe(true);
    // A reinstall over a slow link may take minutes; the default 15s would cut it off part-way.
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(signal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await done).toBeInstanceOf(ApiError);
    expect(writesPending()).toBe(false);
  });

  it('knows when a change is still on its way', async () => {
    let answer: (r: Response) => void = () => {};
    vi.spyOn(globalThis, 'fetch').mockImplementation(() => new Promise<Response>((r) => (answer = r)));
    expect(writesPending()).toBe(false);
    const saving = api.mute('check:web', 'web', 60);
    expect(writesPending()).toBe(true);
    answer(ok({ mutes: [] }));
    await saving;
    expect(writesPending()).toBe(false);
  });
});

describe('usePoll refresh', () => {
  it('asks again after a reply that was already on its way when refresh was called', async () => {
    const answers: ((v: string) => void)[] = [];
    const fetcher = vi.fn(() => new Promise<string>((r) => answers.push(r)));
    const { result } = renderHook(() => usePoll(fetcher, 60000));
    expect(fetcher).toHaveBeenCalledTimes(1);

    // A change lands while the first poll is out; its reply may be from before the change.
    act(() => result.current.refresh());
    expect(fetcher).toHaveBeenCalledTimes(1);
    await act(async () => answers[0]('before'));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(result.current.data).toBeUndefined();
    await act(async () => answers[1]('after'));
    await waitFor(() => expect(result.current.data).toBe('after'));
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});

describe('live-data motion', () => {
  it('marks only rows that arrive while the list is shown', () => {
    type Rows = { key: string; at?: number }[];
    const { result, rerender } = renderHook(({ rows, scope }: { rows: Rows; scope: string }) => useArrivals(rows, scope), {
      initialProps: {
        rows: [
          { key: 'b', at: 20 },
          { key: 'a', at: 10 },
        ],
        scope: 'all',
      },
    });
    expect([...result.current]).toEqual([]); // there at first: not new
    rerender({
      rows: [
        { key: 'c', at: 30 },
        { key: 'b', at: 20 },
        { key: 'a', at: 10 },
      ],
      scope: 'all',
    });
    expect([...result.current]).toEqual(['c']);
    // An older one brought in (Show older) is not news.
    rerender({
      rows: [
        { key: 'c', at: 30 },
        { key: 'b', at: 20 },
        { key: 'a', at: 10 },
        { key: 'z', at: 1 },
      ],
      scope: 'all',
    });
    expect([...result.current]).toEqual(['c']);
    // A new filter is a first showing again.
    rerender({ rows: [{ key: 'd', at: 40 }], scope: 'errors' });
    expect([...result.current]).toEqual([]);
  });

  it('flashes a status that changes on screen, and not one that first appears', () => {
    const { container, rerender } = render(<State status="up" />);
    expect(container.querySelector('.state')).not.toHaveClass('changed');
    rerender(<State status="down" />);
    expect(container.querySelector('.state')).toHaveClass('down', 'changed');
  });
});

describe('scopeCSS', () => {
  const inside = (css: string) => {
    const out = scopeCSS(css, '.sd-frame');
    const at = out.indexOf('@scope (.sd-frame) {\n');
    expect(at).toBeGreaterThanOrEqual(0);
    expect(out.endsWith('\n}')).toBe(true);
    return { out, before: out.slice(0, at), body: out.slice(at + '@scope (.sd-frame) {\n'.length, -2) };
  };

  it("drops a '}' that closes nothing, so it cannot close the scope", () => {
    const { body } = inside('} .masthead, nav { display:none }\n.status-row { color: red } }');
    expect(body).toBe('.masthead, nav { display:none }\n.status-row { color: red }');
  });

  it('does not count braces that are escaped, in strings or in url()', () => {
    // To a browser `.a\\{` is a name, so the '}' after it would close the scope: it goes.
    const { body } = inside('.a\\{ } .masthead { display: none }\n.b { background: url(x}.png) }\n.c { content: "}" }');
    expect(body).toBe('.masthead { display: none }\n.b { background: url(x}.png) }\n.c { content: "}" }');
  });

  it('closes what is left open, and a string at the end of its line', () => {
    expect(inside('.a { color: red').body).toBe('.a { color: red\n}');
    // The browser ends an unfinished string at the line's end, so the '}' after it counts.
    expect(inside('.a { content: "x\n} .b { color: red }').body).toBe('.a { content: "x\n}\n.b { color: red }');
  });

  it('turns comments into spaces and drops an unfinished one', () => {
    expect(inside('.a/**/{ color: red } /* } .b { x: y }').body).toBe('.a { color: red }');
  });

  it("keeps the owner's animations and fonts from replacing the dashboard's", () => {
    const { before, body } = inside(
      "@import url(https://example.com/a.css);\n@property --ink { syntax: '<color>'; inherits: false; initial-value: red }\n" +
        "@font-face { font-family: 'IBM Plex Sans'; src: url(a.woff2) }\n@keyframes pulse { to { opacity: 0 } }\n" +
        ".a { animation: pulse 1s infinite; font-family: 'IBM Plex Sans', sans-serif }\n.b { font: 12px IBM Plex Sans }",
    );
    expect(before).not.toMatch(/@import|@property/);
    expect(before).toContain('@font-face { font-family: "nj-preview-IBM Plex Sans"; src: url(a.woff2) }');
    expect(before).toContain('@keyframes nj-preview-pulse { to { opacity: 0 } }');
    expect(body).toBe('.a { animation: nj-preview-pulse 1s infinite; font-family: "nj-preview-IBM Plex Sans", sans-serif }\n.b { font: 12px "nj-preview-IBM Plex Sans" }');
  });

  it('reads url( written with escapes as url(', () => {
    // To a browser both are url(, so the '{' in them counts for nothing and the extra '}' goes.
    expect(inside('.a { b: u\\rl({) } } .masthead, nav { display: none }').body).toBe('.a { b: u\\rl({) }\n.masthead, nav { display: none }');
    expect(inside('.a { b: \\75 rl({) } } nav { display: none }').body).toBe('.a { b: \\75 rl({) }\nnav { display: none }');
  });

  it('leaves out fonts and animations it cannot rename safely', () => {
    for (const css of [
      // A quote or a line break in the name would turn a string inside out once the name is quoted.
      '@font-face { font-family: a"} nav{display:none} x" }',
      '@font-face { src: "x;font-family: \'a} nav{display:none} x\';" }',
      // Escaped, or named twice: the dashboard's font could still be replaced.
      '@font-face { font-fam\\69 ly: "IBM Plex Sans"; src: url(a) } nav { display: none }',
      "@font-face { font-family: x; font-family: 'IBM Plex Sans' } nav { display: none }",
      '@font-f\\61 ce { font-family: "IBM Plex Sans" } nav { display: none }',
      '@keyframes pul\\73 e { to { opacity: 0 } } nav { display: none }',
    ]) {
      const { before, body } = inside(css);
      expect(before.trim()).toBe('');
      expect(body).not.toMatch(/@font|@keyframes|IBM/);
    }
    // A plain name is renamed however it is written, and a line break in it is only a space.
    expect(inside('@keyframes"pulse"{ to { opacity: 0 } } .a { animation: pulse 1s }').out).toBe(
      '@keyframes "nj-preview-pulse"{ to { opacity: 0 } }\n@scope (.sd-frame) {\n.a { animation: nj-preview-pulse 1s }\n}',
    );
    expect(inside('@font-face { font-family: a\nb; c: "} nav { display: none } "; }').before).toBe('@font-face { font-family: "nj-preview-a b"; c: "} nav { display: none } "; }\n');
  });
});

describe('app config', () => {
  it('asks for the config again when the first request fails', async () => {
    let configs = 0;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      if (String(input) !== '/api/config') return new Response('', { status: 401 });
      // The monitor is still starting the first time it is asked.
      if (configs++ === 0) throw new TypeError('Failed to fetch');
      return ok({ auth_required: true, authorized: false, account: { configured: false }, status_page_enabled: false, status_page_title: '' });
    });
    render(<App />);
    // A fresh install: only the config says to create the admin account rather than sign in.
    expect(await screen.findByText('Create the admin account', {}, { timeout: 4000 })).toBeInTheDocument();
    expect(configs).toBe(2);
  });
});

describe('status page address', () => {
  // On its own domain, or at a path the owner chose, the server marks the page: it shows the status
  // page wherever it is, and asks for nothing of the dashboard (which that domain does not serve).
  it('shows the status page wherever the server marked it', async () => {
    document.documentElement.dataset.page = 'status';
    onTestFinished(() => {
      delete document.documentElement.dataset.page;
    });
    const asked: string[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      asked.push(String(input));
      if (String(input) === '/api/status') return ok({ title: 'Acme status', overall: 'operational', updated: Date.now() / 1000, services: [] });
      return new Response('', { status: 404 });
    });
    render(<App />);
    expect(await screen.findByText('Acme status')).toBeInTheDocument();
    expect(asked.filter((u) => u.startsWith('/api/') && u !== '/api/status')).toEqual([]);
  });
});

describe('new builds', () => {
  const stale = () => {
    const script = document.createElement('script');
    script.type = 'module';
    script.src = '/assets/index-old1.js';
    document.head.appendChild(script);
    const reload = vi.fn();
    const real = window.location;
    Object.defineProperty(window, 'location', { configurable: true, value: { ...real, reload } });
    onTestFinished(() => {
      script.remove();
      Object.defineProperty(window, 'location', { configurable: true, value: real });
    });
    return reload;
  };

  it('does not reload while a box is ticked but not saved', async () => {
    const reload = stale();
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('<script type="module" src="/assets/index-new2.js"></script>'));
    const box = document.createElement('input');
    box.type = 'checkbox';
    document.body.appendChild(box);
    onTestFinished(() => box.remove());
    const { result } = renderHook(({ path }) => useNewBuild(path), { initialProps: { path: '/' } });
    await waitFor(() => expect(result.current).toBe(true));
    fireEvent.click(box);
    document.dispatchEvent(new Event('visibilitychange'));
    expect(reload).not.toHaveBeenCalled();
  });

  it('does not reload while a change is waiting for its answer', async () => {
    const reload = stale();
    let answer: (r: Response) => void = () => {};
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      if (String(input) === '/') return new Response('<script type="module" src="/assets/index-new2.js"></script>');
      return new Promise<Response>((r) => (answer = r));
    });
    const reinstall = api.runHostSSH('h1', 'reinstall-agent');
    const { result, rerender } = renderHook(({ path }) => useNewBuild(path), { initialProps: { path: '/' } });
    await waitFor(() => expect(result.current).toBe(true));
    document.dispatchEvent(new Event('visibilitychange'));
    rerender({ path: '/servers' });
    expect(reload).not.toHaveBeenCalled();

    answer(ok({ output: 'done' }));
    await reinstall;
    rerender({ path: '/' });
    expect(reload).toHaveBeenCalledTimes(1);
  });
});

describe('pageNumbers', () => {
  it('shows the ends and the pages around the current one, with gaps', async () => {
    const { pageNumbers } = await import('../src/components/Pager');
    expect(pageNumbers(0, 1)).toEqual([0]);
    expect(pageNumbers(0, 3)).toEqual([0, 1, 2]);
    expect(pageNumbers(5, 12)).toEqual([0, null, 4, 5, 6, null, 11]);
    // A gap of a single page shows that page instead of "…".
    expect(pageNumbers(3, 12)).toEqual([0, 1, 2, 3, 4, null, 11]);
    expect(pageNumbers(11, 12)).toEqual([0, null, 10, 11]);
  });
});
