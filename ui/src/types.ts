// Minimal subset of the Argo CD Application model used by Zea.
// Full model: https://github.com/argoproj/argo-cd/blob/master/ui/src/app/shared/models.ts
export interface Application {
  metadata: {
    name: string;
    namespace: string;
  };
  spec: {
    project: string;
  };
}

export interface ApplicationList {
  items: Application[] | null;
}

// An Argo CD Application as far as Zea shows it (affected apps).
export interface ArgoApp {
  name: string;
  namespace: string;
  project: string;
  images: string[];
  sync?: string;
  health?: string;
}

// An image reference matched to an image source of a Connection.
export interface ImageMatch {
  image: string;
  connection: string;
  registry: string;
  repository: string;
  tag?: string;
  branch?: string;
  commit?: string;
  commitURL?: string;
}

export interface Me {
  username: string;
  userId: string;
  groups: string[];
  applicationNamespace: string;
  applicationName: string;
  isAdmin: boolean;
  version: string;
  // Actions the user may perform on at least some items of each resource.
  permissions: Record<string, string[]>;
}

export type AccessAction = 'view' | 'run' | 'approve' | 'edit';

// can reports whether the actions of an item (or a permissions summary)
// include action.
export const can = (actions: string[] | undefined, action: AccessAction) => (actions ?? []).includes(action);

export interface Capabilities {
  multiplePipelines: boolean;
  liveLogs: boolean;
  retryFailedJobs: boolean;
  retryJob: boolean;
  playManualJobs: boolean;
  retryRun: boolean;
}

export interface CredentialField {
  key: string;
  label: string;
  help?: string;
  secret: boolean;
  multiline?: boolean;
  options?: string[];
  optional?: boolean;
}

export interface CredentialMode {
  id: string;
  label: string;
  help?: string;
  fields: CredentialField[];
}

export interface ProviderInfo {
  id: string;
  name: string;
  urlExample: string;
  apiURLHelp: string;
  capabilities: Capabilities;
  credentialModes: CredentialMode[];
}

// ImageSource selects images of a Connection in a registry. Patterns are Go
// regular expressions; the named groups "branch" and "sha" are recognized.
export interface ImageSource {
  registry: string;
  repository: string;
  tags?: string;
}

export interface Connection {
  name: string;
  provider: string;
  url: string;
  apiURL?: string;
  images: ImageSource[];
  // Set when the stored image sources cannot be parsed.
  imagesError?: string;
  editable: boolean;
  // Only returned with edit access: which credential keys are set (never values).
  credentialKeys?: string[];
  // Actions the user may perform on this connection.
  actions: string[];
}

export interface ConnectionInput {
  name: string;
  provider: string;
  url: string;
  apiURL: string;
  images: ImageSource[];
  // Empty value keeps the stored secret, "-" removes it.
  credentials: Record<string, string>;
}

export interface RegistryKind {
  id: string;
  name: string;
  urlExample: string;
  urlHelp: string;
  credentialModes: CredentialMode[];
}

export interface Registry {
  name: string;
  kind: string;
  url: string;
  editable: boolean;
  credentialKeys: string[];
  // Connections whose image sources use this registry.
  usedBy: string[];
  actions: string[];
}

export interface RegistryInput {
  name: string;
  kind: string;
  url: string;
  // Empty value keeps the stored secret, "-" removes it.
  credentials: Record<string, string>;
}

export interface RegistryTestResult {
  ok: boolean;
  error?: string;
  repositoryCount?: number;
  repositories?: string[];
}

export interface ImageTag {
  name: string;
  digest?: string;
  sizeBytes?: number;
  pushedAt?: string;
  // Full reference, registry/repository:tag.
  image: string;
  commit?: string;
  commitURL?: string;
}

export interface ImageRepository {
  registry: string;
  name: string;
  image: string;
  branch?: string;
  // Number of tags matching the source, before the per-repository limit.
  tagCount: number;
  tags: ImageTag[];
  error?: string;
}

export interface ImageSourceError {
  // 1-based index of the image source; 0 for connection-level errors.
  source: number;
  registry: string;
  error: string;
}

export interface ImagesResult {
  // False when the Connection has no image sources.
  configured?: boolean;
  repositories: ImageRepository[];
  errors?: ImageSourceError[];
  branch?: string;
  truncated?: boolean;
}

export interface Repository {
  fullName: string;
  defaultBranch: string;
  webURL: string;
}

export interface TestResult {
  ok: boolean;
  error?: string;
  repository?: Repository;
}

export interface Branch {
  name: string;
  commitSHA?: string;
  protected: boolean;
}

export interface BranchList {
  defaultBranch: string;
  branches: Branch[];
  truncated: boolean;
}

export interface Pipeline {
  id: string;
  name: string;
  path?: string;
  dispatchable: boolean;
  reason?: string;
}

export type InputType = 'string' | 'boolean' | 'choice' | 'number' | 'environment' | 'array';

export interface RunInput {
  name: string;
  description?: string;
  type: InputType | string;
  required: boolean;
  default?: string;
  options?: string[];
}

export interface RunForm {
  inputs: RunInput[];
  // True when free key/value variables are accepted (GitLab).
  variables: boolean;
  // Variables the pipeline offers in its run form (GitLab variables with a
  // description); sent as variables.
  prefilledVariables?: RunInput[];
  warning?: string;
}

export interface TriggerInput {
  pipelineID: string;
  ref: string;
  inputs: Record<string, string>;
  variables: Record<string, string>;
}

export type RunStatus = 'queued' | 'running' | 'success' | 'failed' | 'canceled' | 'manual' | 'skipped' | 'unknown';

export interface Run {
  id?: string;
  number?: number;
  pipelineID?: string;
  name?: string;
  title?: string;
  ref?: string;
  commitSHA?: string;
  event?: string;
  actor?: string;
  status: RunStatus;
  webURL?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface Step {
  number: number;
  name: string;
  status: RunStatus;
}

export interface Job {
  id: string;
  name: string;
  stage?: string;
  status: RunStatus;
  webURL?: string;
  startedAt?: string;
  finishedAt?: string;
  steps?: Step[];
}

export interface RunDetail extends Run {
  jobs: Job[];
}
export type StreamParamType = 'string' | 'choice' | 'boolean' | 'branch' | 'image';

// StreamParam is asked when a Stream starts and referenced in steps as
// ${{ params.<name> }}.
export type ParamWidget = '' | 'checkbox' | 'switch' | 'radio' | 'select';

export interface StreamParam {
  name: string;
  // Shown in the run form instead of the name.
  label?: string;
  type: StreamParamType | string;
  description?: string;
  default?: string;
  required?: boolean;
  options?: string[];
  // Choice option (key) -> text shown in the run form.
  optionLabels?: Record<string, string>;
  // How the run form shows a boolean or choice param.
  widget?: ParamWidget | string;
  // Values a boolean passes on; empty means true / false.
  trueValue?: string;
  falseValue?: string;
  // Lists the branches of a branch param.
  connection?: string;
  // Part of the lock when the stream locks by params.
  lock?: boolean;
}

export type StepWhen = 'success' | 'failure' | 'always';

export interface StreamStep {
  id: string;
  name?: string;
  // Empty for a pipeline step; 'approval' waits for a user's decision;
  // 'argocd' waits until an Argo CD Application is synced and healthy.
  type?: string;
  connection: string;
  pipeline: string;
  ref: string;
  inputs?: Record<string, string>;
  variables?: Record<string, string>;
  // Empty: every step of the previous non-empty stage.
  needs?: string[];
  when?: StepWhen | string;
  timeout?: string;
  continueOnError?: boolean;
  // Automatic retries of a failed pipeline; 0 or empty leaves it to the user.
  retries?: number;
  retryDelay?: string;
  // Approval steps: shown to approvers.
  message?: string;
  // Approval steps: the run's starter may not decide.
  noSelfApproval?: boolean;
  // Argo CD steps: "<namespace>/<name>" or "<name>" of the Application.
  app?: string;
  // Argo CD steps: wait until the Application runs this image (or tag).
  image?: string;
  // Argo CD steps: do not ask Argo CD to refresh the Application first.
  noRefresh?: boolean;
}

export interface StreamStage {
  name?: string;
  steps: StreamStep[];
}

export interface StreamSpec {
  // Human-readable name shown instead of the name.
  label?: string;
  description?: string;
  // Groups Streams into tabs on the Streams page.
  category?: string;
  // Runs with the same rendered lock (of any stream) run one at a time.
  lock?: string;
  // The lock is the stream name and the values of the params marked lock.
  lockParams?: boolean;
  // What a start does while the lock is held: wait (default) or fail.
  onLocked?: 'queue' | 'reject';
  params: StreamParam[];
  stages: StreamStage[];
}

export interface StreamProblem {
  step?: string;
  param?: string;
  message: string;
}

export interface Stream extends StreamSpec {
  name: string;
  draftOf?: string;
  // False for Streams managed declaratively (git).
  editable: boolean;
  version: string;
  connections: string[];
  problems: StreamProblem[];
  // Actions the user may perform on this stream.
  actions: string[];
}

// StreamList is the Streams page: the visible streams and the shared order
// of their categories and of the streams inside them.
export interface StreamList {
  streams: Stream[];
  categoryOrder: string[];
  streamOrder: string[];
}

export interface StreamInput extends StreamSpec {
  name: string;
  version?: string;
}

export interface StreamExport {
  name: string;
  fileName: string;
  yaml: string;
}

export type StreamRunStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled';

export type StreamStepStatus = 'pending' | 'starting' | 'running' | 'waiting' | 'succeeded' | 'failed' | 'cancelled' | 'skipped';

export interface StreamRunStep {
  id: string;
  status: StreamStepStatus;
  ref?: string;
  runId?: string;
  runNumber?: number;
  url?: string;
  sha?: string;
  providerStatus?: string;
  triggeredAt?: string;
  finishedAt?: string;
  message?: string;
  // Automatic retries so far in the current attempt.
  try?: number;
  // When a pending step that is retried starts again.
  retryAt?: string;
  // Approval steps: the rendered message and the decision.
  prompt?: string;
  decidedBy?: string;
  comment?: string;
  // Argo CD steps: "<namespace>/<name>", the awaited image and the last
  // seen state; sha is the synced revision.
  app?: string;
  image?: string;
  health?: string;
  sync?: string;
  // Comma separated images the Application runs.
  images?: string;
  name?: string;
  stage: number;
  type?: string;
  connection: string;
  pipeline: string;
  noSelfApproval?: boolean;
}

// A finished attempt of a run that was retried; steps are the states the
// retry replaced (succeeded steps are kept and not listed).
export interface StreamAttempt {
  number: number;
  user: string;
  status: StreamRunStatus;
  message?: string;
  startedAt: string;
  finishedAt?: string;
  steps: Omit<StreamRunStep, 'name' | 'stage' | 'type' | 'connection' | 'pipeline' | 'noSelfApproval'>[];
}

// A failed try of a step that was retried automatically.
export type StreamStepTry = Omit<StreamRunStep, 'name' | 'stage' | 'type' | 'connection' | 'pipeline' | 'noSelfApproval'> & {
  // The run attempt the try belongs to.
  attempt: number;
};

export interface StreamRun {
  id: string;
  stream: string;
  user: string;
  params: Record<string, string>;
  status: StreamRunStatus;
  message?: string;
  cancelRequested?: boolean;
  cancelledBy?: string;
  createdAt: string;
  finishedAt?: string;
  steps: StreamRunStep[];
  // Current attempt, starting at 1.
  attempt: number;
  retriedBy?: string;
  retriedAt?: string;
  // Earlier attempts; only present for a single run.
  attempts?: StreamAttempt[];
  // Automatically retried step tries; only present for a single run.
  tries?: StreamStepTry[];
  // The Spec snapshot; only present for a single run.
  spec?: StreamSpec;
  // The rendered lock; a queued run waits for blockedBy ("<stream>/<run>").
  lock?: string;
  queuedAt?: string;
  blockedBy?: string;
}

// Access policy: roles grant actions on resources whose names match a glob
// pattern; bindings give roles to groups ("*" is everyone) or users.
export interface AccessRule {
  resource: string;
  pattern: string;
  actions: string[];
}

export interface AccessRole {
  name: string;
  description?: string;
  rules: AccessRule[];
}

export interface AccessBinding {
  group?: string;
  user?: string;
  roles: string[];
}

export interface AccessPolicy {
  roles: AccessRole[];
  bindings: AccessBinding[];
}

export interface PolicyProblem {
  path: string;
  message: string;
}

export interface ResourceInfo {
  name: string;
  actions: string[];
}

export interface PolicyDocument {
  policy: AccessPolicy;
  yaml: string;
  version: string;
  // False until the policy ConfigMap is created.
  exists: boolean;
  // False when the ConfigMap is managed declaratively (git).
  editable: boolean;
  // Set when the stored policy is invalid; it then grants nothing.
  error?: string;
  // Zea admins from the chart values; they always have every action.
  admins: { users: string[]; groups: string[] };
  resources: ResourceInfo[];
  // Existing item names per resource, for picking rule patterns.
  names?: Record<string, string[]>;
}

export interface PolicyValidation {
  policy: AccessPolicy;
  problems: PolicyProblem[];
  yaml: string;
}

// AuditEvent is one change made through Zea.
export interface AuditEvent {
  time: string;
  user: string;
  action: string;
  resource?: string;
  name?: string;
  // A run id, or run/step for approvals.
  target?: string;
  status: number;
  error?: string;
}

// AuditPage is the stored events of one day (UTC), newest first.
export interface AuditPage {
  days: string[];
  day: string;
  events: AuditEvent[];
  dropped?: number;
  // Days events are kept; 0 when only logged.
  retention: number;
}

export interface PolicyEvaluation {
  isAdmin: boolean;
  roles: string[];
  // Per resource, the existing items the user has some action on.
  items: Record<string, { name: string; actions: string[] }[]>;
}