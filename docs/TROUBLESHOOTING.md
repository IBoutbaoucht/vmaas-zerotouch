# Troubleshooting

Common failure modes and how to diagnose them. Read this first when
something goes wrong.

## "backend unreachable" in the UI

The little dot next to `ESXi …` in the header is red.

1. `docker compose ps` — is `vmaas-backend` running? If it's restarting:
   `docker compose logs backend`. The most common cause is a missing
   env var:
   ```
   config: VMAAS_TOKEN is required
   ```
   → fill it in `.env` and `docker compose up` again.
2. Can the backend reach ESXi?
   ```sh
   curl -k https://<esxi-host>/sdk       # should return some XML
   ```
   If this fails the backend cannot route to your hypervisor. The
   backend container runs with `network_mode: host`, so its
   reachability is whatever your host's is -- fix the host route or
   tunnel first, the next poll will turn the dot green.

## "esxi unreachable" with the host reachable

Usually means the **govmomi session expired** or hostd's per-user
session cap is full.

```sh
govc session.ls         # how many "vmaas-engine" sessions?
govc session.rm <KEY>   # kill a specific one
```

hostd defaults to ~30 sessions per user. Crashing the backend without a
clean shutdown leaks a session every time. The fix is in the code:
`vmaasd` calls `client.Logout(ctx)` from a `defer` on SIGTERM, so
`docker compose down` is graceful. `docker kill` is not.

## Provision goes `failed` immediately

Read the `error` column in the UI or click `info` for the full record.
The error is set on whichever step failed.

| Error excerpt                      | Cause                                                    |
| ---------------------------------- | -------------------------------------------------------- |
| `pool exhausted`                   | All 8 IPs are in use. Delete a VM or extend the pool.    |
| `gold VM not found`                | `esxi.gold_vm` in `vmaas.yaml` doesn't match inventory.  |
| `mkdir failed: ServerFaultCode`    | Datastore is full, or the VM folder already exists with stale files. Try `govc datastore.ls "[datastore1] <vmname>"` and clean up. |
| `RegisterVM_Task ... invalid VMX`  | The VMX patch produced something hostd hates. Check the .vmx with `govc datastore.download` and diff against the gold one. |
| `timeout waiting for tools`        | cloud-init didn't bring the network up. See "VM boots but no IP" below. |

## VM boots but no IP

Symptoms: the VM is powered on, but `ssh` doesn't work and the UI is
stuck at `starting`.

Connect to the VM via the **ESXi console** (web UI → Hosts → Manage →
the VM) and check:

```sh
ip addr             # is ens34 up? does it have the IP we expected?
sudo journalctl -u systemd-networkd
sudo cat /var/log/cloud-init.log | grep -i error
```

Common causes:

1. **Parent DPG missing "Promiscuous" or "Forged Transmits"**. When the
   ESXi host runs as a nested VM, its parent port group needs both flags
   enabled or inner VMs never see ARP/DHCP traffic. If you're getting
   "Inner VM no IPv4", this is almost always the cause. Verify on the
   host:
   ```sh
   ssh root@<esxi-host>
   esxcli network vswitch standard portgroup policy security get -p "VM Network"
   ```
2. **NIC name mismatch.** The cloud-init network config targets
   `ens34`. If your gold image enumerates the NIC as `ens32` instead,
   netplan applies a config to a non-existent device.
   ```sh
   # in the VM
   ip -o link | awk -F': ' '{print $2}'
   ```
   Edit `network.nic_name` in `vmaas.yaml` and re-provision.
3. **DNS unreachable.** An internal resolver reachable from the host
   often isn't reachable from inside nested VMs. The default config
   uses `1.1.1.1` and `8.8.8.8`. If you changed it to an internal
   resolver and DNS broke, switch back to public ones to confirm the
   issue is DNS-routing and not cloud-init.

## DNS works on the host but not in the VM

An internal resolver that the ESXi host can reach is not necessarily
reachable from VMs sitting behind a nested port group. The default
config uses public resolvers (`1.1.1.1`, `8.8.8.8`) in `vmaas.yaml`.
Don't change that without confirming the new resolver is reachable
through the parent DPG.

## "deletion failed" in the UI

The delete operation is best-effort idempotent. If it fails partway:

1. Re-issue `DELETE /v1/vms/<id>` — it'll skip the steps that already
   succeeded.
2. If the VM is gone from inventory but the record sticks: it's the
   IP release or store delete failing. Check
   `docker compose logs backend` for the specific bbolt error.
3. As a last resort, remove the record manually:
   ```sh
   docker compose down
   # backup first
   cp data/state.db data/state.db.bak
   # ...edit/delete with `bbolt` cli or just nuke and re-import.
   ```

## VMs accumulate in govc after a crash

If the backend dies *between step 5 of clone and the first status
update*, the VM is registered on hostd but the store has no record of
it. It won't show in the UI. Manual cleanup:

```sh
govc ls /ha-datacenter/vm | grep vmaas-
govc vm.destroy /ha-datacenter/vm/vmaas-<id>
```

The corresponding IP is still allocated in bbolt; release it by
provisioning enough VMs to walk past it (allocator skips IPs already
in `ipalloc`), or restart the backend with a fresh `state.db`.

## bbolt complaints on startup

```
open /var/lib/vmaas/state.db: timeout
```

Two processes are trying to open the file. Almost always: a previous
`vmaasd` is still running, or you forgot to set `./data` writeable.
`docker compose down` then `ls -la data/` — make sure no `vmaasd`
processes are around. If you ran `make build && ./backend/vmaasd`
locally while the compose stack is up, that's the conflict.

## "guestinfo not picked up by the VM"

The VMware cloud-init datasource only reads guestinfo **at boot**. If
you `SetExtraConfig` after power-on, nothing happens. If you suspect
the keys aren't there at all:

```sh
govc vm.info -e /ha-datacenter/vm/<name>
```

`-e` includes extraConfig. You should see `guestinfo.metadata`,
`guestinfo.metadata.encoding=base64`, plus the userdata pair.

If the keys are there but cloud-init still ignores them, check the
guest:

```sh
sudo cat /run/cloud-init/result.json
sudo cloud-init query datasource
```

The datasource should be `VMware`. If it's `NoCloud` or `None`, the
gold image's `/etc/cloud/cloud.cfg.d/` is misconfigured.
