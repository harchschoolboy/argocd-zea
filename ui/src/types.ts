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

export interface Me {
  username: string;
  userId: string;
  groups: string[];
  applicationNamespace: string;
  applicationName: string;
  isAdmin: boolean;
  version: string;
}

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
  allowedGroups: string[];
  images: ImageSource[];
  // Set when the stored image sources cannot be parsed.
  imagesError?: string;
  editable: boolean;
  // Only returned to admins: which credential keys are set (never values).
  credentialKeys?: string[];
}

export interface ConnectionInput {
  name: string;
  provider: string;
  url: string;
  apiURL: string;
  allowedGroups: string[];
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
export type StreamParamType = 'string' | 'choice' | 'boolean' | 'branch';

// StreamParam is asked when a Stream starts and referenced in steps as
// ${{ params.<name> }}.
export interface StreamParam {
  name: string;
  type: StreamParamType | string;
  description?: string;
  default?: string;
  required?: boolean;
  options?: string[];
  // Lists the branches of a branch param.
  connection?: string;
}

export type StepWhen = 'success' | 'failure' | 'always';

export interface StreamStep {
  id: string;
  name?: string;
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
}

export interface StreamStage {
  name?: string;
  steps: StreamStep[];
}

export interface StreamSpec {
  description?: string;
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

export type StreamRunStatus = 'running' | 'succeeded' | 'failed' | 'cancelled';

export type StreamStepStatus = 'pending' | 'starting' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'skipped';

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
  name?: string;
  stage: number;
  connection: string;
  pipeline: string;
}

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
  // The Spec snapshot; only present for a single run.
  spec?: StreamSpec;
}