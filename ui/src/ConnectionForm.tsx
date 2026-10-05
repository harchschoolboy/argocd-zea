import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { buildCredentials, CredentialsEditor, inputStyle, pickMode, Row } from './forms';
import { cleanSources, ImageSourcesEditor } from './Images';
import { Connection, ConnectionInput, ImageSource, ProviderInfo, TestResult } from './types';
import { ErrorText, TestResultView, useLoad } from './ui';

interface Props {
  client: ZeaClient;
  providers: ProviderInfo[];
  // Existing Connection to edit; undefined creates a new one.
  existing?: Connection;
  onSaved: (c: Connection) => void;
  onCancel: () => void;
}

export const ConnectionForm = ({ client, providers, existing, onSaved, onCancel }: Props) => {
  const editing = !!existing;
  const [name, setName] = React.useState(existing?.name ?? '');
  const [providerID, setProviderID] = React.useState(existing?.provider ?? providers[0]?.id ?? '');
  const [url, setURL] = React.useState(existing?.url ?? '');
  const [apiURL, setAPIURL] = React.useState(existing?.apiURL ?? '');
  const [groups, setGroups] = React.useState((existing?.allowedGroups ?? []).join(', '));
  const [images, setImages] = React.useState<ImageSource[]>(existing?.images ?? []);
  const [creds, setCreds] = React.useState<Record<string, string>>({});
  const provider = providers.find(p => p.id === providerID);
  const modes = provider?.credentialModes ?? [];
  const [modeID, setModeID] = React.useState(() => pickMode(modes, existing?.credentialKeys));
  const [busy, setBusy] = React.useState<'save' | 'test' | null>(null);
  const [error, setError] = React.useState('');
  const [test, setTest] = React.useState<TestResult | null>(null);
  const [registries] = useLoad(() => client.registries(), [client]);

  const changeProvider = (id: string) => {
    setProviderID(id);
    setModeID(pickMode(providers.find(p => p.id === id)?.credentialModes ?? []));
    setCreds({});
  };

  const buildInput = (): ConnectionInput => ({
    name: name.trim(),
    provider: providerID,
    url: url.trim(),
    apiURL: apiURL.trim(),
    allowedGroups: groups
      .split(',')
      .map(g => g.trim())
      .filter(Boolean),
    images: cleanSources(images),
    credentials: buildCredentials(modes, modeID, creds, existing?.credentialKeys),
  });

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

        <CredentialsEditor
          modes={modes}
          modeID={modeID}
          onModeChange={setModeID}
          values={creds}
          onChange={setCreds}
          storedKeys={existing?.credentialKeys}
        />

        {existing?.imagesError && (
          <ErrorText text={`Stored image sources are invalid and will be replaced on save: ${existing.imagesError}`} />
        )}
        <ImageSourcesEditor client={client} value={images} onChange={setImages} registries={registries} />

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
