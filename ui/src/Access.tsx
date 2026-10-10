import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { Badge, Help } from './StreamFields';
import { AccessBinding, AccessPolicy, AccessRole, AccessRule, PolicyDocument, PolicyEvaluation, PolicyProblem, ResourceInfo } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

const EVERYONE = '*';
const ACTIONS = ['view', 'run', 'edit'];
const VALIDATE_DELAY_MS = 400;

const ACTION_HELP: Record<string, string> = {
  view: 'See the item, its runs and images',
  run: 'Start, cancel and retry runs',
  edit: 'Create, change and delete; see which credentials are set',
};

const clone = (p: AccessPolicy): AccessPolicy => JSON.parse(JSON.stringify(p));
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

const cell: React.CSSProperties = { padding: '0.3em 0.5em', borderTop: `1px solid ${COLORS.border}`, verticalAlign: 'middle' };
const head: React.CSSProperties = { padding: '0.3em 0.5em', textAlign: 'left', fontWeight: 600, color: COLORS.muted, whiteSpace: 'nowrap' };

function uniqueName(base: string, taken: string[]): string {
  for (let i = 1; ; i++) {
    const name = i === 1 ? base : `${base}-${i}`;
    if (!taken.includes(name)) {
      return name;
    }
  }
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

const Section = ({ title, help, children }: { title: string; help?: React.ReactNode; children: React.ReactNode }) => (
  <div className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em' }}>
    <div style={{ fontWeight: 600, fontSize: '1.05em' }}>{title}</div>
    {help && <Help>{help}</Help>}
    <div style={{ marginTop: '0.6em' }}>{children}</div>
  </div>
);

const ProblemsBox = ({ problems }: { problems: PolicyProblem[] }) =>
  problems.length === 0 ? null : (
    <div className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em', color: COLORS.warn }}>
      <b>
        <i className='fa fa-exclamation-triangle' /> The policy cannot be saved yet
      </b>
      <ul style={{ margin: '0.3em 0 0', paddingLeft: '1.2em' }}>
        {problems.map((p, i) => (
          <li key={i} style={{ wordBreak: 'break-word' }}>
            {p.path && <code>{p.path}</code>} {p.message}
          </li>
        ))}
      </ul>
    </div>
  );

interface RuleRowProps {
  rule: AccessRule;
  resources: ResourceInfo[];
  onChange: (r: AccessRule) => void;
  onDelete: () => void;
}

const RuleRow = ({ rule, resources, onChange, onDelete }: RuleRowProps) => {
  const supported = resources.find(r => r.name === rule.resource)?.actions ?? [];
  const toggle = (a: string, on: boolean) => {
    let next = on ? [...rule.actions, a] : rule.actions.filter(x => x !== a);
    if (next.length === 0 && a !== 'view') {
      // Dropping run or edit keeps the view they implied.
      next = ['view'];
    }
    onChange({ ...rule, actions: ACTIONS.filter(x => next.includes(x)) });
  };
  const changeResource = (resource: string) => {
    const allowed = resources.find(r => r.name === resource)?.actions ?? [];
    const actions = rule.actions.filter(a => allowed.includes(a));
    onChange({ ...rule, resource, actions: actions.length > 0 ? actions : ['view'] });
  };
  return (
    <tr>
      <td style={cell}>
        <select className='argo-field' value={rule.resource} onChange={e => changeResource(e.target.value)}>
          {!supported.length && <option value={rule.resource}>{rule.resource || '(choose)'}</option>}
          {resources.map(r => (
            <option key={r.name} value={r.name}>
              {r.name}
            </option>
          ))}
        </select>
      </td>
      <td style={{ ...cell, width: '100%' }}>
        <input
          className='argo-field'
          style={{ width: '100%', fontFamily: 'monospace' }}
          value={rule.pattern}
          placeholder='*'
          title='Glob over item names: * matches any characters, ? one character, [abc] a set'
          onChange={e => onChange({ ...rule, pattern: e.target.value })}
        />
      </td>
      {ACTIONS.map(a => {
        const ok = supported.includes(a);
        const implied = a === 'view' && rule.actions.some(x => x !== 'view');
        return (
          <td key={a} style={{ ...cell, textAlign: 'center' }}>
            {ok ? (
              <input
                type='checkbox'
                checked={implied || rule.actions.includes(a)}
                disabled={implied}
                title={implied ? 'Implied by run and edit' : ACTION_HELP[a]}
                onChange={e => toggle(a, e.target.checked)}
              />
            ) : (
              <span style={{ color: COLORS.muted }} title={`${rule.resource} have no ${a} action`}>
                -
              </span>
            )}
          </td>
        );
      })}
      <td style={cell}>
        <a style={{ cursor: 'pointer', color: COLORS.muted }} title='Remove rule' onClick={onDelete}>
          <i className='fa fa-times' />
        </a>
      </td>
    </tr>
  );
};

interface RoleCardProps {
  role: AccessRole;
  resources: ResourceInfo[];
  invalid: boolean;
  onChange: (r: AccessRole) => void;
  onDelete: () => void;
}

const RoleCard = ({ role, resources, invalid, onChange, onDelete }: RoleCardProps) => {
  // The name is committed on blur, so bindings follow a rename once and not
  // on every keystroke.
  const [name, setName] = React.useState(role.name);
  React.useEffect(() => setName(role.name), [role.name]);
  const commitName = () => {
    if (name.trim() !== role.name) {
      onChange({ ...role, name: name.trim() });
    }
  };
  const setRule = (i: number, r: AccessRule) => onChange({ ...role, rules: role.rules.map((x, j) => (j === i ? r : x)) });
  const addRule = () => onChange({ ...role, rules: [...role.rules, { resource: resources[0]?.name ?? 'streams', pattern: '*', actions: ['view'] }] });
  return (
    <div
      style={{
        border: `1px solid ${invalid ? COLORS.warn : COLORS.border}`,
        borderRadius: 4,
        padding: '0.6em 0.8em',
        marginBottom: '0.8em',
        background: COLORS.surface,
      }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', flexWrap: 'wrap' }}>
        <i className='fa fa-id-badge' style={{ color: COLORS.muted }} />
        <input
          className='argo-field'
          style={{ width: 200, fontWeight: 600 }}
          value={name}
          placeholder='role name'
          onChange={e => setName(e.target.value)}
          onBlur={commitName}
          onKeyDown={e => e.key === 'Enter' && commitName()}
        />
        <input
          className='argo-field'
          style={{ flex: 1, minWidth: 200 }}
          value={role.description ?? ''}
          placeholder='description'
          onChange={e => onChange({ ...role, description: e.target.value || undefined })}
        />
        <button className='argo-button argo-button--base-o' title='Delete role' onClick={onDelete}>
          <i className='fa fa-trash' />
        </button>
      </div>
      {role.rules.length === 0 ? (
        <div style={{ margin: '0.5em 0' }}>
          <Muted>No rules: the role grants nothing.</Muted>
        </div>
      ) : (
        <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: '0.5em' }}>
          <thead>
            <tr>
              <th style={head}>Resource</th>
              <th style={head}>Names (glob)</th>
              {ACTIONS.map(a => (
                <th key={a} style={{ ...head, textAlign: 'center' }} title={ACTION_HELP[a]}>
                  {a}
                </th>
              ))}
              <th style={head} />
            </tr>
          </thead>
          <tbody>
            {role.rules.map((r, i) => (
              <RuleRow
                key={i}
                rule={r}
                resources={resources}
                onChange={next => setRule(i, next)}
                onDelete={() => onChange({ ...role, rules: role.rules.filter((_, j) => j !== i) })}
              />
            ))}
          </tbody>
        </table>
      )}
      <button className='argo-button argo-button--base-o' style={{ marginTop: '0.4em' }} onClick={addRule}>
        <i className='fa fa-plus' /> Add rule
      </button>
    </div>
  );
};

interface MatrixProps {
  policy: AccessPolicy;
  onChange: (bindings: AccessBinding[]) => void;
}

// BindingsMatrix gives roles to subjects: one row per group or user, one
// column per role.
const BindingsMatrix = ({ policy, onChange }: MatrixProps) => {
  const { roles, bindings } = policy;
  const set = (i: number, b: AccessBinding) => onChange(bindings.map((x, j) => (j === i ? b : x)));
  const toggle = (i: number, role: string, on: boolean) => {
    const b = bindings[i];
    const next = on ? [...b.roles, role] : b.roles.filter(r => r !== role);
    set(i, { ...b, roles: roles.map(r => r.name).filter(r => next.includes(r)).concat(next.filter(r => !roles.some(x => x.name === r))) });
  };
  const setKind = (i: number, kind: 'group' | 'user') => {
    const b = bindings[i];
    const value = b.group ?? b.user ?? '';
    set(i, kind === 'group' ? { group: value, roles: b.roles } : { user: value, roles: b.roles });
  };
  const setSubject = (i: number, value: string) => {
    const b = bindings[i];
    set(i, b.user !== undefined ? { user: value, roles: b.roles } : { group: value, roles: b.roles });
  };
  const hasEveryone = bindings.some(b => b.group === EVERYONE);
  return (
    <>
      {bindings.length === 0 ? (
        <Muted>Nobody has access yet, except Zea admins.</Muted>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ borderCollapse: 'collapse', minWidth: '100%' }}>
            <thead>
              <tr>
                <th style={head}>Subject</th>
                {roles.map(r => (
                  <th key={r.name} style={{ ...head, textAlign: 'center' }} title={r.description}>
                    {r.name}
                  </th>
                ))}
                <th style={head} />
              </tr>
            </thead>
            <tbody>
              {bindings.map((b, i) => {
                const unknown = b.roles.filter(r => !roles.some(x => x.name === r));
                return (
                  <tr key={i}>
                    <td style={{ ...cell, whiteSpace: 'nowrap' }}>
                      {b.group === EVERYONE ? (
                        <span title='Everyone who can open Zea'>
                          <i className='fa fa-globe' /> <b>everyone</b> <Muted>(*)</Muted>
                        </span>
                      ) : (
                        <span style={{ display: 'inline-flex', gap: '0.4em' }}>
                          <select className='argo-field' value={b.user !== undefined ? 'user' : 'group'} onChange={e => setKind(i, e.target.value as 'group' | 'user')}>
                            <option value='group'>group</option>
                            <option value='user'>user</option>
                          </select>
                          <input
                            className='argo-field'
                            style={{ width: 220 }}
                            value={b.group ?? b.user ?? ''}
                            placeholder={b.user !== undefined ? 'username or user id' : 'Argo CD group'}
                            onChange={e => setSubject(i, e.target.value)}
                          />
                        </span>
                      )}
                      {unknown.length > 0 && (
                        <div style={{ fontSize: '0.85em', color: COLORS.warn }}>unknown roles: {unknown.join(', ')}</div>
                      )}
                    </td>
                    {roles.map(r => (
                      <td key={r.name} style={{ ...cell, textAlign: 'center' }}>
                        <input type='checkbox' checked={b.roles.includes(r.name)} onChange={e => toggle(i, r.name, e.target.checked)} />
                      </td>
                    ))}
                    <td style={cell}>
                      <a style={{ cursor: 'pointer', color: COLORS.muted }} title='Remove' onClick={() => onChange(bindings.filter((_, j) => j !== i))}>
                        <i className='fa fa-times' />
                      </a>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <div style={{ display: 'flex', gap: '0.5em', marginTop: '0.6em' }}>
        <button className='argo-button argo-button--base-o' onClick={() => onChange([...bindings, { group: '', roles: [] }])}>
          <i className='fa fa-users' /> Add group
        </button>
        <button className='argo-button argo-button--base-o' onClick={() => onChange([...bindings, { user: '', roles: [] }])}>
          <i className='fa fa-user' /> Add user
        </button>
        <button
          className='argo-button argo-button--base-o'
          disabled={hasEveryone}
          title={hasEveryone ? 'Everyone already has a row' : 'Roles for everyone who can open Zea'}
          onClick={() => onChange([...bindings, { group: EVERYONE, roles: [] }])}>
          <i className='fa fa-globe' /> Add everyone
        </button>
      </div>
    </>
  );
};

const CheckAccess = ({ client, policy, resources }: { client: ZeaClient; policy: AccessPolicy; resources: ResourceInfo[] }) => {
  const [user, setUser] = React.useState('');
  const [groups, setGroups] = React.useState('');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const [result, setResult] = React.useState<PolicyEvaluation | null>(null);

  const check = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const list = groups
        .split(',')
        .map(g => g.trim())
        .filter(Boolean);
      setResult(await client.evaluatePolicy(policy, user.trim(), list));
    } catch (err) {
      setResult(null);
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section title='Check access' help='What the policy in the editor (saved or not) grants a user on the items that exist now.'>
      <form onSubmit={check} style={{ display: 'flex', gap: '0.5em', flexWrap: 'wrap', alignItems: 'center' }}>
        <input className='argo-field' style={{ width: 200 }} value={user} placeholder='username' onChange={e => setUser(e.target.value)} />
        <input
          className='argo-field'
          style={{ flex: 1, minWidth: 240 }}
          value={groups}
          placeholder='groups, comma-separated'
          onChange={e => setGroups(e.target.value)}
        />
        <button type='submit' className='argo-button argo-button--base' disabled={busy}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-search'} /> Check
        </button>
      </form>
      {error && <ErrorText text={error} />}
      {result && (
        <div style={{ marginTop: '0.6em' }}>
          {result.isAdmin ? (
            <div>
              <i className='fa fa-crown' style={{ color: COLORS.warn }} /> A Zea admin: every action on everything.
            </div>
          ) : (
            <>
              <div style={{ marginBottom: '0.4em' }}>
                Roles:{' '}
                {result.roles.length === 0 ? (
                  <Muted>none</Muted>
                ) : (
                  result.roles.map(r => (
                    <React.Fragment key={r}>
                      <Badge>{r}</Badge>{' '}
                    </React.Fragment>
                  ))
                )}
              </div>
              {resources.map(res => {
                const items = result.items[res.name] ?? [];
                return (
                  <div key={res.name} style={{ marginBottom: '0.3em', wordBreak: 'break-word' }}>
                    <b>{res.name}:</b>{' '}
                    {items.length === 0 ? (
                      <Muted>nothing</Muted>
                    ) : (
                      items.map((it, i) => (
                        <React.Fragment key={it.name}>
                          {i > 0 && ', '}
                          <code>{it.name}</code> <Muted>({it.actions.join(', ')})</Muted>
                        </React.Fragment>
                      ))
                    )}
                  </div>
                );
              })}
            </>
          )}
        </div>
      )}
    </Section>
  );
};

const ExportPanel = ({ yaml, onClose }: { yaml: string; onClose: () => void }) => {
  const [copied, setCopied] = React.useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(yaml);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <Section title='Export' help={<>The policy in the editor. To manage it in git, put it under the key <code>policy.yaml</code> of the ConfigMap <code>zea-policy</code> in the Connections namespace.</>}>
      <div style={{ display: 'flex', gap: '0.5em', marginBottom: '0.5em' }}>
        <button className='argo-button argo-button--base-o' onClick={copy}>
          <i className={copied ? 'fa fa-check' : 'fa fa-copy'} /> Copy
        </button>
        <button className='argo-button argo-button--base-o' onClick={() => download('zea-policy.yaml', yaml)}>
          <i className='fa fa-download' /> Download
        </button>
        <button className='argo-button argo-button--base-o' onClick={onClose}>
          Close
        </button>
      </div>
      <textarea className='argo-field' readOnly rows={Math.min(30, yaml.split('\n').length + 1)} value={yaml} style={{ width: '100%', fontFamily: 'monospace' }} />
    </Section>
  );
};

const ImportPanel = ({ client, onLoaded, onClose }: { client: ZeaClient; onLoaded: (p: AccessPolicy) => void; onClose: () => void }) => {
  const [text, setText] = React.useState('');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const load = async () => {
    setBusy(true);
    setError('');
    try {
      const v = await client.validatePolicy({ yaml: text });
      onLoaded(v.policy);
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };
  const readFile = (f?: File) => f && f.text().then(setText);
  return (
    <Section title='Import' help='Replaces the policy in the editor; nothing is saved until you press Save.'>
      <div style={{ display: 'flex', gap: '0.5em', marginBottom: '0.5em', alignItems: 'center' }}>
        <input type='file' accept='.yaml,.yml,text/yaml' onChange={e => readFile(e.target.files?.[0])} />
        <div style={{ flex: 1 }} />
        <button className='argo-button argo-button--base' disabled={busy || text.trim() === ''} onClick={load}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-file-import'} /> Load into editor
        </button>
        <button className='argo-button argo-button--base-o' onClick={onClose}>
          Close
        </button>
      </div>
      <textarea
        className='argo-field'
        rows={14}
        value={text}
        placeholder={'roles:\n  - name: developers\n    rules:\n      - resource: streams\n        pattern: "dev-*"\n        actions: [view, run]\nbindings:\n  - group: devs\n    roles: [developers]'}
        onChange={e => setText(e.target.value)}
        style={{ width: '100%', fontFamily: 'monospace' }}
      />
      {error && <ErrorText text={error} />}
    </Section>
  );
};

type Panel = 'none' | 'export' | 'import';

const PolicyEditor = ({ client, initial, onReload }: { client: ZeaClient; initial: PolicyDocument; onReload: () => void }) => {
  const [doc, setDoc] = React.useState(initial);
  const [draft, setDraft] = React.useState(() => clone(initial.policy));
  const [problems, setProblems] = React.useState<PolicyProblem[]>([]);
  const [yaml, setYaml] = React.useState(initial.yaml);
  const [panel, setPanel] = React.useState<Panel>('none');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const [saved, setSaved] = React.useState(false);
  const dirty = !same(draft, doc.policy);

  React.useEffect(() => {
    let cancelled = false;
    const timer = setTimeout(() => {
      client.validatePolicy({ policy: draft }).then(
        v => {
          if (!cancelled) {
            setProblems(v.problems);
            setYaml(v.yaml);
          }
        },
        err => !cancelled && setProblems([{ path: '', message: describeError(err) }]),
      );
    }, VALIDATE_DELAY_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [client, draft]);

  const change = (p: AccessPolicy) => {
    setDraft(p);
    setSaved(false);
  };

  const setRole = (i: number, role: AccessRole) => {
    const prev = draft.roles[i];
    const roles = draft.roles.map((r, j) => (j === i ? role : r));
    // Bindings follow a rename unless the new name is taken by another role.
    const rename = role.name !== prev.name && !!prev.name && !draft.roles.some((r, j) => j !== i && r.name === role.name);
    const bindings = rename ? draft.bindings.map(b => ({ ...b, roles: b.roles.map(r => (r === prev.name ? role.name : r)) })) : draft.bindings;
    change({ roles, bindings });
  };

  const deleteRole = (i: number) => {
    const name = draft.roles[i].name;
    const used = draft.bindings.filter(b => b.roles.includes(name)).length;
    if (used > 0 && !window.confirm(`Role ${name} is bound to ${used} subject${used > 1 ? 's' : ''}. Delete it and its bindings?`)) {
      return;
    }
    change({
      roles: draft.roles.filter((_, j) => j !== i),
      bindings: draft.bindings.map(b => ({ ...b, roles: b.roles.filter(r => r !== name) })),
    });
  };

  const addRole = () => {
    const name = uniqueName('role', draft.roles.map(r => r.name));
    change({ ...draft, roles: [...draft.roles, { name, rules: [] }] });
  };

  const save = async () => {
    setBusy(true);
    setError('');
    try {
      const d = await client.savePolicy(draft, doc.version);
      setDoc(d);
      setDraft(clone(d.policy));
      setSaved(true);
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const revert = () => {
    if (!dirty || window.confirm('Discard the unsaved changes and reload the saved policy?')) {
      onReload();
    }
  };

  const { users, groups } = doc.admins;
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '1em', flexWrap: 'wrap' }}>
        <Muted>Roles grant actions on streams, connections and registries; bindings give roles to groups and users.</Muted>
        <div style={{ flex: 1 }} />
        {dirty && <span style={{ color: COLORS.warn }}>Unsaved changes</span>}
        {saved && !dirty && (
          <span style={{ color: COLORS.ok }}>
            <i className='fa fa-check-circle' /> Saved
          </span>
        )}
        <button
          className='argo-button argo-button--base'
          disabled={busy || !dirty || !doc.editable || problems.length > 0}
          title={!doc.editable ? 'The policy is managed outside Zea' : problems.length > 0 ? 'Fix the problems first' : undefined}
          onClick={save}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-save'} /> Save
        </button>
        <button className='argo-button argo-button--base-o' disabled={busy} onClick={revert} title='Reload the saved policy'>
          <i className='fa fa-undo' /> Revert
        </button>
        <button className='argo-button argo-button--base-o' onClick={() => setPanel(panel === 'import' ? 'none' : 'import')}>
          <i className='fa fa-file-import' /> Import
        </button>
        <button className='argo-button argo-button--base-o' onClick={() => setPanel(panel === 'export' ? 'none' : 'export')}>
          <i className='fa fa-file-export' /> Export
        </button>
      </div>

      <div style={{ marginBottom: '0.8em' }}>
        <Muted>
          <i className='fa fa-crown' /> Zea admins (chart value <code>admins</code>) have every action and edit this policy:{' '}
          {users.length + groups.length === 0 ? (
            <span style={{ color: COLORS.warn }}>none configured</span>
          ) : (
            [...users.map(u => `user ${u}`), ...groups.map(g => `group ${g}`)].join(', ')
          )}
        </Muted>
      </div>
      {doc.error && <ErrorText text={`The saved policy is invalid and grants nothing until it is fixed: ${doc.error}`} />}
      {!doc.editable && (
        <div style={{ color: COLORS.warn, marginBottom: '0.8em' }}>
          <i className='fa fa-lock' /> The ConfigMap zea-policy is managed outside Zea (for example by Argo CD). Change it in git; use Export to get
          the YAML.
        </div>
      )}
      {!doc.exists && (
        <div style={{ marginBottom: '0.8em' }}>
          <Muted>No policy is saved yet, so only Zea admins have access.</Muted>
        </div>
      )}
      {error && <ErrorText text={error} />}

      {panel === 'export' && <ExportPanel yaml={yaml} onClose={() => setPanel('none')} />}
      {panel === 'import' && (
        <ImportPanel
          client={client}
          onClose={() => setPanel('none')}
          onLoaded={p => {
            change(p);
            setPanel('none');
          }}
        />
      )}

      <ProblemsBox problems={problems} />

      <Section
        title={`Roles (${draft.roles.length})`}
        help={
          <>
            A rule grants actions on the items of a resource whose names match the glob, for example <code>*</code>, <code>dev-*</code>{' '}
            or <code>backend</code>. Any action includes view. Saving a stream also needs run access to every connection it uses.
          </>
        }>
        {draft.roles.map((r, i) => (
          <RoleCard
            key={i}
            role={r}
            resources={doc.resources}
            invalid={problems.some(p => p.path.startsWith(`roles[${i}]`))}
            onChange={next => setRole(i, next)}
            onDelete={() => deleteRole(i)}
          />
        ))}
        <button className='argo-button argo-button--base-o' onClick={addRole}>
          <i className='fa fa-plus' /> Add role
        </button>
      </Section>

      <Section title='Bindings' help='Groups come from the Argo CD login (SSO). A user matches by username or user id.'>
        {draft.roles.length === 0 ? <Muted>Add a role first.</Muted> : <BindingsMatrix policy={draft} onChange={bindings => change({ ...draft, bindings })} />}
      </Section>

      <CheckAccess client={client} policy={draft} resources={doc.resources} />
    </>
  );
};

// AccessView is the admin page for the access policy.
export const AccessView = ({ client }: { client: ZeaClient }) => {
  const [doc, reload] = useLoad(() => client.policy(), [client]);
  if (doc.state === 'loading') {
    return <Muted>Loading the access policy...</Muted>;
  }
  if (doc.state === 'error') {
    return <ErrorText text={doc.error} />;
  }
  return <PolicyEditor client={client} initial={doc.data} onReload={reload} />;
};
