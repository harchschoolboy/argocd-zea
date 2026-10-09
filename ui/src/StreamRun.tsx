import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { navigate } from './route';
import { ago, duration, usePoll } from './Runs';
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
import { Stream, StreamRun, StreamRunStep, StreamSpec, StreamStep } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

const RUN_POLL_MS = 4_000;
const HISTORY_POLL_MS = 10_000;

const BoardStep = ({ data, step, state }: { data: ConnData; step: StreamStep; state?: StreamRunStep }) => {
  const failed = state?.status === 'failed';
  const ref = state?.ref || step.ref;
  return (
    <div
      style={{
        border: `1px solid ${failed ? COLORS.error : COLORS.border}`,
        borderRadius: 4,
        padding: '0.4em 0.55em',
        background: 'rgba(128, 128, 128, 0.06)',
      }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em' }}>
        {state && <StreamStatusIcon status={state.status} />}
        <code style={{ ...ellipsis, flex: 1 }}>{step.id}</code>
        {state?.triggeredAt && (
          <span style={{ fontSize: '0.85em', color: COLORS.muted, whiteSpace: 'nowrap' }}>
            {duration(state.triggeredAt, state.finishedAt)}
          </span>
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
          {state.url ? (
            <a href={state.url} target='_blank' rel='noopener noreferrer' title='Open in the CI provider'>
              <i className='fa fa-external-link-alt' /> {state.runNumber ? `#${state.runNumber}` : 'Open'}
            </a>
          ) : (
            state.runNumber && <span>#{state.runNumber}</span>
          )}
          {state.sha && <code style={{ color: COLORS.muted }}>{state.sha.slice(0, 7)}</code>}
        </div>
      )}
      {state && (
        <div style={{ fontSize: '0.85em', marginTop: '0.2em' }}>
          <StreamStatusLabel status={state.status} />
        </div>
      )}
      {state?.message && (
        <div
          style={{
            fontSize: '0.85em',
            marginTop: '0.2em',
            color: failed ? COLORS.error : COLORS.muted,
            wordBreak: 'break-word',
            whiteSpace: 'pre-wrap',
          }}>
          {state.message}
        </div>
      )}
    </div>
  );
};

// StagesBoard shows a Stream as stage columns, with run states when given.
export const StagesBoard = ({ data, spec, run }: { data: ConnData; spec: StreamSpec; run?: StreamRun }) => {
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
                <BoardStep key={step.id} data={data} step={step} state={run?.steps.find(x => x.id === step.id)} />
              ))}
            </div>
          </React.Fragment>
        ))}
      </div>
    </div>
  );
};

interface RunFormProps {
  client: ZeaClient;
  data: ConnData;
  stream: Stream;
  onStarted: (run: StreamRun) => void;
  onClose: () => void;
}

const BranchParam = ({ data, connection, value, onChange }: { data: ConnData; connection?: string; value: string; onChange: (v: string) => void }) => {
  const [branches] = useLoad(() => (connection ? data.branches(connection) : Promise.resolve(undefined)), [data, connection]);
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
export const StreamRunForm = ({ client, data, stream, onStarted, onClose }: RunFormProps) => {
  const [values, setValues] = React.useState<Record<string, string>>(() =>
    Object.fromEntries(stream.params.map(p => [p.name, p.default ?? (p.type === 'boolean' ? 'false' : '')])),
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
    <form
      onSubmit={submit}
      className='white-box'
      style={{ padding: '0.8em 1em', marginBottom: '1em', maxWidth: 640 }}>
      <div style={{ fontWeight: 600, marginBottom: '0.6em' }}>
        Run <b>{stream.name}</b>
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
            <BranchParam data={data} connection={p.connection} value={values[p.name] ?? ''} onChange={v => set(p.name, v)} />
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

function stepSummary(run: StreamRun): string {
  const counts: Record<string, number> = {};
  for (const s of run.steps) {
    counts[s.status] = (counts[s.status] ?? 0) + 1;
  }
  return ['succeeded', 'running', 'starting', 'pending', 'failed', 'cancelled', 'skipped']
    .filter(k => counts[k])
    .map(k => `${counts[k]} ${k}`)
    .join(', ');
}

// StreamRunHistory lists the recent runs of a Stream.
export const StreamRunHistory = ({ client, stream, refreshKey }: { client: ZeaClient; stream: string; refreshKey: number }) => {
  const runs = usePoll(
    () => client.streamRuns(stream),
    [client, stream, refreshKey],
    HISTORY_POLL_MS,
    d => (d ?? []).some(r => r.status === 'running'),
  );
  return (
    <div className='white-box' style={{ padding: '0.8em 1em' }}>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: '0.4em' }}>
        <b style={{ flex: 1 }}>Runs</b>
        <button className='argo-button argo-button--base-o' title='Reload runs' onClick={runs.reload}>
          <i className={runs.loading ? 'fa fa-redo fa-spin' : 'fa fa-redo'} />
        </button>
      </div>
      {runs.error && <ErrorText text={runs.error} />}
      {!runs.data && runs.loading && <Muted>Loading...</Muted>}
      {runs.data && runs.data.length === 0 && <Muted>No runs yet.</Muted>}
      {runs.data?.map(r => (
        <div
          key={r.id}
          onClick={() => navigate({ view: 'streams', stream, srun: r.id })}
          style={{ borderTop: `1px solid ${COLORS.border}`, padding: '0.45em 0', cursor: 'pointer', display: 'flex', gap: '0.6em', alignItems: 'baseline' }}>
          <StreamStatusIcon status={r.status} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ display: 'flex', gap: '0.6em', alignItems: 'baseline', flexWrap: 'wrap' }}>
              <StreamStatusLabel status={r.status} />
              <code>{r.id}</code>
              <Muted>
                {[r.user, ago(r.createdAt), duration(r.createdAt, r.finishedAt)].filter(Boolean).join(' · ')}
              </Muted>
            </div>
            <div style={{ fontSize: '0.85em', color: COLORS.muted, wordBreak: 'break-word' }}>
              {stepSummary(r)}
              {r.message && <> · {r.message}</>}
            </div>
          </div>
        </div>
      ))}
    </div>
  );
};

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
  const r = run.data;

  const cancel = async () => {
    if (!window.confirm(`Cancel run ${runID}? Running pipelines are cancelled, pending steps are skipped.`)) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      await client.cancelStreamRun(stream, runID);
      run.reload();
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

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
            <Muted>
              {[`by ${r.user}`, ago(r.createdAt), duration(r.createdAt, r.finishedAt)].filter(Boolean).join(' · ')}
            </Muted>
          </>
        )}
        <div style={{ flex: 1 }} />
        {r?.status === 'running' && (
          <button className='argo-button argo-button--base-o' disabled={busy || r.cancelRequested} onClick={cancel}>
            <i className={r.cancelRequested ? 'fa fa-circle-notch fa-spin' : 'fa fa-stop'} /> {r.cancelRequested ? 'Cancelling...' : 'Cancel'}
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
          {r.spec ? <StagesBoard data={data} spec={r.spec} run={r} /> : <Help>The run has no stages.</Help>}
        </>
      )}
    </div>
  );
};
