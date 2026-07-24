# TradingChassis fork notes

This repository is an official GitHub fork of
[`oracle/oci-secrets-store-csi-driver-provider`](https://github.com/oracle/oci-secrets-store-csi-driver-provider).

TradingChassis publishes a multi-architecture image to:

`ghcr.io/tradingchassis/oci-secrets-store-csi-driver-provider`

Supported platforms:

- `linux/amd64`
- `linux/arm64`

Application code stays aligned with upstream. TradingChassis changes focus on
container build, CI/CD, documentation, and image distribution.

## Image tags

| Tag | Meaning | Mutable |
|-----|---------|---------|
| `latest` | Most recent successful `main` publish | yes |
| `main` | Same as latest successful `main` publish | yes |
| `<git-sha>` | Exact commit image | **no** |
| `dev` | Latest manual development publish | yes |
| `dev-<branch>` | Manual development publish for a branch | yes |
| `X.Y.Z` | Release from Git tag `vX.Y.Z` | **no** |
| `X.Y` | Latest patch for minor line | yes |
| `X` | Latest minor/patch for major line | yes |

A merge to `main` never invents a semantic version. Version tags are created
only when you push a Git tag `vX.Y.Z`.

GitHub Releases are **not** created automatically. After a versioned image
publish, create the GitHub Release manually if desired.

## Helm usage (override upstream defaults)

Upstream chart defaults are intentionally unchanged so future upstream syncs
stay simpler. Override the image for TradingChassis deployments.

### Values file

```yaml
provider:
  image:
    repository: ghcr.io/tradingchassis/oci-secrets-store-csi-driver-provider
    tag: "main"          # or 1.2.3 / <sha> / digest reference below
    pullPolicy: IfNotPresent
```

### `--set`

Moving tag:

```bash
helm upgrade --install oci-provider charts/oci-secrets-store-csi-driver-provider \
  --namespace kube-system \
  --set provider.image.repository=ghcr.io/tradingchassis/oci-secrets-store-csi-driver-provider \
  --set provider.image.tag=main
```

Semantic tag:

```bash
helm upgrade --install oci-provider charts/oci-secrets-store-csi-driver-provider \
  --namespace kube-system \
  --set provider.image.repository=ghcr.io/tradingchassis/oci-secrets-store-csi-driver-provider \
  --set provider.image.tag=1.2.3
```

Immutable SHA tag:

```bash
helm upgrade --install oci-provider charts/oci-secrets-store-csi-driver-provider \
  --namespace kube-system \
  --set provider.image.repository=ghcr.io/tradingchassis/oci-secrets-store-csi-driver-provider \
  --set provider.image.tag=c8280930fec5ff59f75781df61d7c430f8f21b04
```

Digest (chart joins `repository` + `:` + `tag`, so split the digest reference):

```yaml
provider:
  image:
    repository: ghcr.io/tradingchassis/oci-secrets-store-csi-driver-provider@sha256
    tag: "f0ea7c8b16b0dfa9b1c18978bf8ae433b51d258f0263f2a3a9089b3bbbdc5a2a"
```

Prefer inspecting the digest first:

```bash
docker buildx imagetools inspect \
  ghcr.io/tradingchassis/oci-secrets-store-csi-driver-provider:main
```

## Inspect the multi-arch manifest

```bash
docker buildx imagetools inspect \
  ghcr.io/tradingchassis/oci-secrets-store-csi-driver-provider:main
```

Expect platform entries for `linux/amd64` and `linux/arm64`.

## Local multi-arch build (no push)

```bash
docker buildx create --use --name tc-builder || docker buildx use tc-builder

make docker-buildx \
  IMAGE_REGISTRY=ghcr.io/tradingchassis \
  PLATFORMS=linux/amd64,linux/arm64 \
  BUILDX_OUTPUT=type=oci,dest=/tmp/oci-secrets-store-csi-driver-provider.oci
```

Single-arch validation:

```bash
docker buildx build --platform=linux/amd64 -f build/Dockerfile --output=type=oci,dest=/tmp/amd64.oci .
docker buildx build --platform=linux/arm64 -f build/Dockerfile --output=type=oci,dest=/tmp/arm64.oci .
```

## CI model

### Pull requests against `main`

Workflow: `.github/workflows/ci.yaml`

Runs unit tests, Go build, static checks, Helm lint/template, and Buildx
validation for `linux/amd64` and `linux/arm64` using local OCI outputs.

Does **not**:

- push to GHCR
- set `latest` / `main` / `dev`
- require publish secrets
- run Oracle OCI E2E tests

### Push / merge to `main`

Workflow: `.github/workflows/publish-main.yaml`

Publishes one multi-arch image and updates:

- `latest`
- `main`
- `<git-sha>` (immutable)

### Manual development publish

Workflow: `.github/workflows/publish-dev.yaml` (`workflow_dispatch`)

Allowed tags:

- `dev`
- `dev-<sanitized-branch-name>`
- `<git-sha>`

Never publishes `latest`, `main`, or semantic version tags.

`workflow_dispatch` is available in the Actions UI after this workflow exists on
the default branch (`main`). First-time rollout:

1. Land the workflow via PR into `main`
2. Use **Actions → Publish development image → Run workflow**

### Git tag `vX.Y.Z`

Workflow: `.github/workflows/publish-release.yaml`

Publishes:

- `X.Y.Z` (immutable)
- `X.Y` (mutable)
- `X` (mutable)
- `<git-sha>` (immutable)

No automatic GitHub Release is created.

## Oracle E2E workflow

`.github/workflows/e2e-tests.yaml` remains available but is **manual only**.
It requires an already published `image_path` input and Oracle OCI secrets /
variables. It does not build or push images.

## Upstream synchronization

Preferred model once `main` has TradingChassis commits:

1. Update local `main`
2. `git fetch upstream`
3. Create `chore/sync-upstream-vX.Y.Z` from `main`
4. Merge `upstream/main` into that branch deliberately
5. Resolve conflicts (especially workflows and Dockerfile/Makefile)
6. Run full CI
7. Open a PR against TradingChassis `main`
8. Merge only after CI is green

Do not rely on GitHub **Sync fork** once your `main` diverges; it can discard
TradingChassis changes if used carelessly.

Suggested remotes:

```text
origin    → TradingChassis/oci-secrets-store-csi-driver-provider
upstream  → oracle/oci-secrets-store-csi-driver-provider
```

## GHCR permissions checklist

Before the first publish from this fork:

1. Confirm GitHub Actions are enabled for the repository
2. Open the package `oci-secrets-store-csi-driver-provider` on GHCR
3. Ensure this repository has Actions write access to the package
   (link the package / inherit access / manage Actions access)
4. Prefer `GITHUB_TOKEN` with `packages: write` on publish jobs only
5. Avoid personal access tokens for routine publishes
