import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { navigate, useRoute } from './route';
import { ago, usePoll } from './Runs';
import { Badge, ellipsis, Help, ProblemList, StreamStatusIcon, StreamStatusLabel } from './StreamFields';
import { StreamEditor } from './StreamEditor';
import { ConnData } from './streamModel';
import { StagesBoard, StreamRunForm, StreamRunHistory, StreamRunView } from './StreamRun';
import { Connection, Me, Stream, StreamExport, StreamRun } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

const StreamBadges = ({ stream }: { stream: Stream }) => (
  <>
    {stream.draftOf && <Badge title='An editable copy; export it to replace the original in git'>draft of {stream.draftOf}</Badge>}
    {!stream.editable && (
      <Badge title='Managed in git (Argo CD); edit a draft and export it'>
        <i className='fa fa-lock' /> git
      </Badge>
    )}
    {stream.problems.length > 0 ? (
      <Badge color={COLORS.warn} title={stream.problems.map(p => p.message).join('\n')}>
        {stream.problems.length} problem{stream.problems.length > 1 ? 's' : ''}
      </Badge>
    ) : null}
  </>
);

const CARD_POLL_MS = 10_000;

const LastStreamRun = ({ stream, runs }: { stream: string; runs: { data?: StreamRun[]; error: string } }) => {
  if (runs.error && !runs.data) {
    return <Muted>runs unavailable</Muted>;
  }
  if (!runs.data) {
    return <Muted>...</Muted>;
  }
  const r = runs.data[0];
  if (!r) {
    return <Muted>No runs yet.</Muted>;
  }
  return (
    <span
      style={{ cursor: 'pointer' }}
      onClick={e => {
        e.stopPropagation();
        navigate({ view: 'streams', stream, srun: r.id });
      }}>
      <StreamStatusIcon status={r.status} /> <StreamStatusLabel status={r.status} />{' '}
      <Muted>
        {ago(r.createdAt)} by {r.user}
      </Muted>
    </span>
  );
};

// paramDefaults are the values a run started without input gets, or
// undefined when a required param has no default.
function paramDefaults(stream: Stream): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const p of stream.params) {
    const v = p.default ?? (p.type === 'boolean' ? 'false' : '');
    if (p.required && v.trim() === '') {
      return undefined;
    }
    out[p.name] = v;
  }
  return out;
}

const StreamCard = ({ client, stream }: { client: ZeaClient; stream: Stream }) => {
  const steps = stream.stages.reduce((n, s) => n + s.steps.length, 0);
  const runs = usePoll(
    () => client.streamRuns(stream.name),
    [client, stream.name],
    CARD_POLL_MS,
    d => (d ?? []).some(r => r.status === 'running'),
  );
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const blocked = stream.problems.length > 0;

  const run = async (e: React.MouseEvent) => {
    e.stopPropagation();
    const defaults = paramDefaults(stream);
    if (!defaults) {
      // Required params without defaults need the run form.
      navigate({ view: 'streams', stream: stream.name, mode: 'run' });
      return;
    }
    const lines = Object.entries(defaults).map(([k, v]) => `  ${k} = ${v === '' ? '(empty)' : v}`);
    if (!window.confirm(`Run stream ${stream.name}?${lines.length > 0 ? `\n\nParams:\n${lines.join('\n')}` : ''}`)) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      await client.startStream(stream.name, defaults);
      runs.reload();
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className='white-box' style={{ padding: '0.8em 1em', cursor: 'pointer', margin: 0 }} onClick={() => navigate({ view: 'streams', stream: stream.name })}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em', flexWrap: 'wrap', flex: 1, minWidth: 0 }}>
          <i className='fa fa-stream' style={{ color: COLORS.muted }} />
          <b style={{ ...ellipsis, fontSize: '1.05em' }}>{stream.name}</b>
          <StreamBadges stream={stream} />
        </div>
        <button
          className='argo-button argo-button--base'
          style={{ flex: '0 0 auto' }}
          disabled={busy || blocked}
          title={blocked ? 'Fix the problems first' : paramDefaults(stream) ? 'Run with the default params' : 'Some params need a value'}
          onClick={run}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-play'} /> Run
        </button>
      </div>
      {stream.description && <div style={{ marginTop: '0.3em', wordBreak: 'break-word' }}>{stream.description}</div>}
      <div style={{ fontSize: '0.85em', color: COLORS.muted, marginTop: '0.3em', wordBreak: 'break-word' }}>
        {stream.stages.length} stage{stream.stages.length === 1 ? '' : 's'} ? {steps} step{steps === 1 ? '' : 's'}
        {stream.connections.length > 0 && <> ? {stream.connections.join(', ')}</>}
      </div>
      {error && (
        <div onClick={e => e.stopPropagation()}>
          <ErrorText text={error} />
        </div>
      )}
      <div style={{ marginTop: '0.5em', paddingTop: '0.4em', borderTop: `1px solid ${COLORS.border}` }}>
        <LastStreamRun stream={stream.name} runs={runs} />
      </div>
    </div>
  );
};

const ImportPanel = ({ client, onClose }: { client: ZeaClient; onClose: () => void }) => {
  const [yaml, setYaml] = React.useState('');
  const [replace, setReplace] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      const s = await client.importStream(yaml, replace);
      navigate({ view: 'streams', stream: s.name });
    } catch (err) {
      setError(describeError(err));
      setBusy(false);
    }
  };
  return (
    <div className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em' }}>
      <b>Import a stream</b>
      <Help>Paste an exported ConfigMap, or a document with name, params and stages. The stream is created as UI-managed.</Help>
      <textarea
        className='argo-field'
        rows={14}
        spellCheck={false}
        value={yaml}
        onChange={e => setYaml(e.target.value)}
        style={{ width: '100%', fontFamily: 'monospace', marginTop: '0.5em' }}
      />
      <label style={{ display: 'block', cursor: 'pointer', margin: '0.4em 0' }}>
        <input type='checkbox' checked={replace} onChange={e => setReplace(e.target.checked)} /> Replace a UI-managed stream with the
        same name
      </label>
      {error && <ErrorText text={error} />}
      <div style={{ display: 'flex', gap: '0.5em' }}>
        <button className='argo-button argo-button--base' disabled={busy || !yaml.trim()} onClick={submit}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-file-import'} /> Import
        </button>
        <button className='argo-button argo-button--base-o' disabled={busy} onClick={onClose}>
          Cancel
        </button>
      </div>
    </div>
  );
};

const ExportPanel = ({ client, stream, onClose }: { client: ZeaClient; stream: string; onClose: () => void }) => {
  const [exp] = useLoad<StreamExport>(() => client.exportStream(stream), [client, stream]);
  const [copied, setCopied] = React.useState(false);
  const download = (e: StreamExport) => {
    const url = URL.createObjectURL(new Blob([e.yaml], { type: 'application/yaml' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = e.fileName;
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <div className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em' }}>
        <b style={{ flex: 1 }}>Export for git</b>
        {exp.state === 'ok' && (
          <>
            <button
              className='argo-button argo-button--base-o'
              onClick={() => navigator.clipboard.writeText(exp.data.yaml).then(() => setCopied(true))}>
              <i className={copied ? 'fa fa-check' : 'fa fa-copy'} /> Copy
            </button>
            <button className='argo-button argo-button--base-o' onClick={() => download(exp.data)}>
              <i className='fa fa-download' /> {exp.data.fileName}
            </button>
          </>
        )}
        <button className='argo-button argo-button--base-o' title='Close' onClick={onClose}>
          <i className='fa fa-times' />
        </button>
      </div>
      {exp.state === 'loading' && <Muted>Loading...</Muted>}
      {exp.state === 'error' && <ErrorText text={exp.error} />}
      {exp.state === 'ok' && (
        <>
          <Help>
            Commit this ConfigMap to the repository Argo CD syncs into the Zea namespace. Once synced, the stream <b>{exp.data.name}</b>{' '}
            is read-only in Zea.
          </Help>
          <textarea
            className='argo-field'
            readOnly
            rows={16}
            spellCheck={false}
            value={exp.data.yaml}
            style={{ width: '100%', fontFamily: 'monospace', marginTop: '0.5em' }}
          />
        </>
      )}
    </div>
  );
};

const StreamList = ({ client, me }: { client: ZeaClient; me: Me }) => {
  const [list, reload] = useLoad(() => client.streams(), [client]);
  const [importing, setImporting] = React.useState(false);
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '1em' }}>
        <Muted>Pipelines of several connections, run in order with shared params.</Muted>
        <div style={{ flex: 1 }} />
        {me.isAdmin && (
          <>
            <button className='argo-button argo-button--base-o' onClick={() => setImporting(v => !v)}>
              <i className='fa fa-file-import' /> Import
            </button>
            <button className='argo-button argo-button--base' onClick={() => navigate({ view: 'streams', mode: 'new' })}>
              <i className='fa fa-plus' /> New stream
            </button>
          </>
        )}
        <button className='argo-button argo-button--base-o' onClick={reload}>
          <i className='fa fa-redo' /> Refresh
        </button>
      </div>
      {importing && <ImportPanel client={client} onClose={() => setImporting(false)} />}
      {list.state === 'loading' && <Muted>Loading streams...</Muted>}
      {list.state === 'error' && <ErrorText text={list.error} />}
      {list.state === 'ok' && list.data.length === 0 && (
        <div className='white-box'>
          <div className='white-box__details'>
            {me.isAdmin
              ? 'No streams yet. Use "New stream" to chain pipelines of your connections.'
              : 'No streams are available to you. A stream is visible when you may use every connection it runs.'}
          </div>
        </div>
      )}
      {list.state === 'ok' && list.data.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: '1em' }}>
          {list.data.map(s => (
            <StreamCard key={s.name} client={client} stream={s} />
          ))}
        </div>
      )}
    </>
  );
};

type Panel = 'none' | 'run' | 'export';

const StreamDetail = ({ client, data, me, name, openRun }: { client: ZeaClient; data: ConnData; me: Me; name: string; openRun?: boolean }) => {
  const [st, reload] = useLoad(() => client.stream(name), [client, name]);
  const [panel, setPanel] = React.useState<Panel>(openRun ? 'run' : 'none');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const [runsKey, setRunsKey] = React.useState(0);

  const act = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError('');
    try {
      await fn();
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const draft = () => {
    const base = st.state === 'ok' ? st.data.draftOf || name : name;
    const draftName = window.prompt('Name of the draft', `${base}-draft`);
    if (draftName) {
      act(async () => {
        const d = await client.draftStream(name, draftName.trim());
        navigate({ view: 'streams', stream: d.name, mode: 'edit' });
      });
    }
  };

  const remove = () => {
    if (window.confirm(`Delete stream ${name}? Its run history is kept until it is pruned.`)) {
      act(async () => {
        await client.deleteStream(name);
        navigate({ view: 'streams' });
      });
    }
  };

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '1em', flexWrap: 'wrap' }}>
        <button className='argo-button argo-button--base-o' onClick={() => navigate({ view: 'streams' })}>
          <i className='fa fa-arrow-left' /> Streams
        </button>
        <b style={{ fontSize: '1.1em' }}>{name}</b>
        {st.state === 'ok' && <StreamBadges stream={st.data} />}
        <div style={{ flex: 1 }} />
        {st.state === 'ok' && (
          <>
            <button
              className='argo-button argo-button--base'
              disabled={busy || st.data.problems.length > 0}
              title={st.data.problems.length > 0 ? 'Fix the problems first' : 'Start this stream'}
              onClick={() => setPanel(panel === 'run' ? 'none' : 'run')}>
              <i className='fa fa-play' /> Run
            </button>
            {me.isAdmin && st.data.editable && (
              <button className='argo-button argo-button--base-o' disabled={busy} onClick={() => navigate({ view: 'streams', stream: name, mode: 'edit' })}>
                <i className='fa fa-pencil-alt' /> Edit
              </button>
            )}
            {me.isAdmin && (
              <button className='argo-button argo-button--base-o' disabled={busy} onClick={draft} title='Copy into an editable draft'>
                <i className='fa fa-copy' /> Edit as draft
              </button>
            )}
            <button className='argo-button argo-button--base-o' disabled={busy} onClick={() => setPanel(panel === 'export' ? 'none' : 'export')}>
              <i className='fa fa-file-export' /> Export
            </button>
            {me.isAdmin && st.data.editable && (
              <button className='argo-button argo-button--base-o' disabled={busy} onClick={remove} title='Delete stream'>
                <i className='fa fa-trash' />
              </button>
            )}
          </>
        )}
        <button
          className='argo-button argo-button--base-o'
          title='Reload'
          onClick={() => {
            reload();
            setRunsKey(k => k + 1);
          }}>
          <i className='fa fa-redo' />
        </button>
      </div>
      {error && <ErrorText text={error} />}
      {st.state === 'loading' && <Muted>Loading...</Muted>}
      {st.state === 'error' && <ErrorText text={st.error} />}
      {st.state === 'ok' && (
        <>
          {st.data.description && <div style={{ marginBottom: '0.8em', wordBreak: 'break-word' }}>{st.data.description}</div>}
          {st.data.problems.length > 0 && (
            <div className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em', color: COLORS.warn }}>
              <b>
                <i className='fa fa-exclamation-triangle' /> The stream cannot run yet
              </b>
              <ProblemList problems={st.data.problems} />
            </div>
          )}
          {panel === 'run' && (
            <StreamRunForm
              client={client}
              data={data}
              stream={st.data}
              onStarted={r => navigate({ view: 'streams', stream: name, srun: r.id })}
              onClose={() => setPanel('none')}
            />
          )}
          {panel === 'export' && <ExportPanel client={client} stream={name} onClose={() => setPanel('none')} />}
          <div style={{ marginBottom: '1em' }}>
            <StagesBoard data={data} spec={st.data} />
          </div>
          <StreamRunHistory client={client} stream={name} refreshKey={runsKey} />
        </>
      )}
    </>
  );
};

const EditorPage = ({ client, data, name }: { client: ZeaClient; data: ConnData; name?: string }) => {
  const [loaded] = useLoad(
    async (): Promise<{ stream?: Stream; connections: Connection[] }> => {
      const [stream, connections] = await Promise.all([name ? client.stream(name) : Promise.resolve(undefined), client.connections()]);
      return { stream, connections };
    },
    [client, name],
  );
  if (loaded.state === 'loading') {
    return <Muted>Loading...</Muted>;
  }
  if (loaded.state === 'error') {
    return <ErrorText text={loaded.error} />;
  }
  const { stream, connections } = loaded.data;
  if (stream && !stream.editable) {
    return (
      <>
        <button className='argo-button argo-button--base-o' onClick={() => navigate({ view: 'streams', stream: stream.name })}>
          <i className='fa fa-arrow-left' /> {stream.name}
        </button>
        <ErrorText text='This stream is managed in git and read-only in Zea. Use "Edit as draft" and export the draft.' />
      </>
    );
  }
  return (
    <StreamEditor
      client={client}
      data={data}
      connections={connections}
      existing={stream}
      onSaved={(s, created) => created && navigate({ view: 'streams', stream: s.name, mode: 'edit' })}
      onClose={() => navigate(stream ? { view: 'streams', stream: stream.name } : { view: 'streams' })}
    />
  );
};

export const StreamsView = ({ client, me }: { client: ZeaClient; me: Me }) => {
  const route = useRoute();
  const data = React.useMemo(() => new ConnData(client), [client]);
  if (me.isAdmin && route.mode === 'new') {
    return <EditorPage client={client} data={data} />;
  }
  if (route.stream) {
    if (me.isAdmin && route.mode === 'edit') {
      return <EditorPage key={route.stream} client={client} data={data} name={route.stream} />;
    }
    if (route.srun) {
      return <StreamRunView client={client} data={data} stream={route.stream} runID={route.srun} />;
    }
    return <StreamDetail key={route.stream} client={client} data={data} me={me} name={route.stream} openRun={route.mode === 'run'} />;
  }
  return <StreamList client={client} me={me} />;
};
