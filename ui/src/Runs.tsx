import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { Capabilities, Job, Run, RunDetail, RunStatus } from './types';
import { COLORS, ErrorText, Muted } from './ui';

// Poll intervals while something is still in progress.
const LIST_POLL_MS = 10_000;
const DETAIL_POLL_MS = 5_000;

const ACTIVE: RunStatus[] = ['queued', 'running'];
export const isActive = (s: RunStatus) => ACTIVE.includes(s);

const STATUS_STYLE: Record<RunStatus, { icon: string; color: string; label: string }> = {
  queued: { icon: 'fa fa-clock', color: COLORS.muted, label: 'Queued' },
  running: { icon: 'fa fa-circle-notch fa-spin', color: '#0dadea', label: 'Running' },
  success: { icon: 'fa fa-check-circle', color: COLORS.ok, label: 'Succeeded' },
  failed: { icon: 'fa fa-times-circle', color: COLORS.error, label: 'Failed' },
  canceled: { icon: 'fa fa-ban', color: COLORS.muted, label: 'Canceled' },
  manual: { icon: 'fa fa-hand-paper', color: COLORS.warn, label: 'Waiting for action' },
  skipped: { icon: 'fa fa-forward', color: COLORS.muted, label: 'Skipped' },
  unknown: { icon: 'fa fa-question-circle', color: COLORS.muted, label: 'Unknown' },
};

export const StatusIcon = ({ status }: { status: RunStatus }) => {
  const s = STATUS_STYLE[status] ?? STATUS_STYLE.unknown;
  return <i className={s.icon} style={{ color: s.color, width: '1.1em', textAlign: 'center' }} title={s.label} />;
};

function ago(iso?: string): string {
  if (!iso) {
    return '';
  }
  const sec = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
}

function duration(from?: string, to?: string): string {
  if (!from) {
    return '';
  }
  const end = to ? new Date(to).getTime() : Date.now();
  const sec = Math.max(0, Math.round((end - new Date(from).getTime()) / 1000));
  return sec < 60 ? `${sec}s` : `${Math.floor(sec / 60)}m ${sec % 60}s`;
}

// usePoll loads data, keeps the previous result while refreshing, and
// repeats every intervalMs while shouldPoll(data) is true.
function usePoll<T>(
  fn: () => Promise<T>,
  deps: React.DependencyList,
  intervalMs: number,
  shouldPoll: (data: T | undefined) => boolean,
): { data?: T; error: string; loading: boolean; reload: () => void } {
  const [data, setData] = React.useState<T>();
  const [error, setError] = React.useState('');
  const [loading, setLoading] = React.useState(true);
  const [tick, setTick] = React.useState(0);
  const fnRef = React.useRef(fn);
  fnRef.current = fn;

  React.useEffect(() => {
    setData(undefined);
    setError('');
    setLoading(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  React.useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fnRef.current().then(
      d => {
        if (!cancelled) {
          setData(d);
          setError('');
          setLoading(false);
        }
      },
      err => {
        if (!cancelled) {
          setError(describeError(err));
          setLoading(false);
        }
      },
    );
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);

  const poll = !loading && !error && shouldPoll(data);
  React.useEffect(() => {
    if (!poll) {
      return;
    }
    const t = window.setTimeout(() => setTick(x => x + 1), intervalMs);
    return () => window.clearTimeout(t);
  }, [poll, data, intervalMs]);

  return { data, error, loading, reload: () => setTick(x => x + 1) };
}

interface ListProps {
  client: ZeaClient;
  connection: string;
  gitRef: string;
  capabilities?: Capabilities;
  // Changing refreshKey reloads the list (e.g. after starting a run).
  refreshKey: number;
  // Keep polling until this time even if nothing is active yet: a freshly
  // dispatched GitHub run can take a few seconds to show up.
  watchUntil: number;
  expandRunID?: string;
}

export const RunsList = ({ client, connection, gitRef, capabilities, refreshKey, watchUntil, expandRunID }: ListProps) => {
  const [allBranches, setAllBranches] = React.useState(false);
  const [expanded, setExpanded] = React.useState<string | undefined>(expandRunID);
  const ref = allBranches ? '' : gitRef;
  const runs = usePoll(
    () => client.runs(connection, { ref, limit: 10 }),
    [client, connection, ref, refreshKey],
    LIST_POLL_MS,
    d => Date.now() < watchUntil || (d ?? []).some(r => isActive(r.status)),
  );

  React.useEffect(() => {
    if (expandRunID) {
      setExpanded(expandRunID);
    }
  }, [expandRunID]);

  return (
    <div style={{ marginTop: '1em' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.6em', margin: '0.5em 0' }}>
        <span style={{ fontWeight: 600 }}>Recent runs</span>
        <label style={{ fontSize: '0.85em', color: COLORS.muted, cursor: 'pointer' }}>
          <input type='checkbox' checked={allBranches} onChange={e => setAllBranches(e.target.checked)} /> all branches
        </label>
        <span style={{ flex: 1 }} />
        <button className='argo-button argo-button--base-o' title='Reload runs' onClick={runs.reload}>
          <i className={runs.loading ? 'fa fa-redo fa-spin' : 'fa fa-redo'} />
        </button>
      </div>
      {runs.error && <ErrorText text={runs.error} />}
      {!runs.data && runs.loading && <Muted>Loading...</Muted>}
      {runs.data && runs.data.length === 0 && <Muted>No runs{ref ? ` on ${ref}` : ''} yet.</Muted>}
      {runs.data?.map(r => (
        <RunRow
          key={r.id}
          client={client}
          connection={connection}
          run={r}
          capabilities={capabilities}
          expanded={expanded === r.id}
          onToggle={() => setExpanded(expanded === r.id ? undefined : r.id)}
          onChanged={runs.reload}
        />
      ))}
    </div>
  );
};

interface RowProps {
  client: ZeaClient;
  connection: string;
  run: Run;
  capabilities?: Capabilities;
  expanded: boolean;
  onToggle: () => void;
  onChanged: () => void;
}

const RunRow = ({ client, connection, run, capabilities, expanded, onToggle, onChanged }: RowProps) => {
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const [detailKey, setDetailKey] = React.useState(0);
  const id = run.id ?? '';

  const act = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError('');
    try {
      await fn();
      onChanged();
      setDetailKey(k => k + 1);
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const cancel = () => {
    if (window.confirm(`Cancel run ${run.number ? `#${run.number}` : id}?`)) {
      act(() => client.cancelRun(connection, id));
    }
  };

  const finished = !isActive(run.status) && run.status !== 'manual';
  const canRerunAll = finished && !!capabilities?.retryRun;
  const canRerunFailed = finished && !!capabilities?.retryFailedJobs && (run.status === 'failed' || run.status === 'canceled');

  return (
    <div style={{ borderTop: `1px solid ${COLORS.border}`, padding: '0.35em 0' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em' }}>
        <button
          className='argo-button argo-button--base-o'
          style={{ padding: '0 0.5em', minWidth: 0 }}
          title={expanded ? 'Hide jobs' : 'Show jobs'}
          onClick={onToggle}>
          <i className={expanded ? 'fa fa-angle-down' : 'fa fa-angle-right'} />
        </button>
        <StatusIcon status={run.status} />
        <div style={{ flex: 1, minWidth: 0, cursor: 'pointer' }} onClick={onToggle}>
          <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            <b>{run.name}</b>
            {run.number ? <span style={{ color: COLORS.muted }}> #{run.number}</span> : null}
            {run.title && run.title !== run.name && <span> {run.title}</span>}
          </div>
          <div style={{ fontSize: '0.8em', color: COLORS.muted }}>
            {[run.ref, run.event, run.actor, ago(run.createdAt), run.commitSHA?.slice(0, 7)].filter(Boolean).join(' · ')}
          </div>
        </div>
        {isActive(run.status) && (
          <button className='argo-button argo-button--base-o' disabled={busy} onClick={cancel} title='Cancel this run'>
            <i className='fa fa-stop' /> Cancel
          </button>
        )}
        {canRerunFailed && (
          <button
            className='argo-button argo-button--base-o'
            disabled={busy}
            onClick={() => act(() => client.retryRun(connection, id, true))}
            title='Rerun failed jobs'>
            <i className='fa fa-redo' /> Rerun failed
          </button>
        )}
        {canRerunAll && (
          <button
            className='argo-button argo-button--base-o'
            disabled={busy}
            onClick={() => act(() => client.retryRun(connection, id, false))}
            title='Rerun all jobs'>
            <i className='fa fa-redo' /> Rerun
          </button>
        )}
        {run.webURL && (
          <a href={run.webURL} target='_blank' rel='noopener noreferrer' title='Open in the CI provider'>
            <i className='fa fa-external-link-alt' />
          </a>
        )}
      </div>
      {error && <ErrorText text={error} />}
      {expanded && id && <RunJobs client={client} connection={connection} runID={id} refreshKey={detailKey} />}
    </div>
  );
};

const RunJobs = ({ client, connection, runID, refreshKey }: { client: ZeaClient; connection: string; runID: string; refreshKey: number }) => {
  const detail = usePoll<RunDetail>(
    () => client.run(connection, runID),
    [client, connection, runID, refreshKey],
    DETAIL_POLL_MS,
    d => !!d && (isActive(d.status) || d.jobs.some(j => isActive(j.status))),
  );
  return (
    <div style={{ margin: '0.3em 0 0.3em 2.2em' }}>
      {detail.error && <ErrorText text={detail.error} />}
      {!detail.data && detail.loading && <Muted>Loading jobs...</Muted>}
      {detail.data && detail.data.jobs.length === 0 && <Muted>No jobs yet.</Muted>}
      {detail.data?.jobs.map(j => <JobRow key={j.id} job={j} />)}
    </div>
  );
};

const JobRow = ({ job }: { job: Job }) => {
  const [open, setOpen] = React.useState(false);
  const steps = job.steps ?? [];
  return (
    <div style={{ padding: '0.15em 0' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em' }}>
        <StatusIcon status={job.status} />
        <span
          style={{ flex: 1, minWidth: 0, cursor: steps.length ? 'pointer' : undefined }}
          onClick={() => steps.length && setOpen(!open)}>
          {job.stage && <span style={{ color: COLORS.muted }}>{job.stage} / </span>}
          {job.name}
          {steps.length > 0 && <i className={open ? 'fa fa-angle-down' : 'fa fa-angle-right'} style={{ marginLeft: '0.4em' }} />}
        </span>
        <span style={{ fontSize: '0.8em', color: COLORS.muted }}>{duration(job.startedAt, job.finishedAt)}</span>
        {job.webURL && (
          <a href={job.webURL} target='_blank' rel='noopener noreferrer' title='Open job'>
            <i className='fa fa-external-link-alt' />
          </a>
        )}
      </div>
      {open &&
        steps.map(s => (
          <div key={s.number} style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginLeft: '1.6em', fontSize: '0.9em' }}>
            <StatusIcon status={s.status} />
            <span>{s.name}</span>
          </div>
        ))}
    </div>
  );
};
