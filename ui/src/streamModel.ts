import { ZeaClient } from './api';
import { BranchList, Pipeline, RunForm, StreamParam, StreamProblem, StreamSpec, StreamStage, StreamStep } from './types';

// Keep in sync with backend/internal/streams/stream.go.
export const STREAM_NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
export const STEP_ID_RE = /^[a-z][a-z0-9_-]{0,39}$/;
export const PARAM_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]{0,49}$/;
export const MAX_STREAM_NAME = 50;

export const isTemplate = (v: string) => v.includes('${{');

export function emptySpec(): StreamSpec {
  return { description: '', params: [], stages: [{ name: 'Build', steps: [] }] };
}

// specOf copies the editable part of a Stream (or a run snapshot).
export function specOf(s: { description?: string; params?: StreamParam[] | null; stages?: StreamStage[] | null }): StreamSpec {
  return {
    description: s.description ?? '',
    params: (s.params ?? []).map(p => ({ ...p, options: p.options ? [...p.options] : undefined })),
    stages: (s.stages ?? []).map(st => ({ ...st, steps: (st.steps ?? []).map(x => ({ ...x })) })),
  };
}

export const allSteps = (spec: StreamSpec): StreamStep[] => spec.stages.flatMap(s => s.steps);

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
  const next = { ...step, ref: fix(step.ref), inputs: fixMap(step.inputs), variables: fixMap(step.variables) };
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

// renameParam updates ${{ params.<name> }} references in every step.
export function renameParam(spec: StreamSpec, from: string, to: string): StreamSpec {
  const re = new RegExp(`\\bparams\\.${escapeRe(from)}\\b`, 'g');
  return mapSteps(spec, s => rewriteValues(s, re, `params.${to}`));
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
    for (const id of upstreamOf(spec, stepID)) {
      for (const f of ['ref', 'sha', 'runId', 'url', 'status']) {
        out.push({ group: `Step ${id}`, value: `\${{ steps.${id}.${f} }}`, label: `steps.${id}.${f}` });
      }
    }
  }
  for (const b of ['zea.user', 'stream.name', 'stream.run']) {
    out.push({ group: 'Built-in', value: `\${{ ${b} }}`, label: b });
  }
  return out;
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
