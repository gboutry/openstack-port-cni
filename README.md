# openstack-port

A CNI plugin that wraps [ovs-cni](https://github.com/k8snetworkplumbingwg/ovs-cni). Before delegating to OVS, it creates a Neutron port to reserve an IP and obtain the OVN port ID and MAC address, which are then passed to the OVS plugin.

## How it works

1. **ADD**: Creates a Neutron port on the specified network/subnet, injects the OVN port ID and MAC into the config, and delegates to ovs-cni with static IPAM.
2. **DEL**: Delegates cleanup to ovs-cni first, then deletes the Neutron port.
3. **CHECK**: Verifies the Neutron port still exists, then delegates to ovs-cni.

## Configuration

OpenStack credentials are read from `/opt/cni/os_env` (standard `OS_*` environment variables). # TO BE FIXED!

Example NetworkAttachmentDefinition config:

```json
{
  "cniVersion": "0.4.0",
  "type": "openstack-port",
  "socket_file": "unix:/var/snap/microovn/common/run/switch/db.sock",
  "network_id": "<neutron-network-uuid>",
  "subnet_id": "<neutron-subnet-uuid>",
  "delegate_plugin": "ovs",
  "bridge": "br-int"
}
```

## Build

```sh
go build -o openstack-port .
```

Install the binary to `/opt/cni/bin/`.
