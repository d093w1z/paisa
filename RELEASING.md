# Building and releasing this fork

Development happens on feature branches, which never build images. Two things do (see
`.github/workflows/docker.yml`):

| Push | Image | Use |
|---|---|---|
| branch `ci/docker-dev` | `d093w1z/paisa:ci-docker-dev-hledger` (overwritten each push) | trying a change |
| tag `vX.Y.Z-git.N` | `d093w1z/paisa:vX.Y.Z-git.N-hledger` (fixed) | working setups only |

## Server setup (one time)

`/root/docker-compose.yml` defaults to the current release and can be overridden per command:

```yaml
  paisa:
    image: d093w1z/paisa:${PAISA_TAG:-v0.8.0-git.4-hledger}
```

## Try a change

From the feature branch:

```sh
git push -f origin HEAD:ci/docker-dev
```

`-f` because `ci/docker-dev` takes whichever feature branch is being tested. When the Actions run
is green, on the server:

```sh
cd /root
PAISA_TAG=ci-docker-dev-hledger docker-compose pull paisa
PAISA_TAG=ci-docker-dev-hledger docker-compose up -d paisa
```

Back to the release:

```sh
docker-compose up -d paisa
```

The dev image runs against the real data. Before trying anything that writes to the ledger, commit
`/root/paisa/finance` so there is a point to roll back to.

## Release

```sh
git tag --sort=-creatordate | head -3          # pick the next number
git checkout master && git pull
git merge --no-ff feature/<branch>
git push origin master
git tag -a vX.Y.Z-git.N -m "<what changed>"
git push origin vX.Y.Z-git.N
```

Once CI is green, set the default in `/root/docker-compose.yml` to `vX.Y.Z-git.N-hledger`, then:

```sh
docker-compose pull paisa && docker-compose up -d paisa
```

Dev and release builds share the CI cache, so tagging a commit that already went through
`ci/docker-dev` builds quickly.
