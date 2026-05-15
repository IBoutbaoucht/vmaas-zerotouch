# How it works — one provision, end to end

We're going to follow a single click of the **Provision** button from
the browser to a running Ubuntu VM with SSH ready. This doc is the
"learn by reading the call stack" view; for module boundaries and
state-machine diagrams see [../ARCHITECTURE.md](../ARCHITECTURE.md).

Throughout, code references use `file:line` so you can jump to them.

## 0. What you're looking at

```
[Browser]  →  [nginx (frontend)]  →  [vmaasd (backend)]  →  [ESXi B]
                                          │
                                          └─►  [bbolt: state.db]
```

The frontend container does one job: serve `index.html` + `app.js` and
reverse-proxy `/api/*` to the backend. Everything interesting happens
in `vmaasd` and on host B.

## 1. Click → HTTP

User types a name in the input (or leaves it blank), clicks **Provision**.

`frontend/app.js:147`:

```js
document.getElementById('new-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const name = document.getElementById('new-name').value.trim();
  ...
  const r = await api('/vms', { method: 'POST', body: JSON.stringify({ name }) });
```

`api()` wraps `fetch` to add `Authorization: Bearer <token>` from
`localStorage.vmaas_token`. The request URL is `/api/v1/vms` (the `/api`
prefix is rewritten away by nginx before it hits the backend).

`frontend/nginx.conf`:

```
location /api/ {
  proxy_pass http://host.docker.internal:8080/;
  ...
}
```

`host.docker.internal` resolves to the docker host gateway (we add the
mapping in `docker-compose.yml`), which is where the backend listens
because it runs with `network_mode: host`.

## 2. Backend HTTP layer

The chi router is built in `backend/internal/api/server.go`. The
relevant route:

```go
r.Route("/v1/vms", func(r chi.Router) {
    r.Use(authMiddleware(s.cfg.AuthToken))
    r.Post("/", s.handleCreateVM)
    ...
})
```

`authMiddleware` does a constant-time compare of the Bearer token
against `cfg.AuthToken`. If it fails, 401 and you're done.

`handleCreateVM` in `backend/internal/api/handlers.go:68` is tiny: it
decodes `{ name }`, calls `s.orch.Provision(...)`, and replies 202 with
the new VM's id. **All the work happens after the response goes back.**

```go
id, err := s.orch.Provision(lifecycle.CreateRequest{Name: req.Name})
...
writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "pending"})
```

## 3. Orchestrator kicks off the goroutine

`backend/internal/lifecycle/orchestrator.go`. `Provision`:

1. Generates an id (`vmaas-<rand6>`) and a sanitized name.
2. Writes the record to bbolt with `status: pending`.
3. Spawns `go o.runProvision(id)`.
4. Returns the id immediately.

That's it for the HTTP request. The browser's POST is done.

`runProvision` is a linear walk through the state machine. Each
transition is **persisted before the next step starts**, so a process
crash leaves the store in a state we can resume from.

## 4. Allocating an IP

```go
o.store.UpdateStatus(id, store.StatusAllocating, "")
ip, err := o.alloc.Acquire(id)
```

`ipalloc/alloc.go` walks the configured pool
(`10.X.X.62-10.X.X.69`) inside a single bbolt transaction, picks
the first IP not present in the `ipalloc` bucket, and writes `ip → id`.
Atomic with respect to concurrent provisions.

The store's `VM.IP` is updated next so the UI can see "this VM will be
.62" before cloud-init has even read it.

## 5. The 5-step clone

`backend/internal/clone/clone.go`. The procedure stands in for
`CloneVM_Task`, which only works against vCenter:

| # | Step                          | govmomi call                                                            |
| - | ----------------------------- | ----------------------------------------------------------------------- |
| 1 | mkdir for the new VM folder   | `FileManager.MakeDirectory("[datastore1] vmaas-3f2a")`                  |
| 2 | clone the disk (thin)         | `VirtualDiskManager.CopyVirtualDisk_Task(...)`                          |
| 3 | copy the .vmx                 | `FileManager.CopyDatastoreFile_Task(...)`                               |
| 4 | download .vmx → patch → up    | `Datastore.Download` + regex + `Datastore.Upload`                       |
| 5 | register the VM with hostd    | `Folder.RegisterVM_Task(vmxPath, name, asTemplate=false, pool=nil, host)` |

Each step calls a `once(name, fn)` helper that checks the checkpoint
bucket: if `<id>:<step>` is already set, it skips the call. So a crash
mid-clone resumes exactly where it left off, never re-doing work.

Step 4 — the VMX patch — is where the cloned VM gets its identity. The
input is the gold image's `.vmx`; we rewrite three lines with
`clone/vmx.go`:

```
displayName = "ubuntu-22.04-template"       →   displayName = "vmaas-3f2a"
nvram = "ubuntu-22.04-template.nvram"       →   nvram = "vmaas-3f2a.nvram"
scsi0:0.fileName = "ubuntu-22.04-template.vmdk" → scsi0:0.fileName = "vmaas-3f2a.vmdk"
```

We also **strip** `uuid.bios = "..."` and
`ethernet0.generatedAddress = "..."`, so hostd assigns fresh ones on
register — otherwise every clone would have a colliding UUID and MAC.

## 6. Injecting cloud-init

`backend/internal/cloudinit/render.go` reads two templates (embedded via
`//go:embed`):

- `metadata.yaml.tmpl` — cloud-init network-config v2, sets static IP
  and DNS.
- `userdata.yaml.tmpl` — creates the `cuneyt` user, drops your SSH key
  in `~/.ssh/authorized_keys`, writes a sentinel file
  `/var/log/vmaas-sentinel.log`.

Both are rendered with the per-VM `Vars`, then **base64-encoded**.
That's what cloud-init's VMware datasource expects.

Back in the orchestrator:

```go
o.ex.SetExtraConfig(ctx, vm, map[string]string{
    "guestinfo.metadata":          metaB64,
    "guestinfo.metadata.encoding": "base64",
    "guestinfo.userdata":          userB64,
    "guestinfo.userdata.encoding": "base64",
})
```

`SetExtraConfig` is one `ReconfigVM_Task` call.

## 7. Power on

```go
o.ex.PowerOn(ctx, vm)
```

VMkernel boots the VM. The boot has nothing to do with us anymore;
cloud-init inside the VM:

1. Reads `guestinfo.metadata`, sees the static IP config.
2. Configures `ens34` (`netplan` apply).
3. Reads `guestinfo.userdata`, creates the user + drops SSH key.
4. Writes `/var/log/vmaas-sentinel.log`.

## 8. Waiting for "ready"

`runProvision` calls into a property collector wait that watches the
VM's `guest.toolsRunningStatus` and `guest.net[*].ipAddress` properties.
We declare the VM **ready** when:

- VMware Tools reports `guestToolsRunning` (open-vm-tools is up).
- The guest reports our expected IP on any NIC.

(Sentinel-file polling was the older mechanism. With Tools running and
the IP confirmed, the sentinel is belt-and-suspenders and we don't
gate on it.)

```go
o.store.UpdateStatus(id, store.StatusReady, "")
```

## 9. The browser sees it

The frontend polls `GET /api/v1/vms` and `GET /api/v1/pool` every 2s
(`frontend/app.js:174`):

```js
setInterval(refreshAll, POLL_MS);
```

So within at most 2s after the orchestrator flips status, the row turns
green and shows the IP.

## 10. SSH from another terminal

```sh
ssh -i ~/.ssh/vmaas-lab cuneyt@10.X.X.62
cat /var/log/vmaas-sentinel.log
```

You're inside the new VM.

## 11. Delete

Click the row's "delete" button. `app.js` confirms, then:

```js
await api(`/vms/${id}`, { method: 'DELETE' });
```

`handleDeleteVM` calls `orch.Delete`, which **blocks** (unlike provision)
because the operation is short and the user is waiting:

1. `PowerOffVM_Task` (if powered on).
2. `Unregister` from inventory.
3. `FileManager.DeleteDatastoreFile_Task` on the VM's folder.
4. `ipalloc.Release(id)` — the IP returns to the pool.
5. `store.Delete(id)` — the record is gone.

The UI's next poll shows the VM missing from the list and the pool
meter has one more free cell.

That's the whole loop.
