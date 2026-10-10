import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { navigate } from './route';
import { ago, duration, RunJobs, usePoll } from './Runs';
import {
  Badge,
  ellipsis,
  Field,
  Help,
  PipelineName,
  StreamStatusIcon,
  StreamStatusLabel,
  ValueField,
} from './StreamFields';
import { ConnData } from './streamModel';
import { can, Stream, StreamAttempt, StreamRun, StreamRunStatus, StreamSpec, StreamStep } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

const RUN_POLL_MS = 4_000;
const HISTORY_POLL_MS = 10_000;
const HISTORY_PAGE = 10;
const ACCENT = '#0dadea';

type StepState = StreamAttempt['steps'][number];

const ACTIVE_STEP = ['starting', 'running'];

interface EarlierTry {
  key: string;
  label: string;
  by: string;
  startedAt?: string;
  state: StepState;
}

const tryLabel = (attempt: number, t?: number) => (t ? `Attempt ${attempt} ? try ${t + 1}` : `Attempt ${attempt}`);

// earlierTries returns the earlier states of a step, newest first: its
// automatic retries and its states in earlier (manually retried) attempts.
function earlierTries(run: StreamRun | undefined, id: string): EarlierTry[] {
  if (!run) {
    return [];
  }
  const attempts: StreamAttempt[] = run.attempts ?? [];
  const startOf = (a: number) =>
    a <= attempts.length
      ? { by: attempts[a - 1].user, at: attempts[a - 1].startedAt }
      : { by: run.retriedBy || run.user, at: run.retriedAt || run.createdAt };
  const out: EarlierTry[] = [];
  for (let a = 1; a <= run.attempt; a++) {
    const start = startOf(a);
    for (const t of run.tries ?? []) {
      if (t.attempt === a && t.id === id) {
        out.push({ key: `${a}.${t.try ?? 0}`, label: tryLabel(a, t.try), by: t.try ? 'automatic retry' : start.by, startedAt: start.at, state: t });
      }
    }
    const final = a < run.attempt ? attempts[a - 1]?.steps.find(x => x.id === id) : undefined;
    if (final) {
      out.push({ key: `${a}.end`, label: tryLabel(a, final.try), by: final.try ? 'automatic retry' : start.by, startedAt: start.at, state: final });
    }
  }
  return out.reverse();
}

const RetryWait = ({ at }: { at: string }) => (
  <span style={{ color: COLORS.muted }} title={`The retry starts at ${new Date(at).toLocaleString()}`}>
    <i className='fa fa-redo' /> retry at {new Date(at).toLocaleTimeString()}
  </span>
);

const RunLink = ({ state }: { state: StepState }) => (
  <>
    {state.url ? (
      <a href={state.url} target='_blank' rel='noopener noreferrer' title='Open in the CI provider' onClick={e => e.stopPropagation()}>
        <i className='fa fa-external-link-alt' /> {state.runNumber ? `#${state.runNumber}` : 'Open'}
      </a>
    ) : (
      state.runNumber && <span>#{state.runNumber}</span>
    )}
    {state.sha && <code style={{ color: COLORS.muted }}>{state.sha.slice(0, 7)}</code>}
  </>
);

interface BoardStepProps {
  data: ConnData;
  step: StreamStep;
  state?: StepState;
  retries: number;
  selected: boolean;
  onSelect?: () => void;
}

const BoardStep = ({ data, step, state, retries, selected, onSelect }: BoardStepProps) => {
  const failed = state?.status === 'failed';
  const ref = state?.ref || step.ref;
  return (
    <div
      onClick={onSelect}
      title={onSelect ? 'Show jobs and attempts' : undefined}
      style={{
        border: `1px solid ${selected ? ACCENT : failed ? COLORS.error : COLORS.border}`,
        boxShadow: selected ? `0 0 0 1px ${ACCENT}` : undefined,
        borderRadius: 4,
        padding: '0.4em 0.55em',
        background: 'rgba(128, 128, 128, 0.06)',
        cursor: onSelect ? 'pointer' : undefined,
      }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em' }}>
        {state && <StreamStatusIcon status={state.status} />}
        <code style={{ ...ellipsis, flex: 1 }}>{step.id}</code>
        {retries > 0 && (
          <span style={{ fontSize: '0.8em', color: COLORS.muted }} title={`Retried ${retries} time${retries > 1 ? 's' : ''}`}>
            <i className='fa fa-redo' /> {retries}
          </span>
        )}
        {!state && !!step.retries && (
          <span style={{ fontSize: '0.8em', color: COLORS.muted }} title='Automatic retries of a failed pipeline'>
            <i className='fa fa-redo' /> x{step.retries}
          </span>
        )}
        {state?.triggeredAt && (
          <span style={{ fontSize: '0.85em', color: COLORS.muted, whiteSpace: 'nowrap' }}>{duration(state.triggeredAt, state.finishedAt)}</span>
        )}
      </div>
      <div style={{ ...ellipsis, fontWeight: 600, margin: '0.15em 0' }}>
        {step.name || <PipelineName data={data} connection={step.connection} id={step.pipeline} />}
      </div>
      <div style={{ ...ellipsis, fontSize: '0.85em', color: COLORS.muted }} title={ref}>
        {step.connection} · <i className='fa fa-code-branch' /> {ref}
      </div>
      {state && (state.url || state.runNumber || state.sha) && (
        <div style={{ fontSize: '0.85em', marginTop: '0.2em', display: 'flex', gap: '0.5em', flexWrap: 'wrap' }}>
          <RunLink state={state} />
        </div>
      )}
      {state && (
        <div style={{ fontSize: '0.85em', marginTop: '0.2em' }}>
          {state.status === 'pending' && state.retryAt ? <RetryWait at={state.retryAt} /> : <StreamStatusLabel status={state.status} />}
        </div>
      )}
      {state?.message && (
        <div
          style={{
            ...ellipsis,
            fontSize: '0.85em',
            marginTop: '0.2em',
            color: failed ? COLORS.error : COLORS.muted,
          }}
          title={state.message}>
          {state.message}
        </div>
      )}
    </div>
  );
};

interface BoardProps {
  data: ConnData;
  spec: StreamSpec;
  run?: StreamRun;
  selected?: string;
  onSelect?: (id: string) => void;
}

// StagesBoard shows a Stream as stage columns, with run states when given.
export const StagesBoard = ({ data, spec, run, selected, onSelect }: BoardProps) => {
  if (spec.stages.length === 0) {
    return <Muted>This stream has no stages yet.</Muted>;
  }
  return (
    <div style={{ overflowX: 'auto', paddingBottom: '0.5em' }}>
      <div style={{ display: 'flex', alignItems: 'stretch', gap: '0.5em' }}>
        {spec.stages.map((stage, s) => (
          <React.Fragment key={s}>
            {s > 0 && (
              <div style={{ alignSelf: 'center', color: COLORS.muted }}>
                <i className='fa fa-chevron-right' />
              </div>
            )}
            <div
              style={{
                flex: '0 0 250px',
                width: 250,
                background: COLORS.surface,
                border: `1px solid ${COLORS.border}`,
                borderRadius: 4,
                padding: '0.5em',
                display: 'flex',
                flexDirection: 'column',
                gap: '0.4em',
              }}>
              <div style={{ ...ellipsis, fontWeight: 600 }}>{stage.name || `Stage ${s + 1}`}</div>
              {stage.steps.map(step => (
                <BoardStep
                  key={step.id}
                  data={data}
                  step={step}
                  state={run?.steps.find(x => x.id === step.id)}
                  retries={earlierTries(run, step.id).length}
                  selected={selected === step.id}
                  onSelect={onSelect && (() => onSelect(step.id))}
                />
              ))}
            </div>
          </React.Fragment>
        ))}
      </div>
    </div>
  );
};

const AttemptRow = ({ client, run, stepID, connection, entry }: { client: ZeaClient; run: StreamRun; stepID: string; connection: string; entry: EarlierTry }) => {
  const { state } = entry;
  const [open, setOpen] = React.useState(false);
  return (
    <div style={{ borderTop: `1px solid ${COLORS.border}`, padding: '0.35em 0' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', flexWrap: 'wrap' }}>
        <button
          className='argo-button argo-button--base-o'
          style={{ padding: '0 0.5em', minWidth: 0 }}
          disabled={!state.runId}
          title={open ? 'Hide jobs' : 'Show jobs'}
          onClick={() => setOpen(!open)}>
          <i className={open ? 'fa fa-angle-down' : 'fa fa-angle-right'} />
        </button>
        <StreamStatusIcon status={state.status} />
        <b>{entry.label}</b>
        <StreamStatusLabel status={state.status} />
        <RunLink state={state} />
        <Muted>
          {[entry.by, ago(state.triggeredAt || entry.startedAt), state.triggeredAt && duration(state.triggeredAt, state.finishedAt)]
            .filter(Boolean)
            .join(' · ')}
        </Muted>
      </div>
      {state.message && (
        <div style={{ fontSize: '0.85em', color: state.status === 'failed' ? COLORS.error : COLORS.muted, wordBreak: 'break-word', marginLeft: '2.4em' }}>
          {state.message}
        </div>
      )}
      {open && state.runId && (
        <div style={{ marginLeft: '2.4em' }}>
          <RunJobs client={client} connection={connection} runID={state.runId} refreshKey={0} stream={{ name: run.stream, run: run.id, step: stepID }} />
        </div>
      )}
    </div>
  );
};

const WAITING: Record<string, string> = {
  pending: 'Waiting for the steps it depends on.',
  starting: 'The pipeline was triggered; waiting for the provider to report its run.',
};

// StepDetails shows the provider jobs of a run step and its earlier attempts.
const StepDetails = ({ client, data, run, stepID, onClose }: { client: ZeaClient; data: ConnData; run: StreamRun; stepID: string; onClose: () => void }) => {
  const state = run.steps.find(s => s.id === stepID);
  if (!state) {
    return null;
  }
  const earlier = earlierTries(run, stepID);
  const def = run.spec?.stages.flatMap(s => s.steps).find(s => s.id === stepID);
  return (
    <div className='white-box' style={{ padding: '0.8em 1em', marginTop: '0.8em' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', flexWrap: 'wrap' }}>
        <StreamStatusIcon status={state.status} />
        <code>{state.id}</code>
        <b style={{ ...ellipsis }}>{state.name || <PipelineName data={data} connection={state.connection} id={state.pipeline} />}</b>
        <Muted>
          {state.connection}
          {state.ref && (
            <>
              {' · '}
              <i className='fa fa-code-branch' /> {state.ref}
            </>
          )}
        </Muted>
        {state.status === 'pending' && state.retryAt ? <RetryWait at={state.retryAt} /> : <StreamStatusLabel status={state.status} />}
        {!!def?.retries && (
          <Badge title='Automatic retries of a failed pipeline in this attempt'>
            try {(state.try ?? 0) + 1} of {def.retries + 1}
          </Badge>
        )}
        <RunLink state={state} />
        {state.triggeredAt && <Muted>{duration(state.triggeredAt, state.finishedAt)}</Muted>}
        <div style={{ flex: 1 }} />
        <button className='argo-button argo-button--base-o' title='Close' onClick={onClose}>
          <i className='fa fa-times' />
        </button>
      </div>
      {state.message && (
        <div
          style={{
            marginTop: '0.3em',
            color: state.status === 'failed' ? COLORS.error : COLORS.muted,
            wordBreak: 'break-word',
            whiteSpace: 'pre-wrap',
          }}>
          {state.message}
        </div>
      )}
      <div style={{ marginTop: '0.5em' }}>
        {state.runId ? (
          <RunJobs
            key={state.runId}
            client={client}
            connection={state.connection}
            runID={state.runId}
            refreshKey={0}
            stream={{ name: run.stream, run: run.id, step: stepID }}
          />
        ) : (
          <Muted>{state.status === 'pending' && state.retryAt ? 'Waiting for the retry delay.' : WAITING[state.status] ?? 'The step has no pipeline run.'}</Muted>
        )}
      </div>
      {earlier.length > 0 && (
        <div style={{ marginTop: '0.8em' }}>
          <div style={{ fontWeight: 600, marginBottom: '0.2em' }}>Earlier tries</div>
          {earlier.map(entry => (
            <AttemptRow key={entry.key} client={client} run={run} stepID={stepID} connection={state.connection} entry={entry} />
          ))}
        </div>
      )}
    </div>
  );
};

interface RunFormProps {
  client: ZeaClient;
  stream: Stream;
  // Values to start from (e.g. the params of an earlier run).
  initial?: Record<string, string>;
  title?: string;
  onStarted: (run: StreamRun) => void;
  onClose: () => void;
}

interface BranchParamProps {
  client: ZeaClient;
  stream: string;
  connection?: string;
  value: string;
  onChange: (v: string) => void;
}

// BranchParam lists branches through the stream, so run access to the stream
// is enough.
const BranchParam = ({ client, stream, connection, value, onChange }: BranchParamProps) => {
  const [branches] = useLoad(
    () => (connection ? client.streamBranches(stream, connection) : Promise.resolve(undefined)),
    [client, stream, connection],
  );
  return (
    <>
      <ValueField
        value={value}
        refs={[]}
        branches={branches.state === 'ok' ? branches.data : undefined}
        placeholder={branches.state === 'loading' ? 'Loading branches...' : ''}
        onChange={onChange}
      />
      {branches.state === 'error' && <ErrorText text={branches.error} />}
    </>
  );
};

// StreamRunForm asks for the params of a Stream and starts it.
export const StreamRunForm = ({ client, stream, initial, title, onStarted, onClose }: RunFormProps) => {
  const [values, setValues] = React.useState<Record<string, string>>(() =>
    Object.fromEntries(
      stream.params.map(p => [p.name, initial?.[p.name] ?? p.default ?? (p.type === 'boolean' ? 'false' : '')]),
    ),
  );
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const set = (name: string, v: string) => setValues(prev => ({ ...prev, [name]: v }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const missing = stream.params.find(p => p.required && (values[p.name] ?? '').trim() === '');
    if (missing) {
      setError(`"${missing.name}" is required.`);
      return;
    }
    setBusy(true);
    setError('');
    try {
      onStarted(await client.startStream(stream.name, values));
    } catch (err) {
      setError(describeError(err));
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em', maxWidth: 640 }}>
      <div style={{ fontWeight: 600, marginBottom: '0.6em' }}>
        {title ?? 'Run'} <b>{stream.name}</b>
      </div>
      {stream.params.length === 0 && <Muted>This stream has no params.</Muted>}
      {stream.params.map(p => (
        <Field key={p.name} label={<code>{p.name}</code>} hint={p.type} required={p.required} help={p.description}>
          {p.type === 'boolean' ? (
            <label style={{ cursor: 'pointer' }}>
              <input type='checkbox' checked={values[p.name] === 'true'} onChange={e => set(p.name, e.target.checked ? 'true' : 'false')} />{' '}
              {values[p.name] === 'true' ? 'true' : 'false'}
            </label>
          ) : p.type === 'choice' ? (
            <select className='argo-field' style={{ width: '100%' }} value={values[p.name] ?? ''} onChange={e => set(p.name, e.target.value)}>
              {!p.required && <option value=''>(empty)</option>}
              {(p.options ?? []).map(o => (
                <option key={o} value={o}>
                  {o}
                </option>
              ))}
            </select>
          ) : p.type === 'branch' ? (
            <BranchParam client={client} stream={stream.name} connection={p.connection} value={values[p.name] ?? ''} onChange={v => set(p.name, v)} />
          ) : (
            <input className='argo-field' style={{ width: '100%' }} value={values[p.name] ?? ''} onChange={e => set(p.name, e.target.value)} />
          )}
        </Field>
      ))}
      {error && <ErrorText text={error} />}
      <div style={{ display: 'flex', gap: '0.5em', marginTop: '0.6em' }}>
        <button type='submit' className='argo-button argo-button--base' disabled={busy}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-play'} /> Run
        </button>
        <button type='button' className='argo-button argo-button--base-o' disabled={busy} onClick={onClose}>
          Cancel
        </button>
      </div>
    </form>
  );
};

// RunAgain starts a new run of the current Stream with the params of an
// earlier run.
const RunAgain = ({ client, stream, params, onClose }: { client: ZeaClient; stream: string; params: Record<string, string>; onClose: () => void }) => {
  const [st] = useLoad(() => client.stream(stream), [client, stream]);
  if (st.state === 'loading') {
    return <Muted>Loading...</Muted>;
  }
  if (st.state === 'error') {
    return <ErrorText text={st.error} />;
  }
  if (st.data.problems.length > 0) {
    return <ErrorText text={`The stream cannot run: ${st.data.problems[0].message}`} />;
  }
  return (
    <StreamRunForm
      client={client}
      stream={st.data}
      initial={params}
      title='Run again'
      onStarted={r => navigate({ view: 'streams', stream, srun: r.id })}
      onClose={onClose}
    />
  );
};

// StepDots is a compact view of a run's step statuses, stage by stage.
const StepDots = ({ run }: { run: StreamRun }) => (
  <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.15em', flexWrap: 'wrap' }}>
    {run.steps.map((s, i) => (
      <React.Fragment key={s.id}>
        {i > 0 && s.stage !== run.steps[i - 1].stage && <i className='fa fa-angle-right' style={{ color: COLORS.muted, margin: '0 0.15em' }} />}
        <span title={`${s.id}: ${s.status}${s.message ? ` - ${s.message}` : ''}`}>
          <StreamStatusIcon status={s.status} />
        </span>
      </React.Fragment>
    ))}
  </span>
);

const FILTERS: { key: 'all' | StreamRunStatus; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'running', label: 'Running' },
  { key: 'failed', label: 'Failed' },
  { key: 'succeeded', label: 'Succeeded' },
  { key: 'cancelled', label: 'Cancelled' },
];

// StreamRunHistory lists the runs of a Stream.
export const StreamRunHistory = ({ client, stream, refreshKey }: { client: ZeaClient; stream: string; refreshKey: number }) => {
  const runs = usePoll(
    () => client.streamRuns(stream),
    [client, stream, refreshKey],
    HISTORY_POLL_MS,
    d => (d ?? []).some(r => r.status === 'running'),
  );
  const [filter, setFilter] = React.useState<'all' | StreamRunStatus>('all');
  const [shown, setShown] = React.useState(HISTORY_PAGE);
  const all = runs.data ?? [];
  const list = filter === 'all' ? all : all.filter(r => r.status === filter);
  const count = (k: 'all' | StreamRunStatus) => (k === 'all' ? all.length : all.filter(r => r.status === k).length);

  return (
    <div className='white-box' style={{ padding: '0.8em 1em' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em', marginBottom: '0.4em', flexWrap: 'wrap' }}>
        <b style={{ marginRight: '0.6em' }}>Runs</b>
        {FILTERS.filter(f => f.key === 'all' || count(f.key) > 0).map(f => (
          <a
            key={f.key}
            onClick={() => {
              setFilter(f.key);
              setShown(HISTORY_PAGE);
            }}
            style={{
              cursor: 'pointer',
              padding: '0.05em 0.6em',
              borderRadius: 10,
              fontSize: '0.85em',
              border: `1px solid ${filter === f.key ? ACCENT : COLORS.border}`,
              color: filter === f.key ? undefined : COLORS.muted,
            }}>
            {f.label} {count(f.key)}
          </a>
        ))}
        <div style={{ flex: 1 }} />
        <button className='argo-button argo-button--base-o' title='Reload runs' onClick={runs.reload}>
          <i className={runs.loading ? 'fa fa-redo fa-spin' : 'fa fa-redo'} />
        </button>
      </div>
      {runs.error && <ErrorText text={runs.error} />}
      {!runs.data && runs.loading && <Muted>Loading...</Muted>}
      {runs.data && list.length === 0 && <Muted>{all.length === 0 ? 'No runs yet.' : 'No runs match.'}</Muted>}
      {list.slice(0, shown).map(r => {
        const params = Object.entries(r.params);
        return (
          <div
            key={r.id}
            onClick={() => navigate({ view: 'streams', stream, srun: r.id })}
            style={{ borderTop: `1px solid ${COLORS.border}`, padding: '0.45em 0', cursor: 'pointer', display: 'flex', gap: '0.6em', alignItems: 'baseline' }}>
            <StreamStatusIcon status={r.status} />
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: 'flex', gap: '0.6em', alignItems: 'baseline', flexWrap: 'wrap' }}>
                <StreamStatusLabel status={r.status} />
                <code>{r.id}</code>
                {r.attempt > 1 && (
                  <Badge title={r.retriedBy ? `Retried by ${r.retriedBy} ${ago(r.retriedAt)}` : undefined}>attempt {r.attempt}</Badge>
                )}
                <Muted>{[r.user, ago(r.createdAt), duration(r.retriedAt || r.createdAt, r.finishedAt)].filter(Boolean).join(' · ')}</Muted>
                <StepDots run={r} />
              </div>
              {(params.length > 0 || r.message) && (
                <div style={{ ...ellipsis, fontSize: '0.85em', color: COLORS.muted }}>
                  {params.map(([k, v]) => `${k}=${v || '""'}`).join('  ')}
                  {params.length > 0 && r.message && ' · '}
                  {r.message}
                </div>
              )}
            </div>
          </div>
        );
      })}
      {list.length > shown && (
        <div style={{ borderTop: `1px solid ${COLORS.border}`, paddingTop: '0.5em' }}>
          <a style={{ cursor: 'pointer' }} onClick={() => setShown(n => n + HISTORY_PAGE)}>
            Show {Math.min(HISTORY_PAGE, list.length - shown)} more of {list.length - shown}
          </a>
        </div>
      )}
    </div>
  );
};

// focusStep picks the step to show when the user has not chosen one: the
// first active step, otherwise the first failed one.
function focusStep(run: StreamRun): string | undefined {
  return (run.steps.find(s => ACTIVE_STEP.includes(s.status) || (s.status === 'pending' && s.retryAt)) ?? run.steps.find(s => s.status === 'failed'))?.id;
}

// StreamRunView follows one run until it finishes.
export const StreamRunView = ({ client, data, stream, runID }: { client: ZeaClient; data: ConnData; stream: string; runID: string }) => {
  const run = usePoll(
    () => client.streamRun(stream, runID),
    [client, stream, runID],
    RUN_POLL_MS,
    d => !d || d.status === 'running',
  );
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const [again, setAgain] = React.useState(false);
  // Hides the run actions without run access; when the stream cannot be
  // loaded (e.g. deleted) the backend decides.
  const [st] = useLoad(() => client.stream(stream), [client, stream]);
  const canRun = st.state === 'error' || (st.state === 'ok' && can(st.data.actions, 'run'));
  // undefined follows the active step; null means the panel was closed.
  const [picked, setPicked] = React.useState<string | null>();
  const r = run.data;
  const selected = picked === undefined ? r && focusStep(r) : picked ?? undefined;

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError('');
    try {
      await fn();
      run.reload();
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const cancel = () => {
    if (window.confirm(`Cancel run ${runID}? Running pipelines are cancelled, pending steps are skipped.`)) {
      act(() => client.cancelStreamRun(stream, runID));
    }
  };

  const retry = () => {
    if (window.confirm('Run the steps that did not succeed again? Succeeded steps are kept; the run uses the stream as it was when it started.')) {
      setPicked(undefined);
      act(() => client.retryStreamRun(stream, runID));
    }
  };

  const retryable = r && (r.status === 'failed' || r.status === 'cancelled') && r.steps.some(s => s.status !== 'succeeded');
  const params = Object.entries(r?.params ?? {});
  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.6em', marginBottom: '0.8em', flexWrap: 'wrap' }}>
        <button className='argo-button argo-button--base-o' onClick={() => navigate({ view: 'streams', stream })}>
          <i className='fa fa-arrow-left' /> {stream}
        </button>
        {r && (
          <>
            <StreamStatusIcon status={r.status} />
            <StreamStatusLabel status={r.status} />
            <b>
              Run <code>{r.id}</code>
            </b>
            {r.attempt > 1 && <Badge>attempt {r.attempt}</Badge>}
            <Muted>
              {[
                `by ${r.user}`,
                ago(r.createdAt),
                r.retriedBy && `retried by ${r.retriedBy} ${ago(r.retriedAt)}`,
                duration(r.retriedAt || r.createdAt, r.finishedAt),
              ]
                .filter(Boolean)
                .join(' · ')}
            </Muted>
          </>
        )}
        <div style={{ flex: 1 }} />
        {canRun && r?.status === 'running' && (
          <button className='argo-button argo-button--base-o' disabled={busy || r.cancelRequested} onClick={cancel}>
            <i className={r.cancelRequested ? 'fa fa-circle-notch fa-spin' : 'fa fa-stop'} /> {r.cancelRequested ? 'Cancelling...' : 'Cancel'}
          </button>
        )}
        {canRun && retryable && (
          <button className='argo-button argo-button--base' disabled={busy} onClick={retry} title='Run the failed, cancelled and skipped steps again'>
            <i className='fa fa-redo' /> Retry failed
          </button>
        )}
        {canRun && r && r.status !== 'running' && (
          <button className='argo-button argo-button--base-o' disabled={busy} onClick={() => setAgain(!again)} title='Start a new run with these params'>
            <i className='fa fa-play' /> Run again
          </button>
        )}
        <button className='argo-button argo-button--base-o' title='Reload' onClick={run.reload}>
          <i className={run.loading ? 'fa fa-redo fa-spin' : 'fa fa-redo'} />
        </button>
      </div>
      {error && <ErrorText text={error} />}
      {run.error && <ErrorText text={run.error} />}
      {!r && run.loading && <Muted>Loading...</Muted>}
      {r && (
        <>
          {again && r.status !== 'running' && (
            <RunAgain client={client} stream={stream} params={r.params} onClose={() => setAgain(false)} />
          )}
          {r.message && (
            <div style={{ color: r.status === 'failed' ? COLORS.error : COLORS.muted, marginBottom: '0.6em', wordBreak: 'break-word' }}>
              {r.message}
            </div>
          )}
          {params.length > 0 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.4em', marginBottom: '0.8em' }}>
              {params.map(([k, v]) => (
                <Badge key={k}>
                  {k} = <b>{v || '""'}</b>
                </Badge>
              ))}
            </div>
          )}
          {r.spec ? (
            <>
              <StagesBoard data={data} spec={r.spec} run={r} selected={selected} onSelect={id => setPicked(id === selected ? null : id)} />
              <Help>Click a step to see its jobs and earlier attempts.</Help>
              {selected && <StepDetails client={client} data={data} run={r} stepID={selected} onClose={() => setPicked(null)} />}
            </>
          ) : (
            <Help>The run has no stages.</Help>
          )}
        </>
      )}
    </div>
  );
};
