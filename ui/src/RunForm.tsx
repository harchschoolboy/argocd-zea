import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { Pipeline, Run, RunInput } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

interface Props {
  client: ZeaClient;
  connection: string;
  pipeline: Pipeline;
  gitRef: string;
  onStarted: (run: Run) => void;
  onClose: () => void;
}

interface VarRow {
  key: string;
  value: string;
}

const fieldStyle: React.CSSProperties = { width: '100%', boxSizing: 'border-box' };

function initialValues(inputs: RunInput[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const i of inputs) {
    if (i.type === 'boolean') {
      out[i.name] = i.default === 'true' ? 'true' : 'false';
    } else if (i.type === 'choice' && !i.default && i.options?.length) {
      out[i.name] = i.required ? i.options[0] : '';
    } else {
      out[i.name] = i.default ?? '';
    }
  }
  return out;
}

// RunFormPanel asks for the declared inputs (and free variables when the
// provider accepts them), then starts the pipeline at gitRef.
export const RunFormPanel = ({ client, connection, pipeline, gitRef, onStarted, onClose }: Props) => {
  const [form] = useLoad(() => client.runForm(connection, pipeline.id, gitRef), [client, connection, pipeline.id, gitRef]);
  const [values, setValues] = React.useState<Record<string, string>>({});
  const [vars, setVars] = React.useState<VarRow[]>([]);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');

  React.useEffect(() => {
    if (form.state === 'ok') {
      setValues(initialValues(form.data.inputs));
    }
  }, [form]);

  const setValue = (name: string, v: string) => setValues(prev => ({ ...prev, [name]: v }));
  const setVar = (idx: number, patch: Partial<VarRow>) =>
    setVars(prev => prev.map((r, i) => (i === idx ? { ...r, ...patch } : r)));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (form.state !== 'ok') {
      return;
    }
    const inputs: Record<string, string> = {};
    for (const i of form.data.inputs) {
      const v = values[i.name] ?? '';
      if (i.required && v.trim() === '') {
        setError(`Input "${i.name}" is required.`);
        return;
      }
      // Omitted optional inputs fall back to their defaults in the pipeline.
      if (v !== '' || i.required) {
        inputs[i.name] = v;
      }
    }
    const variables: Record<string, string> = {};
    for (const r of vars) {
      const k = r.key.trim();
      if (!k) {
        continue;
      }
      if (k in variables) {
        setError(`Variable "${k}" is set twice.`);
        return;
      }
      variables[k] = r.value;
    }
    setBusy(true);
    setError('');
    try {
      onStarted(await client.trigger(connection, { pipelineID: pipeline.id, ref: gitRef, inputs, variables }));
    } catch (err) {
      setError(describeError(err));
      setBusy(false);
    }
  };

  const renderInput = (i: RunInput) => {
    const v = values[i.name] ?? '';
    switch (i.type) {
      case 'boolean':
        return (
          <label style={{ display: 'flex', alignItems: 'center', gap: '0.4em', cursor: 'pointer' }}>
            <input type='checkbox' checked={v === 'true'} onChange={e => setValue(i.name, e.target.checked ? 'true' : 'false')} />
            <span>{v === 'true' ? 'true' : 'false'}</span>
          </label>
        );
      case 'choice':
        return (
          <select className='argo-field' style={fieldStyle} value={v} onChange={e => setValue(i.name, e.target.value)}>
            {!i.required && <option value=''>(default)</option>}
            {(i.options ?? []).map(o => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </select>
        );
      case 'number':
        return (
          <input className='argo-field' style={fieldStyle} type='number' value={v} onChange={e => setValue(i.name, e.target.value)} />
        );
      default:
        return (
          <input
            className='argo-field'
            style={{ ...fieldStyle, fontFamily: i.type === 'array' ? 'monospace' : undefined }}
            value={v}
            placeholder={i.type === 'array' ? '["a", "b"]' : i.type === 'environment' ? 'environment name' : ''}
            onChange={e => setValue(i.name, e.target.value)}
          />
        );
    }
  };

  return (
    <form
      onSubmit={submit}
      style={{ background: '#f8fbfb', border: '1px solid #dee6eb', borderRadius: 4, padding: '0.8em', margin: '0.3em 0 0.6em' }}>
      <div style={{ marginBottom: '0.6em' }}>
        Run <b>{pipeline.name}</b> on <code>{gitRef}</code>
      </div>
      {form.state === 'loading' && <Muted>Reading parameters...</Muted>}
      {form.state === 'error' && <ErrorText text={form.error} />}
      {form.state === 'ok' && (
        <>
          {form.data.warning && (
            <div style={{ color: COLORS.warn, marginBottom: '0.5em' }}>
              <i className='fa fa-exclamation-triangle' /> {form.data.warning}
            </div>
          )}
          {form.data.inputs.length === 0 && !form.data.variables && <Muted>This workflow has no inputs.</Muted>}
          {form.data.inputs.map(i => (
            <div key={i.name} style={{ marginBottom: '0.6em' }}>
              <label style={{ display: 'block', fontWeight: 600, marginBottom: '0.2em' }}>
                {i.name}
                {i.required && <span style={{ color: COLORS.error }}> *</span>}
                <span style={{ fontWeight: 400, color: COLORS.muted, fontSize: '0.85em' }}> {i.type}</span>
              </label>
              {renderInput(i)}
              {i.description && <div style={{ fontSize: '0.85em', color: COLORS.muted, marginTop: '0.2em' }}>{i.description}</div>}
            </div>
          ))}
          {form.data.variables && (
            <div style={{ marginBottom: '0.6em' }}>
              <div style={{ fontWeight: 600, marginBottom: '0.2em' }}>Variables</div>
              {vars.length === 0 && (
                <div style={{ fontSize: '0.85em', color: COLORS.muted }}>Optional CI/CD variables passed to this pipeline.</div>
              )}
              {vars.map((r, idx) => (
                <div key={idx} style={{ display: 'flex', gap: '0.4em', marginBottom: '0.3em' }}>
                  <input
                    className='argo-field'
                    style={{ flex: '0 0 35%', fontFamily: 'monospace' }}
                    placeholder='NAME'
                    value={r.key}
                    onChange={e => setVar(idx, { key: e.target.value })}
                  />
                  <input
                    className='argo-field'
                    style={{ flex: 1 }}
                    placeholder='value'
                    value={r.value}
                    onChange={e => setVar(idx, { value: e.target.value })}
                  />
                  <button
                    type='button'
                    className='argo-button argo-button--base-o'
                    title='Remove variable'
                    onClick={() => setVars(prev => prev.filter((_, i) => i !== idx))}>
                    <i className='fa fa-times' />
                  </button>
                </div>
              ))}
              <button
                type='button'
                className='argo-button argo-button--base-o'
                onClick={() => setVars(prev => [...prev, { key: '', value: '' }])}>
                <i className='fa fa-plus' /> Add variable
              </button>
            </div>
          )}
        </>
      )}
      {error && <ErrorText text={error} />}
      <div style={{ display: 'flex', gap: '0.5em', marginTop: '0.6em' }}>
        <button type='submit' className='argo-button argo-button--base' disabled={busy || form.state !== 'ok'}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-play'} /> Run
        </button>
        <button type='button' className='argo-button argo-button--base-o' onClick={onClose} disabled={busy}>
          Cancel
        </button>
      </div>
    </form>
  );
};
