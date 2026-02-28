module openstack-port

go 1.25.7

require (
	github.com/containernetworking/cni v1.3.0
	github.com/gophercloud/gophercloud v1.14.1
	github.com/k8snetworkplumbingwg/ovs-cni v0.39.0
	golang.org/x/sys v0.41.0
)

require github.com/vishvananda/netns v0.0.5 // indirect

replace github.com/k8snetworkplumbingwg/ovs-cni => github.com/vexxhost/ovs-cni v0.0.0-20260115152815-107d5dd18af5
