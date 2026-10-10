# Zea - design

Zea is an Argo CD extension that adds a **Zea** page to the Argo CD left
sidebar. On that page you work with **Connections** - repositories with CI
pipelines (GitHub Actions, GitLab CI, ...). For each Connection you can pick a
branch, start a pipeline, follow its jobs and logs, cancel or retry it, and
later deploy the built image to an Argo CD Application - without leaving
Argo CD.

The name comes from Zea, the ancient naval harbour of Piraeus, whose ship
sheds had stone slipways used to haul triremes ashore and launch them again.

Target: Argo CD 3.x (developed against v3.5.3 / argo-cd chart 10.9.6).

## Principles

1. **Argo CD native.** Connections are stored like Argo CD repositories
   (labelled Secrets), authentication goes through the Argo CD proxy
   extension, installation uses the standard extension mechanisms.
2. **CI agnostic.** Zea never parses or executes pipeline syntax; it drives
   pipelines through provider APIs behind one provider interface.
3. **GitOps.** Deploy is a git commit, never a live patch.
4. **Least privilege.** Per-Connection credentials with the narrowest scope a
   provider allows; short-lived tokens where possible.

## Architecture

```
Browser (Argo CD UI)
  |  Zea page = system-level UI extension (extension-zea.js)
  |
  |  /extensions/zea/*  -> Argo CD proxy extension
  |     + Argocd-Application-Name / Argocd-Project-Name of the ANCHOR app
  v
Argo CD API server
  |  authn (argocd.token cookie)
  |  authz: get on the anchor Application + invoke on extension "zea"
  |  strips Cookie/Authorization, adds Argocd-Username/User-Id/User-Groups,
  |  adds Zea-Proxy-Token from Secret zea-proxy
  v
zea-backend (Go, in the Argo CD namespace)
  |  verifies Zea-Proxy-Token, reads identity headers
  |  Zea authz: admins from values + access policy (roles, bindings)
  |  provider layer: github | gitlab | ...
  v
GitHub API / GitLab API (gitlab.com or self-hosted) / OCI registries / git
```

### UI

The page lists Connections as cards: branch picker, pipelines with **Run**,
and the last run of the selected branch. A card opens the Connection view
(`?connection=<name>[&ref=<branch>][&run=<id>]`, pushed to the browser
history) with the full runs list, jobs and steps, and Test/Edit/Delete. The
page header shows the extension version (webpack `DefinePlugin` from
`ui/package.json`) and links to the repository.

### Anchor Application

The Argo CD proxy extension requires `Argocd-Application-Name` and
`Argocd-Project-Name` on every request and authorizes the user against that
Application. Connections are not tied to Applications, so all Zea calls are
scoped to one **anchor Application** - the Application that deploys Zea
itself (configurable). Consequences:

- Access to Zea at all = `get` on the anchor Application +
  `extensions, invoke, zea`.
- Fine-grained permissions (which Stream, Connection or Registry a user
  may see, run or edit) are enforced by the backend using `Argocd-Username`
  and `Argocd-User-Groups`, see [Access](#access).
- The backend rejects requests scoped to any other Application
  (`ZEA_ANCHOR_APP`). The UI finds the anchor by the label
  `argocd-zea.io/anchor: "true"`.

### Proxy token

The backend Service is reachable by any workload in the cluster. The
`Argocd-*` identity headers are only trustworthy if the request really passed
through the Argo CD API server. Argo CD injects `Zea-Proxy-Token` from the
`zea-proxy` Secret (referenced as `$zea-proxy:token` in `argocd-cm`) and
overrides any client-supplied value; the backend rejects requests without it.

By default the chart generates the token in the cluster: a pre-install /
pre-upgrade hook Job (an Argo CD PreSync hook) runs `zea-backend
init-proxy-token`, which creates the Secret with 32 random bytes if it is
missing and never replaces an existing token. The Job runs under its own
short-lived ServiceAccount (deleted on success), so the long-running backend
has no access to Secrets in the Argo CD namespace. Alternatives:
`proxyToken.source=value` (rendered from values) or `existing` (managed
elsewhere, e.g. SealedSecrets / External Secrets).

Argo CD itself (argocd-cm, argocd-cmd-params-cm, argocd-rbac-cm and the UI
extension installer on argocd-server) is configured through the argo-cd
chart values, not patched by the Zea chart: those objects belong to whoever
manages Argo CD, and patching them from a second owner causes drift.

## Connections

A Connection = one repository + one CI provider + credentials (+ image
sources). Who may use it is decided by the [access policy](#access).

Stored as a Kubernetes Secret, mirroring how Argo CD stores repositories
(`argocd.argoproj.io/secret-type: repository`), but in a dedicated namespace
(`zea-connections` by default):

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: zea-conn-my-service
  namespace: zea-connections
  labels:
    argocd-zea.io/secret-type: connection
stringData:
  name: my-service
  provider: github            # github | gitlab
  url: https://github.com/org/my-service   # self-hosted GitLab/GHE URLs allowed
  # provider-specific credentials, e.g.
  githubAppID: "123"
  githubAppInstallationID: "456"
  githubAppPrivateKey: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
```

Two ways to create Connections:
- **UI** - users with edit access (Zea admins, or a policy role) create,
  edit and delete Connections on the Zea page; the backend writes the Secret
  named `zea-conn-<name>` with the label `argocd-zea.io/managed-by: zea`.
  Credentials are write-only: the API never returns them, and an empty value
  on update keeps the stored one.
- **Declarative** - commit the Secret via SealedSecrets / SOPS / External
  Secrets, exactly like Argo CD repository Secrets. The metadata name is free;
  the `name` key identifies the Connection. Such Connections are read-only in
  the UI.

Why a separate namespace: Kubernetes RBAC cannot limit `list`/`create` on
Secrets by label, so access to Secrets in the Argo CD namespace would expose
`argocd-secret` and repository credentials. The chart creates the namespace
(kept on uninstall) and a Role/RoleBinding limited to it.

## Access

Zea checks every request itself, after Argo CD has let the user in (anchor
Application + `extensions, invoke, zea`). Two layers:

- **Zea admins** - `admins.users` / `admins.groups` in the chart values
  (`ZEA_ADMIN_USERS` / `ZEA_ADMIN_GROUPS`). They have every action and are
  the only ones who see and edit the access policy.
- **Access policy** - roles and bindings in the ConfigMap `zea-policy`
  (key `policy.yaml`) in the Connections namespace, edited on the **Access**
  tab or managed in git.

```yaml
roles:
  - name: developers
    description: Run the dev streams, see all connections
    rules:
      - resource: streams
        pattern: "dev-*"
        actions: [view, run]
      - resource: connections
        pattern: "*"
        actions: [view]
  - name: stream-authors
    rules:
      - resource: streams
        actions: [edit]          # pattern defaults to "*"
      - resource: connections
        actions: [run]
bindings:
  - group: devs
    roles: [developers]
  - user: alice                  # username or user id
    roles: [developers, stream-authors]
  - group: "*"                   # everyone who can open Zea
    roles: []
```

| Resource | view | run | edit |
|----------|------|-----|------|
| `streams` | See it, its runs and their jobs; Export | Start, cancel and retry runs | Create, change, delete, draft, import |
| `connections` | See it, branches, pipelines, runs and jobs, images; Test | Start, cancel and rerun pipelines | Create, change, delete; see which credential keys are set |
| `registries` | See it and which visible Connections use it | - | Create, change, delete; Test |

- `pattern` is a glob over item names (`*`, `?`, `[a-z]`); empty means `*`.
  Any action includes view. Rules only grant; there is no deny. Items the
  user may not view answer 404, missing actions 403. Creating needs edit
  on the new name.
- Saving a Stream needs edit on it and run on every Connection it uses, so
  a Stream author cannot reach pipelines they could not start themselves.
  Running a Stream needs only run on the Stream: access to a Stream
  delegates the use of its Connections. The run form lists branches and the
  run view shows step jobs through the Stream
  (`/streams/{name}/branches`, `/streams/{name}/runs/{run}/jobs/{providerRun}`),
  so no Connection access is needed. Runs follow the Stream name; editing a
  Stream does not change who sees its past runs.
- Image sources of a Connection may only use Registries the user can view
  (checked for sources added in this change).
- Testing unsaved Connection or Registry forms reuses stored credentials
  only with edit on that name.
- Policy edits carry `version` (the ConfigMap resourceVersion) and fail with
  409 when someone else changed it. Like Streams, only a ConfigMap labelled
  `argocd-zea.io/managed-by: zea` and not tracked by Argo CD is editable in
  Zea; a committed one (see `deploy/examples/zea-policy.yaml`) is read-only. Every replica reloads the policy within 5 seconds.
- A stored policy that does not parse or validate grants nothing (admins
  keep every action); the Access tab and the backend log show why.
- Limits: 200 roles, 100 rules per role, 500 bindings.
- Deploy (planned) will additionally require Argo CD `sync` on the target
  Application, checked through the Argo CD API on behalf of the user.

The Access tab edits roles (a rule table with View / Run / Edit per
resource and pattern) and bindings (a matrix of subjects and roles),
validates while editing, imports and exports the YAML and shows what the
unsaved policy grants a given user and groups on the existing items.

**Migration from `allowedGroups`.** Before 0.3.0 a Connection listed the
groups that may use it (`allowedGroups`, `*` for everyone) and only admins
could edit anything. When `zea-policy` does not exist, the backend creates
it on start from these values: per group a role `legacy-<group>`
(`everyone` for `*`) with view and run on its Connections and on the
Streams whose Connections it may all use, bound to that group. Once the
policy exists, `allowedGroups` is ignored (the backend logs a warning for
each Connection that still sets it) and should be removed.

## Providers

Zea talks to CI systems only through their REST APIs. Each provider
implements one interface:

| Method | Purpose |
|--------|---------|
| `ListBranches` | Branch picker |
| `ListPipelines` | GitHub: workflows with `workflow_dispatch`; GitLab: the project pipeline |
| `GetRunForm(pipeline, ref)` | Typed input fields read from the pipeline file at `ref`; whether free variables are allowed |
| `Trigger(pipeline, ref, inputs, variables)` | Start a run, return it (without an id if the provider does not report one) |
| `ListRuns(pipeline?, ref?, limit)` | Recent runs, newest first |
| `GetRun(run)` | Run with jobs (and steps where available) |
| `GetLogs(job, offset)` | Logs, incremental where supported *(next)* |
| `CancelRun`, `RetryRun(failedOnly)` | Pipeline control; `PlayJob` later |
| `Capabilities()` | What the provider supports; the UI hides the rest |

Normalized model: Run -> Job -> Step, statuses `queued`, `running`,
`success`, `failed`, `canceled`, `manual`, `skipped`, `unknown`. GitLab
stages are shown as a job prefix.

Input types: `string`, `boolean`, `choice` (with options), `number`,
`environment`, `array`. Values travel as strings; the GitLab adapter converts
them to typed `inputs` using the form.

### Backend API (phases 2-3)

All paths are relative to `/extensions/zea` and must carry the anchor
Application headers.

| Method and path | Who | Purpose |
|-----------------|-----|---------|
| `GET /api/v1/me` | any | Identity, `isAdmin` and `permissions` (actions per resource on some items) |
| `GET /api/v1/providers` | any | Provider capabilities and credential form schema |
| `GET /api/v1/connections` | any | Connections the user may view, with `actions` |
| `POST /api/v1/connections` | edit | Create |
| `PUT /api/v1/connections/{name}` | edit | Update (UI-managed only) |
| `DELETE /api/v1/connections/{name}` | edit | Delete (UI-managed only) |
| `POST /api/v1/test-connection` | edit | Test unsaved form values |
| `POST /api/v1/connections/{name}/test` | view | Test a saved Connection |
| `GET /api/v1/connections/{name}/branches` | view | Default branch + branches (up to 1000) |
| `GET /api/v1/connections/{name}/pipelines?ref=` | view | Triggerable pipelines at `ref` |
| `GET /api/v1/connections/{name}/pipelines/{pipeline}/form?ref=` | view | Run form |
| `POST /api/v1/connections/{name}/runs` | run | Start: `{pipelineID, ref, inputs, variables}` -> 201 with the run |
| `GET /api/v1/connections/{name}/runs?pipeline=&ref=&limit=` | view | Recent runs (limit up to 50, default 20) |
| `GET /api/v1/connections/{name}/runs/{run}` | view | Run with jobs and steps |
| `POST /api/v1/connections/{name}/runs/{run}/cancel` | run | Cancel -> 202 |
| `POST /api/v1/connections/{name}/runs/{run}/retry` | run | Rerun, body `{failedOnly}` optional -> 202 |

Connections a user may not use answer 404, not 403. Invalid requests and
provider validation errors (GitHub/GitLab 400/422) answer 400/422 with the
provider message; other provider errors answer 502. Every start is logged
with user, Connection, pipeline, ref and parameter names (not values).

### GitHub Actions

| | |
|-|-|
| Auth | GitHub App (JWT -> installation token, scoped per call to one repo and minimal permissions) or fine-grained PAT |
| Branches | `GET /repos/{o}/{r}/branches` |
| Trigger | `POST /repos/{o}/{r}/actions/workflows/{id}/dispatches` with `{ref, inputs, return_run_details: true}` - returns 200 with `workflow_run_id`; on older GHES (422 for the unknown field) retried without it, then 204 without a run id |
| Runs | `GET /actions/workflows/{id}/runs` or `GET /actions/runs`, filter `branch` |
| Status | `GET /actions/runs/{id}`, `GET /actions/runs/{id}/jobs` |
| Logs | `GET /actions/jobs/{id}/logs` - reliable after job completion; no live streaming |
| Control | cancel, rerun, rerun failed jobs (write calls use a separate installation token with `actions: write`) |
| Form | `on.workflow_dispatch.inputs` of the workflow file at the selected ref, in declaration order; max 25 inputs, no free variables |
| Constraints | Workflow must have `workflow_dispatch` and exist on the default branch |

### GitLab CI

| | |
|-|-|
| Auth | Project Access Token (one project, role Developer, scope `api`) or Group Access Token; `read_api` + trigger token for trigger-only setups |
| Branches | `GET /projects/:id/repository/branches` |
| Trigger | `POST /projects/:id/pipeline` with `{ref, variables, inputs}` - returns the pipeline |
| Runs | `GET /projects/:id/pipelines?ref=` |
| Status | `GET /projects/:id/pipelines/:pid`, `GET .../pipelines/:pid/jobs` |
| Logs | `GET /projects/:id/jobs/:jid/trace` - live, incremental |
| Control | cancel, retry (failed and canceled jobs only); retry job and play manual job later |
| Form | `spec:inputs` header of `.gitlab-ci.yml` at the selected ref; prefilled variables (global `variables` with a `description`) from GraphQL `Project.ciConfigVariables(ref)`, which resolves includes, retried briefly while GitLab computes them, with the main file as a fallback; plus free key/value variables (up to 50, key `[A-Za-z0-9_]`). Only changed prefilled values are sent |

Later providers: Gitea/Forgejo Actions, Bitbucket Pipelines, Jenkins.

## Registries and images

A **Registry** is a container registry with credentials, stored as a Secret
in the Connections namespace (label `argocd-zea.io/secret-type: registry`,
UI-managed name `zea-registry-<name>`):

```yaml
stringData:
  name: do
  kind: digitalocean            # digitalocean | gar | oci
  url: registry.digitalocean.com/my-registry
  token: dop_v1_...             # kind-specific credentials
```

Registries are admin-only and have no `allowedGroups`: users never see
registry credentials or the full repository list, only the images selected by
the Connections shared with them.

| Kind | Listing | Credentials |
|------|---------|-------------|
| `digitalocean` | DO API `repositoriesV2` and `tags` (size, digest and push time in one call) | API token (`registry:read`), or a `.dockerconfigjson` whose password is an API token |
| `oci` | Distribution API `/v2/_catalog` (filtered by the URL namespace) and `tags/list`; manifests of the newest 20 tags are read for digest, size (linux/amd64) and creation time | anonymous, username/password, or `.dockerconfigjson`; Bearer token challenges are handled |
| `gar` | Artifact Registry API `packages` and `versions?view=FULL` (tags, digest, size and push time per version); the URL path after `<project>/<repository>` is a package prefix | per-registry Workload Identity Federation (`workloadIdentityProvider`, optional `impersonateServiceAccount`), pod identity (Application Default Credentials: GKE Workload Identity, chart-level federation through `GOOGLE_APPLICATION_CREDENTIALS`), service account JSON key (`serviceAccountKey`) |

A registry Secret may have the type `kubernetes.io/dockerconfigjson`, so the
same Secret can serve as an image pull secret elsewhere.

**Pull secret references.** Kinds that accept docker credentials (`oci`,
`digitalocean`, `gar`) also offer the mode `pullsecret`: the registry stores
`pullSecret: <namespace>/<name>` and the backend reads that Secret's
`.dockerconfigjson` (or legacy `.dockercfg`) on every call, so rotation by
whoever owns the Secret is picked up. Only Secrets in the allowlist
`ZEA_REGISTRY_PULL_SECRETS` (chart value `registries.pullSecrets`) can be
referenced; the chart grants `get` with `resourceNames` on exactly those, and
the UI offers them as a select. For `gar`, the docker login must be
`_json_key`, `_json_key_base64` or `oauth2accesstoken`.

**Workload Identity Federation per registry.** The backend requests a
10-minute token for its own service account through the TokenRequest API
(audience `https:` + provider name, the provider's default allowed audience;
RBAC: `create serviceaccounts/token` with `resourceNames` of that one service
account, chart value `registries.tokenRequest`; the namespace and name come
from the downward API as `ZEA_SERVICE_ACCOUNT_NAMESPACE` /
`ZEA_SERVICE_ACCOUNT_NAME`). It exchanges the token at Google STS and, when a
service account is set, calls `generateAccessToken` to impersonate it. The
mode is hidden when token requests are not configured. Optional credential
fields (`optional: true` in the kind schema) may stay empty; sending `-`
removes a stored one.

Google credentials are cached per key, per provider and service account, or
once for the pod identity;
failed pod-identity lookups are retried after a minute, because probing the
metadata server outside GCP is slow.

GHCR and Docker Hub have no catalog API and are not supported yet (they need
the GitHub packages API and the Docker Hub API).

**Image sources** live on the Connection (Secret key `images`, YAML list):

```yaml
images: |
  - registry: do
    repository: ^vmist-server-(web|static)-(?P<branch>.+)-prod$
    tags: ^[0-9a-f]{7}$
```

- `repository` and `tags` are Go regular expressions (RE2); `tags` is
  optional. At most 20 sources per Connection.
- A named group `branch` in either pattern ties images to a branch: with a
  selected branch only images whose group equals the branch slug are shown
  (lowercase, characters outside `[a-z0-9._-]` replaced by `-`, so
  `feature/X` becomes `feature-x`).
- A group `sha` in the tag pattern links tags to commits; without it, tags
  of 7-40 hex characters are taken as commit SHAs. Links use the provider's
  commit URL.
- Up to 30 repositories per request, 10 newest tags each (`limit` up to
  100). Results are cached for one minute per registry; `refresh=1` bypasses
  the cache, editing or deleting a registry invalidates it.
- Every source must reference an existing registry when the Connection is
  saved; a registry used by Connections cannot be deleted.

### Backend API (phase 4)

| Method and path | Who | Purpose |
|-----------------|-----|---------|
| `GET /api/v1/connections/{name}/images?ref=&limit=&refresh=` | view | Images of the Connection, filtered by branch when `ref` is set; `configured: false` without image sources |
| `POST /api/v1/images/preview` | edit on some Connection, view on the Registries | Resolve unsaved sources: `{images, ref}`, 3 tags per repository |
| `GET /api/v1/registry-kinds` | view on some Registry | Kinds and credential form schema |
| `GET /api/v1/registries` | any | Registries the user may view, with `actions` and `usedBy` (never credential values) |
| `POST /api/v1/registries` | edit | Create |
| `PUT /api/v1/registries/{name}` | edit | Update (UI-managed only); empty credential keeps the stored value |
| `DELETE /api/v1/registries/{name}` | edit | Delete (UI-managed, unused only) |
| `POST /api/v1/registries/{name}/test` | edit | List repositories of a saved registry |
| `POST /api/v1/test-registry` | edit | Test unsaved form values |

Per-source registry errors do not fail the request: they are returned in
`errors` next to the repositories that did resolve.

## Streams

A Stream runs pipelines of several Connections in a fixed order with shared
parameters. It is a list of **stages**: the steps of a stage run in
parallel, a stage starts when the previous one is done. Internally every
step has dependencies (`needs`); a step without `needs` waits for all steps
of the closest earlier non-empty stage, a step with `needs` waits only for
the named steps (which must be in earlier stages). This keeps the editor
simple (columns) while the engine works on a graph, so a free-form graph
editor can be added later without changing the format.

```yaml
description: Build and deploy
params:
  - {name: branch, type: branch, connection: api, default: main, required: true}
  - {name: env, type: choice, options: [dev, prod], default: dev}
stages:
  - name: Build
    steps:
      - {id: api, connection: api, pipeline: "101", ref: "${{ params.branch }}"}
      - {id: web, connection: web, pipeline: "202", ref: "${{ params.branch }}",
         inputs: {env: "${{ params.env }}"}}
  - name: Deploy
    steps:
      - id: deploy
        connection: deploy
        pipeline: "303"
        ref: main
        variables: {API_SHA: "${{ steps.api.sha }}"}
        when: success          # success (default) | failure | always
        timeout: 90m           # 1m..72h, empty = engine default
        continueOnError: false
        retries: 0             # 0..5 automatic retries, 0 = manual only
        retryDelay: 30s        # 0s..1h, empty = 30s
```

- **Params** are asked when the Stream starts: `string`, `choice`
  (`options`), `boolean`, `branch` (branches of `connection`, picked from a
  list). Later: `image` (tags from a registry).
- **References** `${{ ... }}` are allowed in `ref`, `inputs` and
  `variables`: `params.<name>`, `steps.<id>.status|runId|url|sha|ref` (only
  steps that finish before this one), `zea.user`, `stream.name`,
  `stream.run`.
- Limits: 20 stages, 20 steps per stage, 50 steps, 30 params, 50 inputs and
  variables per step, 256 KiB of YAML.

### Storage and GitOps

Streams are ConfigMaps labelled `argocd-zea.io/type: stream` in the
Connections namespace, named `zea-stream-<name>`, with keys `name`,
`stream.yaml` and, for drafts, `draftOf`. Like Connections, only
ConfigMaps labelled `argocd-zea.io/managed-by: zea` and not tracked by Argo
CD (`argocd.argoproj.io/tracking-id` annotation or
`app.kubernetes.io/instance` label) are editable in the UI.

GitOps flow: "Edit as draft" copies any Stream into an editable draft
(`<name>-draft`, `draftOf: <name>`); a draft can be run to try it out.
"Export" returns a declarative ConfigMap (no managed-by label, named after
`draftOf` for drafts) to commit to git. Once Argo CD syncs it, the Stream is
read-only in Zea and the draft can be deleted. Import accepts an exported
ConfigMap or a plain document (`name` plus the spec) and creates a
UI-managed Stream.

Incomplete Streams can be saved; `problems` lists what keeps a Stream from
running (unknown params or steps, missing Connections, invalid ids,
durations, variable names). Updates carry `version` (the ConfigMap
resourceVersion) and fail with 409 when someone else changed the Stream.

### Access rules

See [Access](#access): view, run and edit on `streams` by name; saving
needs run on every Connection of the Stream, running does not.

### Runs

A run is a snapshot of the Spec plus resolved params and the state of every
step, stored as JSON (`run.json`) in a ConfigMap
`zea-srun-<stream>-<run id>` labelled `argocd-zea.io/type: stream-run`,
`argocd-zea.io/stream: <name>` and `argocd-zea.io/phase: active|finished`.
A run starts only when the Stream has no problems (409 otherwise); params
are checked against their definitions (required, choice options, boolean
`true`/`false`) and get their defaults.

The engine is a reconcile loop in the backend (every
`ZEA_STREAM_POLL_INTERVAL`, default 10s, and immediately after a start or
cancel). Each pass, for every active run:

1. Cancel requested: cancel the provider runs of running steps, mark them
   `cancelled`, pending steps `skipped`, the run `cancelled`.
2. Follow running steps with `GetRun` (`success` -> `succeeded`, `failed`,
   `canceled` -> `cancelled`, provider `skipped` -> `skipped`, `manual`
   keeps running with a message). A step running longer than its `timeout`
   (default `ZEA_STREAM_STEP_TIMEOUT`, 6h) is cancelled and fails.
3. Start pending steps whose dependencies have finished:
   - `success`: every direct dependency succeeded (a failure with
     `continueOnError` counts as success); otherwise the step is skipped and
     the skip cascades.
   - `failure`: some step upstream (direct or not) failed or was cancelled
     without `continueOnError`; otherwise skipped.
   - `always`: runs once the dependencies have finished.

   `ref`, `inputs` and `variables` are rendered, the step is saved as
   `starting` and only then triggered, so a crash cannot trigger it twice.
   A trigger error fails the step. A pending step waiting for its retry
   delay is not started yet.
4. When every step has finished, the run is `failed` if a step failed or was
   cancelled without `continueOnError`, otherwise `succeeded`. Older
   finished runs beyond `ZEA_STREAM_RUN_HISTORY` (default 30) are deleted.

A `starting` step without a provider run id (old GHES, or a crash right
after the trigger) is matched with `ListRuns`: the earliest run of the same
pipeline and ref created after the trigger and not owned by another step.
If none appears within 3 minutes the step fails.

All state lives in the ConfigMaps, so a restarted backend resumes active
runs. Writes use the ConfigMap resourceVersion, and a step changes only if
it is still in the state the engine saw, so two backends overlapping during
a rolling update do not trigger a step twice. The engine is still meant for
one replica (`replicaCount: 1`).

**Retry.** A finished run that failed or was cancelled can be retried in
place: steps that succeeded keep their state (and their `steps.<id>.*`
values), every other step goes back to `pending`, and the run becomes
`running` again. The run keeps its Spec snapshot, so edits of the Stream do
not apply; **Run again** starts a new run of the current Stream with the
same params instead. The replaced step states are kept in `attempts`
(number, user, status, start and finish), up to 20 attempts per run.

**Automatic retries.** Retrying is manual by default. A step with
`retries: N` (up to 5) is started again when its pipeline fails, times out
or cannot be triggered; a pipeline cancelled in the provider, a missing
Connection, a rendering error or a run that could not be identified are not
retried. The failed state is appended to the run's `tries` (with the
attempt it belongs to), and the step goes back to `pending` with `try`
increased and `retryAt` set `retryDelay` (default 30s) ahead. Dependants
keep waiting, so they only see the final result. A manual retry starts the
step's automatic retries over.

### UI (S3-S4)

The page has the tabs **Streams** (default), **Connections**,
**Registries** (with view access to some Registry) and, for admins,
**Access**
(`?view=streams[&stream=<name>][&mode=edit|new|run][&srun=<id>]`,
`?view=connections`, `?connection=<name>`, `?view=registries`,
`?view=access`). Buttons follow the user's actions on each item.

- **List** - cards with badges (draft, managed in git, problems), stages and
  steps count, Connections used and the last run (polled while it runs).
  **Run** on a card asks for confirmation (listing the default params) and
  starts the Stream with its defaults; when a required param has no
  default it opens the run form instead. Users with edit on Streams get
  **New stream** and **Import** (paste YAML).
- **Detail** - read-only stage columns, problems, run history (polled while a
  run is active) and the actions **Run**, **Edit** (UI-managed only),
  **Edit as draft**, **Export** (copy or download the ConfigMap) and
  **Delete**.
- **Editor** - name, description and params; a palette of Connections that
  are dragged (or clicked) into stage columns; stages and steps are
  reordered by drag-and-drop, and a drop on the last zone creates a stage.
  The step panel picks the branch and pipeline of the Connection, prefills
  inputs and variables from the provider run form, and inserts
  `${{ ... }}` references to params and upstream steps. Renaming a step or
  a param rewrites its references. The Stream is validated while it is
  edited; it can be saved with problems but not run.
- **Run** - a form for the params (branch params get the branch picker),
  then the run view: stage columns with live step statuses, links to the
  provider runs, **Cancel**, **Retry failed** and **Run again** (the params
  form prefilled from the run). Clicking a step opens its provider jobs and
  job steps (as in the Connection view; the active or failed step opens by
  default) and its earlier tries: automatic retries and earlier attempts.
  A step waiting for a retry shows when it starts again.
- **History** - runs of the Stream with status filters, a step status strip,
  params, attempt number and paging.

### Backend API (phase 7)

| Method and path | Who | Purpose |
|-----------------|-----|---------|
| `GET /api/v1/streams` | any | Streams the user may view, with `actions`, `editable`, `version`, `connections`, `problems` |
| `GET /api/v1/streams/{name}` | view | One Stream |
| `POST /api/v1/streams` | edit + run on its Connections | Create `{name, description, params, stages}` |
| `PUT /api/v1/streams/{name}` | edit + run on its Connections | Replace (UI-managed only); `version` detects concurrent edits |
| `DELETE /api/v1/streams/{name}` | edit | Delete (UI-managed only) |
| `POST /api/v1/streams/{name}/draft` | view; edit on the draft name + run on its Connections | Copy into a draft `{name?}` |
| `GET /api/v1/streams/{name}/export` | view | `{name, fileName, yaml}` - declarative ConfigMap |
| `POST /api/v1/streams/import` | edit + run on its Connections | `{yaml, replace?}` - create, or replace a UI-managed Stream |
| `POST /api/v1/streams/validate` | edit on some Stream | Problems of an unsaved Stream |
| `POST /api/v1/streams/{name}/runs` | run | Start a run `{params}`; 409 when the Stream has problems |
| `GET /api/v1/streams/{name}/runs` | view | Runs, newest first, without the Spec snapshot |
| `GET /api/v1/streams/{name}/runs/{run}` | view | One run with its Spec snapshot |
| `POST /api/v1/streams/{name}/runs/{run}/cancel` | run | Request cancellation (202); 409 when finished |
| `POST /api/v1/streams/{name}/runs/{run}/retry` | run | Run the steps that did not succeed again; 409 when running, succeeded, or retried too often |
| `GET /api/v1/streams/{name}/branches?connection=` | run | Branches of a Connection the Stream uses (branch params) |
| `GET /api/v1/streams/{name}/runs/{run}/jobs/{providerRun}?step=` | view | Jobs of a pipeline run started by that step of the run |

### Backend API (access)

| Method and path | Who | Purpose |
|-----------------|-----|---------|
| `GET /api/v1/policy` | admin | `{policy, yaml, version, exists, editable, error?, admins, resources}` |
| `PUT /api/v1/policy` | admin | Save `{policy, version}`; 400 with problems, 409 on a stale version or a read-only ConfigMap |
| `POST /api/v1/policy/validate` | admin | `{policy}` or `{yaml}` -> normalized `{policy, problems, yaml}` |
| `POST /api/v1/policy/evaluate` | admin | `{policy, user, groups}` -> `{isAdmin, roles, items}`: actions per existing item |

### Plan

- **S1** model, storage, API, validation *(done)*.
- **S2** engine: reconcile loop, run storage, conditions, cancel,
  timeouts, run API *(done)*.
- **S3** UI: Streams tab, drag-and-drop editor, run form, live run view,
  history, import/export *(done)*.
- **S4** retry of failed steps, run again, step jobs in the run view,
  history filters *(done)*.
- **S5** `if` expressions, `image` params.
- Later: passing outputs between steps (an artifact such as
  `zea-outputs.json`; neither provider exposes job outputs through the API).

## Deploy

*Open topic - to be planned after phase 4. The notes below are the original
sketch.*

An Application is linked to a Connection with the annotation
`argocd-zea.io/connection: <name>` (plus `argocd-zea.io/image`,
`argocd-zea.io/tag-template`, aliases for multiple images).

Setup in scope for v1: Applications are defined as plain YAML files in git and
managed by a parent app-of-apps; the image tag lives in the child
Application's spec (`helm.parameters`, `helm.valuesObject`, `kustomize.images`).

1. Find the parent Application via Argo CD resource tracking.
2. Find the file that defines the child Application inside the parent's
   `repoURL` / `path` / `targetRevision` (or use `argocd-zea.io/write-back-file`).
3. Edit the tag with a YAML node-level edit that preserves comments and
   formatting - the diff is one line.
4. Commit (or open a PR/MR with `argocd-zea.io/write-back-mode: pr`) with
   `[skip ci]` and a trailer identifying the Argo CD user.
5. Refresh/sync the parent through the Argo CD API on behalf of the user.

Images: listed from the registries of the Connection (see
[Registries and images](#registries-and-images)).

Not in v1: parents generated by Helm charts, ApplicationSet generators. Live
parameter overrides via the Argo CD API are intentionally not used: a parent
with selfHeal would revert them.

## Phases

1. **Skeleton** - sidebar page, backend behind the proxy, auth chain, Helm
   chart, release artifacts. *(done; currently lists annotated Applications -
   to be replaced by Connections in phase 2)*
2. **Connections + providers core** - Connection Secrets, anchor Application,
   admin/allowedGroups authz (replaced by the access policy in 0.3.0), provider interface, GitHub and GitLab adapters
   for branches and pipeline list, Connections UI (cards). *(done)*
3. **Run** - trigger form, start, status, jobs, cancel/retry *(done)*; logs,
   play manual jobs *(next)*.
4. **Images** - Registries (DigitalOcean, generic OCI), regex image sources
   on Connections, images per branch with commit links *(done)*; GHCR and
   Docker Hub *(later)*.
5. **Deploy** - plain-YAML app-of-apps write-back, parent refresh/sync.
6. **Hardening** - caching, rate limits, audit log, error UX; access policy
   with roles and bindings *(0.3.0)*.
7. **Streams** - multi-Connection pipelines, see [Streams](#streams)
   *(S1-S4 done)*.

Later: more providers, Helm-generated parents, ApplicationSet generators.