import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { BranchPicker } from './BranchPicker';
import { RunFormPanel } from './RunForm';
import { RunsList } from './Runs';
import { Connection, ProviderInfo, Run, TestResult } from './types';
import { COLORS, ErrorText, Muted, ProviderBadge, TestResultView, useLoad } from './ui';

interface Props {
  client: ZeaClient;
  connection: Connection;
  provider?: ProviderInfo;
  isAdmin: boolean;
  onEdit: () => void;
  onDeleted: () => void;
}

export const ConnectionCard = ({ client, connection: c, provider, isAdmin, onEdit, onDeleted }: Props) => {
  const [branches, reloadBranches] = useLoad(() => client.branches(c.name), [client, c.name]);
  const [ref, setRef] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [actionError, setActionError] = React.useState('');
  const [test, setTest] = React.useState<TestResult | null>(null);
  const [formFor, setFormFor] = React.useState<string | null>(null);
  const [started, setStarted] = React.useState<{ run: Run; pipeline: string } | null>(null);
  const [runsKey, setRunsKey] = React.useState(0);
  const [watchUntil, setWatchUntil] = React.useState(0);

  React.useEffect(() => {
    setFormFor(null);
  }, [ref]);

  const onStarted = (pipeline: string, run: Run) => {
    setFormFor(null);
    setStarted({ run, pipeline });
    setWatchUntil(Date.now() + 60_000);
    setRunsKey(k => k + 1);
  };

  React.useEffect(() => {
    if (branches.state === 'ok' && ref === null) {
      setRef(branches.data.defaultBranch);
    }
  }, [branches, ref]);

  const known = branches.state === 'ok' && branches.data.branches.some(b => b.name === ref);
  const [pipelines] = useLoad(
    () => (known && ref ? client.pipelines(c.name, ref) : Promise.resolve(null)),
    [client, c.name, known ? ref : ''],
  );

  const runTest = async () => {
    setBusy(true);
    setActionError('');
    setTest(null);
    try {
      setTest(await client.testConnection(c.name));
    } catch (err) {
      setActionError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!window.confirm(`Delete connection "${c.name}"? The stored credentials are deleted too.`)) {
      return;
    }
    setBusy(true);
    setActionError('');
    try {
      await client.deleteConnection(c.name);
      onDeleted();
    } catch (err) {
      setActionError(describeError(err));
      setBusy(false);
    }
  };

  return (
    <div className='white-box' style={{ margin: 0 }}>
      <div className='white-box__details'>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '0.3em' }}>
          <i className='fa fa-code-branch' />
          <b style={{ fontSize: '1.1em' }}>{c.name}</b>
          <ProviderBadge provider={c.provider} />
          {!c.editable && isAdmin && (
            <span title='Defined by a Secret in git; edit it there.' style={{ fontSize: '0.8em', color: COLORS.muted }}>
              <i className='fa fa-lock' /> declarative
            </span>
          )}
        </div>
        <div style={{ marginBottom: '0.8em', wordBreak: 'break-all' }}>
          <a href={c.url} target='_blank' rel='noopener noreferrer'>
            {c.url}
          </a>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '0.8em' }}>
          <label style={{ fontWeight: 600 }}>Branch</label>
          <BranchPicker
            value={ref ?? ''}
            branches={branches.state === 'ok' ? branches.data.branches : []}
            defaultBranch={branches.state === 'ok' ? branches.data.defaultBranch : undefined}
            onChange={setRef}
            disabled={branches.state !== 'ok'}
            placeholder={branches.state === 'loading' ? 'Loading branches...' : ''}
          />
          <button className='argo-button argo-button--base-o' title='Reload branches' onClick={reloadBranches}>
            <i className='fa fa-redo' />
          </button>
        </div>
        {branches.state === 'error' && <ErrorText text={branches.error} />}
        {branches.state === 'ok' && branches.data.truncated && (
          <Muted>Showing the first {branches.data.branches.length} branches.</Muted>
        )}
        {branches.state === 'ok' && ref && !known && <Muted>Unknown branch.</Muted>}

        {known && (
          <div>
            <div style={{ fontWeight: 600, margin: '0.5em 0' }}>
              {provider?.capabilities.multiplePipelines ? 'Workflows' : 'Pipeline'}
            </div>
            {pipelines.state === 'loading' && <Muted>Loading...</Muted>}
            {pipelines.state === 'error' && <ErrorText text={pipelines.error} />}
            {pipelines.state === 'ok' && pipelines.data && pipelines.data.length === 0 && <Muted>No workflows found.</Muted>}
            {pipelines.state === 'ok' &&
              pipelines.data?.map(p => (
                <React.Fragment key={p.id}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', padding: '0.3em 0', borderTop: '1px solid #eee' }}>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div>{p.name}</div>
                      <div style={{ fontSize: '0.8em', color: COLORS.muted, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                        {p.dispatchable ? p.path : p.reason}
                      </div>
                    </div>
                    <button
                      className={formFor === p.id ? 'argo-button argo-button--base-o' : 'argo-button argo-button--base'}
                      disabled={!p.dispatchable}
                      title={p.dispatchable ? `Run ${p.name} on ${ref}` : p.reason}
                      onClick={() => setFormFor(formFor === p.id ? null : p.id)}>
                      <i className='fa fa-play' /> Run
                    </button>
                  </div>
                  {formFor === p.id && ref && (
                    <RunFormPanel
                      client={client}
                      connection={c.name}
                      pipeline={p}
                      gitRef={ref}
                      onStarted={run => onStarted(p.name, run)}
                      onClose={() => setFormFor(null)}
                    />
                  )}
                </React.Fragment>
              ))}
            {started && (
              <div style={{ color: COLORS.ok, margin: '0.5em 0' }}>
                <i className='fa fa-check-circle' /> Started <b>{started.pipeline}</b> on <code>{started.run.ref}</code>
                {started.run.webURL && (
                  <>
                    {' '}
                    <a href={started.run.webURL} target='_blank' rel='noopener noreferrer'>
                      open <i className='fa fa-external-link-alt' />
                    </a>
                  </>
                )}
                <a style={{ marginLeft: '0.6em', cursor: 'pointer', color: COLORS.muted }} title='Dismiss' onClick={() => setStarted(null)}>
                  <i className='fa fa-times' />
                </a>
              </div>
            )}
            <RunsList
              client={client}
              connection={c.name}
              gitRef={ref ?? ''}
              capabilities={provider?.capabilities}
              refreshKey={runsKey}
              watchUntil={watchUntil}
              expandRunID={started?.run.id}
            />
          </div>
        )}

        {actionError && <ErrorText text={actionError} />}
        {test && <TestResultView result={test} />}

        <div style={{ display: 'flex', gap: '0.5em', marginTop: '1em' }}>
          <button className='argo-button argo-button--base-o' onClick={runTest} disabled={busy}>
            Test
          </button>
          {isAdmin && c.editable && (
            <>
              <button className='argo-button argo-button--base-o' onClick={onEdit} disabled={busy}>
                Edit
              </button>
              <button className='argo-button argo-button--base-o' onClick={remove} disabled={busy}>
                Delete
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
};
