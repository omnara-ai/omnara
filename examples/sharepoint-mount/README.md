# SharePoint Filesystem Mount Example

Give an Omnara agent filesystem access to a SharePoint document library. You
register a machine in the Omnara web console, then run one script: it builds
and starts a Docker container that mounts the library with `rclone` and runs
the Omnara machine daemon alongside it. The agent config targets the machine
by name and works against the mount as its working directory.

## Files

- `connect-machine.sh`: builds the container image and runs it with your
  machine token and SharePoint settings.
- `Dockerfile`: builds the daemon from this repo plus `rclone`/FUSE.
- `entrypoint.sh`: in-container startup — mounts the library, runs the daemon.
- `.env.example`: template for the script's configuration.
- `sharepoint-agent.yaml`: agent config that targets the machine by name.

## 1. Microsoft: SharePoint access

Pick one auth mode:

### Option A: delegated user token (quickest, no Entra app)

Sign in once as a user who can access the site, using rclone's own OAuth
client:

```sh
brew install rclone   # or https://rclone.org/install/
rclone authorize "onedrive"
```

A browser opens; after signing in, rclone prints a token JSON — that becomes
`SHAREPOINT_RCLONE_TOKEN` in `.env`. The mount acts as that user, and the
refresh token expires after ~90 days without use.

### Option B: Entra app with application permissions

1. Entra admin center > **App registrations** > **New registration**. Record
   the **tenant ID** and **client ID**.
2. **Certificates & secrets** > new client secret. Copy the **secret value**.
3. **API permissions** > Microsoft Graph > **Application permissions** > add
   `Sites.Read.All` (or `Sites.ReadWrite.All` if agents should write) >
   **Grant admin consent** (the status column must show green "Granted";
   without consent, tokens carry no roles and Graph fails with
   `generalException`).

### Find the drive ID (both options)

Find the site ID and the document library's drive ID:

   ```sh
   # site id
   az rest --method GET \
     --url "https://graph.microsoft.com/v1.0/sites/{tenant}.sharepoint.com:/sites/{site-name}"
   # drive id of the document library
   az rest --method GET \
     --url "https://graph.microsoft.com/v1.0/sites/{site-id}/drives"
   ```

   If `az rest` returns `accessDenied`, run the same GETs in
   [Graph Explorer](https://developer.microsoft.com/graph/graph-explorer)
   signed in as the admin.

Least-privilege alternative to option B: use the `Sites.Selected` application permission
plus a per-site grant (`POST /sites/{site-id}/permissions`). The grant call
must be made by a SharePoint/Global admin holding the `Sites.FullControl.All`
delegated scope, e.g. `az login --scope
https://graph.microsoft.com/Sites.FullControl.All`.

## 2. Machine prerequisites

Docker. Everything else (the daemon, rclone, FUSE) lives inside the container,
so this works the same on macOS and Linux.

## 3. Register the machine in the web console

In the Omnara web console: **Overview > Machines > Connect machine**. Pick a
machine name (agent configs reference it) and the project whose agents should
use the machine, then copy the machine token — it is shown once.

## 4. Run the container

```sh
cd examples/sharepoint-mount
cp .env.example .env   # paste the machine token, add SharePoint creds
./connect-machine.sh
```

The script builds the image (daemon compiled from this repo, plus rclone and
FUSE), then runs the container in the foreground: the entrypoint mounts the
library at `/mnt/sharepoint` and starts the daemon. Leave
`SHAREPOINT_DRIVE_ID` empty to skip the mount. Re-running replaces the
previous container.

Hosted Omnara is used by default. For a self-hosted deployment, set
`OMNARA_API_URL` to its public HTTPS origin. For an API running directly on the
Docker host, use `http://host.docker.internal:8080`; localhost inside the
container refers to the container itself.

## 5. Create the agent

**Project > Agents > New agent** (or **New agent profile**) > **YAML** tab.
Paste `sharepoint-agent.yaml` and replace:

- `CHANGE_ME_MACHINE_NAME`: the machine name from step 3
- `model.provider_config` / `model.name` if yours differ

## 6. Verify

Ask the agent to run:

```sh
pwd && ls -la
```

It should list the SharePoint document library contents.

## Notes

- The container needs `--cap-add SYS_ADMIN --device /dev/fuse` (the script
  passes these) because rclone mounts FUSE inside it.
- The client secret is written to `rclone.conf` (mode 0600, in-container at
  `/run/omnara-sharepoint`) and removed from the daemon's environment.
- To poke around inside: `docker exec -it omnara-sharepoint-daemon sh`.
- `Sites.Read.All` grants the app read access to every site in the tenant, and
  the mount is effectively read-only (writes fail at the Graph API). Prefer
  `Sites.Selected` with a per-site grant for production.
- To unmount: `umount <mount path>` (macOS) or `fusermount -u <mount path>`
  (Linux).
- References: [site permissions](https://learn.microsoft.com/en-us/graph/api/site-post-permissions),
  [list drives](https://learn.microsoft.com/en-us/graph/api/drive-list),
  [rclone onedrive backend](https://rclone.org/onedrive/).
