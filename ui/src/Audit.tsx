import * as React from 'react';
import { ZeaClient } from './api';
import { navigate, Route } from './route';
import { Help } from './StreamFields';
import { AuditEvent, AuditPage } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

const cell: React.CSSProperties = { padding: '0.3em 0.5em', borderTop: `1px solid ${COLORS.border}`, verticalAlign: 'top' };
const head: React.CSSProperties = { padding: '0.3em 0.5em', textAlign: 'left', fontWeight: 600, color: COLORS.muted, whiteSpace: 'nowrap' };

const clock = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });

// linkOf opens the item an event acted on, while it may still exist.
function linkOf(e: AuditEvent): Route | undefined {
  if (!e.name || e.action.endsWith('.delete')) {
    return undefined;
  }
  switch (e.resource) {
    case 'connection':
      return { view: 'connections', connection: e.name };
    case 'stream': {
      const run = e.action.startsWith('stream.run.') || e.action.startsWith('stream.step.') ? e.target?.split('/')[0] : undefined;
      return { view: 'streams', stream: e.name, srun: run };
    }
    case 'registry':
      return { view: 'registries' };
    case 'policy':
      return { view: 'access' };
  }
  return undefined;
}

const matches = (e: AuditEvent, q: string) =>
  !q || [e.user, e.action, e.name, e.target, e.error, String(e.status), ...(e.groups ?? [])].some(v => v && v.toLowerCase().includes(q));

const StatusText = ({ status }: { status: number }) => (
  <span style={{ color: status >= 400 ? COLORS.error : COLORS.ok, fontWeight: 600 }}>{status}</span>
);

const EventRow = ({ e }: { e: AuditEvent }) => {
  const link = linkOf(e);
  return (
    <tr>
      <td style={{ ...cell, whiteSpace: 'nowrap' }}>{clock(e.time)}</td>
      <td style={cell} title={e.groups?.length ? `groups: ${e.groups.join(', ')}` : undefined}>
        {e.user || <Muted>unknown</Muted>}
      </td>
      <td style={{ ...cell, whiteSpace: 'nowrap' }}>
        <code>{e.action}</code>
      </td>
      <td style={{ ...cell, wordBreak: 'break-word' }}>
        {e.name && (link ? <a onClick={() => navigate(link)}>{e.name}</a> : e.name)}
        {e.target && <Muted> {e.target}</Muted>}
      </td>
      <td style={cell}>
        <StatusText status={e.status} />
        {e.error && <div style={{ color: COLORS.error, wordBreak: 'break-word' }}>{e.error}</div>}
      </td>
    </tr>
  );
};

// AuditView lists the changes made through Zea, one day at a time. Users
// who are not admins get only the events of the items they can see.
export const AuditView = ({ client, admin }: { client: ZeaClient; admin: boolean }) => {
  const [day, setDay] = React.useState('');
  const [query, setQuery] = React.useState('');
  const [state, reload] = useLoad<AuditPage>(() => client.audit(day), [client, day]);
  const last = React.useRef<AuditPage | undefined>(undefined);
  if (state.state === 'ok') {
    last.current = state.data;
  }
  const page = last.current;
  const q = query.trim().toLowerCase();
  const events = page ? page.events.filter(e => matches(e, q)) : [];
  return (
    <div className='white-box' style={{ padding: '0.8em 1em' }}>
      <div style={{ display: 'flex', gap: '0.8em', alignItems: 'center', flexWrap: 'wrap' }}>
        <div style={{ fontWeight: 600, fontSize: '1.05em' }}>Audit</div>
        {page && page.days.length > 0 && (
          <select className='argo-field' value={page.day} onChange={e => setDay(e.target.value)} style={{ width: 'auto' }}>
            {page.days.map(d => (
              <option key={d} value={d}>
                {d}
              </option>
            ))}
          </select>
        )}
        <input
          className='argo-field'
          placeholder='Filter by user, action, name, error'
          value={query}
          onChange={e => setQuery(e.target.value)}
          style={{ flex: '1 1 16em', minWidth: '12em' }}
        />
        <button className='argo-button argo-button--base-o' onClick={reload} disabled={state.state === 'loading'}>
          <i className={`fa fa-sync${state.state === 'loading' ? ' fa-spin' : ''}`} /> Refresh
        </button>
      </div>
      <Help>
        Changes made through Zea: who did what and whether it succeeded. Days are in UTC, times in your timezone.
        {!admin && ' Only the events your audit rules cover (actions and authors) on the streams, connections and registries you can see are listed.'}
        {page && (page.retention > 0 ? ` Kept for ${page.retention} days.` : ' Storing is off (audit.retentionDays: 0); events are only in the backend log.')}
      </Help>
      {state.state === 'error' && <ErrorText text={state.error} />}
      {page && page.dropped ? (
        <div style={{ color: COLORS.warn, marginTop: '0.5em' }}>
          <i className='fa fa-exclamation-triangle' /> {page.dropped} events of this day did not fit and are only in the backend log.
        </div>
      ) : null}
      {!page ? (
        state.state === 'loading' && <Muted>Loading...</Muted>
      ) : events.length === 0 ? (
        <div style={{ marginTop: '0.8em' }}>
          <Muted>{page.events.length === 0 ? 'No changes recorded.' : 'No events match the filter.'}</Muted>
        </div>
      ) : (
        <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: '0.8em' }}>
          <thead>
            <tr>
              <th style={head}>Time</th>
              <th style={head}>User</th>
              <th style={head}>Action</th>
              <th style={head}>Item</th>
              <th style={head}>Result</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e, i) => (
              <EventRow key={`${e.time}-${i}`} e={e} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
};
