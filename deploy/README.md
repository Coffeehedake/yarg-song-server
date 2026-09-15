# Deploying the song server

Three ways in, depending on what you are running it on. All three land on the
same container image; they differ only in what manages it.

| You have | Use |
|---|---|
| **Unraid** | `deploy/unraid/my-yarg-song-server.xml` |
| Any other Docker host | `docker-compose.yml` at the repo root |
| No Docker | A release archive — see `docs/BUILD.md` |

Two things apply to all of them, and the second is the one that bites silently:

- **The song library is mounted READ-ONLY.** The server never writes to it. This
  is deliberate: it is somebody's music folder.
- **`/data` must be owned by `65532:65532`.** The image is distroless and runs as
  `nonroot`. Get this wrong and the server starts perfectly, serves the browse
  page, and then fails the *first time a song actually needs packing* — which
  reads as a broken song rather than a directory permission.

**Read-only and unauthenticated by design. Do not put it behind SWAG, and do not
expose it to the internet.** It is a LAN service.

---

## Unraid

A container started with a hand-rolled `docker run` works, but shows up in the
Docker tab as an **orphan**: no edit form, no update button, no autostart toggle.
Installing the template is what fixes that, and it describes the container
exactly as it already runs, so installing it changes nothing about a running
service.

```bash
# from a shell on the Unraid box
cp my-yarg-song-server.xml /boot/config/plugins/dockerMan/templates-user/
xmllint --noout /boot/config/plugins/dockerMan/templates-user/my-yarg-song-server.xml && echo VALID

# autostart is TWO independent mechanisms; set both
#   1. the docker restart policy is what actually survives a reboot
docker update --restart unless-stopped yarg-song-server
#   2. this file is what makes the WebUI toggle agree, and sets start order
AS=/var/lib/docker/unraid-autostart
grep -qx yarg-song-server "$AS" || echo yarg-song-server >> "$AS"
```

The container name in `docker run --name`, the template's `<Name>`, and the
autostart entry must all be the string `yarg-song-server`.

### Pointing it at your own library

Edit the container in the Docker tab and change **Song library** to wherever your
songs are. Nothing else needs touching. The server re-indexes on start, so it is
a restart, not a migration.

Prefer `/mnt/cache/...` over `/mnt/user/...` on vault2 — the user share sits at
99% full, and going through shfs buys nothing for a read-only mount.

### Things the UI will do that look like faults and are not

- **The Console button fails** with `exec: "sh": executable file not found`. The
  image is distroless: there is no shell in it. Use the WebUI, `/healthz`, or the
  container log.
- **Update shows an update when `main` moves but `:latest` does not.** `:latest`
  means the newest *release*, on purpose. If it looks old, cut a tag — never
  repoint `:latest` at a branch.

---

## Docker Compose

```bash
SONGS_DIR=/srv/music/yarg docker compose up -d
```

`SONGS_DIR` and `DATA_DIR` both have defaults (`./songs`, `./data`) so a bare
`docker compose up -d` works in a scratch directory. Do the `chown` first:

```bash
mkdir -p ./data && sudo chown -R 65532:65532 ./data
```

Compose on Unraid is possible but is not what you want — a compose stack is still
an orphan in the Docker tab, which is the problem the template solves.

---

## Pulling the image

The registry is private. A pull needs a GitLab PAT **with `api` scope**:

```bash
docker login registry.badassium.com
```

The 1Password item *"GitLab Registry Pull (vault2 deploy token)"* is scoped to a
different project and will **not** pull this image — a project-scoped
`read_registry` token for this project is still an open item.

**Always print the `docker login` exit code before believing a failed pull.** A
stale token produces 401s that are indistinguishable from a broken registry, and
that has already voided a morning's worth of measurements on this project once.
