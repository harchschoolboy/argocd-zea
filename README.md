# Zea

Build and deploy from one screen inside Argo CD.

Zea is an [Argo CD](https://argo-cd.readthedocs.io/) extension that adds a
**Zea** page to the Argo CD sidebar. There you manage Connections to
repositories (GitHub Actions, GitLab CI), pick a branch, start a pipeline,
follow its jobs and logs, and deploy a built image the GitOps way (a commit to
the app-of-apps repo).

> Status: phase 4. Connections (GitHub and GitLab), branches, starting
> pipelines with parameters, run status and jobs, cancel and rerun, and
> container images per branch (DigitalOcean and OCI registries) work. Logs
> and Deploy are in progress - see [docs/design.md](docs/design.md).

## How it fits into Argo CD

- **UI**: a system-level UI extension, installed by `argocd-extension-installer`.
- **Backend**: a small Go service behind the Argo CD **proxy extension**;
  Argo CD authenticates the user and enforces RBAC before forwarding.
- **Configuration**: Connections are labelled Secrets
  (`argocd-zea.io/secret-type: connection`), like Argo CD repository Secrets.
  Zea admins create them in the UI, or you commit them declaratively.
- **Access**: Argo CD RBAC (`extensions, invoke, zea` + `get` on the anchor
  Application that deploys Zea), then per-Connection `allowedGroups`.

## Install

Zea has two parts:

| Part | What it is | Installed by |
|------|------------|--------------|
| Backend | Go service, its proxy token, the `zea-connections` namespace and RBAC | the Zea Helm chart (`oci://ghcr.io/harchschoolboy/charts/zea`, source in `deploy/helm/zea`) |
| Argo CD side | UI extension in `argocd-server`, proxy extension config, RBAC policy | your Argo CD installation (argo-cd chart values or manifests) |

The Zea chart never patches Argo CD objects: they belong to whoever manages
Argo CD, and a second owner would cause permanent drift. Instead the chart
prints a ready-to-merge values snippet for the argo-cd chart.

### Requirements

- Argo CD 3.x (tested with v3.5.3 / argo-cd chart 10.9.6).
- Permissions to install into the Argo CD namespace (the backend must live
  there; Argo CD only resolves `$secret:key` references in its own namespace)
  and to create the `zea-connections` namespace.
- Outbound HTTPS from the backend to the GitHub / GitLab APIs, and from
  `argocd-server` to `github.com` to download the UI extension at startup.

In the commands below the Argo CD namespace is `argocd`, the Helm release and
the anchor Application are both called `zea`, and the release is `v0.1.8`.

### Step 1. Install the backend

#### Option A: as an Argo CD Application (recommended)

The Application that deploys Zea is also the **anchor**: Argo CD authorizes
every Zea request against it ("applications, get"), so anyone who can see it
can open Zea.

[deploy/examples/zea-application.yaml](deploy/examples/zea-application.yaml)
contains two objects:

- an AppProject `zea` that allows exactly what the chart needs: the chart
  source, the `argocd` and `zea-connections` namespaces, and the resource
  kinds it renders. Zea therefore works even when the `default` project is
  restricted.
- the Application `zea` itself.

Set your admins in the file, then apply it:

```bash
kubectl apply -n argocd -f deploy/examples/zea-application.yaml
```

If you prefer an existing project, copy the project's `sourceRepos`,
`destinations` and resource whitelists into it and change `spec.project`.

What the chart does on sync:

1. A PreSync hook Job runs `zea-backend init-proxy-token`. If the Secret
   `argocd/zea-proxy` does not exist, the Job creates it with a random token
   and the label `app.kubernetes.io/part-of=argocd`. The Job never replaces
   an existing token. The Job, its ServiceAccount and its Role are deleted
   once it succeeds.
2. Creates the `zea-connections` namespace (kept on uninstall), a Role that
   only allows the backend to manage Secrets there, and the backend
   Deployment and Service.

The anchor defaults to `<release namespace>:<release name>`, which is
`argocd:zea` here. Set `anchorApplication` explicitly if you use
`spec.source.helm.releaseName` or keep Applications in another namespace.

The chart is a public OCI artifact, so Argo CD needs no repository
credentials for it. Note the Argo CD syntax: `repoURL:
ghcr.io/harchschoolboy/charts` (no `oci://`) plus `chart: zea`. To deploy
from git instead (e.g. an unreleased commit), use `repoURL:
https://github.com/harchschoolboy/argocd-zea`, `path: deploy/helm/zea`, and
set `image.tag`.

#### Option B: with the Helm CLI

```bash
helm install zea oci://ghcr.io/harchschoolboy/charts/zea --version 0.1.8 \
  -n argocd \
  --set anchorApplication=argocd:<existing-app> \
  --set 'admins.users={admin}'
kubectl -n argocd label application <existing-app> argocd-zea.io/anchor=true
```

Without an Argo CD Application for Zea itself, pick an existing Application
that your Zea users can see as the anchor. A good choice is the one that
manages Argo CD. Label it as shown.

Check the result:

```bash
kubectl -n argocd get secret zea-proxy                         # created by the hook
kubectl -n argocd rollout status deploy/zea
kubectl -n argocd get application zea --show-labels            # argocd-zea.io/anchor=true
```

`helm get notes zea -n argocd` prints the Argo CD snippet for Step 2 with
your actual service name, Secret name and versions filled in.

### Step 2. Configure Argo CD

Do this after Step 1. Argo CD reads the token from `zea-proxy` when it loads
the extension config: at startup and on every `argocd-cm` change. If Argo CD
was configured before the Secret existed, restart `argocd-server`.

Four things have to be enabled on the Argo CD side:

- the UI extension (installed into `argocd-server` by
  [argocd-extension-installer](https://github.com/argoproj-labs/argocd-extension-installer));
- the proxy extension feature flag;
- the `zea` proxy route with the token header;
- the RBAC permission to invoke the extension.

#### Argo CD installed with the argo-cd Helm chart

Merge [deploy/examples/argocd-values.yaml](deploy/examples/argocd-values.yaml)
into your argo-cd values and upgrade Argo CD:

```yaml
server:
  extensions:
    enabled: true
    extensionList:
      - name: extension-zea
        env:
          - name: EXTENSION_NAME
            value: zea
          - name: EXTENSION_VERSION
            value: v0.1.8
          - name: EXTENSION_URL
            value: https://github.com/harchschoolboy/argocd-zea/releases/download/v0.1.8/extension-zea.tar.gz
          - name: EXTENSION_CHECKSUM_URL
            value: https://github.com/harchschoolboy/argocd-zea/releases/download/v0.1.8/extension-zea_checksums.txt

configs:
  params:
    server.enable.proxy.extension: "true"
  cm:
    extension.config.zea: |
      services:
        - url: http://zea.argocd.svc:8080
          headers:
            - name: Zea-Proxy-Token
              value: '$zea-proxy:token'
  rbac:
    policy.csv: |
      p, role:readonly, extensions, invoke, zea, allow
```

If you already have `policy.csv`, append the line instead of replacing it.

#### Argo CD that manages itself (GitOps)

If Argo CD is deployed by its own Application (app of apps, ApplicationSet),
do not patch it from the cluster: the next sync of the Argo CD Application
reverts the change. Put both Zea and its Argo CD configuration into the same
git repository instead. One commit plus one sync installs everything:

1. Add Zea as one more Application in the Argo CD namespace, the same way as
   your other platform apps: OCI chart `ghcr.io/harchschoolboy/charts`, chart
   `zea`, release name `zea`, label `argocd-zea.io/anchor: "true"`. Add
   `ghcr.io/harchschoolboy/charts` to the project's `sourceRepos`.
2. Merge the values from the previous section into the argo-cd values file
   in git.
3. Commit, let Zea sync first (it creates `zea-proxy`), then sync the Argo CD
   Application. The changed pod template restarts `argocd-server`, which then
   loads the extension and the token.

If Zea Applications are generated by an ApplicationSet with Go templates, the
template can take labels from the generator, for example from a `labels` map
in a git file generator's `app.yaml`:

```yaml
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  templatePatch: |
    {{- if hasKey . "labels" }}
    metadata:
      labels:
        {{- toYaml .labels | nindent 4 }}
    {{- end }}
```

Remove the Application from Option A first if you tried it, so there is only
one Zea installation.

#### Argo CD installed from plain manifests or Kustomize

1. Enable the proxy extension and add the route:

   ```bash
   kubectl -n argocd patch cm argocd-cmd-params-cm --type merge \
     -p '{"data":{"server.enable.proxy.extension":"true"}}'
   kubectl -n argocd patch cm argocd-cm --type merge -p '{"data":{"extension.config.zea":"services:\n  - url: http://zea.argocd.svc:8080\n    headers:\n      - name: Zea-Proxy-Token\n        value: $zea-proxy:token\n"}}'
   ```

2. Add `p, role:readonly, extensions, invoke, zea, allow` to `policy.csv` in
   `argocd-rbac-cm`.

3. Add the extension installer to `argocd-server`, for example as a
   Kustomize strategic merge patch:

   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: argocd-server
   spec:
     template:
       spec:
         initContainers:
           - name: extension-zea
             image: quay.io/argoprojlabs/argocd-extension-installer:v1.1.0
             env:
               - name: EXTENSIONS_DIR
                 value: /tmp/extensions
               - name: EXTENSION_NAME
                 value: zea
               - name: EXTENSION_VERSION
                 value: v0.1.8
               - name: EXTENSION_URL
                 value: https://github.com/harchschoolboy/argocd-zea/releases/download/v0.1.8/extension-zea.tar.gz
               - name: EXTENSION_CHECKSUM_URL
                 value: https://github.com/harchschoolboy/argocd-zea/releases/download/v0.1.8/extension-zea_checksums.txt
             securityContext:
               runAsNonRoot: true
               runAsUser: 1000
               readOnlyRootFilesystem: true
               allowPrivilegeEscalation: false
               capabilities:
                 drop: [ALL]
             volumeMounts:
               - name: extensions
                 mountPath: /tmp/extensions/
               - name: tmp
                 mountPath: /tmp
         containers:
           - name: argocd-server
             volumeMounts:
               - name: extensions
                 mountPath: /tmp/extensions/
         volumes:
           - name: extensions
             emptyDir: {}
   ```

4. Restart the server: `kubectl -n argocd rollout restart deploy/argocd-server`.

### Step 3. Verify

Open Argo CD. A **Zea** item appears in the left sidebar. Log in as a Zea admin
(by default the local `admin`) to see the **Add connection** button.

To check the proxy path from the command line, use an Argo CD session token:

```bash
ARGOCD=https://argocd.example.com
TOKEN=$(curl -sk $ARGOCD/api/v1/session -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<password>"}' | jq -r .token)
curl -sk $ARGOCD/extensions/zea/api/v1/me \
  -H "Cookie: argocd.token=$TOKEN" \
  -H 'Argocd-Application-Name: argocd:zea' -H 'Argocd-Project-Name: zea'
```

The response should contain `"isAdmin": true` for an admin.

### Step 4. Access control

Who can do what:

| Action | Requirement |
|--------|-------------|
| Open Zea | Argo CD RBAC: `extensions, invoke, zea` and `applications, get` on the anchor |
| See and use a Connection | Zea admin, or a member of one of the Connection's `allowedGroups` (`*` = everyone who can open Zea) |
| Create, edit, delete Connections | Zea admin: `admins.users` (Argo CD usernames) or `admins.groups` (SSO groups) in the chart values |

The example policy grants Zea to `role:readonly`. To limit it to an SSO
group instead:

```csv
p, role:zea, extensions, invoke, zea, allow
p, role:zea, applications, get, zea/zea, allow
g, my-sso-group, role:zea
```

Local accounts such as `admin` have no groups, so list them in
`admins.users`.

### Step 5. Add Connections

A Connection is one repository plus a CI provider and credentials. Create
Connections on the Zea page as an admin (**Test** checks the credentials
before saving), or commit them as Secrets in `zea-connections`, see
[deploy/examples/connection-secret.yaml](deploy/examples/connection-secret.yaml).
Declarative Connections are read-only in the UI. Credentials are write-only:
the API never returns them.

**GitHub App (recommended).** Org or user settings -> Developer settings ->
GitHub Apps -> New GitHub App:

- Webhook: off.
- Repository permissions: **Actions** read and write (Run needs write),
  **Contents** read, **Metadata** read.
- Install the App on the repositories you want to connect.
- In Zea, enter the App ID, the installation ID (the number at the end of the
  installation URL `.../settings/installations/<id>`) and a generated
  private key (PEM).

For every request Zea issues a short-lived installation token that only
covers the one repository and only has the permissions it needs.

**GitHub token.** A fine-grained personal access token limited to the
repository, with Actions read and write and Contents read.

**GitLab.** A project access token (Project -> Settings -> Access tokens) with
scope `api` and role Developer. Use Maintainer if pipelines have to run on
protected branches that Developers cannot push to. For self-managed GitLab
served under a sub-path, set the API URL (`https://host/gitlab/api/v4`).

### Running pipelines

Pick a branch on a Connection card and press **Run** next to a workflow. Zea
reads the parameters from the pipeline file at that branch and shows a form.

- **GitHub Actions**: the workflow needs an `on.workflow_dispatch` trigger on
  the selected branch, and the workflow must also exist on the default branch
  (a GitHub rule). The form is built from `workflow_dispatch.inputs`
  (`string`, `boolean`, `choice`, `number`, `environment`), with
  descriptions, defaults and required marks. GitHub accepts only the declared
  inputs, at most 25.
- Text inputs whose name looks like a branch (`branch`, `app_branch`, `ref`,
  `git-ref`, `gitRef`) get the branch list of the Connection's repository;
  any other value can still be typed.
- **GitLab CI**: the form is built from the `spec:inputs` header of
  `.gitlab-ci.yml` (inputs without a default are required). In addition, any
  CI/CD variables can be passed as key/value pairs; they arrive in jobs as
  regular environment variables. Passing variables needs the Developer role
  or higher, and the project setting "Minimum role to use pipeline
  variables" must allow it.

Each card shows the last run of the selected branch. Click the card (or its
name, or the last run) to open the Connection view: it keeps the branch and
Run controls and lists recent runs of the selected branch (or all branches)
with status, jobs and steps; failed jobs open with their steps, and active
runs refresh automatically. The view has its own URL
(`/zea?connection=<name>`), so it can be bookmarked and the browser Back
button returns to the list. **Test**, **Edit** and **Delete** are in this
view too.

Running runs can be cancelled; finished runs can be rerun (GitHub) or have
their failed jobs retried (GitHub and GitLab). Everyone who can see a
Connection (`allowedGroups` or admin) can run, cancel and rerun its
pipelines. Every start is written to the backend log with the user, ref and
the names of the parameters (values are not logged).

### Images

The **Images** tab of a Connection lists the container images built from it,
for the selected branch (or all branches), with tags, push time, size,
digest, a link to the commit and a button that copies the full image
reference.

1. **Add a registry.** As an admin open **Registries** on the Zea page and
   add one; **Test registry** lists what the credentials can see. Or commit a
   Secret, see
   [deploy/examples/registry-secret.yaml](deploy/examples/registry-secret.yaml).
   - **DigitalOcean**: URL `registry.digitalocean.com/<registry>` and a
     DigitalOcean API token with read access to the registry (custom scope
     `registry:read`), or the `.dockerconfigjson` written by
     `doctl registry login` / `doctl registry docker-config`.
   - **Generic OCI** (Harbor, Zot, distribution, GitLab registry, ...): URL
     `host/namespace`; anonymous, username/password or `.dockerconfigjson`.
     The registry must support the catalog API (`/v2/_catalog`). GHCR and
     Docker Hub do not, and are not supported yet.

   Registries are admin-only: users never see their credentials, only the
   images of the Connections shared with them.

2. **Add image sources** to the Connection (**Edit** -> **Images**). Each
   source is a registry plus a regular expression for repository names and an
   optional one for tags. A named group `(?P<branch>...)` ties images to
   branches; **Preview** shows what matches. For example, a repository that
   pushes `vmist-server-web-<branch>-prod:<short sha>`:

   | Registry | Repository pattern | Tag pattern |
   |----------|--------------------|-------------|
   | `do` | `^vmist-server-(web\|static)-(?P<branch>.+)-prod$` | `^[0-9a-f]{7}$` |

   Branch names are compared in slug form (`feature/X` -> `feature-x`). Tags
   that look like a commit SHA (7-40 hex characters) link to the commit; for
   other tag formats capture the SHA with `(?P<sha>...)`, e.g.
   `^v[0-9.]+-(?P<sha>[0-9a-f]{7})$`.

Registry results are cached for a minute; the refresh button next to the list
reloads them.

### Upgrade

Bump the version in both places, so the UI and the backend match:

- `targetRevision` of the Zea Application, or
  `helm upgrade zea oci://ghcr.io/harchschoolboy/charts/zea --version <new> -n argocd --reuse-values`;
- `EXTENSION_VERSION` / `EXTENSION_URL` / `EXTENSION_CHECKSUM_URL` in the
  Argo CD values.

Available versions: `helm show chart oci://ghcr.io/harchschoolboy/charts/zea`
(latest) or the GitHub releases page; chart version = release tag without
`v`.

The proxy token and the Connections survive upgrades.

### Rotate the proxy token

With `proxyToken.source=generate`:

```bash
kubectl -n argocd delete secret zea-proxy
# re-sync the Zea Application (or helm upgrade) - the hook creates a new token
# both read the token only at startup
kubectl -n argocd rollout restart deploy/zea deploy/argocd-server
```

### Uninstall

1. Remove the Zea parts from the Argo CD values (or undo the patches).
2. Delete the Zea Application (or `helm uninstall zea -n argocd`).
3. Kept on purpose: the `zea-connections` namespace with all Connections, and
   the generated `argocd/zea-proxy` Secret. Delete them manually if you
   no longer need them:

   ```bash
   kubectl delete namespace zea-connections
   kubectl -n argocd delete secret zea-proxy
   ```

### Chart values

| Value | Default | Meaning |
|-------|---------|---------|
| `anchorApplication` | `<release ns>:<release name>` | Anchor Application `<namespace>:<name>` |
| `admins.users` / `admins.groups` | `[admin]` / `[]` | Zea admins |
| `connections.namespace` | `zea-connections` | Namespace with Connection Secrets |
| `connections.createNamespace` | `true` | Create that namespace (kept on uninstall) |
| `connections.rbac` | `true` | Role and RoleBinding for the backend in that namespace |
| `proxyToken.source` | `generate` | `generate`: hook Job creates it; `value`: rendered from `proxyToken.value`; `existing`: you create it ([example](deploy/examples/zea-proxy-secret.yaml)) |
| `proxyToken.secretName` / `proxyToken.key` | `zea-proxy` / `token` | Must match `$<secret>:<key>` in `extension.config.zea` |
| `httpTimeout` | `20s` | Timeout for GitHub and GitLab API calls |
| `image.repository` / `image.tag` | `ghcr.io/harchschoolboy/argocd-zea-backend` / appVersion | Backend image |

See [values.yaml](deploy/helm/zea/values.yaml) for the rest (resources,
security contexts, scheduling).

### Troubleshooting

| Symptom | Check |
|---------|-------|
| No Zea item in the sidebar | The Zea chart installs only the backend; the UI comes from the Argo CD configuration in [Step 2](#step-2-configure-argo-cd). Check that `argocd-server` has an `extension-zea` init container, that the release has the `extension-zea.tar.gz` asset, and the installer logs: `kubectl -n argocd logs deploy/argocd-server -c extension-zea`. Then hard-refresh the browser |
| Zea says no anchor Application found | The anchor has the label `argocd-zea.io/anchor: "true"` and the user can `get` it |
| `request did not come through the Argo CD proxy` (401) | `zea-proxy` has the label `app.kubernetes.io/part-of=argocd`. Restart `argocd-server` if Argo CD was configured before the Secret existed. After a token rotation, restart both `zea` and `argocd-server` |
| `requests must be scoped to the Zea anchor Application` (403) | `anchorApplication` matches the labelled Application's `<namespace>:<name>` |
| 403 from Argo CD on `/extensions/zea` | RBAC policy `extensions, invoke, zea` |
| 404 on `/extensions/zea` | `server.enable.proxy.extension: "true"` and `extension.config.zea` are set; `argocd-server` was restarted |
| Install or sync stuck on the hook Job | `kubectl -n argocd logs job/zea-proxy-token` |
| `... is not permitted in project` / `do not match any of the allowed destinations` | The Application's project does not allow the chart source or namespaces. Use the `zea` AppProject from the example, or extend your project the same way |
| `failed to fetch chart` / `401` / `403` from `ghcr.io` | The chart version is not released, or the ghcr package is not public |
| `GitHub API returned 401` / `GitLab API returned 401` | Credentials of the Connection; use **Test** in the Connection view |
| `GitHub API returned 403` on Run, Cancel or Rerun | The GitHub App or token needs **Actions** read and write (an App owner must also accept the new permissions on the installation) |
| `Unexpected inputs provided` (GitHub) | The workflow file on the branch changed after the form was opened; close and reopen the form |
| `GitLab API returned 403` on Run | Token role (Developer or higher; Maintainer for protected branches) and the "Minimum role to use pipeline variables" project setting |
| `DigitalOcean API returned 401` / `403` | The registry token is invalid or lacks `registry:read`; **Test** on the Registries page |
| `registry does not support the catalog API` | The OCI registry has no `/v2/_catalog` (GHCR, Docker Hub); not supported yet |
| Images tab is empty for a branch | **Preview** in the Connection form with an empty branch shows the repository names; check the `branch` group of the pattern against the branch slug |

## Development

| Task | Command |
|------|---------|
| Backend tests | `make backend-test` (uses Docker if Go is not installed) |
| Backend image | `make backend-image` |
| UI build + package | `make ui` |
| Helm lint | `make helm-lint` |

Run the backend locally without Argo CD (uses your current kubeconfig for
Connection Secrets):

```bash
cd backend
ZEA_INSECURE_SKIP_PROXY_AUTH=true ZEA_ANCHOR_APP=argocd:zea \
  ZEA_CONNECTIONS_NAMESPACE=zea-connections ZEA_ADMIN_USERS=admin \
  go run ./cmd/zea-backend
curl -H 'Argocd-Application-Name: argocd:zea' -H 'Argocd-Project-Name: default' \
  -H 'Argocd-Username: admin' localhost:8080/api/v1/me
```

Backend configuration (set by the Helm chart):

| Variable | Meaning |
|----------|---------|
| `ZEA_PROXY_TOKEN` | Shared secret injected by Argo CD (`Zea-Proxy-Token` header) |
| `ZEA_ANCHOR_APP` | `<namespace>:<name>` of the anchor Application |
| `ZEA_ADMIN_USERS`, `ZEA_ADMIN_GROUPS` | Comma-separated Zea admins |
| `ZEA_CONNECTIONS_NAMESPACE` | Namespace with Connection Secrets |
| `ZEA_HTTP_TIMEOUT` | Timeout for GitHub/GitLab API calls (default `20s`) |
| `ZEA_LOG_LEVEL` | `debug`, `info`, `warn`, `error` |
| `ZEA_INSECURE_SKIP_PROXY_AUTH` | Local development only |

Releases are built by GitHub Actions on `v*` tags (`v1.2.3` -> version
`1.2.3`):

- backend image `ghcr.io/harchschoolboy/argocd-zea-backend:<version>`;
- Helm chart `oci://ghcr.io/harchschoolboy/charts/zea:<version>`, whose
  version and appVersion are set from the tag (the `version` in `Chart.yaml`
  in git is only used for local development);
- `extension-zea.tar.gz` and its checksum file, attached to the GitHub
  release.

GitHub creates new ghcr packages as private. After the first release, set
both packages (`argocd-zea-backend` and `charts/zea`) to **Public** in the
package settings, otherwise anonymous pulls fail.
