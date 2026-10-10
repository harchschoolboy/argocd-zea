import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { buildCredentials, CredentialsEditor, inputStyle, pickMode, Row } from './forms';
import { navigate, routeHref } from './route';
import { can, Registry, RegistryInput, RegistryKind, RegistryTestResult } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

const RegistryTestView = ({ result }: { result: RegistryTestResult }) =>
  result.ok ? (
    <div style={{ margin: '0.5em 0', wordBreak: 'break-word' }}>
      <span style={{ color: COLORS.ok }}>
        <i className='fa fa-check-circle' /> Connected, {result.repositoryCount} repositor{result.repositoryCount === 1 ? 'y' : 'ies'}
      </span>
      {result.repositories && result.repositories.length > 0 && (
        <Muted>
          : {result.repositories.join(', ')}
          {(result.repositoryCount ?? 0) > result.repositories.length && ', ...'}
        </Muted>
      )}
    </div>
  ) : (
    <ErrorText text={result.error ?? 'Registry test failed'} />
  );

interface FormProps {
  client: ZeaClient;
  kinds: RegistryKind[];
  existing?: Registry;
  onSaved: () => void;
  onCancel: () => void;
}

const RegistryForm = ({ client, kinds, existing, onSaved, onCancel }: FormProps) => {
  const editing = !!existing;
  const [name, setName] = React.useState(existing?.name ?? '');
  const [kindID, setKindID] = React.useState(existing?.kind ?? kinds[0]?.id ?? '');
  const [url, setURL] = React.useState(existing?.url ?? '');
  const [creds, setCreds] = React.useState<Record<string, string>>({});
  const kind = kinds.find(k => k.id === kindID);
  const modes = kind?.credentialModes ?? [];
  const [modeID, setModeID] = React.useState(() => pickMode(modes, existing?.credentialKeys));
  const [busy, setBusy] = React.useState<'save' | 'test' | null>(null);
  const [error, setError] = React.useState('');
  const [test, setTest] = React.useState<RegistryTestResult | null>(null);

  const changeKind = (id: string) => {
    setKindID(id);
    setModeID(pickMode(kinds.find(k => k.id === id)?.credentialModes ?? []));
    setCreds({});
  };

  const buildInput = (): RegistryInput => ({
    name: name.trim(),
    kind: kindID,
    url: url.trim(),
    credentials: buildCredentials(modes, modeID, creds, existing?.credentialKeys),
  });

  const runTest = async () => {
    setBusy('test');
    setError('');
    setTest(null);
    try {
      setTest(await client.testRegistryDraft(buildInput()));
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
      await (editing ? client.updateRegistry(input) : client.createRegistry(input));
      onSaved();
    } catch (err) {
      setError(describeError(err));
      setBusy(null);
    }
  };

  return (
    <form className='white-box' onSubmit={save} style={{ marginBottom: '1.5em' }}>
      <div className='white-box__details'>
        <p style={{ fontWeight: 600, fontSize: '1.1em' }}>{editing ? `Edit registry ${existing!.name}` : 'New registry'}</p>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', columnGap: '1.5em' }}>
          <Row label='Name' help='Lowercase letters, digits and "-". Image sources refer to it; cannot be changed later.'>
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
          <Row label='Kind'>
            <select className='argo-field' style={inputStyle} value={kindID} onChange={e => changeKind(e.target.value)} disabled={editing}>
              {kinds.map(k => (
                <option key={k.id} value={k.id}>
                  {k.name}
                </option>
              ))}
            </select>
          </Row>
        </div>
        <Row label='Registry URL' help={kind?.urlHelp}>
          <input
            className='argo-field'
            style={inputStyle}
            value={url}
            onChange={e => setURL(e.target.value)}
            placeholder={kind?.urlExample}
            required
          />
        </Row>
        <CredentialsEditor
          modes={modes}
          modeID={modeID}
          onModeChange={setModeID}
          values={creds}
          onChange={setCreds}
          storedKeys={existing?.credentialKeys}
        />

        {error && <ErrorText text={error} />}
        {test && <RegistryTestView result={test} />}

        <div style={{ display: 'flex', gap: '0.5em', marginTop: '1em' }}>
          <button type='submit' className='argo-button argo-button--base' disabled={busy !== null}>
            {busy === 'save' ? 'Saving...' : editing ? 'Save' : 'Create'}
          </button>
          <button type='button' className='argo-button argo-button--base-o' onClick={runTest} disabled={busy !== null}>
            {busy === 'test' ? 'Testing...' : 'Test registry'}
          </button>
          <button type='button' className='argo-button argo-button--base-o' onClick={onCancel} disabled={busy === 'save'}>
            Cancel
          </button>
        </div>
      </div>
    </form>
  );
};

const RegistryRow = ({
  client,
  registry: r,
  kind,
  onEdit,
  onDeleted,
}: {
  client: ZeaClient;
  registry: Registry;
  kind?: RegistryKind;
  onEdit: () => void;
  onDeleted: () => void;
}) => {
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const [test, setTest] = React.useState<RegistryTestResult | null>(null);

  const runTest = async () => {
    setBusy(true);
    setError('');
    setTest(null);
    try {
      setTest(await client.testRegistry(r.name));
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!window.confirm(`Delete registry "${r.name}"? The stored credentials are deleted too.`)) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      await client.deleteRegistry(r.name);
      onDeleted();
    } catch (err) {
      setError(describeError(err));
      setBusy(false);
    }
  };

  return (
    <div className='white-box' style={{ margin: 0 }}>
      <div className='white-box__details'>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', flexWrap: 'wrap' }}>
          <i className='fa fa-database' />
          <b style={{ fontSize: '1.1em' }}>{r.name}</b>
          <Muted>{kind?.name ?? r.kind}</Muted>
          {!r.editable && (
            <span title='Defined by a Secret in git; edit it there.' style={{ fontSize: '0.8em', color: COLORS.muted }}>
              <i className='fa fa-lock' /> declarative
            </span>
          )}
        </div>
        <div style={{ fontFamily: 'monospace', margin: '0.3em 0', wordBreak: 'break-all' }}>{r.url}</div>
        <div style={{ fontSize: '0.9em' }}>
          <Muted>Used by: </Muted>
          {r.usedBy.length === 0 ? (
            <Muted>no connections</Muted>
          ) : (
            r.usedBy.map((c, i) => (
              <React.Fragment key={c}>
                {i > 0 && ', '}
                <a
                  href={routeHref({ connection: c })}
                  onClick={e => {
                    e.preventDefault();
                    navigate({ connection: c });
                  }}>
                  {c}
                </a>
              </React.Fragment>
            ))
          )}
        </div>
        {error && <ErrorText text={error} />}
        {test && <RegistryTestView result={test} />}
        {can(r.actions, 'edit') && (
          <div style={{ display: 'flex', gap: '0.5em', marginTop: '0.8em' }}>
            <button className='argo-button argo-button--base-o' onClick={runTest} disabled={busy}>
              Test
            </button>
            {r.editable && (
              <>
                <button className='argo-button argo-button--base-o' onClick={onEdit} disabled={busy}>
                  Edit
                </button>
                <button
                  className='argo-button argo-button--base-o'
                  onClick={remove}
                  disabled={busy || r.usedBy.length > 0}
                  title={r.usedBy.length > 0 ? 'Remove it from the image sources of its connections first' : undefined}>
                  Delete
                </button>
              </>
            )}
          </div>
        )}
      </div>
    </div>
  );
};

type Editing = { mode: 'none' } | { mode: 'create' } | { mode: 'edit'; registry: Registry };

// RegistriesView lists container registries; canCreate shows "Add registry".
export const RegistriesView = ({ client, canCreate }: { client: ZeaClient; canCreate: boolean }) => {
  const [data, reload] = useLoad(async () => {
    const [kinds, registries] = await Promise.all([client.registryKinds(), client.registries()]);
    return { kinds, registries };
  }, [client]);
  const [editing, setEditing] = React.useState<Editing>({ mode: 'none' });

  const saved = () => {
    setEditing({ mode: 'none' });
    reload();
  };

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '1em' }}>
        <div style={{ flex: 1 }} />
        {canCreate && editing.mode === 'none' && data.state === 'ok' && (
          <button className='argo-button argo-button--base' onClick={() => setEditing({ mode: 'create' })}>
            <i className='fa fa-plus' /> Add registry
          </button>
        )}
        <button className='argo-button argo-button--base-o' onClick={reload}>
          <i className='fa fa-redo' /> Refresh
        </button>
      </div>
      <p style={{ color: COLORS.muted, marginTop: 0 }}>
        Container registries that connections list their images from. Registry credentials are used by the Zea backend only;
        users see images through the connections shared with them.
      </p>

      {data.state === 'loading' && <Muted>Loading registries...</Muted>}
      {data.state === 'error' && <ErrorText text={data.error} />}
      {data.state === 'ok' && (
        <>
          {editing.mode !== 'none' && (
            <RegistryForm
              key={editing.mode === 'edit' ? editing.registry.name : 'new'}
              client={client}
              kinds={data.data.kinds}
              existing={editing.mode === 'edit' ? editing.registry : undefined}
              onSaved={saved}
              onCancel={() => setEditing({ mode: 'none' })}
            />
          )}
          {data.data.registries.length === 0 ? (
            <div className='white-box'>
              <div className='white-box__details'>
                {canCreate ? 'No registries yet. Use "Add registry" to connect one.' : 'No registries are shared with you.'}
              </div>
            </div>
          ) : (
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(420px, 1fr))', gap: '1em' }}>
              {data.data.registries.map(r => (
                <RegistryRow
                  key={r.name}
                  client={client}
                  registry={r}
                  kind={data.data.kinds.find(k => k.id === r.kind)}
                  onEdit={() => setEditing({ mode: 'edit', registry: r })}
                  onDeleted={reload}
                />
              ))}
            </div>
          )}
        </>
      )}
    </>
  );
};
