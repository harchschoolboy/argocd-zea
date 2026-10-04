import * as React from 'react';
import { ANCHOR_LABEL, findAnchors, ZeaClient } from './api';
import { ConnectionCard } from './ConnectionCard';
import { ConnectionForm } from './ConnectionForm';
import { navigate, useRoute } from './route';
import { Connection, Me, ProviderInfo } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

const REPO_URL = 'https://github.com/harchschoolboy/argocd-zea';

interface Context {
  me: Me;
  providers: ProviderInfo[];
}

type Editing = { mode: 'none' } | { mode: 'create' } | { mode: 'edit'; connection: Connection };

const Connections = ({ client, ctx }: { client: ZeaClient; ctx: Context }) => {
  const [conns, reload] = useLoad(() => client.connections(), [client]);
  const [editing, setEditing] = React.useState<Editing>({ mode: 'none' });
  const route = useRoute();
  const { me, providers } = ctx;

  const saved = () => {
    setEditing({ mode: 'none' });
    reload();
  };

  const back = () => {
    setEditing({ mode: 'none' });
    navigate({});
  };

  const card = (c: Connection, detail: boolean) => (
    <ConnectionCard
      key={c.name}
      client={client}
      connection={c}
      provider={providers.find(p => p.id === c.provider)}
      isAdmin={me.isAdmin}
      detail={detail}
      initialRef={detail ? route.ref : undefined}
      expandRunID={detail ? route.run : undefined}
      onOpen={(ref, run) => navigate({ connection: c.name, ref, run })}
      onEdit={() => setEditing({ mode: 'edit', connection: c })}
      onDeleted={() => {
        if (detail) {
          navigate({});
        }
        reload();
      }}
    />
  );

  const selected = route.connection;
  const current = selected && conns.state === 'ok' ? conns.data.find(c => c.name === selected) : undefined;

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '1em' }}>
        {selected && (
          <button className='argo-button argo-button--base-o' onClick={back}>
            <i className='fa fa-arrow-left' /> Connections
          </button>
        )}
        <Muted>
          Signed in as <b>{me.username || me.userId || 'unknown'}</b>
          {me.groups.length > 0 && <> ({me.groups.join(', ')})</>}
          {me.isAdmin && <> - Zea admin</>}
        </Muted>
        <div style={{ flex: 1 }} />
        {me.isAdmin && editing.mode === 'none' && !selected && (
          <button className='argo-button argo-button--base' onClick={() => setEditing({ mode: 'create' })}>
            <i className='fa fa-plus' /> Add connection
          </button>
        )}
        <button className='argo-button argo-button--base-o' onClick={reload}>
          <i className='fa fa-redo' /> Refresh
        </button>
      </div>

      {editing.mode !== 'none' && (
        <ConnectionForm
          key={editing.mode === 'edit' ? editing.connection.name : 'new'}
          client={client}
          providers={providers}
          existing={editing.mode === 'edit' ? editing.connection : undefined}
          onSaved={saved}
          onCancel={() => setEditing({ mode: 'none' })}
        />
      )}

      {conns.state === 'loading' && <Muted>Loading connections...</Muted>}
      {conns.state === 'error' && <ErrorText text={conns.error} />}
      {selected && conns.state === 'ok' && !current && (
        <ErrorText text={`Connection "${selected}" does not exist or is not shared with you.`} />
      )}
      {current && card(current, true)}
      {!selected && conns.state === 'ok' && conns.data.length === 0 && (
        <div className='white-box'>
          <div className='white-box__details'>
            {me.isAdmin
              ? 'No connections yet. Use "Add connection" to connect a GitHub or GitLab repository.'
              : 'No connections are shared with you. Ask a Zea admin to add your group to a connection.'}
          </div>
        </div>
      )}
      {!selected && conns.state === 'ok' && conns.data.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(420px, 1fr))', gap: '1em' }}>
          {conns.data.map(c => card(c, false))}
        </div>
      )}
    </>
  );
};

const Workspace = ({ client }: { client: ZeaClient }) => {
  const [ctx] = useLoad(
    async (): Promise<Context> => {
      const [me, providers] = await Promise.all([client.me(), client.providers()]);
      return { me, providers };
    },
    [client],
  );
  if (ctx.state === 'loading') {
    return <Muted>Connecting to the Zea backend...</Muted>;
  }
  if (ctx.state === 'error') {
    return <ErrorText text={ctx.error} />;
  }
  return <Connections client={client} ctx={ctx.data} />;
};

export const ZeaPage = () => {
  const [anchors] = useLoad(findAnchors, []);
  const client = React.useMemo(
    () => (anchors.state === 'ok' && anchors.data.length > 0 ? new ZeaClient(anchors.data[0]) : null),
    [anchors],
  );

  return (
    <div style={{ padding: '1em 2em' }}>
      <h2 style={{ marginTop: 0, display: 'flex', alignItems: 'baseline', gap: '0.6em' }}>
        <span>
          <i className='fa fa-anchor' /> Zea
        </span>
        <a
          href={`${REPO_URL}/releases/tag/v${__ZEA_VERSION__}`}
          target='_blank'
          rel='noopener noreferrer'
          title='Release notes'
          style={{ fontSize: '0.55em', color: COLORS.muted }}>
          v{__ZEA_VERSION__}
        </a>
        <a href={REPO_URL} target='_blank' rel='noopener noreferrer' title='Zea on GitHub' style={{ fontSize: '0.55em' }}>
          <i className='fab fa-github' /> GitHub
        </a>
      </h2>
      {anchors.state === 'loading' && <Muted>Loading...</Muted>}
      {anchors.state === 'error' && <ErrorText text={anchors.error} />}
      {anchors.state === 'ok' && anchors.data.length === 0 && (
        <div className='white-box'>
          <div className='white-box__details'>
            <p style={{ fontWeight: 600 }}>Zea is not available to you</p>
            <p>
              No Application labelled <code>{ANCHOR_LABEL}: "true"</code> is visible. Either you lack <code>get</code>{' '}
              permission on the Zea anchor Application, or the label is missing on it.
            </p>
          </div>
        </div>
      )}
      {anchors.state === 'ok' && anchors.data.length > 1 && (
        <ErrorText
          text={`Several anchor Applications found; using ${anchors.data[0].metadata.namespace}/${anchors.data[0].metadata.name}.`}
        />
      )}
      {client && <Workspace client={client} />}
    </div>
  );
};
