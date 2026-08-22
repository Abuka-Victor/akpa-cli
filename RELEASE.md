# Releasing akpa

One-time setup, then every release is two commands.

---

## One-time setup

### 1. Install GoReleaser locally

```sh
go install github.com/goreleaser/goreleaser/v2@latest
```

Check it:

```sh
goreleaser --version
```

If `goreleaser: command not found`, `~/go/bin` isn't on your PATH:

```sh
echo 'export PATH="$HOME/go/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc
```

### 2. Add a LICENSE

GoReleaser is configured to include it in each archive. Create one on GitHub
(Add file → Create new file → type `LICENSE` → "Choose a license template" → MIT)
or write your own.

### 3. Confirm the two config files are in place

```
akpa-cli/
├── .goreleaser.yaml
└── .github/
    └── workflows/
        └── release.yml
```

### 4. Test locally before tagging anything

```sh
goreleaser release --snapshot --clean
```

This builds every platform **without** publishing. It should finish with a `dist/`
directory:

```sh
ls dist/
```

You want to see:

```
akpa_linux_amd64.tar.gz
akpa_linux_arm64.tar.gz
akpa_darwin_amd64.tar.gz
akpa_darwin_arm64.tar.gz
akpa_windows_amd64.zip
checksums.txt
```

**Those filenames must match what `install.sh` builds.** The script constructs
`akpa_${os}_${arch}.tar.gz`. If GoReleaser produced `akpa_Linux_x86_64.tar.gz`
instead, the `name_template` is wrong and every install will 404.

Verify the binary works and is stamped:

```sh
tar xzf dist/akpa_linux_amd64.tar.gz -C /tmp
/tmp/akpa --version
```

Should print something like `akpa v0.0.1-next` — not `dev`. If it says `dev`, the
`ldflags` in `.goreleaser.yaml` are not matching your variable names.

---

## Every release

### 1. Commit everything

```sh
git status          # must be clean
git push
```

GoReleaser refuses to run on a dirty working tree.

### 2. Tag and push

```sh
git tag v1.0.0
git push origin v1.0.0
```

That's it. Pushing the tag triggers the workflow.

### 3. Watch it

Go to your repo → **Actions** tab. The `release` workflow takes 2–3 minutes.

When it finishes, check the **Releases** page. You should have a new release with
all the archives attached and `checksums.txt`.

### 4. Verify the real install path

From a different machine, or a container:

```sh
curl -fsSL https://akpa.victorabuka.com/install.sh | bash
akpa --version
```

---

## Version numbers

Use [semver](https://semver.org): `vMAJOR.MINOR.PATCH`.

| Change | Bump | Example |
| --- | --- | --- |
| Bug fix, no behaviour change | PATCH | `v1.0.0` → `v1.0.1` |
| New flag or feature, still backward compatible | MINOR | `v1.0.1` → `v1.1.0` |
| Something that breaks existing usage | MAJOR | `v1.1.0` → `v2.0.0` |

Pre-releases get marked automatically:

```sh
git tag v2.0.0-rc1
```

GoReleaser sees the `-rc1` suffix and marks it a pre-release, so
`/releases/latest` keeps pointing at `v1.x` and `install.sh` won't hand it to
users. Good for testing a release without shipping it.

---

## When something goes wrong

**Deleting a bad tag** (before anyone installs it):

```sh
git tag -d v1.0.0                  # local
git push origin :refs/tags/v1.0.0  # remote
```

Then delete the release on GitHub's Releases page, fix the problem, and re-tag.

**Once people have installed it, don't delete — ship `v1.0.1`.** Re-pointing an
existing tag at different code breaks checksums for anyone who already has it.

**Workflow failed:**

| Error | Cause |
| --- | --- |
| `git is currently in a dirty state` | Uncommitted changes; commit and re-tag |
| `failed to build for X` | Compile error on that platform; run `goreleaser release --snapshot --clean` locally |
| `403` from GitHub | `permissions: contents: write` missing from `release.yml` |
| Tests failed | The workflow runs `go test ./...` before building — fix the tests |

**Install works but `akpa --version` says `dev`:** the `ldflags` variable names in
`.goreleaser.yaml` don't match the `var version` declaration in your code. They
must both be in package `main` and spelled identically.

---

## Releasing v2

Identical. Tag `v2.0.0`, push, done.

Nothing on the server needs changing. `install.sh` never mentions a version —
it asks GitHub for `/releases/latest` every time it runs, and GitHub redirects to
whatever the newest tag is. The install command on your website starts serving v2
the moment the workflow finishes.
