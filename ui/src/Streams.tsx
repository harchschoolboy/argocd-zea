import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { navigate, useRoute } from './route';
import { ago, usePoll } from './Runs';
import { Badge, ellipsis, Help, ProblemList, StreamStatusIcon, StreamStatusLabel } from './StreamFields';
import { StreamEditor } from './StreamEditor';
import { ConnData, MAX_STREAM_NAME, specOf, streamLabel } from './streamModel';
import { ApprovalBadge, LockBadge, runActive, StagesBoard, StreamRunForm, StreamRunHistory, StreamRunView, waitingSteps } from './StreamRun';
import { can, Connection, Me, Stream, StreamExport, StreamInput, StreamList as StreamListData, StreamRun, StreamSpec } from './types';
import { COLORS, ErrorText, Muted, useLoad } from './ui';

const StreamBadges = ({ stream }: { stream: Stream }) => (
  <>
    {stream.draftOf && <Badge title='An editable copy; export it to replace the original in git'>draft of {stream.draftOf}</Badge>}
    {!stream.editable && (
      <Badge title='Managed in git (Argo CD); edit a draft and export it'>
        <i className='fa fa-lock' /> git
      </Badge>
    )}
    {stream.lock && <LockBadge lock={stream.lock} />}
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
  const open = (e: React.MouseEvent, id: string) => {
    e.stopPropagation();
    navigate({ view: 'streams', stream, srun: id });
  };
  // Older runs may wait for an approval too; link the oldest one.
  const waiting = runs.data.filter(x => waitingSteps(x).length > 0);
  const first = waiting[waiting.length - 1];
  return (
    <span style={{ display: 'inline-flex', gap: '0.5em', alignItems: 'baseline', flexWrap: 'wrap' }}>
      <span style={{ cursor: 'pointer' }} onClick={e => open(e, r.id)}>
        <StreamStatusIcon status={r.status} /> <StreamStatusLabel status={r.status} />{' '}
        <Muted>
          {ago(r.createdAt)} by {r.user}
        </Muted>
      </span>
      {first && (
        <span style={{ cursor: 'pointer' }} onClick={e => open(e, first.id)}>
          <ApprovalBadge steps={waiting.flatMap(waitingSteps)} />
          {waiting.length > 1 && <Muted> {waiting.length} runs</Muted>}
        </span>
      )}
    </span>
  );
};

// StreamCard is a Stream on the list page: a card, or one compact row.
// Narrow compact rows (category columns) show the last run under the name.
const StreamCard = ({
  client,
  stream,
  compact,
  narrow,
  drag,
}: {
  client: ZeaClient;
  stream: Stream;
  compact?: boolean;
  narrow?: boolean;
  drag?: DragProps;
}) => {
  const steps = stream.stages.reduce((n, s) => n + s.steps.length, 0);
  const runs = usePoll(
    () => client.streamRuns(stream.name),
    [client, stream.name],
    CARD_POLL_MS,
    d => (d ?? []).some(r => runActive(r.status)),
  );
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const blocked = stream.problems.length > 0;

  const parameterized = stream.params.length > 0;
  const label = streamLabel(stream);
  const tooltip = [label !== stream.name ? stream.name : '', stream.description ?? ''].filter(x => x).join('\n') || undefined;

  const run = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (parameterized) {
      navigate({ view: 'streams', stream: stream.name, mode: 'run' });
      return;
    }
    if (!window.confirm(`Run stream ${label}?`)) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      await client.startStream(stream.name, {});
      runs.reload();
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const runButton = can(stream.actions, 'run') && (
    <button
      className='argo-button argo-button--base'
      style={{ flex: '0 0 auto' }}
      disabled={busy || blocked}
      title={blocked ? 'Fix the problems first' : parameterized ? 'Run with parameters' : 'Run'}
      onClick={run}>
      <i className={busy ? 'fa fa-circle-notch fa-spin' : parameterized ? 'fa fa-sliders-h' : 'fa fa-play'} /> {parameterized ? 'Run...' : 'Run'}
    </button>
  );

  if (compact) {
    const title = (
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em', minWidth: 0 }}>
        <i className='fa fa-stream' style={{ color: COLORS.muted }} />
        <b style={ellipsis}>{label}</b>
      </div>
    );
    return (
      <div
        {...drag}
        className='white-box'
        style={{ padding: '0.4em 1em', cursor: 'pointer', margin: 0, ...drag?.style }}
        title={tooltip}
        onClick={() => navigate({ view: 'streams', stream: stream.name })}>
        {narrow ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em' }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              {title}
              <div style={{ fontSize: '0.85em', marginTop: '0.15em' }}>
                <LastStreamRun stream={stream.name} runs={runs} />
              </div>
            </div>
            {runButton}
          </div>
        ) : (
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1.4fr) 110px', gap: '1em', alignItems: 'center' }}>
            {title}
            <div style={{ minWidth: 0 }}>
              <LastStreamRun stream={stream.name} runs={runs} />
            </div>
            <div style={{ textAlign: 'right' }}>{runButton}</div>
          </div>
        )}
        {error && (
          <div onClick={e => e.stopPropagation()}>
            <ErrorText text={error} />
          </div>
        )}
      </div>
    );
  }

  return (
    <div
      {...drag}
      className='white-box'
      style={{ padding: '0.8em 1em', cursor: 'pointer', margin: 0, ...drag?.style }}
      title={label !== stream.name ? stream.name : undefined}
      onClick={() => navigate({ view: 'streams', stream: stream.name })}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em', flexWrap: 'wrap', flex: 1, minWidth: 0 }}>
          <i className='fa fa-stream' style={{ color: COLORS.muted }} />
          <b style={{ ...ellipsis, fontSize: '1.05em' }}>{label}</b>
          <StreamBadges stream={stream} />
        </div>
        {runButton}
      </div>
      {stream.description && <div style={{ marginTop: '0.3em', wordBreak: 'break-word' }}>{stream.description}</div>}
      <div style={{ fontSize: '0.85em', color: COLORS.muted, marginTop: '0.3em', wordBreak: 'break-word' }}>
        {stream.stages.length} stage{stream.stages.length === 1 ? '' : 's'}{' \u00b7 '}{steps} step{steps === 1 ? '' : 's'}
        {stream.connections.length > 0 && <>{' \u00b7 '}{stream.connections.join(', ')}</>}
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

const IMPORT_KEY = 'zea.streams.import';
// The parsed import, kept until the editor opens it; also in session
// storage so that a reload of the editor keeps it.
let pendingImport: StreamInput | null = null;

function keepImport(s: StreamInput) {
  pendingImport = s;
  try {
    window.sessionStorage.setItem(IMPORT_KEY, JSON.stringify(s));
  } catch {
    // Kept in memory only.
  }
}

function readImport(): StreamInput | null {
  try {
    const raw = window.sessionStorage.getItem(IMPORT_KEY);
    if (raw) {
      return JSON.parse(raw) as StreamInput;
    }
  } catch {
    // Falls back to the copy in memory.
  }
  return pendingImport;
}

function dropImport() {
  pendingImport = null;
  try {
    window.sessionStorage.removeItem(IMPORT_KEY);
  } catch {
    // Nothing kept.
  }
}

// freeName is base with a suffix that no stream uses yet.
function freeName(base: string, taken: Set<string>): string {
  for (let i = 1; ; i++) {
    const suffix = i === 1 ? '-copy' : `-copy-${i}`;
    const name = base.slice(0, MAX_STREAM_NAME - suffix.length).replace(/-+$/, '') + suffix;
    if (!taken.has(name)) {
      return name;
    }
  }
}

const ImportPanel = ({ client, onClose }: { client: ZeaClient; onClose: () => void }) => {
  const [yaml, setYaml] = React.useState('');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      keepImport(await client.parseStream(yaml));
      navigate({ view: 'streams', mode: 'import' });
    } catch (err) {
      setError(describeError(err));
      setBusy(false);
    }
  };
  return (
    <div className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em' }}>
      <b>Import a stream</b>
      <Help>
        Paste an exported ConfigMap, or a document with name, params and stages. It opens in the editor, where duplicates are marked;
        nothing is saved until you press Save.
      </Help>
      <textarea
        className='argo-field'
        rows={14}
        spellCheck={false}
        value={yaml}
        onChange={e => setYaml(e.target.value)}
        style={{ width: '100%', fontFamily: 'monospace', marginTop: '0.5em' }}
      />
      {error && <ErrorText text={error} />}
      <div style={{ display: 'flex', gap: '0.5em', marginTop: '0.4em' }}>
        <button className='argo-button argo-button--base' disabled={busy || !yaml.trim()} onClick={submit}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-file-import'} /> Open in editor
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

// OTHER is the tab of Streams without a category.
const OTHER = '~other';
// GROUP prefixes a category used as a drop target for streams.
const GROUP = 'group:';
// NEW is the drop target that moves a stream into a new category.
const NEW = '~new';
const MAX_CATEGORY = 60;

const categoryOf = (s: Stream) => s.category?.trim() || OTHER;

const CategoryTab = ({
  active,
  onClick,
  drag,
  children,
}: {
  active: boolean;
  onClick: () => void;
  drag?: DragProps;
  children: React.ReactNode;
}) => (
  <a
    onClick={onClick}
    {...drag}
    style={{
      cursor: 'pointer',
      padding: '0.4em 0.2em',
      marginBottom: -1,
      whiteSpace: 'nowrap',
      fontWeight: active ? 600 : undefined,
      color: active ? undefined : COLORS.muted,
      borderBottom: `2px solid ${active ? 'currentColor' : 'transparent'}`,
      ...drag?.style,
    }}>
    {children}
  </a>
);

const DROP_COLOR = '#0dadea';

type DragProps = Pick<React.HTMLAttributes<HTMLElement>, 'draggable' | 'title' | 'onDragStart' | 'onDragOver' | 'onDrop' | 'onDragEnd' | 'style'>;

// orderCategories puts categories in the saved order; the others follow
// alphabetically.
function orderCategories(categories: string[], saved: string[]): string[] {
  const known = new Set(categories);
  const first = saved.filter(c => known.has(c));
  const placed = new Set(first);
  return [...first, ...categories.filter(c => !placed.has(c)).sort((a, b) => a.localeCompare(b))];
}

const LAYOUT_KEY = 'zea.streams.layout';
const GROUPED_KEY = 'zea.streams.grouped';

type Layout = 'cards' | 'rows';

function savedLayout(): Layout {
  try {
    return window.localStorage.getItem(LAYOUT_KEY) === 'rows' ? 'rows' : 'cards';
  } catch {
    return 'cards';
  }
}

function savedGrouped(): boolean {
  try {
    return window.localStorage.getItem(GROUPED_KEY) !== 'false';
  } catch {
    return true;
  }
}

const LayoutButton = ({ value, current, icon, title, onPick }: { value: Layout; current: Layout; icon: string; title: string; onPick: (l: Layout) => void }) => (
  <button
    className={current === value ? 'argo-button argo-button--base' : 'argo-button argo-button--base-o'}
    style={{ padding: '0 0.7em' }}
    title={title}
    onClick={() => onPick(value)}>
    <i className={icon} />
  </button>
);

const StreamList = ({ client, me, tab }: { client: ZeaClient; me: Me; tab?: string }) => {
  const [list, reload] = useLoad<StreamListData>(() => client.streams(), [client]);
  const [layout, setLayout] = React.useState<Layout>(savedLayout);
  const [grouped, setGrouped] = React.useState(savedGrouped);
  const pickLayout = (l: Layout) => {
    setLayout(l);
    try {
      window.localStorage.setItem(LAYOUT_KEY, l);
    } catch {
      // The layout is only kept for this page view.
    }
  };
  const toggleGrouped = () => {
    setGrouped(!grouped);
    try {
      window.localStorage.setItem(GROUPED_KEY, String(!grouped));
    } catch {
      // The choice is only kept for this page view.
    }
  };
  const canCreate = can(me.permissions.streams, 'edit');
  const [importing, setImporting] = React.useState(false);
  // The orders saved by this page until the list is loaded again.
  const [savedOrder, setSavedOrder] = React.useState<string[] | null>(null);
  const [savedStreams, setSavedStreams] = React.useState<string[] | null>(null);
  const [orderError, setOrderError] = React.useState('');
  const [dragging, setDragging] = React.useState<string | null>(null);
  const [dropAt, setDropAt] = React.useState<{ target: string; after: boolean } | null>(null);
  const [dragStream, setDragStream] = React.useState<Stream | null>(null);
  // key is a stream name, or GROUP + category for the end of a category.
  const [streamDrop, setStreamDrop] = React.useState<{ key: string; after: boolean } | null>(null);
  React.useEffect(() => {
    setSavedOrder(null);
    setSavedStreams(null);
  }, [list]);

  const raw = list.state === 'ok' ? list.data.streams : [];
  const order = savedOrder ?? (list.state === 'ok' ? list.data.categoryOrder : []);
  const categories = orderCategories([...new Set(raw.map(s => s.category?.trim() ?? '').filter(c => c !== ''))], order);
  const rank = new Map((savedStreams ?? (list.state === 'ok' ? list.data.streamOrder : [])).map((n, i) => [n, i]));
  const groupIndex = (k: string) => (k === OTHER ? categories.length : categories.indexOf(k));
  // Streams by category, then in the saved order; the others by label.
  const all = [...raw].sort((a, b) => {
    const ra = rank.get(a.name);
    const rb = rank.get(b.name);
    return (
      groupIndex(categoryOf(a)) - groupIndex(categoryOf(b)) ||
      (ra !== undefined && rb !== undefined ? ra - rb : ra !== undefined ? -1 : rb !== undefined ? 1 : streamLabel(a).localeCompare(streamLabel(b)))
    );
  });
  const uncategorized = all.filter(s => categoryOf(s) === OTHER).length;
  const known = tab === OTHER ? uncategorized > 0 : !!tab && categories.includes(tab);
  const current = known ? tab : undefined;
  const shown = all.filter(s => !current || categoryOf(s) === current);
  const canOrder = canCreate && categories.length > 1;
  const groups = [
    ...categories.map(c => ({ key: c, title: c, streams: all.filter(s => categoryOf(s) === c) })),
    ...(uncategorized > 0 ? [{ key: OTHER, title: 'Without category', streams: all.filter(s => categoryOf(s) === OTHER) }] : []),
  ];

  const endDrag = () => {
    setDragging(null);
    setDropAt(null);
    setDragStream(null);
    setStreamDrop(null);
  };

  const moveCategory = async (from: string, target: string, after: boolean) => {
    endDrag();
    if (from === target) {
      return;
    }
    const next = categories.filter(c => c !== from);
    next.splice(next.indexOf(target) + (after ? 1 : 0), 0, from);
    // Keeps the place of categories this user cannot see after the others.
    const wanted = [...next, ...order.filter(c => !next.includes(c))];
    const prev = savedOrder;
    setSavedOrder(wanted);
    setOrderError('');
    try {
      setSavedOrder(await client.setCategoryOrder(wanted));
    } catch (err) {
      setSavedOrder(prev);
      setOrderError(`Category order not saved: ${describeError(err)}`);
    }
  };

  // A stream moves to another category only when the user may edit it.
  const canMoveTo = (s: Stream, group: string) => categoryOf(s) === group || (s.editable && can(s.actions, 'edit'));

  // moveStream puts a stream before or after another one, or at the end of
  // a category (anchor null), changing its category when needed.
  const moveStream = async (s: Stream, group: string, anchor: string | null, after: boolean) => {
    endDrag();
    if (anchor === s.name || !canMoveTo(s, group)) {
      return;
    }
    const seq = groups.map(g => ({ key: g.key, names: g.streams.map(x => x.name).filter(n => n !== s.name) }));
    let target = seq.find(g => g.key === group);
    if (!target) {
      target = { key: group, names: [] };
      seq.push(target);
    }
    const at = anchor === null ? -1 : target.names.indexOf(anchor);
    target.names.splice(at < 0 ? target.names.length : at + (after ? 1 : 0), 0, s.name);
    const wanted = seq.flatMap(g => g.names);
    const prev = savedStreams;
    setOrderError('');
    if (categoryOf(s) === group) {
      setSavedStreams(wanted);
      try {
        setSavedStreams(await client.setStreamOrder(wanted));
      } catch (err) {
        setSavedStreams(prev);
        setOrderError(`Stream order not saved: ${describeError(err)}`);
      }
      return;
    }
    try {
      await client.updateStream({ name: s.name, version: s.version, ...specOf(s), category: group === OTHER ? undefined : group });
    } catch (err) {
      setOrderError(`Stream ${streamLabel(s)} not moved: ${describeError(err)}`);
      return;
    }
    try {
      await client.setStreamOrder(wanted);
    } catch (err) {
      setOrderError(`Stream order not saved: ${describeError(err)}`);
    }
    reload();
  };

  // groupProps makes a category tab (both), a column header (categories)
  // or a column (streams) a drag source or drop target.
  const groupProps = (c: string, mode: 'tab' | 'header' | 'column'): DragProps | undefined => {
    if (!canCreate) {
      return undefined;
    }
    const sortable = mode !== 'column' && canOrder && c !== OTHER;
    const takesStreams = mode !== 'header';
    const catMark = dropAt?.target === c && mode !== 'column' ? `inset ${dropAt.after ? -2 : 2}px 0 0 0 ${DROP_COLOR}` : undefined;
    const streamMark = takesStreams && streamDrop?.key === GROUP + c ? `inset 0 0 0 2px ${DROP_COLOR}` : undefined;
    return {
      draggable: sortable || undefined,
      title: sortable ? 'Drag to reorder the categories' : undefined,
      style: { boxShadow: catMark ?? streamMark, opacity: sortable && dragging === c ? 0.5 : undefined },
      onDragStart: sortable
        ? e => {
            setDragging(c);
            e.dataTransfer.effectAllowed = 'move';
            // Firefox starts a drag only when some data is set.
            e.dataTransfer.setData('text/plain', c);
          }
        : undefined,
      onDragOver: e => {
        if (dragging) {
          if (!sortable || dragging === c) {
            return;
          }
          e.preventDefault();
          const r = e.currentTarget.getBoundingClientRect();
          const after = e.clientX > r.left + r.width / 2;
          if (dropAt?.target !== c || dropAt.after !== after) {
            setDropAt({ target: c, after });
          }
          return;
        }
        if (dragStream && takesStreams && canMoveTo(dragStream, c)) {
          e.preventDefault();
          e.stopPropagation();
          if (streamDrop?.key !== GROUP + c) {
            setStreamDrop({ key: GROUP + c, after: true });
          }
        }
      },
      onDrop: e => {
        if (dragging) {
          if (!sortable) {
            return;
          }
          e.preventDefault();
          if (dropAt) {
            moveCategory(dragging, dropAt.target, dropAt.after);
          } else {
            endDrag();
          }
          return;
        }
        if (dragStream && takesStreams) {
          e.preventDefault();
          e.stopPropagation();
          moveStream(dragStream, c, null, true);
        }
      },
      onDragEnd: endDrag,
    };
  };

  // streamProps lets a stream be dragged before or after another one; axis
  // is the direction of the list it is in.
  const streamProps = (s: Stream, axis: 'x' | 'y'): DragProps | undefined => {
    if (!canCreate) {
      return undefined;
    }
    const at = streamDrop?.key === s.name ? (streamDrop.after ? -2 : 2) : 0;
    return {
      draggable: true,
      style: {
        boxShadow: at === 0 ? undefined : axis === 'y' ? `inset 0 ${at}px 0 0 ${DROP_COLOR}` : `inset ${at}px 0 0 0 ${DROP_COLOR}`,
        opacity: dragStream?.name === s.name ? 0.5 : undefined,
      },
      onDragStart: e => {
        e.stopPropagation();
        // Deferred: Chrome cancels a drag when the page changes its layout
        // (the extra drop targets) inside dragstart.
        window.setTimeout(() => setDragStream(s), 0);
        e.dataTransfer.effectAllowed = 'move';
        e.dataTransfer.setData('text/plain', s.name);
      },
      onDragOver: e => {
        if (!dragStream || dragStream.name === s.name || !canMoveTo(dragStream, categoryOf(s))) {
          return;
        }
        e.preventDefault();
        e.stopPropagation();
        const r = e.currentTarget.getBoundingClientRect();
        const after = axis === 'y' ? e.clientY > r.top + r.height / 2 : e.clientX > r.left + r.width / 2;
        if (streamDrop?.key !== s.name || streamDrop.after !== after) {
          setStreamDrop({ key: s.name, after });
        }
      },
      onDrop: e => {
        if (!dragStream) {
          return;
        }
        e.preventDefault();
        e.stopPropagation();
        moveStream(dragStream, categoryOf(s), s.name, streamDrop?.key === s.name ? streamDrop.after : false);
      },
      onDragEnd: endDrag,
    };
  };

  // movable is the dragged stream when it may change its category.
  const movable = dragStream && dragStream.editable && can(dragStream.actions, 'edit') ? dragStream : null;
  const newCategoryProps: DragProps = {
    title: 'Drop a stream here to move it into a new category',
    style: {
      border: `1px dashed ${COLORS.border}`,
      borderRadius: 4,
      padding: '0.3em 0.6em',
      marginBottom: 4,
      boxShadow: streamDrop?.key === GROUP + NEW ? `inset 0 0 0 2px ${DROP_COLOR}` : undefined,
    },
    onDragOver: e => {
      if (!movable) {
        return;
      }
      e.preventDefault();
      e.stopPropagation();
      if (streamDrop?.key !== GROUP + NEW) {
        setStreamDrop({ key: GROUP + NEW, after: true });
      }
    },
    onDrop: e => {
      if (!movable) {
        return;
      }
      e.preventDefault();
      e.stopPropagation();
      const s = movable;
      endDrag();
      const c = window.prompt(`New category for ${streamLabel(s)}`, '')?.trim();
      if (!c) {
        return;
      }
      if (c.length > MAX_CATEGORY) {
        setOrderError(`Category name is longer than ${MAX_CATEGORY} characters.`);
        return;
      }
      moveStream(s, c, null, true);
    },
    onDragEnd: endDrag,
  };

  const columns = grouped && !current && categories.length > 0;

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '1em' }}>
        <Muted>Pipelines of several connections, run in order with shared params.</Muted>
        <div style={{ flex: 1 }} />
        {canCreate && (
          <>
            <button className='argo-button argo-button--base-o' onClick={() => setImporting(v => !v)}>
              <i className='fa fa-file-import' /> Import
            </button>
            <button className='argo-button argo-button--base' onClick={() => navigate({ view: 'streams', mode: 'new' })}>
              <i className='fa fa-plus' /> New stream
            </button>
          </>
        )}
        <div style={{ display: 'flex', gap: '0.2em' }}>
          <LayoutButton value='cards' current={layout} icon='fa fa-th-large' title='Cards' onPick={pickLayout} />
          <LayoutButton value='rows' current={layout} icon='fa fa-list' title='Compact list' onPick={pickLayout} />
          {categories.length > 0 && (
            <button
              className={grouped ? 'argo-button argo-button--base' : 'argo-button argo-button--base-o'}
              style={{ padding: '0 0.7em' }}
              title='Columns by category on the All tab'
              onClick={toggleGrouped}>
              <i className='fa fa-columns' />
            </button>
          )}
        </div>
        <button className='argo-button argo-button--base-o' onClick={reload}>
          <i className='fa fa-redo' /> Refresh
        </button>
      </div>
      {importing && <ImportPanel client={client} onClose={() => setImporting(false)} />}
      {list.state === 'loading' && <Muted>Loading streams...</Muted>}
      {list.state === 'error' && <ErrorText text={list.error} />}
      {list.state === 'ok' && all.length === 0 && (
        <div className='white-box'>
          <div className='white-box__details'>
            {canCreate
              ? 'No streams yet. Use "New stream" to chain pipelines of your connections.'
              : 'No streams are shared with you. Ask a Zea admin for access.'}
          </div>
        </div>
      )}
      {(categories.length > 0 || movable) && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0 1.5em', borderBottom: `1px solid ${COLORS.border}`, marginBottom: '1em' }}>
          <CategoryTab active={!current} onClick={() => navigate({ view: 'streams' })}>
            All
          </CategoryTab>
          {categories.map(c => (
            <CategoryTab key={c} active={current === c} drag={groupProps(c, 'tab')} onClick={() => navigate({ view: 'streams', tab: c })}>
              {c}
            </CategoryTab>
          ))}
          {uncategorized > 0 && (
            <CategoryTab active={current === OTHER} drag={groupProps(OTHER, 'tab')} onClick={() => navigate({ view: 'streams', tab: OTHER })}>
              Without category
            </CategoryTab>
          )}
          {movable && uncategorized === 0 && categoryOf(movable) !== OTHER && (
            <CategoryTab active={false} drag={groupProps(OTHER, 'tab')} onClick={() => undefined}>
              Without category
            </CategoryTab>
          )}
          {movable && (
            <CategoryTab active={false} drag={newCategoryProps} onClick={() => undefined}>
              <i className='fa fa-plus' /> New category
            </CategoryTab>
          )}
        </div>
      )}
      {orderError && <ErrorText text={orderError} />}
      {all.length > 0 && columns && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))', gap: '1.5em 1em', alignItems: 'start' }}>
          {groups.map(g => {
            const drag = groupProps(g.key, 'header');
            const column = groupProps(g.key, 'column');
            return (
              <div key={g.key} {...column} style={{ minWidth: 0, borderRadius: 4, ...column?.style }}>
                <div
                  {...drag}
                  style={{
                    display: 'flex',
                    alignItems: 'baseline',
                    padding: '0.2em 0.3em',
                    marginBottom: '0.5em',
                    borderBottom: `1px solid ${COLORS.border}`,
                    ...drag?.style,
                  }}>
                  <a style={{ cursor: 'pointer', fontWeight: 600, ...ellipsis }} onClick={() => navigate({ view: 'streams', tab: g.key })}>
                    {g.title}
                  </a>
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: layout === 'rows' ? '0.4em' : '0.6em' }}>
                  {g.streams.map(s => (
                    <StreamCard key={s.name} client={client} stream={s} compact={layout === 'rows'} narrow drag={streamProps(s, 'y')} />
                  ))}
                </div>
              </div>
            );
          })}
        </div>
      )}
      {all.length > 0 && !columns && (
        <div
          style={
            layout === 'rows'
              ? { display: 'flex', flexDirection: 'column', gap: '0.4em' }
              : { display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: '1em' }
          }>
          {shown.map(s => (
            <StreamCard key={s.name} client={client} stream={s} compact={layout === 'rows'} drag={streamProps(s, layout === 'rows' ? 'y' : 'x')} />
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
        <b style={{ fontSize: '1.1em' }} title={st.state === 'ok' && st.data.label?.trim() ? name : undefined}>
          {st.state === 'ok' ? streamLabel(st.data) : name}
        </b>
        {st.state === 'ok' && <StreamBadges stream={st.data} />}
        <div style={{ flex: 1 }} />
        {st.state === 'ok' && (
          <>
            {can(st.data.actions, 'run') && (
              <button
                className='argo-button argo-button--base'
                disabled={busy || st.data.problems.length > 0}
                title={st.data.problems.length > 0 ? 'Fix the problems first' : 'Start this stream'}
                onClick={() => setPanel(panel === 'run' ? 'none' : 'run')}>
                <i className='fa fa-play' /> Run
              </button>
            )}
            {can(st.data.actions, 'edit') && st.data.editable && (
              <button className='argo-button argo-button--base-o' disabled={busy} onClick={() => navigate({ view: 'streams', stream: name, mode: 'edit' })}>
                <i className='fa fa-pencil-alt' /> Edit
              </button>
            )}
            {can(me.permissions.streams, 'edit') && (
              <button className='argo-button argo-button--base-o' disabled={busy} onClick={draft} title='Copy into an editable draft'>
                <i className='fa fa-copy' /> Edit as draft
              </button>
            )}
            {can(me.permissions.streams, 'edit') && (
              <button
                className='argo-button argo-button--base-o'
                disabled={busy}
                onClick={() => navigate({ view: 'streams', mode: 'new', from: name })}
                title='Open a copy in the editor as a new stream'>
                <i className='fa fa-clone' /> Clone
              </button>
            )}
            <button className='argo-button argo-button--base-o' disabled={busy} onClick={() => setPanel(panel === 'export' ? 'none' : 'export')}>
              <i className='fa fa-file-export' /> Export
            </button>
            {can(st.data.actions, 'edit') && st.data.editable && (
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
          {panel === 'run' && can(st.data.actions, 'run') && (
            <StreamRunForm
              client={client}
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

type Initial = { name: string; spec: StreamSpec; note: string };

// EditorPage edits a stream (name), or a new one: empty, a clone of from,
// or the pending import.
const EditorPage = ({ client, data, name, from, imported }: { client: ZeaClient; data: ConnData; name?: string; from?: string; imported?: boolean }) => {
  const [loaded] = useLoad(
    async (): Promise<{ stream?: Stream; initial?: Initial; others: Stream[]; connections: Connection[]; categories: string[] }> => {
      const [stream, source, connections, streams] = await Promise.all([
        name ? client.stream(name) : Promise.resolve(undefined),
        from ? client.stream(from) : Promise.resolve(undefined),
        client.connections(),
        // Only offers existing categories; the editor works without them.
        client.streams().then(
          l => l.streams,
          () => [] as Stream[],
        ),
      ]);
      const categories = [...new Set(streams.map(s => s.category?.trim() ?? '').filter(c => c !== ''))].sort((a, b) => a.localeCompare(b));
      let initial: Initial | undefined;
      if (source) {
        const spec = specOf(source);
        const label = source.label?.trim();
        initial = {
          name: freeName(source.name, new Set(streams.map(s => s.name))),
          spec: { ...spec, label: label ? `${label} (copy)`.slice(0, 100) : undefined },
          note: `clone of ${source.name}`,
        };
      } else if (imported) {
        const parsed = readImport();
        if (!parsed) {
          throw new Error('The imported stream is no longer available; import it again.');
        }
        initial = { name: parsed.name, spec: parsed, note: 'imported' };
      }
      const others = streams.filter(s => s.name !== name);
      // A stream can only use connections the user may run.
      return { stream, initial, others, connections: connections.filter(c => can(c.actions, 'run')), categories };
    },
    [client, name, from, imported],
  );
  if (loaded.state === 'loading') {
    return <Muted>Loading...</Muted>;
  }
  if (loaded.state === 'error') {
    return <ErrorText text={loaded.error} />;
  }
  const { stream, initial, others, connections, categories } = loaded.data;
  if (stream && !can(stream.actions, 'edit')) {
    return (
      <>
        <button className='argo-button argo-button--base-o' onClick={() => navigate({ view: 'streams', stream: stream.name })}>
          <i className='fa fa-arrow-left' /> {streamLabel(stream)}
        </button>
        <ErrorText text='You have no edit access to this stream.' />
      </>
    );
  }
  if (stream && !stream.editable) {
    return (
      <>
        <button className='argo-button argo-button--base-o' onClick={() => navigate({ view: 'streams', stream: stream.name })}>
          <i className='fa fa-arrow-left' /> {streamLabel(stream)}
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
      initial={initial}
      categories={categories}
      others={others}
      onSaved={(s, created) => {
        if (created) {
          if (imported) {
            dropImport();
          }
          navigate({ view: 'streams', stream: s.name, mode: 'edit' });
        }
      }}
      onClose={() => {
        if (imported) {
          dropImport();
        }
        navigate(stream ? { view: 'streams', stream: stream.name } : from ? { view: 'streams', stream: from } : { view: 'streams' });
      }}
    />
  );
};

export const StreamsView = ({ client, me }: { client: ZeaClient; me: Me }) => {
  const route = useRoute();
  const data = React.useMemo(() => new ConnData(client), [client]);
  const canCreate = can(me.permissions.streams, 'edit');
  if (canCreate && route.mode === 'new') {
    return <EditorPage key={`new:${route.from ?? ''}`} client={client} data={data} from={route.from} />;
  }
  if (canCreate && route.mode === 'import') {
    return <EditorPage key='import' client={client} data={data} imported />;
  }
  if (route.stream) {
    if (canCreate && route.mode === 'edit') {
      return <EditorPage key={route.stream} client={client} data={data} name={route.stream} />;
    }
    if (route.srun) {
      return <StreamRunView client={client} data={data} me={me} stream={route.stream} runID={route.srun} />;
    }
    return <StreamDetail key={route.stream} client={client} data={data} me={me} name={route.stream} openRun={route.mode === 'run'} />;
  }
  return <StreamList client={client} me={me} tab={route.tab} />;
};
