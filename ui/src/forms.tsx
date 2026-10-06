import * as React from 'react';
import { CredentialMode } from './types';
import { COLORS } from './ui';

export const Row = ({ label, help, children }: { label: string; help?: string; children: React.ReactNode }) => (
  <div className='argo-form-row' style={{ marginBottom: '1em' }}>
    <label style={{ display: 'block', fontWeight: 600, marginBottom: '0.3em' }}>{label}</label>
    {children}
    {help && <div style={{ fontSize: '0.85em', color: COLORS.muted, marginTop: '0.2em' }}>{help}</div>}
  </div>
);

export const inputStyle: React.CSSProperties = { width: '100%', boxSizing: 'border-box' };

// pickMode returns the credential mode whose fields are all stored. A mode
// without fields (anonymous) is chosen only when nothing else matches.
export function pickMode(modes: CredentialMode[], storedKeys: string[] = []): string {
  if (modes.length === 0) {
    return '';
  }
  const keys = new Set(storedKeys);
  const set = modes.find(m => m.fields.length > 0 && m.fields.every(f => keys.has(f.key)));
  const empty = storedKeys.length === 0 ? modes.find(m => m.fields.length === 0) : undefined;
  return (set ?? empty ?? modes[0]).id;
}

// buildCredentials returns the credential changes for a save or test: values
// of the selected mode (empty keeps the stored one) and "-" for stored keys of
// the other modes, so switching modes drops the old credentials.
export function buildCredentials(
  modes: CredentialMode[],
  modeID: string,
  values: Record<string, string>,
  storedKeys: string[] = [],
): Record<string, string> {
  const stored = new Set(storedKeys);
  const selected = new Set((modes.find(m => m.id === modeID)?.fields ?? []).map(f => f.key));
  const out: Record<string, string> = {};
  for (const m of modes) {
    for (const f of m.fields) {
      if (selected.has(f.key)) {
        out[f.key] = values[f.key] ?? '';
      } else if (stored.has(f.key)) {
        out[f.key] = '-';
      }
    }
  }
  return out;
}

interface CredentialsEditorProps {
  modes: CredentialMode[];
  modeID: string;
  onModeChange: (id: string) => void;
  values: Record<string, string>;
  onChange: (values: Record<string, string>) => void;
  storedKeys?: string[];
}

export const CredentialsEditor = ({ modes, modeID, onModeChange, values, onChange, storedKeys = [] }: CredentialsEditorProps) => {
  const mode = modes.find(m => m.id === modeID);
  const stored = new Set(storedKeys);
  return (
    <>
      {modes.length > 1 && (
        <Row label='Authentication'>
          <div style={{ display: 'flex', gap: '1.5em', flexWrap: 'wrap' }}>
            {modes.map(m => (
              <label key={m.id} style={{ cursor: 'pointer' }}>
                <input type='radio' checked={modeID === m.id} onChange={() => onModeChange(m.id)} /> {m.label}
              </label>
            ))}
          </div>
        </Row>
      )}
      {mode?.help && <div style={{ color: COLORS.muted, marginBottom: '0.8em' }}>{mode.help}</div>}
      {mode?.fields.map(f => {
        const common = {
          className: 'argo-field',
          style: inputStyle,
          value: values[f.key] ?? '',
          placeholder: stored.has(f.key) ? '(stored - leave empty to keep)' : '',
          autoComplete: 'off',
          onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
            onChange({ ...values, [f.key]: e.target.value }),
        };
        return (
          <Row key={f.key} label={f.label} help={f.help}>
            {f.options ? (
              <select {...common} onChange={e => onChange({ ...values, [f.key]: e.target.value })}>
                <option value=''>{stored.has(f.key) ? '(stored - keep)' : 'Select...'}</option>
                {f.options.map(o => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </select>
            ) : f.multiline ? (
              <textarea {...common} rows={6} spellCheck={false} style={{ ...inputStyle, fontFamily: 'monospace' }} />
            ) : (
              <input {...common} type={f.secret ? 'password' : 'text'} />
            )}
          </Row>
        );
      })}
    </>
  );
};
