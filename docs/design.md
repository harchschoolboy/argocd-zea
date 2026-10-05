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
  |  Zea authz: admin group / Connection.allowedGroups
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
- Fine-grained permissions (which Connection a user may see, build,
  administer) are enforced by the backend using `Argocd-User-Groups`.
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

A Connection = one repository + one CI provider + credentials + access rules.

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
  allowedGroups: devs,ops     # who may see and run pipelines
  # provider-specific credentials, e.g.
  githubAppID: "123"
  githubAppInstallationID: "456"
  githubAppPrivateKey: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
```

Two ways to create Connections:
- **UI** - Zea admins (`admins.users` / `admins.groups` in the chart) create,
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

### Access rules

| Who | Can |
|-----|-----|
| Zea admins (`adminGroups` in backend config) | Create/edit/delete Connections and Registries, everything below |
| Members of `allowedGroups` of a Connection | See it, list branches and images, start/cancel/retry pipelines, read logs |
| Others | Do not see the Connection |

Deploy additionally requires Argo CD `sync` on the target Application
(checked by the backend through the Argo CD API on behalf of the user).

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
| `GET /api/v1/me` | any | Identity and `isAdmin` |
| `GET /api/v1/providers` | any | Provider capabilities and credential form schema |
| `GET /api/v1/connections` | any | Connections the user may use |
| `POST /api/v1/connections` | admin | Create |
| `PUT /api/v1/connections/{name}` | admin | Update (UI-managed only) |
| `DELETE /api/v1/connections/{name}` | admin | Delete (UI-managed only) |
| `POST /api/v1/test-connection` | admin | Test unsaved form values |
| `POST /api/v1/connections/{name}/test` | user | Test a saved Connection |
| `GET /api/v1/connections/{name}/branches` | user | Default branch + branches (up to 1000) |
| `GET /api/v1/connections/{name}/pipelines?ref=` | user | Triggerable pipelines at `ref` |
| `GET /api/v1/connections/{name}/pipelines/{pipeline}/form?ref=` | user | Run form |
| `POST /api/v1/connections/{name}/runs` | user | Start: `{pipelineID, ref, inputs, variables}` -> 201 with the run |
| `GET /api/v1/connections/{name}/runs?pipeline=&ref=&limit=` | user | Recent runs (limit up to 50, default 20) |
| `GET /api/v1/connections/{name}/runs/{run}` | user | Run with jobs and steps |
| `POST /api/v1/connections/{name}/runs/{run}/cancel` | user | Cancel -> 202 |
| `POST /api/v1/connections/{name}/runs/{run}/retry` | user | Rerun, body `{failedOnly}` optional -> 202 |

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
| Form | `spec:inputs` header of `.gitlab-ci.yml` at the selected ref, plus free key/value variables (up to 50, key `[A-Za-z0-9_]`) |

Later providers: Gitea/Forgejo Actions, Bitbucket Pipelines, Jenkins.

## Registries and images

A **Registry** is a container registry with credentials, stored as a Secret
in the Connections namespace (label `argocd-zea.io/secret-type: registry`,
UI-managed name `zea-registry-<name>`):

```yaml
stringData:
  name: do
  kind: digitalocean            # digitalocean | oci
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

A registry Secret may have the type `kubernetes.io/dockerconfigjson`, so the
same Secret can serve as an image pull secret elsewhere.

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
| `GET /api/v1/connections/{name}/images?ref=&limit=&refresh=` | user | Images of the Connection, filtered by branch when `ref` is set; `configured: false` without image sources |
| `POST /api/v1/images/preview` | admin | Resolve unsaved sources: `{images, ref}`, 3 tags per repository |
| `GET /api/v1/registry-kinds` | admin | Kinds and credential form schema |
| `GET /api/v1/registries` | admin | Registries with `usedBy` (never credential values) |
| `POST /api/v1/registries` | admin | Create |
| `PUT /api/v1/registries/{name}` | admin | Update (UI-managed only); empty credential keeps the stored value |
| `DELETE /api/v1/registries/{name}` | admin | Delete (UI-managed, unused only) |
| `POST /api/v1/registries/{name}/test` | admin | List repositories of a saved registry |
| `POST /api/v1/test-registry` | admin | Test unsaved form values |

Per-source registry errors do not fail the request: they are returned in
`errors` next to the repositories that did resolve.

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
   admin/allowedGroups authz, provider interface, GitHub and GitLab adapters
   for branches and pipeline list, Connections UI (cards). *(done)*
3. **Run** - trigger form, start, status, jobs, cancel/retry *(done)*; logs,
   play manual jobs *(next)*.
4. **Images** - Registries (DigitalOcean, generic OCI), regex image sources
   on Connections, images per branch with commit links *(done)*; GHCR and
   Docker Hub *(later)*.
5. **Deploy** - plain-YAML app-of-apps write-back, parent refresh/sync.
6. **Hardening** - caching, rate limits, audit log, error UX.

Later: more providers, Helm-generated parents, ApplicationSet generators.