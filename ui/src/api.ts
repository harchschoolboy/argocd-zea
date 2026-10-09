import {
  Application,
  ApplicationList,
  BranchList,
  Connection,
  ConnectionInput,
  ImageSource,
  ImagesResult,
  Me,
  Pipeline,
  ProviderInfo,
  Registry,
  RegistryInput,
  RegistryKind,
  RegistryTestResult,
  Run,
  RunDetail,
  RunForm,
  Stream,
  StreamExport,
  StreamInput,
  StreamProblem,
  StreamRun,
  TestResult,
  TriggerInput,
} from './types';

export class ApiError extends Error {
  constructor(public readonly status: number, message: string) {
    super(message);
  }
}

// Argo CD may be served under a sub-path (server.rootpath / server.basehref).
function baseHref(): string {
  const href = document.querySelector('base')?.getAttribute('href') ?? '/';
  return href.endsWith('/') ? href : `${href}/`;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${baseHref()}${path.replace(/^\//, '')}`, {
    credentials: 'same-origin',
    ...init,
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = await res.json();
      message = body.error ?? body.message ?? message;
    } catch {
      // Non-JSON error body; keep statusText.
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 202 || res.status === 204) {
    return undefined as T;
  }
  return res.json() as Promise<T>;
}

export const ANCHOR_LABEL = 'argocd-zea.io/anchor';

// Finds the anchor Application through the regular Argo CD API, so the result
// is already filtered by the user's RBAC.
export async function findAnchors(): Promise<Application[]> {
  const fields = ['items.metadata.name', 'items.metadata.namespace', 'items.spec.project'].join(',');
  const list = await request<ApplicationList>(
    `api/v1/applications?selector=${encodeURIComponent(`${ANCHOR_LABEL}=true`)}&fields=${encodeURIComponent(fields)}`,
  );
  return (list.items ?? []).sort((a, b) =>
    `${a.metadata.namespace}/${a.metadata.name}`.localeCompare(`${b.metadata.namespace}/${b.metadata.name}`),
  );
}

// Client for the Zea backend. Every call goes through the Argo CD proxy
// extension scoped to the anchor Application: Argo CD checks that the user
// can read it and may invoke the "zea" extension before forwarding.
export class ZeaClient {
  constructor(private readonly anchor: Application) {}

  private call<T>(path: string, init?: RequestInit & { json?: unknown }): Promise<T> {
    const headers = new Headers(init?.headers);
    headers.set('Argocd-Application-Name', `${this.anchor.metadata.namespace}:${this.anchor.metadata.name}`);
    headers.set('Argocd-Project-Name', this.anchor.spec.project);
    let body = init?.body;
    if (init?.json !== undefined) {
      headers.set('Content-Type', 'application/json');
      body = JSON.stringify(init.json);
    }
    return request<T>(`extensions/zea/${path.replace(/^\//, '')}`, { ...init, headers, body });
  }

  me(): Promise<Me> {
    return this.call<Me>('api/v1/me');
  }

  async providers(): Promise<ProviderInfo[]> {
    return (await this.call<{ providers: ProviderInfo[] }>('api/v1/providers')).providers;
  }

  async connections(): Promise<Connection[]> {
    return (await this.call<{ connections: Connection[] }>('api/v1/connections')).connections;
  }

  createConnection(input: ConnectionInput): Promise<Connection> {
    return this.call<Connection>('api/v1/connections', { method: 'POST', json: input });
  }

  updateConnection(input: ConnectionInput): Promise<Connection> {
    return this.call<Connection>(`api/v1/connections/${encodeURIComponent(input.name)}`, { method: 'PUT', json: input });
  }

  deleteConnection(name: string): Promise<void> {
    return this.call<void>(`api/v1/connections/${encodeURIComponent(name)}`, { method: 'DELETE' });
  }

  testDraft(input: ConnectionInput): Promise<TestResult> {
    return this.call<TestResult>('api/v1/test-connection', { method: 'POST', json: input });
  }

  testConnection(name: string): Promise<TestResult> {
    return this.call<TestResult>(`api/v1/connections/${encodeURIComponent(name)}/test`, { method: 'POST' });
  }

  branches(name: string): Promise<BranchList> {
    return this.call<BranchList>(`api/v1/connections/${encodeURIComponent(name)}/branches`);
  }

  async pipelines(name: string, ref: string): Promise<Pipeline[]> {
    const q = ref ? `?ref=${encodeURIComponent(ref)}` : '';
    return (await this.call<{ pipelines: Pipeline[] }>(`api/v1/connections/${encodeURIComponent(name)}/pipelines${q}`))
      .pipelines;
  }

  runForm(name: string, pipelineID: string, ref: string): Promise<RunForm> {
    return this.call<RunForm>(
      `api/v1/connections/${encodeURIComponent(name)}/pipelines/${encodeURIComponent(pipelineID)}/form?ref=${encodeURIComponent(ref)}`,
    );
  }

  trigger(name: string, input: TriggerInput): Promise<Run> {
    return this.call<Run>(`api/v1/connections/${encodeURIComponent(name)}/runs`, { method: 'POST', json: input });
  }

  async runs(name: string, filter: { pipeline?: string; ref?: string; limit?: number }): Promise<Run[]> {
    const q = new URLSearchParams();
    if (filter.pipeline) q.set('pipeline', filter.pipeline);
    if (filter.ref) q.set('ref', filter.ref);
    if (filter.limit) q.set('limit', String(filter.limit));
    const qs = q.toString();
    return (await this.call<{ runs: Run[] }>(`api/v1/connections/${encodeURIComponent(name)}/runs${qs ? `?${qs}` : ''}`)).runs;
  }

  run(name: string, id: string): Promise<RunDetail> {
    return this.call<RunDetail>(`api/v1/connections/${encodeURIComponent(name)}/runs/${encodeURIComponent(id)}`);
  }

  async cancelRun(name: string, id: string): Promise<void> {
    await this.call<void>(`api/v1/connections/${encodeURIComponent(name)}/runs/${encodeURIComponent(id)}/cancel`, {
      method: 'POST',
    });
  }

  async retryRun(name: string, id: string, failedOnly: boolean): Promise<void> {
    await this.call<void>(`api/v1/connections/${encodeURIComponent(name)}/runs/${encodeURIComponent(id)}/retry`, {
      method: 'POST',
      json: { failedOnly },
    });
  }

  images(name: string, opts: { ref?: string; limit?: number; refresh?: boolean }): Promise<ImagesResult> {
    const q = new URLSearchParams();
    if (opts.ref) q.set('ref', opts.ref);
    if (opts.limit) q.set('limit', String(opts.limit));
    if (opts.refresh) q.set('refresh', '1');
    const qs = q.toString();
    return this.call<ImagesResult>(`api/v1/connections/${encodeURIComponent(name)}/images${qs ? `?${qs}` : ''}`);
  }

  previewImages(images: ImageSource[], ref: string): Promise<ImagesResult> {
    return this.call<ImagesResult>('api/v1/images/preview', { method: 'POST', json: { images, ref } });
  }

  async registryKinds(): Promise<RegistryKind[]> {
    return (await this.call<{ kinds: RegistryKind[] }>('api/v1/registry-kinds')).kinds;
  }

  async registries(): Promise<Registry[]> {
    return (await this.call<{ registries: Registry[] }>('api/v1/registries')).registries;
  }

  createRegistry(input: RegistryInput): Promise<Registry> {
    return this.call<Registry>('api/v1/registries', { method: 'POST', json: input });
  }

  updateRegistry(input: RegistryInput): Promise<Registry> {
    return this.call<Registry>(`api/v1/registries/${encodeURIComponent(input.name)}`, { method: 'PUT', json: input });
  }

  deleteRegistry(name: string): Promise<void> {
    return this.call<void>(`api/v1/registries/${encodeURIComponent(name)}`, { method: 'DELETE' });
  }

  testRegistry(name: string): Promise<RegistryTestResult> {
    return this.call<RegistryTestResult>(`api/v1/registries/${encodeURIComponent(name)}/test`, { method: 'POST' });
  }

  testRegistryDraft(input: RegistryInput): Promise<RegistryTestResult> {
    return this.call<RegistryTestResult>('api/v1/test-registry', { method: 'POST', json: input });
  }
  async streams(): Promise<Stream[]> {
    return (await this.call<{ streams: Stream[] }>('api/v1/streams')).streams;
  }

  stream(name: string): Promise<Stream> {
    return this.call<Stream>(`api/v1/streams/${encodeURIComponent(name)}`);
  }

  createStream(input: StreamInput): Promise<Stream> {
    return this.call<Stream>('api/v1/streams', { method: 'POST', json: input });
  }

  updateStream(input: StreamInput): Promise<Stream> {
    return this.call<Stream>(`api/v1/streams/${encodeURIComponent(input.name)}`, { method: 'PUT', json: input });
  }

  deleteStream(name: string): Promise<void> {
    return this.call<void>(`api/v1/streams/${encodeURIComponent(name)}`, { method: 'DELETE' });
  }

  draftStream(name: string, draftName?: string): Promise<Stream> {
    return this.call<Stream>(`api/v1/streams/${encodeURIComponent(name)}/draft`, {
      method: 'POST',
      json: { name: draftName ?? '' },
    });
  }

  exportStream(name: string): Promise<StreamExport> {
    return this.call<StreamExport>(`api/v1/streams/${encodeURIComponent(name)}/export`);
  }

  importStream(yaml: string, replace: boolean): Promise<Stream> {
    return this.call<Stream>('api/v1/streams/import', { method: 'POST', json: { yaml, replace } });
  }

  async validateStream(input: StreamInput): Promise<StreamProblem[]> {
    return (await this.call<{ problems: StreamProblem[] }>('api/v1/streams/validate', { method: 'POST', json: input })).problems;
  }

  async streamRuns(name: string): Promise<StreamRun[]> {
    return (await this.call<{ runs: StreamRun[] }>(`api/v1/streams/${encodeURIComponent(name)}/runs`)).runs;
  }

  streamRun(name: string, id: string): Promise<StreamRun> {
    return this.call<StreamRun>(`api/v1/streams/${encodeURIComponent(name)}/runs/${encodeURIComponent(id)}`);
  }

  startStream(name: string, params: Record<string, string>): Promise<StreamRun> {
    return this.call<StreamRun>(`api/v1/streams/${encodeURIComponent(name)}/runs`, { method: 'POST', json: { params } });
  }

  async cancelStreamRun(name: string, id: string): Promise<void> {
    await this.call<void>(`api/v1/streams/${encodeURIComponent(name)}/runs/${encodeURIComponent(id)}/cancel`, {
      method: 'POST',
    });
  }
}

export function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.status) {
      case 401:
        return 'Session expired. Please log in to Argo CD again.';
      case 403:
        return `Access denied: ${err.message}`;
      case 404:
        return err.message && err.message !== 'Not Found'
          ? err.message
          : 'Zea backend not found. Is the proxy extension enabled (server.enable.proxy.extension) and extension.config.zea set in argocd-cm?';
      case 400:
      case 422:
        return err.message;
      case 502:
        return err.message || 'Upstream error.';
      case 503:
      case 504:
        return `Zea backend or upstream is unreachable (${err.status}): ${err.message}`;
      default:
        return `${err.status}: ${err.message}`;
    }
  }
  return err instanceof Error ? err.message : String(err);
}
