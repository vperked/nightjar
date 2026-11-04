import { api } from '../api/client';
import type { PluginCard } from '../api/types';
import { formatAgo } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';
import { Section } from './ui';

// Cards from plugins: whatever a plugin prints as its card, shown on the overview. A card is a
// few labelled values, a line of text, or both.

export function PluginCardBody({ card }: { card: PluginCard }) {
  if (card.error) return <p className="plugin-card-error">{card.error}</p>;
  return (
    <div className="plugin-card">
      {card.text && <p className="plugin-card-text">{card.text}</p>}
      {!!card.items?.length && (
        <dl className="plugin-card-items">
          {card.items.map((it, i) => (
            <div key={i} title={it.hint}>
              <dt>{it.label}</dt>
              <dd className={`num ${it.tone ? `tone-${it.tone}` : ''}`}>{it.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {!card.text && !card.items?.length && <p className="note">The plugin printed nothing to show.</p>}
    </div>
  );
}

// PluginCards are every turned-on plugin's card, refreshed as the plugins run.
export function PluginCards() {
  const { data } = usePoll(api.pluginCards, 30000);
  const now = useNow(30000);
  return (
    <>
      {(data?.cards ?? []).map((c) => (
        <Section
          key={c.plugin}
          label={c.title}
          aside={
            <span className="small ink-3 plugin-card-aside">
              {c.link ? (
                <a href={c.link} target="_blank" rel="noreferrer">
                  Open ↗
                </a>
              ) : (
                formatAgo(c.updated, now)
              )}
            </span>
          }
        >
          <PluginCardBody card={c} />
        </Section>
      ))}
    </>
  );
}
