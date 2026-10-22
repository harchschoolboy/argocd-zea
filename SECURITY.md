# Security Policy

## Supported versions

Zea is before 1.0. Security fixes are released only for the latest minor
version; upgrade to get them.

| Version | Supported |
|---------|-----------|
| 0.4.x   | Yes       |
| < 0.4   | No        |

## Reporting a vulnerability

Do not report vulnerabilities in public issues, pull requests or discussions.

Report them privately through GitHub:
[Report a vulnerability](https://github.com/harchschoolboy/argocd-zea/security/advisories/new)
(the **Security** tab of the repository).

Include:

- the Zea version (chart, backend image and UI extension) and the Argo CD
  version;
- the relevant configuration (chart values, Argo CD proxy extension and RBAC
  settings) with secrets removed;
- steps to reproduce, or a proof of concept;
- the impact: what an attacker can do and which access they need first.

## What to expect

Zea is a small project maintained on a best-effort basis. The targets are:

- acknowledgement within 7 days;
- an initial assessment within 14 days;
- a fix in a patch release, then a published GitHub Security Advisory that
  credits the reporter (unless they ask not to be named).

Please keep the details private until the advisory is published. If a fix
takes longer, the timeline is agreed with the reporter.

## Scope

In scope:

- the backend (`backend/`) and its container image;
- the UI extension (`ui/`) and the release archive `extension-zea.tar.gz`;
- the Helm chart (`deploy/helm/zea`) and the example manifests in
  `deploy/examples`;
- the release workflow and the published artifacts.

Out of scope:

- vulnerabilities in Argo CD, Kubernetes, GitHub, GitLab or container
  registries themselves - report them to those projects;
- setups that turn off documented protections, for example
  `ZEA_INSECURE_SKIP_PROXY_AUTH=true` (local development only) or a leaked
  `zea-proxy` token;
- actions available to users the deployment trusts on purpose: Zea admins,
  users who can edit the access policy, and cluster administrators;
- what a pipeline does once Zea has started it: pipelines run in the CI
  provider with the permissions given there.

## Security model

Useful background for reports and for hardening a deployment. The full
design is in [docs/design.md](docs/design.md).

- **Authentication** is done by Argo CD. Requests reach the backend only
  through the Argo CD proxy extension, which requires `get` on the anchor
  Application and `extensions, invoke, zea`.
- **Proxy token.** Argo CD adds the `Zea-Proxy-Token` header from the
  `zea-proxy` Secret. The backend rejects requests without a valid token, so
  workloads in the cluster cannot call it directly with forged `Argocd-*`
  identity headers.
- **Authorization.** Every request is checked by the backend against the Zea
  admins and the access policy (view, run, approve and edit on Streams,
  Connections and Registries). Hiding things in the UI is not a security
  boundary.
- **Credentials** of Connections and Registries are Kubernetes Secrets in a
  separate namespace (`zea-connections` by default). The backend's RBAC is
  limited to that namespace. The API never returns credentials. They are
  not written to the audit log either.
- **Audit log.** Every change is recorded with the user, the action and the
  result.
- **Pod.** The backend runs on a distroless image as a non-root user with a
  read-only root filesystem, no privilege escalation, all capabilities
  dropped and the `RuntimeDefault` seccomp profile.

## Hardening recommendations

- Give Connections the narrowest credentials the provider allows: a GitHub
  App or a fine-grained token limited to the repository, a GitLab project
  access token. Registry credentials only need read access.
- Limit who can `get` the anchor Application and `invoke` the extension in
  Argo CD RBAC, and grant Zea roles per Stream and Connection instead of
  `*`.
- Restrict ingress to the backend Service to `argocd-server` with a
  NetworkPolicy. The chart does not create one.
- Never set `ZEA_INSECURE_SKIP_PROXY_AUTH` outside local development.
- Rotate the proxy token as described in the README
  ([Rotate the proxy token](README.md#rotate-the-proxy-token)) if it may have
  leaked.
- Pin the image tag of the `extension-zea` init container (or its digest).
  If you download the UI from GitHub releases instead, set
  `EXTENSION_CHECKSUM_URL` so that the extension installer verifies the
  archive.
- Ship the backend log (`audit` lines) to central logging if the audit trail
  must outlive the in-cluster retention.
