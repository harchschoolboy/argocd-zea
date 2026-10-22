import * as React from 'react';
import { describeError, fetchArgoApps, ZeaClient } from './api';
import { isBranchInput } from './RunForm';
import {
  Badge,
  ellipsis,
  Field,
  Help,
  KeyValueEditor,
  PipelineName,
  ProblemList,
  RefSelect,
  useDebounced,
  ValueField,
} from './StreamFields';
import {
  allSteps,
  APPROVAL,
  ARGOCD,
  boolValues,
  ConnData,
  DEFAULT_APPROVAL_TIMEOUT,
  DEFAULT_ARGO_TIMEOUT,
  emptySpec,
  insertStep,
  isApproval,
  isArgoCD,
  isTemplate,
  MAX_STREAM_NAME,
  moveStage,
  moveStep,
  PARAM_NAME_RE,
  paramFromInput,
  paramRef,
  positionOf,
  previousStageSteps,
  referencesFor,
  stepValueRefs,
  removeStep,
  renameParam,
  renameStep,
  replaceStep,
  setKey,
  specOf,
  STEP_ID_RE,
  StepPos,
  stepProblems,
  STREAM_NAME_RE,
  uniqueStepId,
} from './streamModel';
import { Connection, RunInput, Stream, StreamParam, StreamProblem, StreamSpec, StreamStep } from './types';
import { COLORS, ErrorText, Muted, ProviderBadge, useLoad } from './ui';

const ACCENT = '#0dadea';
const COLUMN_WIDTH = 250;

// A source drag adds a new step: a pipeline of a connection, an approval or
// an Argo CD sync.
type Source = { kind: 'conn'; name: string } | { kind: 'approval' } | { kind: 'argocd' };

const isSource = (d: { kind: string }): d is Source => d.kind === 'conn' || d.kind === 'approval' || d.kind === 'argocd';

// Steps that are not pipelines, offered above the connections.
const CONTROL_STEPS: { kind: 'approval' | 'argocd'; label: string; icon: string; color: string; title: string }[] = [
  { kind: 'approval', label: 'Approval', icon: 'fa fa-user-check', color: COLORS.warn, title: 'Pauses the run until someone approves' },
  {
    kind: 'argocd',
    label: 'Argo CD sync',
    icon: 'fa fa-sync',
    color: '#0dadea',
    title: 'Waits until an Argo CD application is synced (optionally to an image) and healthy',
  },
];
type Drag = Source | { kind: 'step'; from: StepPos } | { kind: 'stage'; from: number };
// A step drop with stage equal to the stage count creates a new stage.
type Drop = { kind: 'step'; stage: number; index: number } | { kind: 'stage'; index: number };

const sameDrop = (a: Drop | null, b: Drop | null) => JSON.stringify(a) === JSON.stringify(b);


interface StepCardProps {
  data: ConnData;
  step: StreamStep;
  problems: StreamProblem[];
  selected: boolean;
  onSelect: () => void;
  onDragStart: (e: React.DragEvent) => void;
  onDragEnd: () => void;
  onDragOver: (e: React.DragEvent) => void;
}

const StepCard = ({ data, step, problems, selected, onSelect, onDragStart, onDragEnd, onDragOver }: StepCardProps) => (
  <div
    draggable
    onDragStart={onDragStart}
    onDragEnd={onDragEnd}
    onDragOver={onDragOver}
    onClick={onSelect}
    style={{
      border: `1px solid ${selected ? ACCENT : problems.length > 0 ? COLORS.error : COLORS.border}`,
      background: selected ? 'rgba(13, 173, 234, 0.08)' : 'rgba(128, 128, 128, 0.06)',
      borderRadius: 4,
      padding: '0.4em 0.55em',
      cursor: 'grab',
      userSelect: 'none',
    }}>
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.4em' }}>
      <code style={{ ...ellipsis, flex: 1 }}>{step.id || '(no id)'}</code>
      {problems.length > 0 && (
        <span style={{ color: COLORS.error, whiteSpace: 'nowrap' }} title={problems.map(p => p.message).join('\n')}>
          <i className='fa fa-exclamation-circle' /> {problems.length}
        </span>
      )}
    </div>
    {isApproval(step) ? (
      <>
        <div style={{ ...ellipsis, fontWeight: 600, margin: '0.15em 0' }}>
          <i className='fa fa-user-check' style={{ color: COLORS.warn }} /> {step.name || 'Approval'}
        </div>
        <div style={{ ...ellipsis, fontSize: '0.85em', color: COLORS.muted }} title={step.message}>
          {step.message || 'waits for approval'}
        </div>
      </>
    ) : isArgoCD(step) ? (
      <>
        <div style={{ ...ellipsis, fontWeight: 600, margin: '0.15em 0' }}>
          <i className='fa fa-sync' style={{ color: '#0dadea' }} /> {step.name || 'Argo CD sync'}
        </div>
        <div style={{ ...ellipsis, fontSize: '0.85em', color: COLORS.muted }} title={[step.app, step.image].filter(Boolean).join('\n')}>
          {step.app || '(no application)'}
          {step.image && (
            <>
              {' · '}
              <i className='fa fa-cube' /> {step.image}
            </>
          )}
        </div>
      </>
    ) : (
      <>
        <div style={{ ...ellipsis, fontWeight: 600, margin: '0.15em 0' }}>
          {step.name || <PipelineName data={data} connection={step.connection} id={step.pipeline} />}
        </div>
        <div style={{ ...ellipsis, fontSize: '0.85em', color: COLORS.muted }}>
          {step.connection || '(no connection)'}
          {' · '}
          <i className='fa fa-code-branch' /> {step.ref || 'no ref'}
        </div>
      </>
    )}
    {(step.when && step.when !== 'success') ||
    (step.needs?.length ?? 0) > 0 ||
    step.continueOnError ||
    step.timeout ||
    step.retries ||
    step.noSelfApproval ||
    step.noRefresh ? (
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25em', marginTop: '0.3em' }}>
        {step.when && step.when !== 'success' && <Badge color={COLORS.warn}>on {step.when}</Badge>}
        {(step.needs?.length ?? 0) > 0 && <Badge title='Waits only for these steps'>needs {step.needs?.join(', ')}</Badge>}
        {step.continueOnError && <Badge>continue on error</Badge>}
        {step.timeout && <Badge>timeout {step.timeout}</Badge>}
        {!!step.retries && <Badge title='Automatic retries of a failed pipeline'>retry x{step.retries}</Badge>}
        {step.noSelfApproval && <Badge title='The user who started the run may not decide'>no self-approval</Badge>}
        {step.noRefresh && <Badge title='Argo CD is not asked to refresh the application'>no refresh</Badge>}
      </div>
    ) : null}
  </div>
);

const DropBar = ({ vertical }: { vertical?: boolean }) => (
  <div
    style={
      vertical
        ? { flex: '0 0 4px', alignSelf: 'stretch', background: ACCENT, borderRadius: 2 }
        : { height: 4, background: ACCENT, borderRadius: 2, margin: '2px 0' }
    }
  />
);

interface StepPanelProps {
  data: ConnData;
  spec: StreamSpec;
  step: StreamStep;
  pos: StepPos;
  connections: Connection[];
  problems: StreamProblem[];
  readOnly?: boolean;
  onChange: (next: StreamStep) => void;
  // Adds a param (when given) and changes the step in one edit.
  onMakeParam: (param: StreamParam | undefined, next: StreamStep) => void;
  onRename: (id: string) => void;
  onDelete: () => void;
  onClose: () => void;
}

// MakeParam turns a pipeline input into a Stream param asked at start.
const MakeParam = ({ value, onClick }: { value: string; onClick: () => void }) =>
  /^\s*\$\{\{\s*params\.[A-Za-z0-9_]+\s*\}\}\s*$/.test(value) ? null : (
    <button
      className='argo-button argo-button--base-o'
      style={{ flex: '0 0 auto' }}
      title='Ask for this value when the stream starts: adds a param and uses it here'
      onClick={onClick}>
      <i className='fa fa-sliders-h' />
    </button>
  );

const WHEN_HELP: Record<string, string> = {
  success: 'Runs when the steps it waits for succeeded.',
  failure: 'Runs only when an earlier step failed or was cancelled, e.g. a rollback or a notification.',
  always: 'Runs after the steps it waits for, whatever their result.',
};

const StepPanel = ({ data, spec, step, pos, connections, problems, onChange, onMakeParam, onRename, onDelete, onClose }: StepPanelProps) => {
  const set = (patch: Partial<StreamStep>) => onChange({ ...step, ...patch });
  const makeParam = (i: RunInput, kind: 'inputs' | 'variables') => {
    const [param, name] = paramFromInput(spec, i, step.connection, isBranchInput(i));
    onMakeParam(param, { ...step, [kind]: setKey(step[kind], i.name, paramRef(name)) });
  };
  const approval = isApproval(step);
  const argocd = isArgoCD(step);
  const pipelineStep = !approval && !argocd;
  const [idText, setIdText] = React.useState(step.id);
  const [argoApps] = useLoad(() => (argocd ? fetchArgoApps() : Promise.resolve([])), [argocd]);
  const appNames = argoApps.state === 'ok' ? argoApps.data.map(a => `${a.namespace}/${a.name}`).sort() : [];
  React.useEffect(() => setIdText(step.id), [step.id]);

  const conn = step.connection;
  const [branches] = useLoad(() => (conn ? data.branches(conn) : Promise.resolve(undefined)), [data, conn]);
  const bl = branches.state === 'ok' ? branches.data : undefined;
  const typedRef = useDebounced(step.ref.trim(), 600);
  const ready = !conn || branches.state !== 'loading';
  // Templated refs are only known at run time; read the pipeline list and
  // its inputs from the default branch instead.
  const formRef = typedRef && !isTemplate(typedRef) ? typedRef : bl?.defaultBranch ?? '';
  const [pipelines] = useLoad(
    () => (conn && ready ? data.pipelines(conn, formRef) : Promise.resolve(undefined)),
    [data, conn, ready, formRef],
  );
  const [form] = useLoad(
    () => (conn && ready && step.pipeline ? data.runForm(conn, step.pipeline, formRef) : Promise.resolve(undefined)),
    [data, conn, ready, step.pipeline, formRef],
  );

  const refs = referencesFor(spec, step.id);
  const valueRefs = [...refs, ...stepValueRefs(step)];
  const list = pipelines.state === 'ok' ? pipelines.data ?? [] : [];
  const fd = form.state === 'ok' ? form.data : undefined;
  const declared = fd?.inputs.map(i => i.name) ?? [];
  const prefilled = fd?.prefilledVariables ?? [];
  const earlier = spec.stages.slice(0, pos.stage).flatMap(s => s.steps.map(x => x.id));
  const prev = previousStageSteps(spec, pos.stage);
  const needs = step.needs ?? [];
  const extraInputs = Object.keys(step.inputs ?? {}).filter(k => !declared.includes(k));
  const freeVars = Object.keys(step.variables ?? {}).filter(k => !prefilled.some(p => p.name === k));

  const toggleNeed = (id: string, on: boolean) => {
    const next = on ? [...needs, id] : needs.filter(n => n !== id);
    set({ needs: next.length > 0 ? next : undefined });
  };

  const commitID = () => {
    const v = idText.trim();
    if (v !== step.id) {
      onRename(v);
    }
  };
  const panelRef = React.useRef<HTMLDivElement>(null);
  React.useEffect(() => {
    panelRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [step.id]);

  return (
    <div ref={panelRef} className='white-box' style={{ padding: '0.8em 1em', marginTop: '1em' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '0.8em' }}>
        <b style={{ ...ellipsis, flex: 1 }}>
          {approval ? 'Approval' : argocd ? 'Argo CD sync' : 'Step'} <code>{step.id}</code> <Muted>· stage {pos.stage + 1}</Muted>
        </b>
        <button className='argo-button argo-button--base-o' title='Delete step' onClick={onDelete}>
          <i className='fa fa-trash' />
        </button>
        <button className='argo-button argo-button--base-o' title='Close' onClick={onClose}>
          <i className='fa fa-times' />
        </button>
      </div>
      {problems.length > 0 && (
        <div style={{ color: COLORS.error, marginBottom: '0.8em' }}>
          <ProblemList problems={problems.map(p => ({ message: p.message }))} />
        </div>
      )}

      <div style={{ display: 'flex', gap: '0.6em' }}>
        <div style={{ flex: '0 0 40%' }}>
          <Field label='Id' help={STEP_ID_RE.test(idText) ? undefined : 'a-z, 0-9, - and _; starts with a letter'}>
            <input
              className='argo-field'
              style={{ width: '100%', fontFamily: 'monospace' }}
              value={idText}
              spellCheck={false}
              onChange={e => setIdText(e.target.value)}
              onBlur={commitID}
              onKeyDown={e => e.key === 'Enter' && commitID()}
            />
          </Field>
        </div>
        <div style={{ flex: 1 }}>
          <Field label='Name'>
            <input
              className='argo-field'
              style={{ width: '100%' }}
              value={step.name ?? ''}
              placeholder={approval ? 'Approval' : argocd ? 'Argo CD sync' : 'pipeline name'}
              onChange={e => set({ name: e.target.value || undefined })}
            />
          </Field>
        </div>
      </div>

      {approval && (
        <>
          <Field label='Message' help='Shown to approvers. May use references, e.g. the commit a build produced.'>
            <div style={{ display: 'flex', gap: '0.3em', alignItems: 'flex-start' }}>
              <textarea
                className='argo-field'
                rows={3}
                style={{ flex: 1, minWidth: 0, resize: 'vertical', fontFamily: isTemplate(step.message ?? '') ? 'monospace' : undefined }}
                value={step.message ?? ''}
                placeholder='Deploy ${{ steps.build.sha }} to production?'
                spellCheck={false}
                onChange={e => set({ message: e.target.value || undefined })}
              />
              {refs.length > 0 && <RefSelect refs={refs} onPick={v => set({ message: (step.message ?? '') + v })} />}
            </div>
          </Field>
          <label style={{ display: 'block', cursor: 'pointer' }}>
            <input
              type='checkbox'
              checked={!!step.noSelfApproval}
              onChange={e => set({ noSelfApproval: e.target.checked || undefined })}
            />{' '}
            Someone else must decide
          </label>
          <Help>
            The user who started the run (or retried it) may not approve or reject. Users with the approve action on this stream
            and admins decide; a rejection or a timeout fails the step.
          </Help>
          <div style={{ marginBottom: '0.7em' }} />
        </>
      )}

      {argocd && (
        <>
          <Field
            label='Application'
            required
            help='<namespace>/<name>, or only the name for the first namespace Zea is allowed to use. May use references.'>
            <div style={{ display: 'flex', gap: '0.3em', alignItems: 'center' }}>
              <input
                className='argo-field'
                style={{ flex: 1, minWidth: 0, fontFamily: 'monospace' }}
                list={`zea-apps-${step.id}`}
                value={step.app ?? ''}
                placeholder={argoApps.state === 'loading' ? 'Loading applications...' : 'argocd/my-app'}
                spellCheck={false}
                onChange={e => set({ app: e.target.value.trim() || undefined })}
              />
              <datalist id={`zea-apps-${step.id}`}>
                {appNames.map(n => (
                  <option key={n} value={n} />
                ))}
              </datalist>
              {refs.length > 0 && <RefSelect refs={refs} onPick={v => set({ app: (step.app ?? '') + v })} />}
            </div>
          </Field>
          <Field
            label='Image'
            help='Optional. Wait until the application runs this image: a full reference or only a tag, e.g. ${{ params.tag }}. Empty: wait for a sync that finishes after the step starts.'>
            <div style={{ display: 'flex', gap: '0.3em', alignItems: 'center' }}>
              <input
                className='argo-field'
                style={{ flex: 1, minWidth: 0, fontFamily: 'monospace' }}
                value={step.image ?? ''}
                placeholder='${{ params.tag }}'
                spellCheck={false}
                onChange={e => set({ image: e.target.value.trim() || undefined })}
              />
              {refs.length > 0 && <RefSelect refs={refs} onPick={v => set({ image: (step.image ?? '') + v })} />}
            </div>
          </Field>
          <label style={{ display: 'block', cursor: 'pointer', marginBottom: '0.2em' }}>
            <input type='checkbox' checked={!step.noRefresh} onChange={e => set({ noRefresh: e.target.checked ? undefined : true })} /> Refresh
            the application first
          </label>
          <Help>
            Zea does not sync or change the application: a pipeline or Argo CD auto-sync deploys it, the step only waits until it is
            synced and healthy. Degraded for 5 minutes fails the step.
          </Help>
          <div style={{ marginBottom: '0.7em' }} />
        </>
      )}

      {pipelineStep && (
        <>
          <Field label='Connection' required>
            <select
              className='argo-field'
              style={{ width: '100%' }}
              value={conn}
              onChange={e => set({ connection: e.target.value, pipeline: '', inputs: undefined, variables: undefined })}>
              <option value=''>Select a connection...</option>
              {connections.map(c => (
                <option key={c.name} value={c.name}>
                  {c.name}
                </option>
              ))}
              {conn && !connections.some(c => c.name === conn) && <option value={conn}>{conn} (missing)</option>}
            </select>
          </Field>

          <Field
            label='Branch or tag'
            required
            help={isTemplate(step.ref) ? `Resolved when the step starts. Pipelines and inputs below are read from ${formRef || 'the default branch'}.` : undefined}>
            <ValueField value={step.ref} branches={bl} refs={refs} placeholder='main or ${{ params.branch }}' onChange={v => set({ ref: v })} />
          </Field>

          <Field label='Pipeline' required>
            <select
              className='argo-field'
              style={{ width: '100%' }}
              value={step.pipeline}
              disabled={!conn}
              onChange={e => set({ pipeline: e.target.value })}>
              <option value=''>{pipelines.state === 'loading' && conn ? 'Loading...' : 'Select a pipeline...'}</option>
              {list.map(p => (
                <option key={p.id} value={p.id} disabled={!p.dispatchable} title={p.reason}>
                  {p.name}
                  {p.path ? ` (${p.path})` : ''}
                  {!p.dispatchable ? ' - cannot be started' : ''}
                </option>
              ))}
              {step.pipeline && !list.some(p => p.id === step.pipeline) && <option value={step.pipeline}>{step.pipeline}</option>}
            </select>
            {pipelines.state === 'error' && <ErrorText text={pipelines.error} />}
          </Field>

          {step.pipeline && (
            <div style={{ borderTop: `1px solid ${COLORS.border}`, paddingTop: '0.6em', marginBottom: '0.6em' }}>
              <div style={{ fontWeight: 600, marginBottom: '0.5em' }}>Parameters</div>
              {form.state === 'loading' && <Muted>Reading the pipeline inputs...</Muted>}
              {form.state === 'error' && <ErrorText text={form.error} />}
              {fd?.warning && (
                <div style={{ color: COLORS.warn, marginBottom: '0.5em' }}>
                  <i className='fa fa-exclamation-triangle' /> {fd.warning}
                </div>
              )}
              {fd && fd.inputs.length === 0 && prefilled.length === 0 && !fd.variables && <Muted>This pipeline has no inputs.</Muted>}
              {fd?.inputs.map(i => (
                <Field key={i.name} label={<code>{i.name}</code>} hint={i.type} required={i.required && !i.default} help={i.description}>
                  <div style={{ display: 'flex', gap: '0.3em', alignItems: 'flex-start' }}>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <ValueField
                        value={step.inputs?.[i.name] ?? ''}
                        refs={valueRefs}
                        options={i.type === 'choice' ? i.options : i.type === 'boolean' ? ['true', 'false'] : undefined}
                        branches={isBranchInput(i) ? bl : undefined}
                        placeholder={i.default ? `default: ${i.default}` : ''}
                        onChange={v => set({ inputs: setKey(step.inputs, i.name, v) })}
                      />
                    </div>
                    <MakeParam value={step.inputs?.[i.name] ?? ''} onClick={() => makeParam(i, 'inputs')} />
                  </div>
                </Field>
              ))}
              {(form.state === 'error' || extraInputs.length > 0) && (
                <Field
                  label='Other inputs'
                  help={form.state === 'error' ? 'The pipeline inputs could not be read; enter them by name.' : 'Not declared by this pipeline.'}>
                  <KeyValueEditor
                    entries={step.inputs}
                    exclude={declared}
                    refs={valueRefs}
                    keyPlaceholder='input'
                    addLabel='Add input'
                    onChange={inputs => set({ inputs })}
                  />
                </Field>
              )}
              {prefilled.map(p => (
                <Field key={p.name} label={<code>{p.name}</code>} help={p.description}>
                  <div style={{ display: 'flex', gap: '0.3em', alignItems: 'flex-start' }}>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <ValueField
                        value={step.variables?.[p.name] ?? ''}
                        refs={valueRefs}
                        options={p.options?.length ? p.options : undefined}
                        placeholder={p.default ? `default: ${p.default}` : ''}
                        mono
                        onChange={v => set({ variables: setKey(step.variables, p.name, v) })}
                      />
                    </div>
                    <MakeParam value={step.variables?.[p.name] ?? ''} onClick={() => makeParam(p, 'variables')} />
                  </div>
                </Field>
              ))}
              {(fd?.variables || freeVars.length > 0) && (
                <Field label={prefilled.length > 0 ? 'Other variables' : 'Variables'}>
                  <KeyValueEditor
                    entries={step.variables}
                    exclude={prefilled.map(p => p.name)}
                    refs={valueRefs}
                    keyPlaceholder='NAME'
                    addLabel='Add variable'
                    onChange={variables => set({ variables })}
                  />
                </Field>
              )}
            </div>
          )}
        </>
      )}

      <div style={{ borderTop: `1px solid ${COLORS.border}`, paddingTop: '0.6em' }}>
        <Field label='Waits for'>
          {earlier.length === 0 ? (
            <Muted>First stage: starts as soon as the run starts.</Muted>
          ) : (
            <>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3em 1em' }}>
                {earlier.map((id, i) => (
                  <label key={`${id}:${i}`} style={{ cursor: 'pointer', fontWeight: 400 }}>
                    <input type='checkbox' checked={needs.includes(id)} onChange={e => toggleNeed(id, e.target.checked)} /> <code>{id}</code>
                  </label>
                ))}
              </div>
              <Help>
                {needs.length > 0
                  ? 'Starts when the checked steps finish, without waiting for the rest of the previous stage.'
                  : `Nothing checked: waits for the whole previous stage (${prev.join(', ')}).`}
              </Help>
            </>
          )}
        </Field>
        <div style={{ display: 'flex', gap: '0.6em' }}>
          <div style={{ flex: 1 }}>
            <Field label='Run when'>
              <select
                className='argo-field'
                style={{ width: '100%' }}
                value={step.when || 'success'}
                onChange={e => set({ when: e.target.value === 'success' ? undefined : e.target.value })}>
                <option value='success'>previous succeeded</option>
                <option value='failure'>something failed</option>
                <option value='always'>always</option>
              </select>
            </Field>
          </div>
          <div style={{ flex: '0 0 35%' }}>
            <Field label='Timeout'>
              <input
                className='argo-field'
                style={{ width: '100%' }}
                value={step.timeout ?? ''}
                placeholder={approval ? DEFAULT_APPROVAL_TIMEOUT : argocd ? DEFAULT_ARGO_TIMEOUT : 'default'}
                onChange={e => set({ timeout: e.target.value.trim() || undefined })}
              />
            </Field>
          </div>
        </div>
        <Help>
          {WHEN_HELP[step.when || 'success']}{' '}
          {approval
            ? `Timeout: how long to wait for a decision, ${DEFAULT_APPROVAL_TIMEOUT} by default.`
            : argocd
            ? `Timeout: how long to wait for the application, ${DEFAULT_ARGO_TIMEOUT} by default.`
            : 'Timeout: e.g. 90m or 2h.'}
        </Help>
        {pipelineStep && (
          <>
            <div style={{ display: 'flex', gap: '0.6em', marginTop: '0.6em' }}>
              <div style={{ flex: 1 }}>
                <Field label='Retries'>
                  <select
                    className='argo-field'
                    style={{ width: '100%' }}
                    value={step.retries ?? 0}
                    onChange={e => {
                      const n = Number(e.target.value);
                      set(n > 0 ? { retries: n } : { retries: undefined, retryDelay: undefined });
                    }}>
                    <option value={0}>manual only</option>
                    {[1, 2, 3, 4, 5].map(n => (
                      <option key={n} value={n}>
                        {n} automatic
                      </option>
                    ))}
                  </select>
                </Field>
              </div>
              <div style={{ flex: '0 0 35%' }}>
                <Field label='Retry delay'>
                  <input
                    className='argo-field'
                    style={{ width: '100%' }}
                    value={step.retryDelay ?? ''}
                    placeholder='30s'
                    disabled={!step.retries}
                    onChange={e => set({ retryDelay: e.target.value.trim() || undefined })}
                  />
                </Field>
              </div>
            </div>
            <Help>
              A failed or timed out pipeline, or a failed trigger, is started again before the steps after it react. A pipeline
              cancelled in the provider is not retried. Without retries, use Retry failed on the run.
            </Help>
          </>
        )}
        <label style={{ display: 'block', cursor: 'pointer', marginTop: '0.6em' }}>
          <input
            type='checkbox'
            checked={!!step.continueOnError}
            onChange={e => set({ continueOnError: e.target.checked || undefined })}
          />{' '}
          Continue on error
        </label>
        <Help>A failure of this step does not stop the steps after it, and the run can still succeed.</Help>
      </div>
    </div>
  );
};

interface ParamRowProps {
  data: ConnData;
  param: StreamParam;
  connections: Connection[];
  problems: StreamProblem[];
  onChange: (next: StreamParam) => void;
  onRename: (name: string) => void;
  onDelete: () => void;
}

const BOOL_WIDGETS: [string, string][] = [
  ['', 'checkbox'],
  ['switch', 'switch'],
  ['radio', 'on / off'],
  ['select', 'dropdown'],
];
const CHOICE_WIDGETS: [string, string][] = [
  ['', 'dropdown'],
  ['radio', 'radio buttons'],
];

const parseOptions = (s: string) =>
  s
    .split(',')
    .map(x => x.trim())
    .filter(Boolean);

const ParamRow = ({ data, param, connections, problems, onChange, onRename, onDelete }: ParamRowProps) => {
  const [nameText, setNameText] = React.useState(param.name);
  const [optText, setOptText] = React.useState((param.options ?? []).join(', '));
  React.useEffect(() => setNameText(param.name), [param.name]);
  React.useEffect(() => {
    if (parseOptions(optText).join('\u0000') !== (param.options ?? []).join('\u0000')) {
      setOptText((param.options ?? []).join(', '));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [param.options]);
  const [branches] = useLoad(
    () => (param.type === 'branch' && param.connection ? data.branches(param.connection) : Promise.resolve(undefined)),
    [data, param.type, param.connection],
  );
  const set = (patch: Partial<StreamParam>) => onChange({ ...param, ...patch });
  const commitName = () => nameText.trim() !== param.name && onRename(nameText.trim());

  const setType = (type: string) =>
    set({
      type,
      default: type === 'boolean' ? 'false' : type === param.type ? param.default : undefined,
      options: type === 'choice' ? param.options : undefined,
      optionLabels: type === 'choice' ? param.optionLabels : undefined,
      connection: type === 'branch' || type === 'image' ? param.connection : undefined,
      widget: type === param.type ? param.widget : undefined,
      trueValue: type === 'boolean' ? param.trueValue : undefined,
      falseValue: type === 'boolean' ? param.falseValue : undefined,
    });
  const widgets = param.type === 'boolean' ? BOOL_WIDGETS : param.type === 'choice' ? CHOICE_WIDGETS : [];
  const [onValue, offValue] = boolValues(param);
  const setOptionLabel = (o: string, label: string) => {
    const next = { ...(param.optionLabels ?? {}) };
    if (label) {
      next[o] = label;
    } else {
      delete next[o];
    }
    set({ optionLabels: Object.keys(next).length > 0 ? next : undefined });
  };

  return (
    <div style={{ borderTop: `1px solid ${COLORS.border}`, padding: '0.5em 0' }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5em', alignItems: 'flex-end' }}>
        <div style={{ flex: '1 1 140px' }}>
          <Help>Name</Help>
          <input
            className='argo-field'
            style={{ width: '100%', fontFamily: 'monospace' }}
            value={nameText}
            spellCheck={false}
            onChange={e => setNameText(e.target.value)}
            onBlur={commitName}
            onKeyDown={e => e.key === 'Enter' && commitName()}
          />
        </div>
        <div style={{ flex: '0 0 110px' }}>
          <Help>Type</Help>
          <select className='argo-field' style={{ width: '100%' }} value={param.type} onChange={e => setType(e.target.value)}>
            <option value='string'>text</option>
            <option value='branch'>branch</option>
            <option value='image'>image</option>
            <option value='choice'>choice</option>
            <option value='boolean'>boolean</option>
          </select>
        </div>
        {(param.type === 'branch' || param.type === 'image') && (
          <div style={{ flex: '1 1 140px' }}>
            <Help>{param.type === 'image' ? 'Image tags of' : 'Branches of'}</Help>
            <select
              className='argo-field'
              style={{ width: '100%' }}
              value={param.connection ?? ''}
              onChange={e => set({ connection: e.target.value || undefined })}>
              <option value=''>Select a connection...</option>
              {connections
                .filter(c => param.type !== 'image' || c.images.length > 0 || c.name === param.connection)
                .map(c => (
                  <option key={c.name} value={c.name}>
                    {c.name}
                  </option>
                ))}
            </select>
          </div>
        )}
        {param.type === 'choice' && (
          <div style={{ flex: '2 1 180px' }}>
            <Help>Options, comma separated</Help>
            <input
              className='argo-field'
              style={{ width: '100%' }}
              value={optText}
              placeholder='dev, staging, prod'
              onChange={e => {
                setOptText(e.target.value);
                const opts = parseOptions(e.target.value);
                set({ options: opts.length > 0 ? opts : undefined });
              }}
            />
          </div>
        )}
        <div style={{ flex: '1 1 140px' }}>
          <Help>Default</Help>
          {param.type === 'choice' || param.type === 'boolean' ? (
            <select
              className='argo-field'
              style={{ width: '100%' }}
              value={param.default ?? ''}
              onChange={e => set({ default: e.target.value || undefined })}>
              {param.type === 'choice' && <option value=''>(none)</option>}
              {param.type === 'boolean' ? (
                <>
                  <option value='false'>off ({offValue})</option>
                  <option value='true'>on ({onValue})</option>
                  {param.default && param.default !== 'true' && param.default !== 'false' && (
                    <option value={param.default}>{param.default}</option>
                  )}
                </>
              ) : (
                (param.options ?? []).map(o => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))
              )}
            </select>
          ) : (
            <ValueField
              value={param.default ?? ''}
              refs={[]}
              branches={branches.state === 'ok' ? branches.data : undefined}
              onChange={v => set({ default: v || undefined })}
            />
          )}
        </div>
        <label style={{ flex: '0 0 auto', cursor: 'pointer', paddingBottom: '0.4em' }}>
          <input type='checkbox' checked={!!param.required} onChange={e => set({ required: e.target.checked || undefined })} /> required
        </label>
        <button className='argo-button argo-button--base-o' title='Remove param' onClick={onDelete}>
          <i className='fa fa-times' />
        </button>
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5em', alignItems: 'flex-end', marginTop: '0.4em' }}>
        <div style={{ flex: '1 1 160px' }}>
          <Help>Label in the start form</Help>
          <input
            className='argo-field'
            style={{ width: '100%' }}
            value={param.label ?? ''}
            placeholder={param.name}
            onChange={e => set({ label: e.target.value || undefined })}
          />
        </div>
        {widgets.length > 0 && (
          <div style={{ flex: '0 0 110px' }}>
            <Help>Shown as</Help>
            <select
              className='argo-field'
              style={{ width: '100%' }}
              value={param.widget ?? ''}
              onChange={e => set({ widget: (e.target.value || undefined) as StreamParam['widget'] })}>
              {widgets.map(([v, l]) => (
                <option key={v} value={v}>
                  {l}
                </option>
              ))}
            </select>
          </div>
        )}
        {param.type === 'boolean' && (
          <>
            <div style={{ flex: '0 1 110px' }}>
              <Help>On value</Help>
              <input
                className='argo-field'
                style={{ width: '100%', fontFamily: 'monospace' }}
                value={param.trueValue ?? ''}
                placeholder='true'
                spellCheck={false}
                onChange={e => set({ trueValue: e.target.value || undefined })}
              />
            </div>
            <div style={{ flex: '0 1 110px' }}>
              <Help>Off value</Help>
              <input
                className='argo-field'
                style={{ width: '100%', fontFamily: 'monospace' }}
                value={param.falseValue ?? ''}
                placeholder='false'
                spellCheck={false}
                onChange={e => set({ falseValue: e.target.value || undefined })}
              />
            </div>
          </>
        )}
      </div>
      {param.type === 'choice' && (param.options ?? []).length > 0 && (
        <div style={{ marginTop: '0.4em' }}>
          <Help>Option labels (optional; the value sent to the pipeline stays the option)</Help>
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(80px, max-content) 1fr', gap: '0.3em 0.5em', alignItems: 'center' }}>
            {(param.options ?? []).map(o => (
              <React.Fragment key={o}>
                <code style={ellipsis}>{o}</code>
                <input
                  className='argo-field'
                  style={{ width: '100%' }}
                  value={param.optionLabels?.[o] ?? ''}
                  placeholder={o}
                  onChange={e => setOptionLabel(o, e.target.value)}
                />
              </React.Fragment>
            ))}
          </div>
        </div>
      )}
      <input
        className='argo-field'
        style={{ width: '100%', marginTop: '0.4em' }}
        value={param.description ?? ''}
        placeholder='Description shown when the stream starts (optional)'
        onChange={e => set({ description: e.target.value || undefined })}
      />
      {problems.length > 0 && (
        <div style={{ color: COLORS.error }}>
          <ProblemList problems={problems.map(p => ({ message: p.message }))} />
        </div>
      )}
    </div>
  );
};

const ParamsEditor = ({
  data,
  spec,
  connections,
  problems,
  onChange,
}: {
  data: ConnData;
  spec: StreamSpec;
  connections: Connection[];
  problems: StreamProblem[];
  onChange: (spec: StreamSpec) => void;
}) => {
  const setParam = (i: number, p: StreamParam) => onChange({ ...spec, params: spec.params.map((x, j) => (j === i ? p : x)) });
  const rename = (i: number, name: string) => {
    const old = spec.params[i].name;
    const next = PARAM_NAME_RE.test(old) && PARAM_NAME_RE.test(name) ? renameParam(spec, old, name) : spec;
    onChange({ ...next, params: next.params.map((x, j) => (j === i ? { ...x, name } : x)) });
  };
  const add = () => {
    const names = new Set(spec.params.map(p => p.name));
    const hasBranch = spec.params.some(p => p.type === 'branch');
    let name = hasBranch ? 'param' : 'branch';
    for (let n = 2; names.has(name); n++) {
      name = `${hasBranch ? 'param' : 'branch'}${n}`;
    }
    const p: StreamParam = hasBranch
      ? { name, type: 'string' }
      : { name, type: 'branch', connection: connections[0]?.name, required: true };
    onChange({ ...spec, params: [...spec.params, p] });
  };
  return (
    <div>
      {spec.params.length === 0 && (
        <Help>
          Params are asked when the stream starts and used in steps as <code>{'${{ params.<name> }}'}</code>.
        </Help>
      )}
      {spec.params.map((p, i) => (
        <ParamRow
          key={i}
          data={data}
          param={p}
          connections={connections}
          problems={problems.filter(x => x.param === p.name && !x.step)}
          onChange={next => setParam(i, next)}
          onRename={name => rename(i, name)}
          onDelete={() => onChange({ ...spec, params: spec.params.filter((_, j) => j !== i) })}
        />
      ))}
      <button className='argo-button argo-button--base-o' style={{ marginTop: '0.5em' }} onClick={add}>
        <i className='fa fa-plus' /> Add param
      </button>
    </div>
  );
};

interface EditorProps {
  client: ZeaClient;
  data: ConnData;
  connections: Connection[];
  existing?: Stream;
  // Categories of the other streams, offered in the category field.
  categories?: string[];
  onSaved: (stream: Stream, created: boolean) => void;
  onClose: () => void;
}

export const StreamEditor = ({ client, data, connections, existing, categories, onSaved, onClose }: EditorProps) => {
  const [name, setName] = React.useState(existing?.name ?? '');
  const [spec, setSpec] = React.useState<StreamSpec>(() => (existing ? specOf(existing) : emptySpec()));
  const [version, setVersion] = React.useState(existing?.version ?? '');
  const [selected, setSelected] = React.useState<StreamStep | null>(null);
  const [panelKey, setPanelKey] = React.useState(0);
  const [problems, setProblems] = React.useState<StreamProblem[]>(existing?.problems ?? []);
  const [validateError, setValidateError] = React.useState('');
  const [dirty, setDirty] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const [showParams, setShowParams] = React.useState(true);
  const drag = React.useRef<Drag | null>(null);
  const [drop, setDrop] = React.useState<Drop | null>(null);

  const nameOK = STREAM_NAME_RE.test(name) && name.length <= MAX_STREAM_NAME;

  React.useEffect(() => {
    let cancelled = false;
    const t = window.setTimeout(() => {
      client.validateStream({ name: nameOK ? name : 'unnamed', ...spec }).then(
        p => {
          if (!cancelled) {
            setProblems(p);
            setValidateError('');
          }
        },
        err => !cancelled && setValidateError(describeError(err)),
      );
    }, 500);
    return () => {
      cancelled = true;
      window.clearTimeout(t);
    };
  }, [client, name, nameOK, spec]);

  const change = (next: StreamSpec) => {
    setSpec(next);
    setDirty(true);
  };

  const select = (step: StreamStep | null) => {
    if (step !== selected) {
      setPanelKey(k => k + 1);
    }
    setSelected(step);
  };

  const selectByID = (id: string) => select(spec.stages.flatMap(s => s.steps).find(s => s.id === id) ?? null);

  const addStep = (src: Source, pos: StepPos) => {
    let step: StreamStep;
    if (src.kind === 'approval') {
      step = { id: uniqueStepId(spec, 'approve'), type: APPROVAL, connection: '', pipeline: '', ref: '' };
    } else if (src.kind === 'argocd') {
      step = { id: uniqueStepId(spec, 'sync'), type: ARGOCD, connection: '', pipeline: '', ref: '' };
    } else {
      const connection = src.name;
      const branch = spec.params.find(p => p.type === 'branch' && p.connection === connection && PARAM_NAME_RE.test(p.name));
      step = {
        id: uniqueStepId(spec, connection),
        connection,
        pipeline: '',
        ref: branch ? `\${{ params.${branch.name} }}` : '',
      };
    }
    change(insertStep(spec, step, pos));
    select(step);
  };

  const addToLastStage = (src: Source) => {
    const last = Math.max(0, spec.stages.length - 1);
    addStep(src, { stage: last, index: spec.stages[last]?.steps.length ?? 0 });
  };

  const deleteStage = (s: number) => {
    const stage = spec.stages[s];
    if (stage.steps.length > 0 && !window.confirm(`Delete stage ${stage.name || s + 1} and its ${stage.steps.length} step(s)?`)) {
      return;
    }
    let next = spec;
    for (const step of stage.steps) {
      next = removeStep(next, step);
    }
    change({ ...next, stages: next.stages.filter((_, i) => i !== s) });
    if (selected && stage.steps.includes(selected)) {
      select(null);
    }
  };

  const setDropIf = (d: Drop | null) => setDrop(prev => (sameDrop(prev, d) ? prev : d));

  const endDrag = () => {
    drag.current = null;
    setDrop(null);
  };

  const applyDrop = () => {
    const d = drag.current;
    const t = drop;
    endDrag();
    if (!d || !t) {
      return;
    }
    if (d.kind === 'stage' && t.kind === 'stage') {
      if (t.index !== d.from && t.index !== d.from + 1) {
        change(moveStage(spec, d.from, t.index));
      }
    } else if (d.kind === 'step' && t.kind === 'step') {
      if (t.stage === d.from.stage && (t.index === d.from.index || t.index === d.from.index + 1)) {
        return;
      }
      change(moveStep(spec, d.from, { stage: t.stage, index: t.index }));
    } else if (isSource(d) && t.kind === 'step') {
      addStep(d, { stage: t.stage, index: t.index });
    }
  };

  const startDrag = (e: React.DragEvent, d: Drag) => {
    e.stopPropagation();
    drag.current = d;
    e.dataTransfer.effectAllowed = isSource(d) ? 'copy' : 'move';
    // Firefox starts a drag only when some data is set.
    e.dataTransfer.setData('text/plain', d.kind === 'conn' ? d.name : d.kind);
  };

  const overColumn = (e: React.DragEvent, s: number) => {
    const d = drag.current;
    if (!d) {
      return;
    }
    e.preventDefault();
    if (d.kind === 'stage') {
      const r = e.currentTarget.getBoundingClientRect();
      setDropIf({ kind: 'stage', index: e.clientX < r.left + r.width / 2 ? s : s + 1 });
    } else {
      setDropIf({ kind: 'step', stage: s, index: spec.stages[s].steps.length });
    }
  };

  const overCard = (e: React.DragEvent, s: number, i: number) => {
    const d = drag.current;
    if (!d || d.kind === 'stage') {
      return;
    }
    e.preventDefault();
    e.stopPropagation();
    const r = e.currentTarget.getBoundingClientRect();
    setDropIf({ kind: 'step', stage: s, index: e.clientY < r.top + r.height / 2 ? i : i + 1 });
  };

  const save = async () => {
    if (!nameOK) {
      setError(`Invalid name: use lowercase letters, digits and '-', max ${MAX_STREAM_NAME} characters.`);
      return;
    }
    setBusy(true);
    setError('');
    try {
      const input = { name, version, ...spec };
      const saved = existing ? await client.updateStream(input) : await client.createStream(input);
      setVersion(saved.version);
      setProblems(saved.problems);
      setDirty(false);
      setBusy(false);
      onSaved(saved, !existing);
    } catch (err) {
      setError(describeError(err));
      setBusy(false);
    }
  };

  const close = () => {
    if (!dirty || window.confirm('Discard unsaved changes?')) {
      onClose();
    }
  };

  const pos = selected ? positionOf(spec, selected) : undefined;

  // Edits outside the step panel (param renames, deleted stages) may replace
  // the selected step object; follow it by id.
  React.useEffect(() => {
    if (selected && !positionOf(spec, selected)) {
      setSelected(allSteps(spec).find(s => s.id === selected.id) ?? null);
    }
  }, [spec, selected]);

  const general = problems.filter(p => !p.step && !p.param);
  const stepCount = spec.stages.reduce((n, s) => n + s.steps.length, 0);

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5em', marginBottom: '0.8em', flexWrap: 'wrap' }}>
        <button className='argo-button argo-button--base-o' onClick={close}>
          <i className='fa fa-arrow-left' /> {existing ? existing.name : 'Streams'}
        </button>
        <b style={{ fontSize: '1.1em' }}>{existing ? 'Edit stream' : 'New stream'}</b>
        {existing?.draftOf && <Badge>draft of {existing.draftOf}</Badge>}
        <div style={{ flex: 1 }} />
        {dirty && <Muted>Unsaved changes</Muted>}
        {problems.length > 0 ? (
          <span style={{ color: COLORS.warn }} title='The stream can be saved, but not run'>
            <i className='fa fa-exclamation-triangle' /> {problems.length} problem{problems.length > 1 ? 's' : ''}
          </span>
        ) : (
          <span style={{ color: COLORS.ok }}>
            <i className='fa fa-check-circle' /> ready to run
          </span>
        )}
        <button className='argo-button argo-button--base' disabled={busy} onClick={save}>
          <i className={busy ? 'fa fa-circle-notch fa-spin' : 'fa fa-save'} /> Save
        </button>
        <button className='argo-button argo-button--base-o' disabled={busy} onClick={close}>
          Close
        </button>
      </div>
      {error && <ErrorText text={error} />}
      {validateError && <ErrorText text={`Validation: ${validateError}`} />}

      <div className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em' }}>
        <div style={{ display: 'flex', gap: '1em', flexWrap: 'wrap' }}>
          <div style={{ flex: '0 0 280px' }}>
            <Field label='Name' required help={existing ? undefined : 'Lowercase letters, digits and -; cannot be changed later.'}>
              <input
                className='argo-field'
                style={{ width: '100%' }}
                value={name}
                disabled={!!existing}
                placeholder='build-and-deploy'
                onChange={e => setName(e.target.value.trim())}
              />
              {name && !nameOK && <ErrorText text='Invalid name' />}
            </Field>
          </div>
          <div style={{ flex: '1 1 300px' }}>
            <Field label='Description'>
              <input
                className='argo-field'
                style={{ width: '100%' }}
                value={spec.description ?? ''}
                placeholder='What this stream does'
                onChange={e => change({ ...spec, description: e.target.value })}
              />
            </Field>
          </div>
          <div style={{ flex: '0 1 200px' }}>
            <Field label='Category' help='Streams are grouped by category in tabs on the Streams page.'>
              <input
                className='argo-field'
                style={{ width: '100%' }}
                value={spec.category ?? ''}
                list='zea-stream-categories'
                placeholder='none'
                onChange={e => change({ ...spec, category: e.target.value || undefined })}
              />
              <datalist id='zea-stream-categories'>
                {(categories ?? []).map(c => (
                  <option key={c} value={c} />
                ))}
              </datalist>
            </Field>
          </div>
          <div style={{ flex: '1 1 220px' }}>
            <Field
              label='Lock'
              help='Runs with the same lock, of any stream, run one at a time; later ones wait in a queue. May use ${{ params.<name> }}, e.g. deploy-${{ params.env }}. Empty: runs never wait.'>
              <input
                className='argo-field'
                style={{ width: '100%' }}
                value={spec.lock ?? ''}
                placeholder='no lock'
                onChange={e => change({ ...spec, lock: e.target.value || undefined })}
              />
            </Field>
          </div>
        </div>
        {general.length > 0 && (
          <div style={{ color: COLORS.warn, marginTop: '0.8em' }}>
            <ProblemList problems={general} />
          </div>
        )}
      </div>

      <div className='white-box' style={{ padding: '0.8em 1em', marginBottom: '1em' }}>
        <div style={{ fontWeight: 600, cursor: 'pointer', userSelect: 'none' }} onClick={() => setShowParams(v => !v)}>
          <i className={showParams ? 'fa fa-angle-down' : 'fa fa-angle-right'} /> Params ({spec.params.length})
        </div>
        {showParams && <ParamsEditor data={data} spec={spec} connections={connections} problems={problems} onChange={change} />}
      </div>

      <div style={{ display: 'flex', gap: '1em', alignItems: 'flex-start' }}>
        <div className='white-box' style={{ flex: '0 0 190px', padding: '0.8em', alignSelf: 'flex-start' }}>
          <div style={{ fontWeight: 600, marginBottom: '0.3em' }}>Control</div>
          <div style={{ margin: '0.4em 0 0.8em' }}>
            {CONTROL_STEPS.map(c => (
              <div
                key={c.kind}
                draggable
                onDragStart={e => startDrag(e, { kind: c.kind })}
                onDragEnd={endDrag}
                onClick={() => addToLastStage({ kind: c.kind })}
                title={c.title}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '0.4em',
                  border: `1px solid ${COLORS.border}`,
                  borderRadius: 4,
                  padding: '0.35em 0.5em',
                  marginBottom: '0.3em',
                  cursor: 'grab',
                  userSelect: 'none',
                }}>
                <i className='fa fa-grip-vertical' style={{ color: COLORS.muted }} />
                <span style={{ ...ellipsis, flex: 1 }}>{c.label}</span>
                <i className={c.icon} style={{ color: c.color }} />
              </div>
            ))}
          </div>
          <div style={{ fontWeight: 600, marginBottom: '0.3em' }}>Connections</div>
          <Help>Drag onto a stage, or click to add to the last stage.</Help>
          {connections.length === 0 && <Muted>No connections.</Muted>}
          {connections.map(c => (
            <div
              key={c.name}
              draggable
              onDragStart={e => startDrag(e, { kind: 'conn', name: c.name })}
              onDragEnd={endDrag}
              onClick={() => addToLastStage({ kind: 'conn', name: c.name })}
              title={c.url}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '0.4em',
                border: `1px solid ${COLORS.border}`,
                borderRadius: 4,
                padding: '0.35em 0.5em',
                marginTop: '0.4em',
                cursor: 'grab',
                userSelect: 'none',
              }}>
              <i className='fa fa-grip-vertical' style={{ color: COLORS.muted }} />
              <span style={{ ...ellipsis, flex: 1 }}>{c.name}</span>
              <ProviderBadge provider={c.provider} />
            </div>
          ))}
        </div>

        <div
          style={{ flex: 1, minWidth: 0, overflowX: 'auto', paddingBottom: '0.5em' }}
          onDragOver={e => drag.current && e.preventDefault()}
          onDrop={e => {
            e.preventDefault();
            applyDrop();
          }}
          onDragLeave={e => {
            if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
              setDropIf(null);
            }
          }}>
          <div style={{ display: 'flex', alignItems: 'stretch', gap: '0.5em', minHeight: 200 }}>
            {spec.stages.map((stage, s) => (
              <React.Fragment key={s}>
                {s > 0 && !(drop?.kind === 'stage' && drop.index === s) && (
                  <div style={{ alignSelf: 'center', color: COLORS.muted }}>
                    <i className='fa fa-chevron-right' />
                  </div>
                )}
                {drop?.kind === 'stage' && drop.index === s && <DropBar vertical />}
                <div
                  onDragOver={e => overColumn(e, s)}
                  style={{
                    flex: `0 0 ${COLUMN_WIDTH}px`,
                    width: COLUMN_WIDTH,
                    background: COLORS.surface,
                    border: `1px solid ${COLORS.border}`,
                    borderRadius: 4,
                    padding: '0.5em',
                    display: 'flex',
                    flexDirection: 'column',
                    gap: '0.4em',
                  }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.3em' }}>
                    <span
                      draggable
                      onDragStart={e => startDrag(e, { kind: 'stage', from: s })}
                      onDragEnd={endDrag}
                      title='Drag to reorder stages'
                      style={{ cursor: 'grab', color: COLORS.muted, padding: '0 0.2em' }}>
                      <i className='fa fa-grip-vertical' />
                    </span>
                    <input
                      className='argo-field'
                      style={{ flex: 1, minWidth: 0, fontWeight: 600 }}
                      value={stage.name ?? ''}
                      placeholder={`Stage ${s + 1}`}
                      onChange={e => change({ ...spec, stages: spec.stages.map((x, i) => (i === s ? { ...x, name: e.target.value } : x)) })}
                    />
                    <button
                      className='argo-button argo-button--base-o'
                      style={{ padding: '0 0.5em' }}
                      title='Delete stage'
                      onClick={() => deleteStage(s)}>
                      <i className='fa fa-times' />
                    </button>
                  </div>
                  <Help>{stage.steps.length > 1 ? `${stage.steps.length} steps in parallel` : `stage ${s + 1}`}</Help>
                  {stage.steps.map((step, i) => (
                    <React.Fragment key={i}>
                      {drop?.kind === 'step' && drop.stage === s && drop.index === i && <DropBar />}
                      <StepCard
                        data={data}
                        step={step}
                        problems={stepProblems(problems, step.id)}
                        selected={step === selected}
                        onSelect={() => select(step)}
                        onDragStart={e => startDrag(e, { kind: 'step', from: { stage: s, index: i } })}
                        onDragEnd={endDrag}
                        onDragOver={e => overCard(e, s, i)}
                      />
                    </React.Fragment>
                  ))}
                  {drop?.kind === 'step' && drop.stage === s && drop.index === stage.steps.length && <DropBar />}
                  {stage.steps.length === 0 && (
                    <div
                      style={{
                        border: `1px dashed ${COLORS.border}`,
                        borderRadius: 4,
                        padding: '1em 0.5em',
                        textAlign: 'center',
                        color: COLORS.muted,
                      }}>
                      Drop a connection or a control step here
                    </div>
                  )}
                </div>
              </React.Fragment>
            ))}
            {drop?.kind === 'stage' && drop.index === spec.stages.length && <DropBar vertical />}
            <div
              onDragOver={e => {
                const d = drag.current;
                if (d && d.kind !== 'stage') {
                  e.preventDefault();
                  e.stopPropagation();
                  setDropIf({ kind: 'step', stage: spec.stages.length, index: 0 });
                }
              }}
              onClick={() => change({ ...spec, stages: [...spec.stages, { name: '', steps: [] }] })}
              style={{
                flex: '0 0 160px',
                border: `1px dashed ${drop?.kind === 'step' && drop.stage === spec.stages.length ? ACCENT : COLORS.border}`,
                borderRadius: 4,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                color: COLORS.muted,
                cursor: 'pointer',
                textAlign: 'center',
                padding: '0.5em',
              }}>
              <span>
                <i className='fa fa-plus' /> New stage
                <br />
                <span style={{ fontSize: '0.85em' }}>click, or drop a step here</span>
              </span>
            </div>
          </div>
          <Help>
            {spec.stages.length} stage{spec.stages.length === 1 ? '' : 's'}, {stepCount} step{stepCount === 1 ? '' : 's'}. Steps of a
            stage run in parallel; a stage starts when the previous one is done. Drag steps between stages and stages by their handle.
          </Help>
        </div>

      </div>
      {selected && pos && (
        <StepPanel
          key={panelKey}
          data={data}
          spec={spec}
          step={selected}
          pos={pos}
          connections={connections}
          problems={stepProblems(problems, selected.id)}
          onChange={next => {
            change(replaceStep(spec, selected, next));
            setSelected(next);
          }}
          onMakeParam={(param, next) => {
            change(replaceStep(param ? { ...spec, params: [...spec.params, param] } : spec, selected, next));
            setSelected(next);
            setShowParams(true);
          }}
          onRename={id => {
            const [next, renamed] = renameStep(spec, selected, id);
            change(next);
            setSelected(renamed);
          }}
          onDelete={() => {
            change(removeStep(spec, selected));
            select(null);
          }}
          onClose={() => select(null)}
        />
      )}
      {problems.some(p => p.step) && (
        <div className='white-box' style={{ padding: '0.8em 1em', marginTop: '1em', color: COLORS.warn }}>
          <b>Step problems</b>
          <ProblemList problems={problems.filter(p => p.step)} onStep={selectByID} />
        </div>
      )}
    </div>
  );
};
