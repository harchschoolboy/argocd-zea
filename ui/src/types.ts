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
