import { ZeaClient } from './api';
import { BranchList, Pipeline, RunForm, RunInput, StreamParam, StreamProblem, StreamSpec, StreamStage, StreamStep } from './types';

// Keep in sync with backend/internal/streams/stream.go.
export const STREAM_NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
export const STEP_ID_RE = /^[a-z][a-z0-9_-]{0,39}$/;
export const PARAM_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]{0,49}$/;
export const MAX_STREAM_NAME = 50;

export const isTemplate = (v: string) => v.includes('${{');

export const APPROVAL = 'approval';
export const isApproval = (s: { type?: string }) => s.type === APPROVAL;
// Keep in sync with DefaultApprovalTimeout in backend/internal/streams.
export const DEFAULT_APPROVAL_TIMEOUT = '24h';

export const ARGOCD = 'argocd';
export const isArgoCD = (s: { type?: string }) => s.type === ARGOCD;
// Keep in sync with DefaultArgoTimeout in backend/internal/streams.
export const DEFAULT_ARGO_TIMEOUT = '30m';

export function emptySpec(): StreamSpec {
  return { description: '', params: [], stages: [{ name: 'Build', steps: [] }] };
}

// specOf copies the editable part of a Stream (or a run snapshot).
export function specOf(s: {
  label?: string;
  description?: string;
  category?: string;
  lock?: string;
  params?: StreamParam[] | null;
  stages?: StreamStage[] | null;
}): StreamSpec {
  return {
    label: s.label || undefined,
    description: s.description ?? '',
    category: s.category || undefined,
    lock: s.lock || undefined,
    params: (s.params ?? []).map(p => ({
      ...p,
      options: p.options ? [...p.options] : undefined,
      optionLabels: p.optionLabels ? { ...p.optionLabels } : undefined,
    })),
    stages: (s.stages ?? []).map(st => ({ ...st, steps: (st.steps ?? []).map(x => ({ ...x })) })),
  };
}

// streamLabel is the text shown for a stream: its label or its name.
export const streamLabel = (s: { name: string; label?: string }) => s.label?.trim() || s.name;

export const allSteps = (spec: StreamSpec): StreamStep[] => spec.stages.flatMap(s => s.steps);

// paramLabel is the text the run form shows for a param.
export const paramLabel = (p: StreamParam) => p.label?.trim() || p.name;

// boolValues are the values a boolean param passes on when on and off.
export const boolValues = (p: StreamParam): [string, string] => [p.trueValue || 'true', p.falseValue || 'false'];

// boolOn reads a boolean value: "true", "false" or the param's own values.
export const boolOn = (p: StreamParam, v: string | undefined) => v === 'true' || (v !== undefined && v !== '' && v === boolValues(p)[0]);

export const optionLabel = (p: StreamParam, option: string) => p.optionLabels?.[option]?.trim() || option;

// paramFromInput makes a Stream param that asks for a pipeline input when
// the stream starts. It returns the param to add (none when a param of that
// name exists and is reused) and the param's name.
export function paramFromInput(spec: StreamSpec, input: RunInput, connection: string, branch: boolean): [StreamParam | undefined, string] {
  let base = input.name.replace(/[^A-Za-z0-9_]/g, '_');
  if (!/^[A-Za-z_]/.test(base)) {
    base = `p_${base}`;
  }
  base = base.slice(0, 45);
  if (spec.params.some(p => p.name === base)) {
    return [undefined, base];
  }
  const param: StreamParam = { name: base, type: 'string', description: input.description || undefined };
  const def = input.default ?? '';
  if (input.type === 'boolean') {
    param.type = 'boolean';
    param.default = def === 'true' ? 'true' : 'false';
  } else if (input.type === 'choice' && (input.options ?? []).length > 0) {
    param.type = 'choice';
    param.options = [...(input.options ?? [])];
    param.default = param.options.includes(def) ? def : undefined;
    param.required = input.required || undefined;
  } else if (branch && connection) {
    param.type = 'branch';
    param.connection = connection;
    param.default = def || undefined;
    param.required = (input.required && !def) || undefined;
  } else {
    param.default = def || undefined;
    param.required = (input.required && !def) || undefined;
  }
  return [param, base];
}

export const paramRef = (name: string) => `\${{ params.${name} }}`;

export interface StepPos {
  stage: number;
  index: number;
}

export function positionOf(spec: StreamSpec, step: StreamStep): StepPos | undefined {
  for (let s = 0; s < spec.stages.length; s++) {
    const i = spec.stages[s].steps.indexOf(step);
    if (i >= 0) {
      return { stage: s, index: i };
    }
  }
  return undefined;
}

// directDeps mirrors the backend graph: a step waits for its needs, or for
// every step of the closest earlier non-empty stage.
export function directDeps(spec: StreamSpec): Map<string, string[]> {
  const out = new Map<string, string[]>();
  let prev: string[] = [];
  for (const st of spec.stages) {
    for (const step of st.steps) {
      out.set(step.id, step.needs && step.needs.length > 0 ? [...step.needs] : [...prev]);
    }
    if (st.steps.length > 0) {
      prev = st.steps.map(s => s.id);
    }
  }
  return out;
}

// upstreamOf returns the steps that finish before id, directly or not.
export function upstreamOf(spec: StreamSpec, id: string): string[] {
  const deps = directDeps(spec);
  const seen = new Set<string>();
  const stack = [...(deps.get(id) ?? [])];
  while (stack.length > 0) {
    const d = stack.pop() as string;
    if (d === id || seen.has(d)) {
      continue;
    }
    seen.add(d);
    stack.push(...(deps.get(d) ?? []));
  }
  const order = allSteps(spec).map(s => s.id);
  return order.filter(x => seen.has(x));
}

// previousStageSteps lists the steps a step without needs waits for.
export function previousStageSteps(spec: StreamSpec, stage: number): string[] {
  for (let s = stage - 1; s >= 0; s--) {
    if (spec.stages[s].steps.length > 0) {
      return spec.stages[s].steps.map(x => x.id);
    }
  }
  return [];
}

function slug(s: string): string {
  const v = s
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^[^a-z]+/, '')
    .replace(/[-_]+$/, '')
    .slice(0, 32);
  return v || 'step';
}

export function uniqueStepId(spec: StreamSpec, base: string): string {
  const ids = new Set(allSteps(spec).map(s => s.id));
  const b = slug(base);
  if (!ids.has(b)) {
    return b;
  }
  for (let i = 2; ; i++) {
    const c = `${b}-${i}`;
    if (!ids.has(c)) {
      return c;
    }
  }
}

const cloneStages = (spec: StreamSpec) => spec.stages.map(s => ({ ...s, steps: [...s.steps] }));

// mapSteps applies fn to every step, keeping the objects fn returns as is.
function mapSteps(spec: StreamSpec, fn: (s: StreamStep) => StreamStep): StreamSpec {
  return { ...spec, stages: spec.stages.map(st => ({ ...st, steps: st.steps.map(fn) })) };
}

export function replaceStep(spec: StreamSpec, old: StreamStep, next: StreamStep): StreamSpec {
  return mapSteps(spec, s => (s === old ? next : s));
}

// insertStep puts step at pos; pos.stage equal to the stage count appends a
// new stage.
export function insertStep(spec: StreamSpec, step: StreamStep, pos: StepPos): StreamSpec {
  const stages = cloneStages(spec);
  if (pos.stage >= stages.length) {
    stages.push({ name: '', steps: [] });
  }
  stages[pos.stage].steps.splice(Math.min(pos.index, stages[pos.stage].steps.length), 0, step);
  return { ...spec, stages };
}

export function moveStep(spec: StreamSpec, from: StepPos, to: StepPos): StreamSpec {
  const stages = cloneStages(spec);
  const [step] = stages[from.stage].steps.splice(from.index, 1);
  let index = to.index;
  if (from.stage === to.stage && from.index < to.index) {
    index--;
  }
  if (to.stage >= stages.length) {
    stages.push({ name: '', steps: [] });
  }
  stages[to.stage].steps.splice(index, 0, step);
  return { ...spec, stages };
}

// moveStage moves stage from to the slot before index "to".
export function moveStage(spec: StreamSpec, from: number, to: number): StreamSpec {
  const stages = [...spec.stages];
  const [stage] = stages.splice(from, 1);
  stages.splice(from < to ? to - 1 : to, 0, stage);
  return { ...spec, stages };
}

export function removeStep(spec: StreamSpec, step: StreamStep): StreamSpec {
  const stages = spec.stages.map(st => ({ ...st, steps: st.steps.filter(s => s !== step) }));
  return mapSteps({ ...spec, stages }, s =>
    s.needs?.includes(step.id) ? { ...s, needs: s.needs.filter(n => n !== step.id) } : s,
  );
}

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// rewriteValues replaces re in the templated values of a step; an unchanged
// step keeps its identity (the editor selects steps by object).
function rewriteValues(step: StreamStep, re: RegExp, to: string): StreamStep {
  let changed = false;
  const fix = (v: string) => {
    const out = isTemplate(v) ? v.replace(re, to) : v;
    changed = changed || out !== v;
    return out;
  };
  const fixMap = (m?: Record<string, string>) => (m ? Object.fromEntries(Object.entries(m).map(([k, v]) => [k, fix(v)])) : m);
  const next = {
    ...step,
    ref: fix(step.ref),
    inputs: fixMap(step.inputs),
    variables: fixMap(step.variables),
    message: step.message === undefined ? undefined : fix(step.message),
  };
  return changed ? next : step;
}

// renameStep changes a step id and every needs entry and ${{ steps.<id>.* }}
// reference to it. Returns the spec and the renamed step object.
export function renameStep(spec: StreamSpec, step: StreamStep, id: string): [StreamSpec, StreamStep] {
  const re = new RegExp(`\\bsteps\\.${escapeRe(step.id)}\\.`, 'g');
  let renamed = step;
  const next = mapSteps(spec, s => {
    let out = rewriteValues(s, re, `steps.${id}.`);
    if (out.needs?.includes(step.id)) {
      out = { ...out, needs: out.needs.map(n => (n === step.id ? id : n)) };
    }
    if (s === step) {
      out = { ...out, id };
      renamed = out;
    }
    return out;
  });
  return [next, renamed];
}

// renameParam updates ${{ params.<name> }} references in every step and
// the lock.
export function renameParam(spec: StreamSpec, from: string, to: string): StreamSpec {
  const re = new RegExp(`\\bparams\\.${escapeRe(from)}\\b`, 'g');
  const next = mapSteps(spec, s => rewriteValues(s, re, `params.${to}`));
  return next.lock && isTemplate(next.lock) ? { ...next, lock: next.lock.replace(re, `params.${to}`) } : next;
}

// setKey sets or (for an empty value) removes a key of an optional map.
export function setKey(m: Record<string, string> | undefined, key: string, value: string): Record<string, string> | undefined {
  const out = { ...(m ?? {}) };
  if (value === '') {
    delete out[key];
  } else {
    out[key] = value;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

export interface Reference {
  group: string;
  value: string;
  label: string;
}

// referencesFor lists the ${{ ... }} values a step may use.
export function referencesFor(spec: StreamSpec, stepID?: string): Reference[] {
  const out: Reference[] = spec.params
    .filter(p => PARAM_NAME_RE.test(p.name))
    .map(p => ({ group: 'Params', value: `\${{ params.${p.name} }}`, label: `params.${p.name}` }));
  if (stepID !== undefined) {
    const types = new Map(allSteps(spec).map(s => [s.id, s.type ?? '']));
    for (const id of upstreamOf(spec, stepID)) {
      const t = types.get(id);
      const fields = t === APPROVAL ? ['status', 'user'] : t === ARGOCD ? ['revision', 'health', 'images', 'status'] : ['ref', 'sha', 'runId', 'url', 'status'];
      for (const f of fields) {
        out.push({ group: `Step ${id}`, value: `\${{ steps.${id}.${f} }}`, label: `steps.${id}.${f}` });
      }
    }
  }
  for (const b of ['zea.user', 'stream.name', 'stream.run']) {
    out.push({ group: 'Built-in', value: `\${{ ${b} }}`, label: b });
  }
  return out;
}

// stepValueRefs lists the var.name / var.value references the inputs and
// variables of a pipeline step may use: its own values that do not use var.*.
export function stepValueRefs(step: StreamStep): Reference[] {
  const keys = new Set<string>();
  for (const m of [step.inputs, step.variables]) {
    for (const [k, v] of Object.entries(m ?? {})) {
      if (!v.includes('var.')) {
        keys.add(k);
      }
    }
  }
  return [...keys]
    .sort()
    .flatMap(k => [
      { group: 'This step', value: `\${{ var.name.${k} }}`, label: `var.name.${k}` },
      { group: 'This step', value: `\${{ var.value.${k} }}`, label: `var.value.${k}` }
    ]);
}

export const stepProblems = (problems: StreamProblem[], id: string) => problems.filter(p => p.step === id);

// ConnData caches branches, pipelines and run forms of Connections while a
// Stream is edited or started. Failed loads are retried on the next call.
export class ConnData {
  private readonly cache = new Map<string, Promise<unknown>>();

  constructor(private readonly client: ZeaClient) {}

  private get<T>(key: string, fn: () => Promise<T>): Promise<T> {
    let p = this.cache.get(key) as Promise<T> | undefined;
    if (!p) {
      p = fn();
      this.cache.set(key, p);
      p.catch(() => this.cache.delete(key));
    }
    return p;
  }

  branches(conn: string): Promise<BranchList> {
    return this.get(`b\u0000${conn}`, () => this.client.branches(conn));
  }

  pipelines(conn: string, ref: string): Promise<Pipeline[]> {
    return this.get(`p\u0000${conn}\u0000${ref}`, () => this.client.pipelines(conn, ref));
  }

  runForm(conn: string, pipeline: string, ref: string): Promise<RunForm> {
    return this.get(`f\u0000${conn}\u0000${pipeline}\u0000${ref}`, () => this.client.runForm(conn, pipeline, ref));
  }
}
