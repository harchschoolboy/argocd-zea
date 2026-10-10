import * as React from 'react';
import { describeError, ZeaClient } from './api';
import { inputStyle } from './forms';
import { ago } from './Runs';
import { ImageRepository, ImageSource, ImagesResult, Registry } from './types';
import { COLORS, ErrorText, Load, Muted, useLoad } from './ui';

const mono: React.CSSProperties = { fontFamily: 'monospace' };

export function formatSize(bytes?: number): string {
  if (!bytes) {
    return '';
  }
  const units = ['B', 'KB', 'MB', 'GB'];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

const shortDigest = (d?: string) => (d ? d.replace(/^sha256:/, '').slice(0, 12) : '');

export const CopyButton = ({ text, title }: { text: string; title?: string }) => {
  const [copied, setCopied] = React.useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      window.prompt('Copy:', text);
    }
  };
  return (
    <button
      type='button'
      className='argo-button argo-button--base-o'
      style={{ flexShrink: 0, padding: '0 0.6em' }}
      title={title ?? `Copy ${text}`}
      onClick={copy}>
      <i className={copied ? 'fa fa-check' : 'fa fa-copy'} />
    </button>
  );
};

const SourceErrors = ({ result }: { result: ImagesResult }) => (
  <>
    {(result.errors ?? []).map((e, i) => (
      <ErrorText key={i} text={e.source > 0 ? `Image source ${e.source} (${e.registry}): ${e.error}` : e.error} />
    ))}
    {result.truncated && (
      <Muted>
        Showing the first {result.repositories.length} repositories. Make the repository pattern more specific.
      </Muted>
    )}
  </>
);

interface EditorProps {
  client: ZeaClient;
  value: ImageSource[];
  onChange: (v: ImageSource[]) => void;
  registries: Load<Registry[]>;
}

// ImageSourcesEditor edits the image sources of a Connection in its form.
export const ImageSourcesEditor = ({ client, value, onChange, registries }: EditorProps) => {
  const [previewRef, setPreviewRef] = React.useState('');
  const [preview, setPreview] = React.useState<{ busy: boolean; result?: ImagesResult; error?: string }>({ busy: false });
  const regs = registries.state === 'ok' ? registries.data : [];

  const update = (i: number, patch: Partial<ImageSource>) => onChange(value.map((s, j) => (j === i ? { ...s, ...patch } : s)));

  const add = () => onChange([...value, { registry: regs[0]?.name ?? '', repository: '', tags: '' }]);

  const runPreview = async () => {
    setPreview({ busy: true });
    try {
      setPreview({ busy: false, result: await client.previewImages(cleanSources(value), previewRef.trim()) });
    } catch (err) {
      setPreview({ busy: false, error: describeError(err) });
    }
  };

  return (
    <div style={{ marginBottom: '1em' }}>
      <label style={{ display: 'block', fontWeight: 600, marginBottom: '0.3em' }}>Images</label>
      <div style={{ fontSize: '0.85em', color: COLORS.muted, marginBottom: '0.5em' }}>
        Regular expressions that select the images built from this repository. A named group <code>(?P&lt;branch&gt;...)</code> in
        the repository or tag pattern ties images to a branch; <code>(?P&lt;sha&gt;...)</code> in the tag pattern links tags to
        commits. Tags that look like a commit SHA are linked automatically.
      </div>
      {registries.state === 'error' && <ErrorText text={registries.error} />}
      {registries.state === 'ok' && regs.length === 0 && (
        <Muted>No registries yet. Add one under Registries first.</Muted>
      )}
      {value.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(8em, 1fr) 2fr 1.5fr auto', gap: '0.4em 0.5em', alignItems: 'center' }}>
          <Muted>Registry</Muted>
          <Muted>Repository pattern</Muted>
          <Muted>Tag pattern (optional)</Muted>
          <span />
          {value.map((s, i) => (
            <React.Fragment key={i}>
              <select className='argo-field' style={inputStyle} value={s.registry} onChange={e => update(i, { registry: e.target.value })}>
                {!regs.some(r => r.name === s.registry) && <option value={s.registry}>{s.registry || '(select)'}</option>}
                {regs.map(r => (
                  <option key={r.name} value={r.name}>
                    {r.name}
                  </option>
                ))}
              </select>
              <input
                className='argo-field'
                style={{ ...inputStyle, ...mono }}
                value={s.repository}
                placeholder='^myapp-(?P<branch>.+)$'
                spellCheck={false}
                required
                onChange={e => update(i, { repository: e.target.value })}
              />
              <input
                className='argo-field'
                style={{ ...inputStyle, ...mono }}
                value={s.tags ?? ''}
                placeholder='^[0-9a-f]{7}$'
                spellCheck={false}
                onChange={e => update(i, { tags: e.target.value })}
              />
              <button
                type='button'
                className='argo-button argo-button--base-o'
                title='Remove image source'
                onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <i className='fa fa-times' />
              </button>
            </React.Fragment>
          ))}
        </div>
      )}
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginTop: '0.5em', flexWrap: 'wrap' }}>
        <button type='button' className='argo-button argo-button--base-o' onClick={add} disabled={regs.length === 0}>
          <i className='fa fa-plus' /> Add image source
        </button>
        {value.length > 0 && (
          <>
            <span style={{ flex: 1 }} />
            <input
              className='argo-field'
              style={{ width: '14em' }}
              value={previewRef}
              placeholder='Branch (empty = all)'
              onChange={e => setPreviewRef(e.target.value)}
              onKeyDown={e => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  runPreview();
                }
              }}
            />
            <button type='button' className='argo-button argo-button--base-o' onClick={runPreview} disabled={preview.busy}>
              {preview.busy ? 'Loading...' : 'Preview'}
            </button>
          </>
        )}
      </div>
      {preview.error && <ErrorText text={preview.error} />}
      {preview.result && (
        <div style={{ marginTop: '0.5em', padding: '0.5em 0.8em', background: COLORS.surface, borderRadius: 4 }}>
          <SourceErrors result={preview.result} />
          {preview.result.repositories.length === 0 && !(preview.result.errors ?? []).length && <Muted>No images match.</Muted>}
          {preview.result.repositories.map(r => (
            <div key={r.image} style={{ padding: '0.2em 0', wordBreak: 'break-all' }}>
              <span style={mono}>{r.image}</span>
              {r.branch && <BranchTag branch={r.branch} />}{' '}
              <Muted>
                {r.error ? '' : `${r.tagCount} tag${r.tagCount === 1 ? '' : 's'}`}
                {r.tags.length > 0 && `: ${r.tags.map(t => t.name).join(', ')}${r.tagCount > r.tags.length ? ', ...' : ''}`}
              </Muted>
              {r.error && <ErrorText text={r.error} />}
            </div>
          ))}
        </div>
      )}
    </div>
  );
};

// cleanSources trims the patterns and drops the empty optional tag pattern.
export function cleanSources(sources: ImageSource[]): ImageSource[] {
  return sources.map(s => {
    const out: ImageSource = { registry: s.registry.trim(), repository: s.repository.trim() };
    const tags = (s.tags ?? '').trim();
    if (tags) {
      out.tags = tags;
    }
    return out;
  });
}

const BranchTag = ({ branch }: { branch: string }) => (
  <span
    title='Branch'
    style={{
      marginLeft: '0.5em',
      padding: '0 0.4em',
      border: `1px solid ${COLORS.border}`,
      borderRadius: 3,
      fontSize: '0.85em',
      whiteSpace: 'nowrap',
    }}>
    <i className='fa fa-code-branch' /> {branch}
  </span>
);

interface PanelProps {
  client: ZeaClient;
  connection: string;
  gitRef: string;
  // False while the selected branch is still being resolved.
  ready: boolean;
  configured: boolean;
  canEdit: boolean;
}

// ImagesPanel lists the images of a Connection for the selected branch.
export const ImagesPanel = ({ client, connection, gitRef, ready, configured, canEdit }: PanelProps) => {
  const [allBranches, setAllBranches] = React.useState(false);
  const refresh = React.useRef(false);
  const ref = allBranches ? '' : gitRef;
  const waiting = !allBranches && !ready;
  const [images, reload] = useLoad(() => {
    if (!configured || waiting) {
      return Promise.resolve(null);
    }
    const force = refresh.current;
    refresh.current = false;
    return client.images(connection, { ref, refresh: force });
  }, [client, connection, ref, configured, waiting]);

  if (!configured) {
    return (
      <Muted>
        No images configured for this connection.
        {canEdit && ' Add a registry under Registries, then add image sources with Edit.'}
      </Muted>
    );
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '1em', margin: '0.5em 0' }}>
        <label style={{ cursor: 'pointer' }}>
          <input type='checkbox' checked={allBranches} onChange={e => setAllBranches(e.target.checked)} /> All branches
        </label>
        <span style={{ flex: 1 }} />
        <button
          className='argo-button argo-button--base-o'
          title='Reload from the registry'
          disabled={images.state === 'loading'}
          onClick={() => {
            refresh.current = true;
            reload();
          }}>
          <i className='fa fa-redo' />
        </button>
      </div>
      {(images.state === 'loading' || waiting) && <Muted>Loading images...</Muted>}
      {images.state === 'error' && <ErrorText text={images.error} />}
      {images.state === 'ok' && images.data && (
        <>
          <SourceErrors result={images.data} />
          {images.data.repositories.length === 0 && !(images.data.errors ?? []).length && (
            <Muted>{ref ? `No images for branch ${ref}.` : 'No images found.'}</Muted>
          )}
          {images.data.repositories.map(r => (
            <RepositoryBlock key={r.image} repo={r} />
          ))}
        </>
      )}
    </div>
  );
};

const RepositoryBlock = ({ repo }: { repo: ImageRepository }) => (
  <div style={{ borderTop: `1px solid ${COLORS.border}`, padding: '0.5em 0' }}>
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.3em', flexWrap: 'wrap' }}>
      <i className='fa fa-box' style={{ color: COLORS.muted }} />
      <b style={{ ...mono, wordBreak: 'break-all' }}>{repo.name}</b>
      {repo.branch && <BranchTag branch={repo.branch} />}
      <span style={{ flex: 1 }} />
      <Muted>
        {repo.registry}
        {!repo.error && ` - ${repo.tagCount} tag${repo.tagCount === 1 ? '' : 's'}`}
      </Muted>
    </div>
    {repo.error && <ErrorText text={repo.error} />}
    {repo.tags.map(t => (
      <div
        key={t.name}
        style={{ display: 'flex', alignItems: 'center', gap: '0.8em', padding: '0.25em 0 0.25em 1.3em', minWidth: 0 }}>
        {t.commitURL && t.commit && t.name.includes(t.commit.slice(0, 7)) ? (
          <a
            href={t.commitURL}
            target='_blank'
            rel='noopener noreferrer'
            title={`${t.image}\nOpen commit ${t.commit}`}
            style={{ ...mono, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {t.name}
          </a>
        ) : (
          <code style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={t.image}>
            {t.name}
          </code>
        )}
        <span style={{ flex: 1 }} />
        {t.commit && t.commitURL && !t.name.includes(t.commit.slice(0, 7)) && (
          <a href={t.commitURL} target='_blank' rel='noopener noreferrer' title={`Commit ${t.commit}`} style={{ whiteSpace: 'nowrap' }}>
            <i className='fa fa-code-commit' /> {t.commit.slice(0, 7)}
          </a>
        )}
        {t.digest && (
          <span title={t.digest} style={{ ...mono, color: COLORS.muted, fontSize: '0.85em' }}>
            {shortDigest(t.digest)}
          </span>
        )}
        {t.sizeBytes ? <Muted>{formatSize(t.sizeBytes)}</Muted> : null}
        <span style={{ color: COLORS.muted, whiteSpace: 'nowrap', minWidth: '5em', textAlign: 'right' }} title={t.pushedAt}>
          {ago(t.pushedAt)}
        </span>
        <CopyButton text={t.image} title={`Copy ${t.image}`} />
      </div>
    ))}
    {repo.tagCount > repo.tags.length && (
      <div style={{ paddingLeft: '1.3em' }}>
        <Muted>
          and {repo.tagCount - repo.tags.length} older tag{repo.tagCount - repo.tags.length === 1 ? '' : 's'}
        </Muted>
      </div>
    )}
  </div>
);
