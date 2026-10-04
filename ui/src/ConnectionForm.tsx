import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { Connection, ConnectionInput, CredentialMode, ProviderInfo, TestResult } from './types';
import { ErrorText, TestResultView } from './ui';

interface Props {
  client: ZeaClient;
  providers: ProviderInfo[];
  // Existing Connection to edit; undefined creates a new one.
  existing?: Connection;
  onSaved: (c: Connection) => void;
  onCancel: () => void;
}

function initialMode(p: ProviderInfo | undefined, existing?: Connection): string {
  if (!p) {
    return '';
  }
  const keys = new Set(existing?.credentialKeys ?? []);
  const set = p.credentialModes.find(m => m.fields.every(f => keys.has(f.key)));
  return (set ?? p.credentialModes[0]).id;
}

const Row = ({ label, help, children }: { label: string; help?: string; children: React.ReactNode }) => (
  <div className='argo-form-row' style={{ marginBottom: '1em' }}>
    <label style={{ display: 'block', fontWeight: 600, marginBottom: '0.3em' }}>{label}</label>
    {children}
    {help && <div style={{ fontSize: '0.85em', color: '#6d7f8b', marginTop: '0.2em' }}>{help}</div>}
  </div>
);

const inputStyle: React.CSSProperties = { width: '100%', boxSizing: 'border-box' };

export const ConnectionForm = ({ client, providers, existing, onSaved, onCancel }: Props) => {
  const editing = !!existing;
  const [name, setName] = React.useState(existing?.name ?? '');
  const [providerID, setProviderID] = React.useState(existing?.provider ?? providers[0]?.id ?? '');
  const [url, setURL] = React.useState(existing?.url ?? '');
  const [apiURL, setAPIURL] = React.useState(existing?.apiURL ?? '');
  const [groups, setGroups] = React.useState((existing?.allowedGroups ?? []).join(', '));
  const [creds, setCreds] = React.useState<Record<string, string>>({});
  const provider = providers.find(p => p.id === providerID);
  const [modeID, setModeID] = React.useState(() => initialMode(provider, existing));
  const [busy, setBusy] = React.useState<'save' | 'test' | null>(null);
  const [error, setError] = React.useState('');
  const [test, setTest] = React.useState<TestResult | null>(null);

  const mode: CredentialMode | undefined = provider?.credentialModes.find(m => m.id === modeID);
  const storedKeys = new Set(existing?.credentialKeys ?? []);

  const changeProvider = (id: string) => {
    setProviderID(id);
    setModeID(initialMode(providers.find(p => p.id === id)));
    setCreds({});
  };

  const buildInput = (): ConnectionInput => {
    const credentials: Record<string, string> = {};
    for (const m of provider?.credentialModes ?? []) {
      for (const f of m.fields) {
        if (m.id === modeID) {
          credentials[f.key] = creds[f.key] ?? '';
        } else if (storedKeys.has(f.key)) {
          // Switching modes: drop the credentials of the other mode.
          credentials[f.key] = '-';
        }
      }
    }
    return {
      name: name.trim(),
      provider: providerID,
      url: url.trim(),
      apiURL: apiURL.trim(),
      allowedGroups: groups
        .split(',')
        .map(g => g.trim())
        .filter(Boolean),
      credentials,
    };
  };

  const runTest = async () => {
    setBusy('test');
    setError('');
    setTest(null);
    try {
      setTest(await client.testDraft(buildInput()));
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(null);
    }
  };

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy('save');
    setError('');
    try {
      const input = buildInput();
      onSaved(editing ? await client.updateConnection(input) : await client.createConnection(input));
    } catch (err) {
      setError(describeError(err));
      setBusy(null);
    }
  };

  return (
    <form className='white-box' onSubmit={save} style={{ marginBottom: '1.5em' }}>
      <div className='white-box__details'>
        <p style={{ fontWeight: 600, fontSize: '1.1em' }}>{editing ? `Edit connection ${existing!.name}` : 'New connection'}</p>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', columnGap: '1.5em' }}>
          <Row label='Name' help='Lowercase letters, digits and "-". Cannot be changed later.'>
            <input
              className='argo-field'
              style={inputStyle}
              value={name}
              onChange={e => setName(e.target.value)}
              disabled={editing}
              required
              pattern='[a-z0-9]([-a-z0-9]*[a-z0-9])?'
              maxLength={50}
            />
          </Row>
          <Row label='Provider'>
            <select
              className='argo-field'
              style={inputStyle}
              value={providerID}
              onChange={e => changeProvider(e.target.value)}
              disabled={editing}>
              {providers.map(p => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Row>
          <Row label='Repository URL'>
            <input
              className='argo-field'
              style={inputStyle}
              value={url}
              onChange={e => setURL(e.target.value)}
              placeholder={provider?.urlExample}
              required
            />
          </Row>
          <Row label='API URL (optional)' help={provider?.apiURLHelp}>
            <input className='argo-field' style={inputStyle} value={apiURL} onChange={e => setAPIURL(e.target.value)} />
          </Row>
        </div>

        <Row
          label='Allowed groups'
          help='Comma-separated Argo CD groups that may see and run this connection. "*" means everyone who can open Zea. Zea admins always can.'>
          <input className='argo-field' style={inputStyle} value={groups} onChange={e => setGroups(e.target.value)} />
        </Row>

        {provider && provider.credentialModes.length > 1 && (
          <Row label='Authentication'>
            <div style={{ display: 'flex', gap: '1.5em' }}>
              {provider.credentialModes.map(m => (
                <label key={m.id} style={{ cursor: 'pointer' }}>
                  <input type='radio' name='mode' checked={modeID === m.id} onChange={() => setModeID(m.id)} /> {m.label}
                </label>
              ))}
            </div>
          </Row>
        )}
        {mode?.help && <div style={{ color: '#6d7f8b', marginBottom: '0.8em' }}>{mode.help}</div>}

        {mode?.fields.map(f => {
          const placeholder = storedKeys.has(f.key) ? '(stored - leave empty to keep)' : '';
          const common = {
            className: 'argo-field',
            style: inputStyle,
            value: creds[f.key] ?? '',
            placeholder,
            autoComplete: 'off',
            onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
              setCreds({ ...creds, [f.key]: e.target.value }),
          };
          return (
            <Row key={f.key} label={f.label} help={f.help}>
              {f.multiline ? (
                <textarea {...common} rows={6} spellCheck={false} style={{ ...inputStyle, fontFamily: 'monospace' }} />
              ) : (
                <input {...common} type={f.secret ? 'password' : 'text'} />
              )}
            </Row>
          );
        })}

        {error && <ErrorText text={error} />}
        {test && <TestResultView result={test} />}

        <div style={{ display: 'flex', gap: '0.5em', marginTop: '1em' }}>
          <button type='submit' className='argo-button argo-button--base' disabled={busy !== null}>
            {busy === 'save' ? 'Saving...' : editing ? 'Save' : 'Create'}
          </button>
          <button type='button' className='argo-button argo-button--base-o' onClick={runTest} disabled={busy !== null}>
            {busy === 'test' ? 'Testing...' : 'Test connection'}
          </button>
          <button type='button' className='argo-button argo-button--base-o' onClick={onCancel} disabled={busy === 'save'}>
            Cancel
          </button>
        </div>
      </div>
    </form>
  );
};
