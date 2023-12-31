// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package openstack

import (
	"fmt"
	"log"
	"net"

	"github.com/google/uuid"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/attachinterfaces"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/external"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
	"github.com/gophercloud/gophercloud/pagination"
)

// CheckFloatingIP gets a floating IP by its ID and checks if it is already
// associated with any internal interface.
// It returns floating IP if it can be used.
func CheckFloatingIP(client *gophercloud.ServiceClient, id string) (*floatingips.FloatingIP, error) {
	floatingIP, err := floatingips.Get(client, id).Extract()
	if err != nil {
		return nil, err
	}
	if floatingIP.PortID != "" {
		return nil, fmt.Errorf("provided floating IP '%s' is already associated with port '%s'",
			id, floatingIP.PortID)
	}

	return floatingIP, nil
}

// FindFreeFloatingIP returns free unassociated floating IP.
// It will return first floating IP if there are many.
func FindFreeFloatingIP(client *gophercloud.ServiceClient) (*floatingips.FloatingIP, error) {
	var freeFloatingIP *floatingips.FloatingIP

	pager := floatingips.List(client, floatingips.ListOpts{
		Status: "DOWN",
	})
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		candidates, err := floatingips.ExtractFloatingIPs(page)
		if err != nil {
			return false, err // stop and throw error out
		}

		for _, candidate := range candidates {
			if candidate.PortID != "" {
				continue // this floating IP is associated with port, move to next in list
			}

			// Floating IP is able to be allocated.
			freeFloatingIP = &candidate
			return false, nil // stop iterating over pages
		}
		return true, nil // try the next page
	})
	if err != nil {
		return nil, err
	}
	if freeFloatingIP == nil {
		return nil, fmt.Errorf("no free floating IPs found")
	}

	return freeFloatingIP, nil
}

// GetInstancePortID returns internal port of the instance that can be used for
// the association of a floating IP.
// It will return an ID of a first port if there are many.
func GetInstancePortID(client *gophercloud.ServiceClient, id string, instance_float_net string) (string, error) {

	selected_interface := 0

	interfacesPage, err := attachinterfaces.List(client, id).AllPages()
	if err != nil {
		return "", err
	}
	interfaces, err := attachinterfaces.ExtractInterfaces(interfacesPage)
	if err != nil {
		return "", err
	}
	if len(interfaces) == 0 {
		return "", fmt.Errorf("instance '%s' has no interfaces", id)
	}

	for i := 0; i < len(interfaces); i++ {
		log.Printf("Instance interface: %v: %+v\n", i, interfaces[i])
		if interfaces[i].NetID == instance_float_net {
			log.Printf("Found preferred interface: %v\n", i)
			selected_interface = i
			log.Printf("Using interface value: %v", selected_interface)
		}
	}

	return interfaces[selected_interface].PortID, nil
}

// CheckFloatingIPNetwork checks provided network reference and returns a valid
// Networking service ID.
func CheckFloatingIPNetwork(client *gophercloud.ServiceClient, networkRef string) (string, error) {
	if _, err := uuid.Parse(networkRef); err != nil {
		return GetFloatingIPNetworkIDByName(client, networkRef)
	}

	return networkRef, nil
}

// ExternalNetwork is a network with external router.
type ExternalNetwork struct {
	networks.Network
	external.NetworkExternalExt
}

// GetFloatingIPNetworkIDByName searches for the external network ID by the provided name.
func GetFloatingIPNetworkIDByName(client *gophercloud.ServiceClient, networkName string) (string, error) {
	var externalNetworks []ExternalNetwork

	allPages, err := networks.List(client, networks.ListOpts{
		Name: networkName,
	}).AllPages()
	if err != nil {
		return "", err
	}

	if err := networks.ExtractNetworksInto(allPages, &externalNetworks); err != nil {
		return "", err
	}

	if len(externalNetworks) == 0 {
		return "", fmt.Errorf("can't find external network %s", networkName)
	}
	// Check and return the first external network.
	if !externalNetworks[0].External {
		return "", fmt.Errorf("network %s is not external", networkName)
	}

	return externalNetworks[0].ID, nil
}

// DiscoverProvisioningNetwork finds the first network whose subnet matches the given network ranges.
func DiscoverProvisioningNetwork(client *gophercloud.ServiceClient, cidrs []string) (string, error) {
	allPages, err := subnets.List(client, subnets.ListOpts{}).AllPages()
	if err != nil {
		return "", err
	}

	allSubnets, err := subnets.ExtractSubnets(allPages)
	if err != nil {
		return "", err
	}

	for _, subnet := range allSubnets {
		_, tenantIPNet, err := net.ParseCIDR(subnet.CIDR)
		if err != nil {
			return "", err
		}

		for _, cidr := range cidrs {
			_, candidateIPNet, err := net.ParseCIDR(cidr)
			if err != nil {
				return "", err
			}

			if containsNet(candidateIPNet, tenantIPNet) {
				return subnet.NetworkID, nil
			}
		}
	}

	return "", fmt.Errorf("failed to discover a provisioning network")
}

// containsNet returns true whenever IPNet `a` contains IPNet `b`
func containsNet(a *net.IPNet, b *net.IPNet) bool {
	aMask, _ := a.Mask.Size()
	bMask, _ := b.Mask.Size()
	return a.Contains(b.IP) && aMask <= bMask
}
