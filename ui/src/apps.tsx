import * as React from 'react';
import { fetchArgoApps, ZeaClient } from './api';
import { usePoll } from './Runs';
import { ArgoApp, ImageMatch } from './types';
import { COLORS, Muted } from './ui';

// An Argo CD Application running an image of a Connection.
export interface AffectedApp {
  app: ArgoApp;
  match: ImageMatch;
}

export type AffectedMap = Record<string, AffectedApp[]>;

const APPS_POLL_MS = 30_000;

// useAffectedApps lists the Applications the user can see in Argo CD (its
// RBAC applies) and maps their images to the Connections they come from.
export function useAffectedApps(client: ZeaClient, enabled = true) {
  return usePoll<AffectedMap>(
    async () => {
      if (!enabled) {
        return {};
      }
      const apps = await fetchArgoApps();
      const images = Array.from(new Set(apps.flatMap(a => a.images))).slice(0, 1000);
      if (images.length === 0) {
        return {};
      }
      const matches = await client.matchImages(images);
      const byImage = new Map<string, ImageMatch[]>();
      for (const m of matches) {
        byImage.set(m.image, [...(byImage.get(m.image) ?? []), m]);
      }
      const out: AffectedMap = {};
      for (const app of apps) {
        const seen = new Set<string>();
        for (const img of app.images) {
          for (const m of byImage.get(img) ?? []) {
            const key = `${m.connection}\u0000${img}`;
            if (!seen.has(key)) {
              seen.add(key);
              (out[m.connection] ??= []).push({ app, match: m });
            }
          }
        }
      }
      for (const list of Object.values(out)) {
        list.sort((a, b) => `${a.app.namespace}/${a.app.name}`.localeCompare(`${b.app.namespace}/${b.app.name}`));
      }
      return out;
    },
    [client, enabled],
    APPS_POLL_MS,
    () => enabled,
  );
}

// appURL links to the Application in the Argo CD UI.
export function appURL(app: ArgoApp): string {
  const href = document.querySelector('base')?.getAttribute('href') ?? '/';
  return `${href.endsWith('/') ? href : `${href}/`}applications/${encodeURIComponent(app.namespace)}/${encodeURIComponent(app.name)}`;
}

const HEALTH: Record<string, { icon: string; color: string }> = {
  Healthy: { icon: 'fa fa-heart', color: COLORS.ok },
  Progressing: { icon: 'fa fa-circle-notch fa-spin', color: '#0dadea' },
  Degraded: { icon: 'fa fa-heart-broken', color: COLORS.error },
  Suspended: { icon: 'fa fa-pause-circle', color: COLORS.muted },
  Missing: { icon: 'fa fa-ghost', color: COLORS.warn },
};

const SYNC: Record<string, { icon: string; color: string }> = {
  Synced: { icon: 'fa fa-check-circle', color: COLORS.ok },
  OutOfSync: { icon: 'fa fa-arrow-alt-circle-up', color: COLORS.warn },
};

export const AppHealth = ({ app }: { app: ArgoApp }) => {
  const h = HEALTH[app.health ?? ''] ?? { icon: 'fa fa-question-circle', color: COLORS.muted };
  const s = SYNC[app.sync ?? ''] ?? { icon: 'fa fa-question-circle', color: COLORS.muted };
  return (
    <span style={{ whiteSpace: 'nowrap' }}>
      <i className={h.icon} style={{ color: h.color }} title={app.health || 'Unknown health'} />{' '}
      <i className={s.icon} style={{ color: s.color }} title={app.sync || 'Unknown sync status'} />
    </span>
  );
};

const short = (sha: string) => sha.slice(0, 7);

// AffectedAppsList shows the Applications running images of a Connection.
// current marks rows whose image commit belongs to the given SHAs.
export const AffectedAppsList = ({ items, current }: { items: AffectedApp[]; current?: string[] }) => (
  <div>
    {items.map(({ app, match }) => {
      const mine = !!match.commit && !!current?.some(sha => sha.startsWith(match.commit!) || match.commit!.startsWith(sha));
      return (
        <div
          key={`${app.namespace}/${app.name}/${match.image}`}
          style={{ display: 'flex', alignItems: 'baseline', gap: '0.5em', padding: '0.2em 0', borderTop: `1px solid ${COLORS.border}`, flexWrap: 'wrap' }}>
          <AppHealth app={app} />
          <a href={appURL(app)} target='_blank' rel='noopener noreferrer' style={{ fontWeight: 600 }}>
            {app.name}
          </a>
          <Muted>{app.namespace}</Muted>
          <span style={{ flex: 1 }} />
          {match.branch && (
            <Muted>
              <i className='fa fa-code-branch' /> {match.branch}
            </Muted>
          )}
          {match.commit &&
            (match.commitURL ? (
              <a href={match.commitURL} target='_blank' rel='noopener noreferrer'>
                <code>{short(match.commit)}</code>
              </a>
            ) : (
              <code>{short(match.commit)}</code>
            ))}
          {!match.commit && match.tag && <code title={match.image}>{match.tag}</code>}
          {mine && (
            <span style={{ color: COLORS.ok, fontSize: '0.85em' }} title='Runs the commit this run built'>
              <i className='fa fa-check' /> this run
            </span>
          )}
        </div>
      );
    })}
  </div>
);

// AffectedSummary is the compact form for cards: health icons and names.
export const AffectedSummary = ({ items }: { items: AffectedApp[] }) => {
  const names = Array.from(new Map(items.map(i => [`${i.app.namespace}/${i.app.name}`, i.app])).values());
  return (
    <div style={{ display: 'flex', gap: '0.8em', flexWrap: 'wrap', alignItems: 'baseline', fontSize: '0.9em' }}>
      <Muted>
        <i className='fa fa-cubes' /> Deployed in
      </Muted>
      {names.slice(0, 6).map(app => (
        <span key={`${app.namespace}/${app.name}`} style={{ whiteSpace: 'nowrap' }}>
          <AppHealth app={app} />{' '}
          <a href={appURL(app)} target='_blank' rel='noopener noreferrer'>
            {app.name}
          </a>
        </span>
      ))}
      {names.length > 6 && <Muted>+{names.length - 6} more</Muted>}
    </div>
  );
};
