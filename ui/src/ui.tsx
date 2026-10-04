import * as React from 'react';
import { describeError } from './api';
import { TestResult } from './types';

export type Load<T> = { state: 'loading' } | { state: 'ok'; data: T } | { state: 'error'; error: string };

export const COLORS = {
  ok: '#18be94',
  error: '#e96d76',
  muted: '#6d7f8b',
  warn: '#f4c030',
  // Translucent neutrals work on both the light and the dark Argo CD theme.
  surface: 'rgba(128, 128, 128, 0.08)',
  border: 'rgba(128, 128, 128, 0.25)',
};

export const ErrorText = ({ text }: { text: string }) => (
  <div style={{ color: COLORS.error, margin: '0.5em 0', wordBreak: 'break-word' }}>
    <i className='fa fa-times-circle' /> {text}
  </div>
);

export const Muted = ({ children }: { children: React.ReactNode }) => (
  <span style={{ color: COLORS.muted }}>{children}</span>
);

export const TestResultView = ({ result }: { result: TestResult }) =>
  result.ok && result.repository ? (
    <div style={{ color: COLORS.ok, margin: '0.5em 0' }}>
      <i className='fa fa-check-circle' /> Connected to <b>{result.repository.fullName}</b> (default branch{' '}
      <code>{result.repository.defaultBranch}</code>)
    </div>
  ) : (
    <ErrorText text={result.error ?? 'Connection test failed'} />
  );

const PROVIDER_BADGE: Record<string, { label: string; color: string }> = {
  github: { label: 'GitHub', color: '#24292f' },
  gitlab: { label: 'GitLab', color: '#e24329' },
};

export const ProviderBadge = ({ provider }: { provider: string }) => {
  const b = PROVIDER_BADGE[provider] ?? { label: provider, color: COLORS.muted };
  return (
    <span
      style={{
        background: b.color,
        color: '#fff',
        borderRadius: 3,
        padding: '0.1em 0.5em',
        fontSize: '0.8em',
        fontWeight: 600,
        verticalAlign: 'middle',
      }}>
      {b.label}
    </span>
  );
};

// useLoad runs fn whenever deps change and ignores results of stale runs.
export function useLoad<T>(fn: () => Promise<T>, deps: React.DependencyList): [Load<T>, () => void] {
  const [state, setState] = React.useState<Load<T>>({ state: 'loading' });
  const [tick, setTick] = React.useState(0);
  React.useEffect(() => {
    let cancelled = false;
    setState({ state: 'loading' });
    fn().then(
      data => !cancelled && setState({ state: 'ok', data }),
      err => !cancelled && setState({ state: 'error', error: describeError(err) }),
    );
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  return [state, () => setTick(t => t + 1)];
}
