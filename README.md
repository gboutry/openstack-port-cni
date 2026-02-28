# openstack-port

A CNI plugin that wraps [ovs-cni](https://github.com/k8snetworkplumbingwg/ovs-cni) with OpenStack Neutron port management. Uses a thin/thick architecture where a lightweight CNI binary delegates OpenStack operations to a daemon over a Unix domain socket.

## Architecture

```
kubelet → openstack-port-cni → Unix socket → openstack-port-daemon → Neutron
                 ↓
             ovs-cni (delegation)
```

**Two binaries:**

- **`openstack-port-cni`** (thin) — Installed in `/opt/cni/bin/`, invoked by kubelet. Talks to the daemon over a Unix socket, then delegates to OVS CNI.
- **`openstack-port-daemon`** (thick) — Runs as a DaemonSet. Holds OpenStack credentials in memory, manages Neutron ports. Deployed by a Juju charm with a Keystone relation.

## Security

- **Credentials never touch disk** — injected into the daemon via `OS_*` environment variables by the Juju charm
- **Unix domain socket** (`/var/run/openstack-cni/cni.sock`) — local-only, no network exposure
- **Filesystem permissions** — socket created with `0660`
- **Peer credential verification** — daemon verifies connecting process UID is 0 (root) via `SO_PEERCRED`
- The thin CNI has **zero access** to OpenStack credentials

## How it works

1. **ADD**: Thin CNI calls the daemon to create a Neutron port, receives IP/MAC/port ID, injects OVN port ID and MAC into the config, and delegates to ovs-cni with static IPAM.
2. **DEL**: Thin CNI delegates cleanup to ovs-cni first, then asks the daemon to delete the Neutron port.
3. **CHECK**: Thin CNI asks the daemon to verify the Neutron port exists, then delegates to ovs-cni.

## Configuration

### Daemon

The daemon reads OpenStack credentials from standard `OS_*` environment variables (e.g., `OS_AUTH_URL`, `OS_USERNAME`, `OS_PASSWORD`, `OS_PROJECT_NAME`, etc.). These should be injected by the Juju charm via a Keystone relation.

### CNI

Example NetworkAttachmentDefinition config:

```json
{
  "cniVersion": "0.4.0",
  "type": "openstack-port-cni",
  "socket_file": "unix:/var/snap/microovn/common/run/switch/db.sock",
  "network_id": "<neutron-network-uuid>",
  "subnet_id": "<neutron-subnet-uuid>",
  "delegate_plugin": "ovs",
  "bridge": "br-int"
}
```

An optional `"socket_path"` field can override the default daemon socket path (`/var/run/openstack-cni/cni.sock`).

## Build

```sh
make
```

This produces two binaries: `openstack-port-cni` and `openstack-port-daemon`.

Install `openstack-port-cni` to `/opt/cni/bin/`. Run `openstack-port-daemon` as a DaemonSet.
