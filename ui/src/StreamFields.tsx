import * as React from 'react';
import { BranchPicker } from './BranchPicker';
import { ConnData, isTemplate, Reference } from './streamModel';
import { BranchList, Pipeline, StreamProblem } from './types';
import { COLORS, useLoad } from './ui';

export const STREAM_STATUS: Record<string, { icon: string; color: string; label: string }> = {
  pending: { icon: 'fa fa-clock', color: COLORS.muted, label: 'Pending' },
  queued: { icon: 'fa fa-hourglass-half', color: COLORS.muted, label: 'Queued' },
  starting: { icon: 'fa fa-circle-notch fa-spin', color: COLORS.muted, label: 'Starting' },
  running: { icon: 'fa fa-circle-notch fa-spin', color: '#0dadea', label: 'Running' },
  waiting: { icon: 'fa fa-hand-paper', color: COLORS.warn, label: 'Waiting for approval' },
  succeeded: { icon: 'fa fa-check-circle', color: COLORS.ok, label: 'Succeeded' },
  failed: { icon: 'fa fa-times-circle', color: COLORS.error, label: 'Failed' },
  cancelled: { icon: 'fa fa-ban', color: COLORS.muted, label: 'Cancelled' },
  skipped: { icon: 'fa fa-forward', color: COLORS.muted, label: 'Skipped' },
};

const statusOf = (s: string) => STREAM_STATUS[s] ?? { icon: 'fa fa-question-circle', color: COLORS.muted, label: s };

export const StreamStatusIcon = ({ status }: { status: string }) => {
  const s = statusOf(status);
  return <i className={s.icon} style={{ color: s.color, width: '1.1em', textAlign: 'center' }} title={s.label} />;
};

export const StreamStatusLabel = ({ status }: { status: string }) => {
  const s = statusOf(status);
  return <span style={{ color: s.color, fontWeight: 600 }}>{s.label}</span>;
};

export const ellipsis: React.CSSProperties = { overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 };

export const Help = ({ children }: { children: React.ReactNode }) => (
  <div style={{ fontSize: '0.85em', color: COLORS.muted, marginTop: '0.2em', wordBreak: 'break-word' }}>{children}</div>
);

// cardStyle frames one item of a list (a param, a step input or variable).
export const cardStyle: React.CSSProperties = {
  border: `1px solid ${COLORS.border}`,
  borderRadius: 4,
  background: COLORS.surface,
  padding: '0.5em 0.7em',
  marginBottom: '0.5em',
};

// matchesFilter matches names containing the filter text, ignoring case.
export const matchesFilter = (name: string, filter: string) => {
  const f = filter.trim().toLowerCase();
  return f === '' || name.toLowerCase().includes(f);
};

export const FilterInput = ({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) => (
  <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em', marginBottom: '0.5em' }}>
    <i className='fa fa-search' style={{ color: COLORS.muted }} />
    <input
      className='argo-field'
      style={{ flex: 1, minWidth: 0, fontFamily: 'monospace' }}
      value={value}
      placeholder={placeholder}
      spellCheck={false}
      onChange={e => onChange(e.target.value)}
      onKeyDown={e => e.key === 'Escape' && onChange('')}
    />
    {value && (
      <button type='button' className='argo-button argo-button--base-o' style={{ padding: '0 0.6em' }} title='Clear filter' onClick={() => onChange('')}>
        <i className='fa fa-times' />
      </button>
    )}
  </div>
);

export const Field = ({
  label,
  hint,
  required,
  help,
  card,
  children,
}: {
  label: React.ReactNode;
  hint?: string;
  required?: boolean;
  help?: React.ReactNode;
  card?: boolean;
  children: React.ReactNode;
}) => (
  <div style={card ? cardStyle : { marginBottom: '0.7em' }}>
    <label style={{ display: 'block', fontWeight: 600, marginBottom: '0.2em', wordBreak: 'break-word' }}>
      {label}
      {required && <span style={{ color: COLORS.error }}> *</span>}
      {hint && <span style={{ fontWeight: 400, color: COLORS.muted, fontSize: '0.85em' }}> {hint}</span>}
    </label>
    {children}
    {help && <Help>{help}</Help>}
  </div>
);

// InlineField puts the label and the control on one line, with the help
// text under the control.
export const InlineField = ({
  label,
  required,
  help,
  children,
}: {
  label: React.ReactNode;
  required?: boolean;
  help?: React.ReactNode;
  children: React.ReactNode;
}) => (
  <div style={{ display: 'grid', gridTemplateColumns: 'minmax(110px, 200px) minmax(0, 1fr)', columnGap: '1em', alignItems: 'baseline', marginBottom: '0.7em' }}>
    <label style={{ fontWeight: 600, wordBreak: 'break-word' }}>
      {label}
      {required && <span style={{ color: COLORS.error }}> *</span>}
    </label>
    <div style={{ minWidth: 0 }}>
      {children}
      {help && <Help>{help}</Help>}
    </div>
  </div>
);

export const Badge = ({ children, color, title }: { children: React.ReactNode; color?: string; title?: string }) => (
  <span
    title={title}
    style={{
      display: 'inline-block',
      border: `1px solid ${color ?? COLORS.border}`,
      color: color ?? COLORS.muted,
      borderRadius: 3,
      padding: '0 0.4em',
      fontSize: '0.8em',
      lineHeight: 1.6,
      whiteSpace: 'nowrap',
    }}>
    {children}
  </span>
);

export const ProblemList = ({ problems, onStep }: { problems: StreamProblem[]; onStep?: (id: string) => void }) => (
  <ul style={{ margin: '0.3em 0 0', paddingLeft: '1.2em' }}>
    {problems.map((p, i) => (
      <li key={i} style={{ wordBreak: 'break-word' }}>
        {p.step && (
          <a style={{ cursor: onStep ? 'pointer' : undefined }} onClick={() => onStep?.(p.step as string)}>
            step <code>{p.step}</code>
          </a>
        )}
        {p.param && (
          <>
            param <code>{p.param}</code>
          </>
        )}
        {(p.step || p.param) && ': '}
        {p.message}
      </li>
    ))}
  </ul>
);

// useDebounced returns value once it has not changed for ms.
export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = React.useState(value);
  React.useEffect(() => {
    const t = window.setTimeout(() => setV(value), ms);
    return () => window.clearTimeout(t);
  }, [value, ms]);
  return v;
}

// RefSelect is a compact menu that inserts a ${{ ... }} reference.
export const RefSelect = ({ refs, onPick }: { refs: Reference[]; onPick: (value: string) => void }) => {
  const groups = Array.from(new Set(refs.map(r => r.group)));
  return (
    <select
      className='argo-field'
      title='Insert a reference to a param, an earlier step or a built-in value'
      value=''
      onChange={e => e.target.value && onPick(e.target.value)}
      style={{ flex: '0 0 auto', width: '3.4em', fontFamily: 'monospace', cursor: 'pointer' }}>
      <option value=''>{'{ }'}</option>
      {groups.map(g => (
        <optgroup key={g} label={g}>
          {refs
            .filter(r => r.group === g)
            .map(r => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
        </optgroup>
      ))}
    </select>
  );
};

interface ValueFieldProps {
  value: string;
  onChange: (v: string) => void;
  refs: Reference[];
  placeholder?: string;
  // Choice and boolean values: a list, unless the value is an expression.
  options?: string[];
  // Branch values: a branch picker that still accepts any text.
  branches?: BranchList;
  mono?: boolean;
}

// ValueField edits a step value that may contain ${{ ... }} references.
export const ValueField = ({ value, onChange, refs, placeholder, options, branches, mono }: ValueFieldProps) => {
  const asList = !!options && !isTemplate(value) && (value === '' || options.includes(value));
  const insert = (ref: string) => onChange(asList || value === '' ? ref : value + ref);
  return (
    <div style={{ display: 'flex', gap: '0.3em', alignItems: 'center' }}>
      {asList ? (
        <select className='argo-field' style={{ flex: 1, minWidth: 0 }} value={value} onChange={e => onChange(e.target.value)}>
          <option value=''>{placeholder || '(not set)'}</option>
          {(options ?? []).map(o => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      ) : branches && branches.branches.length > 0 ? (
        <BranchPicker
          value={value}
          branches={branches.branches}
          defaultBranch={branches.defaultBranch}
          placeholder={placeholder}
          onChange={onChange}
        />
      ) : (
        <input
          className='argo-field'
          style={{ flex: 1, minWidth: 0, fontFamily: mono || isTemplate(value) ? 'monospace' : undefined }}
          value={value}
          placeholder={placeholder}
          spellCheck={false}
          autoComplete='off'
          onChange={e => onChange(e.target.value)}
        />
      )}
      {refs.length > 0 && <RefSelect refs={refs} onPick={insert} />}
    </div>
  );
};

interface KeyValueProps {
  entries?: Record<string, string>;
  // Keys edited elsewhere (declared inputs, pipeline variables).
  exclude?: string[];
  onChange: (next: Record<string, string> | undefined) => void;
  refs: Reference[];
  keyPlaceholder: string;
  addLabel: string;
  // Only rows whose name matches are shown; rows without a name always are.
  filter?: string;
}

// KeyValueEditor edits free name/value pairs; values may be expressions.
export const KeyValueEditor = ({ entries, exclude = [], onChange, refs, keyPlaceholder, addLabel, filter = '' }: KeyValueProps) => {
  // Row order and rows whose name is still empty; values live in entries.
  const [rows, setRows] = React.useState<string[]>([]);
  const keys = Object.keys(entries ?? {}).filter(k => !exclude.includes(k));
  const shown = [...rows.filter(r => r === '' || keys.includes(r)), ...keys.filter(k => !rows.includes(k))];

  const emit = (nextRows: string[], values: Record<string, string>) => {
    setRows(nextRows);
    const out: Record<string, string> = {};
    for (const [k, v] of Object.entries(entries ?? {})) {
      if (exclude.includes(k)) {
        out[k] = v;
      }
    }
    for (const r of nextRows) {
      if (r !== '' && !(r in out)) {
        out[r] = values[r] ?? '';
      }
    }
    onChange(Object.keys(out).length > 0 ? out : undefined);
  };

  const rename = (i: number, k: string) => {
    const old = shown[i];
    const values = { ...(entries ?? {}) };
    if (old !== '' && k !== '' && !(k in values)) {
      values[k] = values[old] ?? '';
    }
    emit(
      shown.map((r, j) => (j === i ? k : r)),
      values,
    );
  };

  return (
    <div>
      {shown.map((k, i) =>
        k !== '' && !matchesFilter(k, filter) ? null : (
        <div key={i} style={{ ...cardStyle, display: 'flex', gap: '0.3em', alignItems: 'center', padding: '0.4em 0.5em' }}>
          <input
            className='argo-field'
            style={{ flex: '0 0 38%', minWidth: 0, fontFamily: 'monospace' }}
            placeholder={keyPlaceholder}
            value={k}
            spellCheck={false}
            onChange={e => rename(i, e.target.value)}
          />
          <div style={{ flex: 1, minWidth: 0 }}>
            <ValueField
              value={k ? entries?.[k] ?? '' : ''}
              refs={refs}
              placeholder={k ? 'value' : 'enter a name first'}
              onChange={v => k && emit(shown, { ...(entries ?? {}), [k]: v })}
            />
          </div>
          <button
            type='button'
            className='argo-button argo-button--base-o'
            style={{ padding: '0 0.6em' }}
            title='Remove'
            onClick={() =>
              emit(
                shown.filter((_, j) => j !== i),
                entries ?? {},
              )
            }>
            <i className='fa fa-times' />
          </button>
          {k !== '' && shown.indexOf(k) !== i && (
            <i className='fa fa-exclamation-triangle' style={{ color: COLORS.warn }} title='Duplicate name: only the first one is used' />
          )}
        </div>
        ),
      )}
      <button type='button' className='argo-button argo-button--base-o' onClick={() => setRows([...shown, ''])}>
        <i className='fa fa-plus' /> {addLabel}
      </button>
    </div>
  );
};

// PipelineName shows the name of a pipeline id, read from the default branch.
export const PipelineName = ({ data, connection, id }: { data: ConnData; connection: string; id: string }) => {
  const [list] = useLoad(
    () => (connection && id ? data.pipelines(connection, '') : Promise.resolve([] as Pipeline[])),
    [data, connection, id],
  );
  const name = list.state === 'ok' ? list.data.find(p => p.id === id)?.name : undefined;
  return <>{name ?? (id || '(no pipeline)')}</>;
};
