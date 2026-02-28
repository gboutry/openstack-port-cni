.PHONY: all clean

all: openstack-port-cni openstack-port-daemon

openstack-port-cni:
	go build -o $@ ./cmd/openstack-port-cni/

openstack-port-daemon:
	go build -o $@ ./cmd/openstack-port-daemon/

clean:
	rm -f openstack-port-cni openstack-port-daemon
