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

export interface Connection {
  name: string;
  provider: string;
  url: string;
  apiURL?: string;
  allowedGroups: string[];
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
  // Empty value keeps the stored secret, "-" removes it.
  credentials: Record<string, string>;
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