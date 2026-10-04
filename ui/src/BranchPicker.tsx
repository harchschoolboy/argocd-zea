import * as React from 'react';
import { Branch } from './types';
import { COLORS } from './ui';

interface Props {
  value: string;
  branches: Branch[];
  defaultBranch?: string;
  disabled?: boolean;
  placeholder?: string;
  onChange: (value: string) => void;
}

// BranchPicker is a combobox: opening it lists every branch, typing filters
// the list, and any text is still accepted. A native <datalist> is not used
// because browsers filter it by the current value, so a selected branch
// hides all the others.
export const BranchPicker = ({ value, branches, defaultBranch, disabled, placeholder, onChange }: Props) => {
  const [open, setOpen] = React.useState(false);
  // null shows all branches; a string is what the user typed since opening.
  const [filter, setFilter] = React.useState<string | null>(null);
  const [active, setActive] = React.useState(-1);
  const inputRef = React.useRef<HTMLInputElement>(null);
  const listRef = React.useRef<HTMLDivElement>(null);

  const query = (filter ?? '').trim().toLowerCase();
  const items = query ? branches.filter(b => b.name.toLowerCase().includes(query)) : branches;

  React.useEffect(() => {
    if (open && active >= 0) {
      (listRef.current?.children[active] as HTMLElement | undefined)?.scrollIntoView({ block: 'nearest' });
    }
  }, [open, active]);

  const show = (f: string | null) => {
    setFilter(f);
    setActive(-1);
    setOpen(true);
  };

  const pick = (name: string) => {
    onChange(name);
    setOpen(false);
    setFilter(null);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        if (!open) {
          show(null);
        } else {
          setActive(a => Math.min(a + 1, items.length - 1));
        }
        break;
      case 'ArrowUp':
        e.preventDefault();
        setActive(a => Math.max(a - 1, 0));
        break;
      case 'Enter':
        if (open && active >= 0 && active < items.length) {
          e.preventDefault();
          pick(items[active].name);
        } else {
          setOpen(false);
        }
        break;
      case 'Escape':
        setOpen(false);
        break;
    }
  };

  return (
    <div style={{ position: 'relative', flex: 1 }}>
      <input
        ref={inputRef}
        className='argo-field'
        style={{ width: '100%', paddingRight: '1.8em' }}
        value={value}
        disabled={disabled}
        placeholder={placeholder}
        role='combobox'
        aria-expanded={open}
        autoComplete='off'
        spellCheck={false}
        onFocus={e => {
          e.target.select();
          show(null);
        }}
        onBlur={() => setOpen(false)}
        onClick={() => {
          if (!open) {
            show(null);
          }
        }}
        onChange={e => {
          onChange(e.target.value);
          show(e.target.value);
        }}
        onKeyDown={onKeyDown}
      />
      <i
        className={`fa fa-caret-${open ? 'up' : 'down'}`}
        style={{
          position: 'absolute',
          right: '0.5em',
          top: '50%',
          transform: 'translateY(-50%)',
          cursor: disabled ? 'default' : 'pointer',
          color: COLORS.muted,
        }}
        onMouseDown={e => {
          // Keep focus in the input so onBlur does not close the list.
          e.preventDefault();
          if (disabled) {
            return;
          }
          if (open) {
            setOpen(false);
          } else {
            inputRef.current?.focus();
            show(null);
          }
        }}
      />
      {open && !disabled && (
        <div
          ref={listRef}
          role='listbox'
          className='white-box'
          style={{
            position: 'absolute',
            top: '100%',
            left: 0,
            right: 0,
            zIndex: 20,
            margin: '2px 0 0',
            padding: '0.3em 0',
            maxHeight: 280,
            overflowY: 'auto',
            boxShadow: '0 4px 12px rgba(0, 0, 0, 0.35)',
          }}>
          {items.length === 0 && (
            <div style={{ padding: '0.4em 0.8em', color: COLORS.muted }}>
              No matching branches. The typed name is used as is.
            </div>
          )}
          {items.map((b, i) => (
            <div
              key={b.name}
              role='option'
              aria-selected={b.name === value}
              onMouseDown={e => {
                e.preventDefault();
                pick(b.name);
              }}
              onMouseEnter={() => setActive(i)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '0.5em',
                padding: '0.4em 0.8em',
                cursor: 'pointer',
                background: i === active ? 'rgba(128, 128, 128, 0.2)' : undefined,
                fontWeight: b.name === value ? 600 : undefined,
              }}>
              <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {b.name}
              </span>
              {b.name === defaultBranch && <span style={{ fontSize: '0.8em', color: COLORS.muted }}>default</span>}
              {b.protected && <i className='fa fa-lock' title='Protected' style={{ color: COLORS.muted }} />}
            </div>
          ))}
        </div>
      )}
    </div>
  );
};
