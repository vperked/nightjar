import { useId, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useCharts } from '../appearance';

export interface Point {
  t: number; // unix seconds
  v: number | null; // null breaks the line (e.g. a failed check); sparklines bridge it
}

// withGaps breaks a line where points are missing: buckets more than one step apart get a null
// between them, so a stretch with no readings is drawn as a gap rather than a straight line.
export function withGaps(points: Point[], step: number): Point[] {
  const out: Point[] = [];
  for (const p of points) {
    const prev = out.at(-1);
    if (prev && p.t - prev.t > step * 1.5) out.push({ t: prev.t + step, v: null });
    out.push(p);
  }
  return out;
}

export interface Series {
  label: string;
  color: string; // a CSS colour, normally var(--series-N)
  points: Point[];
}

interface Props {
  series: Series[];
  height?: number;
  compact?: boolean; // sparkline mode: no axes, scaled to its own range
  raw?: boolean; // draw every reading as it is, for counts whose steps matter (streams playing)
  max?: number; // fixed y max (e.g. 100 for percentages); ignored by sparklines
  format: (v: number) => string;
  // tip formats tooltip values when the axis labels leave the unit out (11 → "11 MB/s"); format otherwise.
  tip?: (v: number) => string;
  markers?: number[]; // timestamps to flag along the baseline (failures)
  emptyText?: string;
  // onScrub replaces the tooltip: the parent shows the hovered moment itself (a sparkline's card
  // swaps its headline number), and gets null when the pointer leaves.
  onScrub?: (hover: Scrub | null) => void;
}

// Scrub is the hovered moment: its time (already formatted) and each series' value then.
export interface Scrub {
  time: string;
  values: (number | null)[];
}

const AXIS = { top: 10, right: 4, bottom: 24 };
const Y_TICKS = 4;
const TIME_STEPS = [60, 300, 900, 1800, 3600, 2 * 3600, 3 * 3600, 6 * 3600, 12 * 3600, 86400, 2 * 86400, 3 * 86400, 7 * 86400];

// niceStep is a 1/2/2.5/5 × 10ⁿ step that splits [0, v] into about `ticks` parts.
function niceStep(v: number, ticks: number): number {
  if (v <= 0) return 1;
  const raw = v / ticks;
  const mag = 10 ** Math.floor(Math.log10(raw));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * mag >= raw) return m * mag;
  return 10 * mag;
}

// timeTicks picks round local times (whole minutes, hours or days) across [t0, t1].
function timeTicks(t0: number, t1: number, maxTicks: number): { ticks: number[]; step: number } {
  const step = TIME_STEPS.find((s) => (t1 - t0) / s <= maxTicks) ?? TIME_STEPS[TIME_STEPS.length - 1];
  const offset = new Date(t0 * 1000).getTimezoneOffset() * 60;
  const ticks: number[] = [];
  for (let t = Math.ceil((t0 - offset) / step) * step + offset; t <= t1; t += step) ticks.push(t);
  return { ticks, step };
}

function formatTick(t: number, step: number): string {
  const d = new Date(t * 1000);
  if (step >= 86400 || (d.getHours() === 0 && d.getMinutes() === 0)) return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  return d.toLocaleTimeString([], step >= 3600 ? { hour: 'numeric' } : { hour: 'numeric', minute: '2-digit' });
}

function formatHover(t: number, span: number): string {
  const opts: Intl.DateTimeFormatOptions = { hour: 'numeric', minute: '2-digit' };
  if (span > 20 * 3600) Object.assign(opts, { month: 'short', day: 'numeric' });
  if (span < 3600) opts.second = '2-digit';
  return new Date(t * 1000).toLocaleString([], opts);
}

// useWidth follows the element's rendered width, so the chart draws at real pixels (no stretched text or dots).
function useWidth(ref: React.RefObject<HTMLDivElement>): number {
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.clientWidth || 640);
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(([entry]) => setWidth(Math.round(entry.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return width;
}

// nearest is the point closest to t, by binary search: points arrive in time order.
function nearest(points: Point[], t: number): Point | undefined {
  if (points.length === 0) return undefined;
  let lo = 0;
  let hi = points.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (points[mid].t < t) lo = mid + 1;
    else hi = mid;
  }
  return lo > 0 && t - points[lo - 1].t < points[lo].t - t ? points[lo - 1] : points[lo];
}

function runs(points: Point[], bridge: boolean): Point[][] {
  const out: Point[][] = [];
  let run: Point[] = [];
  for (const p of points) {
    if (p.v !== null) run.push(p);
    else if (!bridge && run.length) {
      out.push(run);
      run = [];
    }
  }
  if (run.length) out.push(run);
  return out;
}

// thin averages a run into at most `buckets` points, so a sparkline shows the trend, not the noise.
function thin(run: Point[], buckets: number): Point[] {
  if (run.length <= buckets || buckets < 2) return run;
  const out: Point[] = [];
  const size = run.length / buckets;
  for (let b = 0; b < buckets; b++) {
    const part = run.slice(Math.floor(b * size), Math.floor((b + 1) * size));
    if (!part.length) continue;
    out.push({ t: part.reduce((n, p) => n + p.t, 0) / part.length, v: part.reduce((n, p) => n + (p.v as number), 0) / part.length });
  }
  // The newest reading stays exact, so the line ends where the number says.
  out[out.length - 1] = run[run.length - 1];
  return out;
}

// curve is a monotone cubic through the points: smooth, and never overshooting a reading.
function curve(pts: [number, number][]): string {
  const f = (n: number) => n.toFixed(1);
  if (pts.length < 3) return pts.map(([x, y], i) => `${i ? 'L' : 'M'}${f(x)},${f(y)}`).join('');
  const n = pts.length;
  const dx: number[] = [];
  const m: number[] = [];
  for (let i = 0; i < n - 1; i++) {
    dx[i] = pts[i + 1][0] - pts[i][0];
    m[i] = dx[i] ? (pts[i + 1][1] - pts[i][1]) / dx[i] : 0;
  }
  const t: number[] = [m[0]];
  for (let i = 1; i < n - 1; i++) {
    t[i] = m[i - 1] * m[i] <= 0 ? 0 : (3 * (dx[i - 1] + dx[i])) / ((2 * dx[i] + dx[i - 1]) / m[i - 1] + (dx[i] + 2 * dx[i - 1]) / m[i]);
  }
  t[n - 1] = m[n - 2];
  let d = `M${f(pts[0][0])},${f(pts[0][1])}`;
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i] / 3;
    d += `C${f(pts[i][0] + h)},${f(pts[i][1] + t[i] * h)} ${f(pts[i + 1][0] - h)},${f(pts[i + 1][1] - t[i + 1] * h)} ${f(pts[i + 1][0])},${f(pts[i + 1][1])}`;
  }
  return d;
}

// Charts are drawn on a dot matrix (unless Settings → Appearance asks for lines): a sparkline is
// columns of lit dots on a field of dim ones, and a full chart's area is dots under its line. Each column is a dot wide, and its height is a whole
// number of dots, so no dot is ever cut.
const PITCH = { compact: 5, full: 6 };
const DOT = { compact: 1.6, full: 1.35 };

interface Column {
  x: number; // the column's left edge
  lit: number; // dots lit, from the baseline
}

// sample is the run's value at the centre of each dot column it spans: the line between readings,
// or with `raw`, the reading in force then.
function sample(run: Point[], x: (t: number) => number, left: number, pitch: number, raw: boolean): { x: number; v: number }[] {
  const out: { x: number; v: number }[] = [];
  if (!run.length) return out;
  const x1 = x(run[run.length - 1].t);
  let i = 0;
  for (let c = Math.max(0, Math.ceil((x(run[0].t) - left) / pitch - 0.5)); left + (c + 0.5) * pitch <= x1 + 0.5; c++) {
    const cx = left + (c + 0.5) * pitch;
    while (i < run.length - 2 && x(run[i + 1].t) <= cx) i++;
    const a = run[i];
    const b = run[i + 1] ?? a;
    const xa = x(a.t);
    const xb = x(b.t);
    const f = raw ? (cx >= xb ? 1 : 0) : xb === xa ? 0 : Math.max(0, Math.min(1, (cx - xa) / (xb - xa)));
    out.push({ x: left + c * pitch, v: (a.v as number) + ((b.v as number) - (a.v as number)) * f });
  }
  return out;
}

// cells is a path of whole dot cells: each column from `from` (a row edge) up, or down, `lit` dots.
function cells(cols: Column[], from: number, pitch: number, down = false, only?: number): string {
  return cols
    .filter((c) => c.lit > 0)
    .map((c) => {
      const n = only ?? c.lit;
      const top = down ? from + (c.lit - n) * pitch : from - c.lit * pitch;
      return `M${c.x},${top}h${pitch}v${n * pitch}h${-pitch}Z`;
    })
    .join('');
}

// TrendChart is a line over a dotted area with one y-axis, time ticks and a crosshair tooltip, or,
// compact, a sparkline of dot columns.
export function TrendChart({ series, height = 180, compact = false, raw = false, max, format, markers = [], emptyText = 'Collecting data…', onScrub, tip = format }: Props) {
  const dotId = useId().replace(/:/g, '');
  const dots = useCharts() === 'dots';
  const ref = useRef<HTMLDivElement>(null);
  const width = useWidth(ref);
  const [hoverT, setHoverT] = useState<number | null>(null);

  const model = useMemo(() => {
    const valid = series.map((s) => s.points.filter((p) => p.v !== null));
    if (!width || !valid.some((v) => v.length >= 2)) return null;
    const all = series.flatMap((s) => s.points);
    const values = valid.flat().map((p) => p.v as number);
    const t0 = Math.min(...all.map((p) => p.t));
    const t1 = Math.max(...all.map((p) => p.t), t0 + 1);

    let yMin = 0;
    let yMax: number;
    let yTicks: number[] = [];
    if (compact) {
      // Sparklines show the shape of the trend, so they scale to their own range with some air.
      const lo = Math.min(...values);
      const hi = Math.max(...values);
      const padding = Math.max((hi - lo) * 0.25, Math.abs(hi) * 0.05, 1e-9);
      yMin = Math.max(0, lo - padding);
      yMax = hi + padding;
    } else {
      const step = niceStep(max ?? Math.max(...values, 0) * 1.08, Y_TICKS);
      yMax = max ?? Math.max(step, Math.ceil((Math.max(...values, 0) * 1.08) / step) * step);
      for (let v = 0; v <= yMax + step / 1e6; v += step) yTicks.push(v);
      if (max !== undefined) yTicks = yTicks.filter((v) => v <= max);
    }

    const left = compact ? 0 : Math.max(...yTicks.map((v) => format(v).length)) * 6.6 + 12;
    // A sparkline's fill runs to its bottom edge, so a tile can bleed it to the card's edge.
    const pad = compact ? { top: 0, right: 0, bottom: 0, left: 0 } : { ...AXIS, left };
    const plotW = Math.max(1, width - pad.left - pad.right);
    const x = (t: number) => pad.left + ((t - t0) / (t1 - t0)) * plotW;
    const y = (v: number) => pad.top + (1 - (Math.min(v, yMax) - yMin) / (yMax - yMin || 1)) * (height - pad.top - pad.bottom);
    const base = height - pad.bottom;

    const pitch = compact ? PITCH.compact : PITCH.full;
    // The dot grid lines up with the baseline and the plot's left edge.
    const rows = Math.floor((base - pad.top) / pitch);
    const top = base - rows * pitch;
    // A sparkline of two series (in and out) is mirrored about its middle: the first grows up, the second down.
    const mirror = dots && compact && series.length === 2;
    const mid = base - Math.floor(rows / 2) * pitch;
    const peak = mirror ? Math.max(...values, 1e-9) : 0;

    const paths = series.map((s, i) => {
      // Lines are smoothed curves through readings averaged to about one every few pixels (five on a
      // sparkline, six on a full chart), so noise reads as a trend. Hovering still shows each reading.
      const segs = runs(s.points, compact).map((r) => (raw ? r : thin(r, Math.round((x(r[r.length - 1].t) - x(r[0].t)) / pitch))));
      const draw = (r: Point[]) => (raw ? r.map((p, i) => `${i ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p.v as number).toFixed(1)}`).join('') : curve(r.map((p) => [x(p.t), y(p.v as number)])));
      const line = segs.map(draw).join('');
      const room = mirror ? (i === 0 ? (mid - top) / pitch : (base - mid) / pitch) : rows;
      const cols: Column[] = segs.flatMap((r) =>
        sample(r, x, pad.left, pitch, raw).map((c) => {
          const share = mirror ? c.v / peak : (Math.min(c.v, yMax) - yMin) / (yMax - yMin || 1);
          // A sparkline always lights its bottom dot, so a quiet stretch still reads as data.
          return { x: c.x, lit: Math.min(room, Math.max(compact ? 1 : 0, Math.round(share * room))) };
        }),
      );
      const down = mirror && i === 1;
      const from = mirror ? mid : base;
      // Drawn as lines, the area is a gradient under the curve; as dots, whole cells under it.
      const area = dots
        ? cells(cols, from, pitch, down)
        : segs
            .filter((r) => r.length > 1)
            .map((r) => `M${x(r[0].t).toFixed(1)},${base}` + draw(r).replace(/^M/, 'L') + `L${x(r[r.length - 1].t).toFixed(1)},${base}Z`)
            .join('');
      // The top dot of each sparkline column is lit fully, so the trend reads as a line of dots.
      const crest = dots && compact ? cells(cols, from, pitch, down, 1) : '';
      const last = [...s.points].reverse().find((p) => p.v !== null);
      return { line, area, crest, cols, from, down, last };
    });
    const time = compact ? { ticks: [], step: 0 } : timeTicks(t0, t1, Math.max(3, Math.floor(plotW / 90)));
    return { t0, t1, x, y, pad, base, paths, yTicks, time, pitch, top, mirror, mid };
  }, [series, width, height, max, compact, raw, format, dots]);

  if (!model) {
    return (
      <div ref={ref} className={`chart-empty ${compact ? 'compact' : ''}`} style={{ height }}>
        {!compact && emptyText}
      </div>
    );
  }
  const { t0, t1, x, y, pad, base, paths, yTicks, time, pitch, top, mirror, mid } = model;

  const onMove = (e: React.PointerEvent) => {
    const rect = ref.current!.getBoundingClientRect();
    const t = t0 + ((e.clientX - rect.left - pad.left) / (rect.width - pad.left - pad.right)) * (t1 - t0);
    const p = nearest(series[0].points, Math.max(t0, Math.min(t1, t)));
    setHoverT(p ? p.t : null);
    if (onScrub) onScrub(p ? { time: formatHover(p.t, t1 - t0), values: series.map((s) => nearest(s.points, p.t)?.v ?? null) } : null);
  };
  const onLeave = () => {
    setHoverT(null);
    onScrub?.(null);
  };
  const hx = hoverT !== null ? x(hoverT) : null;
  const hovered = hoverT === null ? [] : series.map((s) => nearest(s.points, hoverT));
  // The tooltip sits inside the plot beside the crosshair, flipping to the left near the right edge.
  const tipLeft = hx !== null && hx > width - 170;

  return (
    <div className={`chart ${compact ? 'compact' : ''}`} ref={ref} onPointerMove={onMove} onPointerLeave={onLeave}>
      <svg width={width} height={height} role="img" aria-label={series.map((s) => s.label).join(', ')}>
        <defs>
          {dots
            ? // One dot per cell, in each series' ink, and the dim field a sparkline sits on.
              [...series.map((s) => s.color), 'var(--dot-off)'].map((color, i) => (
                <pattern key={i} id={`${dotId}-${i}`} x={pad.left % pitch} y={base % pitch} width={pitch} height={pitch} patternUnits="userSpaceOnUse">
                  <circle cx={pitch / 2} cy={pitch / 2} r={compact ? DOT.compact : DOT.full} fill={color} />
                </pattern>
              ))
            : series.map((s, i) => (
                <linearGradient key={i} id={`${dotId}-${i}`} x1="0" x2="0" y1="0" y2="1">
                  <stop offset="0%" stopColor={s.color} stopOpacity={compact ? 0.34 : 0.26} />
                  <stop offset="100%" stopColor={s.color} stopOpacity={0} />
                </linearGradient>
              ))}
        </defs>
        {dots && compact && <rect className="dot-field" x={pad.left} y={top} width={width - pad.left - pad.right} height={base - top} fill={`url(#${dotId}-${series.length})`} />}
        {yTicks.map((v) => (
          <g key={v}>
            <line className={v === 0 ? 'axis-line' : 'grid-line'} x1={pad.left} x2={width - pad.right} y1={y(v)} y2={y(v)} />
            <text className="axis-text" x={pad.left - 10} y={y(v)} textAnchor="end" dominantBaseline="middle">
              {format(v)}
            </text>
          </g>
        ))}
        {/* A tick by the right edge ends there, so "5:00 PM" isn't cut in half on a phone. */}
        {time.ticks.map((t) => {
          const end = x(t) > width - 28;
          return (
            <text key={t} className="axis-text" x={end ? width - 1 : x(t)} y={height - 6} textAnchor={end ? 'end' : 'middle'}>
              {formatTick(t, time.step)}
            </text>
          );
        })}
        {/* The plot draws itself in from the left the first time it appears. */}
        <g className="plot">
          {paths.map((p, i) => (
            <g key={i}>
              {/* A full chart fills dots under its first series only; the others are lines over it. */}
              {(!dots || compact || i === 0) && <path d={p.area} className={dots ? (compact ? 'dots' : 'dots full') : undefined} fill={`url(#${dotId}-${i})`} />}
              {dots && compact ? (
                <path d={p.crest} fill={`url(#${dotId}-${i})`} />
              ) : (
                // The second line is dashed, so it never relies on its shade alone (the palette is black and white).
                <path d={p.line} className="line" stroke={series[i].color} strokeDasharray={i === 1 ? '5 4' : undefined} />
              )}
            </g>
          ))}
        </g>
        {mirror && <line className="mirror-line" x1={pad.left} x2={width - pad.right} y1={mid} y2={mid} />}
        {markers.map((t) => (
          <rect key={t} className="down-mark" x={x(t) - 1.5} y={base - 6} width={3} height={6} rx={1.5} />
        ))}
        {hx !== null && dots && compact
          ? // A sparkline lights the hovered column in full.
            paths.map((p, i) => {
              const c = p.cols.find((c) => hx >= c.x && hx < c.x + pitch);
              return c ? <path key={i} d={cells([c], p.from, pitch, p.down)} fill={`url(#${dotId}-${i})`} /> : null;
            })
          : hx !== null && <line className="crosshair" x1={hx} x2={hx} y1={pad.top} y2={base} />}
        {!(dots && compact) && hovered.map((p, i) => (p && p.v !== null ? <circle key={i} className="hover-dot" cx={x(p.t)} cy={y(p.v)} r={4} fill={series[i].color} /> : null))}
      </svg>
      {hoverT !== null && hx !== null && !onScrub && (
        <div
          className="tooltip"
          style={{
            left: tipLeft ? hx - 12 : hx + 12,
            top: compact ? 0 : pad.top,
            // Sparklines are short, so their tooltip floats above them.
            transform: `${tipLeft ? 'translateX(-100%) ' : ''}${compact ? 'translateY(calc(-100% - 6px))' : ''}` || undefined,
          }}
        >
          <div className="tooltip-time">{formatHover(hoverT, t1 - t0)}</div>
          {series.map((s, i) => {
            const p = hovered[i];
            return (
              <div className="tooltip-row" key={s.label}>
                <span className="swatch" style={{ background: s.color }} />
                {series.length > 1 && <span className="tooltip-label">{s.label}</span>}
                <strong>{p && p.v !== null ? tip(p.v) : 'down'}</strong>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

export function Legend({ series }: { series: Pick<Series, 'label' | 'color'>[] }) {
  return (
    <div className="legend">
      {series.map((s, i) => (
        <span key={s.label}>
          <span className={`swatch ${i === 1 ? 'dashed' : ''}`} style={{ ['--swatch' as string]: s.color, background: i === 1 ? undefined : s.color }} />
          {s.label}
        </span>
      ))}
    </div>
  );
}
