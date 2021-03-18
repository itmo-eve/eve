// Copyright (c) 2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Handle NetworkInstance setup

package zedrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lf-edge/eve/pkg/pillar/base"
	uuid "github.com/satori/go.uuid"
	"net"
	"strings"

	"github.com/eriknordmark/netlink"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	"github.com/lf-edge/eve/pkg/pillar/devicenetwork"
	"github.com/lf-edge/eve/pkg/pillar/iptables"
	"github.com/lf-edge/eve/pkg/pillar/types"
)

// isSharedPortLabel
// port names "uplink" and "freeuplink" are actually built in labels
//	we used for ports used by Dom0 itself to reach the cloud. But
//      these can also be shared by the applications.
func isSharedPortLabel(label string) bool {
	// XXX - I think we can get rid of these built-in labels (uplink/freeuplink).
	//	This will be cleaned up as part of support for deviceConfig
	//	from cloud.
	if strings.EqualFold(label, "uplink") {
		return true
	}
	if strings.EqualFold(label, "freeuplink") {
		return true
	}
	return false
}

// checkPortAvailable
//	A port can be used for NetworkInstance if the following are satisfied:
//	a) Port should be part of Device Port Config
//	b) For type switch, port should not be part of any other
// 			Network Instance
// Any device, which is not a port, cannot be used in network instance
//	and can only be assigned as a directAttach device.
func checkPortAvailable(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("NetworkInstance(%s-%s), logicallabel: %s, currentUplinkIntf: %s",
		status.DisplayName, status.UUID, status.Logicallabel,
		status.CurrentUplinkIntf)

	if status.CurrentUplinkIntf == "" {
		log.Functionf("CurrentUplinkIntf not specified\n")
		return nil
	}

	if isSharedPortLabel(status.CurrentUplinkIntf) {
		return nil
	}
	portStatus := ctx.deviceNetworkStatus.GetPortByIfName(status.CurrentUplinkIntf)
	if portStatus == nil {
		errStr := fmt.Sprintf("PortStatus for %s not found for network instance %s-%s\n",
			status.CurrentUplinkIntf, status.Key(), status.DisplayName)
		return errors.New(errStr)
	}
	return nil
}

func disableIcmpRedirects(bridgeName string) {
	sysctlSetting := fmt.Sprintf("net.ipv4.conf.%s.send_redirects=0", bridgeName)
	args := []string{"-w", sysctlSetting}
	log.Functionf("Calling command %s %v\n", "sysctl", args)
	out, err := base.Exec(log, "sysctl", args...).CombinedOutput()
	if err != nil {
		errStr := fmt.Sprintf("sysctl command %s failed %s output %s",
			args, err, out)
		log.Errorln(errStr)
	}
}

// doCreateBridge
//		returns (error, bridgeMac-string)
func doCreateBridge(bridgeName string, bridgeNum int,
	status *types.NetworkInstanceStatus) (error, string) {

	if !strings.HasPrefix(status.BridgeName, "bn") {
		log.Fatalf("bridgeCreate(%s) %s not possible",
			status.DisplayName, status.BridgeName)
	}
	// Start clean
	// delete the bridge
	attrs := netlink.NewLinkAttrs()
	attrs.Name = bridgeName
	link := &netlink.Bridge{LinkAttrs: attrs}
	netlink.LinkDel(link)

	//    ip link add ${bridgeName} type bridge
	attrs = netlink.NewLinkAttrs()
	attrs.Name = bridgeName
	bridgeMac := fmt.Sprintf("00:16:3e:06:00:%02x", bridgeNum)
	hw, err := net.ParseMAC(bridgeMac)
	if err != nil {
		log.Fatal("ParseMAC failed: ", bridgeMac, err)
	}
	attrs.HardwareAddr = hw
	link = &netlink.Bridge{LinkAttrs: attrs}
	if err := netlink.LinkAdd(link); err != nil {
		errStr := fmt.Sprintf("LinkAdd on %s failed: %s",
			bridgeName, err)
		return errors.New(errStr), ""
	}
	//    ip link set ${bridgeName} up
	if err := netlink.LinkSetUp(link); err != nil {
		errStr := fmt.Sprintf("LinkSetUp on %s failed: %s",
			bridgeName, err)
		return errors.New(errStr), ""
	}
	disableIcmpRedirects(bridgeName)

	// Get Ifindex of bridge and store it in network instance status
	bridgeLink, err := netlink.LinkByName(bridgeName)
	if err != nil {
		errStr := fmt.Sprintf("doCreateBridge: LinkByName(%s) failed: %s",
			bridgeName, err)
		log.Errorln(errStr)
		return errors.New(errStr), ""
	}
	index := bridgeLink.Attrs().Index
	status.BridgeIfindex = index
	return err, bridgeMac
}

// doLookupBridge is used for switch network instance where nim
// has created the bridge. All such NIs have an external port.
//	returns (bridgeName, bridgeMac-string, error)
func doLookupBridge(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) (string, string, error) {

	ifNameList := getIfNameListForLLOrIfname(ctx, status.Logicallabel)
	if len(ifNameList) == 0 {
		err := fmt.Errorf("doLookupBridge IfNameList empty for %s",
			status.Key())
		log.Error(err)
		return "", "", err
	}
	ifname := ifNameList[0]
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		err = fmt.Errorf("doLookupBridge LinkByName(%s) failed: %v",
			ifname, err)
		log.Error(err)
		return "", "", err
	}
	linkType := link.Type()
	if linkType != "bridge" {
		err = fmt.Errorf("doLookupBridge(%s) not a bridge", ifname)
		log.Error(err)
		return "", "", err
	}
	var macAddrStr string
	macAddr := link.Attrs().HardwareAddr
	if len(macAddr) != 0 {
		macAddrStr = macAddr.String()
	}
	log.Noticef("doLookupBridge found %s, %s", ifname, macAddrStr)
	return ifname, macAddrStr, nil
}

func networkInstanceBridgeDelete(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {
	// Here we explicitly delete the iptables rules which are tied to the Linux bridge
	// itself and not the rules for specific domU vifs.

	aclArgs := types.AppNetworkACLArgs{IsMgmt: false, BridgeName: status.BridgeName,
		BridgeIP: status.BridgeIPAddr, NIType: status.Type, UpLinks: status.IfNameList}
	handleNetworkInstanceACLConfiglet("-D", aclArgs)

	if !strings.HasPrefix(status.BridgeName, "bn") {
		log.Noticef("networkInstanceBridgeDelete(%s) %s ignored",
			status.DisplayName, status.BridgeName)
	} else {
		attrs := netlink.NewLinkAttrs()
		attrs.Name = status.BridgeName
		link := &netlink.Bridge{LinkAttrs: attrs}
		// Remove link and associated addresses
		netlink.LinkDel(link)
	}

	if status.BridgeNum != 0 {
		status.BridgeName = ""
		status.BridgeNum = 0
		bridgeNumFree(ctx, status.UUID)
	}
}

func isNetworkInstanceCloud(status *types.NetworkInstanceStatus) bool {
	if status.Type == types.NetworkInstanceTypeCloud {
		return true
	}
	return false
}

func doBridgeAclsDelete(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	// Delete ACLs attached to this network aka linux bridge
	items := ctx.pubAppNetworkStatus.GetAll()
	for _, ans := range items {
		appNetStatus := ans.(types.AppNetworkStatus)
		appID := appNetStatus.UUIDandVersion.UUID
		for _, ulStatus := range appNetStatus.UnderlayNetworkList {
			if ulStatus.Network != status.UUID {
				continue
			}
			if ulStatus.Bridge == "" {
				continue
			}
			log.Functionf("NetworkInstance - deleting Acls for UL Interface(%s)",
				ulStatus.Name)
			aclArgs := types.AppNetworkACLArgs{IsMgmt: false, BridgeName: ulStatus.Bridge,
				VifName: ulStatus.Vif, BridgeIP: ulStatus.BridgeIPAddr, AppIP: ulStatus.AllocatedIPAddr,
				UpLinks: status.IfNameList}
			rules := getNetworkACLRules(ctx, appID, ulStatus.Name)
			ruleList, err := deleteACLConfiglet(aclArgs, rules.ACLRules)
			if err != nil {
				log.Errorf("NetworkInstance DeleteACL failed: %s\n",
					err)
			}
			setNetworkACLRules(ctx, appID, ulStatus.Name, ruleList)
		}
	}
	return
}

func getNetworkACLRules(ctx *zedrouterContext, appID uuid.UUID, intf string) types.ULNetworkACLs {
	tmpMap := ctx.NLaclMap[appID]
	if tmpMap == nil {
		ctx.NLaclMap[appID] = make(map[string]types.ULNetworkACLs)
	}

	if _, ok := ctx.NLaclMap[appID][intf]; !ok {
		ctx.NLaclMap[appID][intf] = types.ULNetworkACLs{}
	}
	return ctx.NLaclMap[appID][intf]
}

func setNetworkACLRules(ctx *zedrouterContext, appID uuid.UUID, intf string, rulelist types.IPTablesRuleList) {
	tmpMap := ctx.NLaclMap[appID]
	if tmpMap == nil {
		ctx.NLaclMap[appID] = make(map[string]types.ULNetworkACLs)
	}

	if len(rulelist) == 0 {
		delete(ctx.NLaclMap[appID], intf)
	} else {
		rlist := types.ULNetworkACLs{ACLRules: rulelist}
		ctx.NLaclMap[appID][intf] = rlist
	}
}

func handleNetworkInstanceModify(
	ctxArg interface{},
	key string,
	configArg interface{},
	oldConfigArg interface{}) {

	ctx := ctxArg.(*zedrouterContext)
	pub := ctx.pubNetworkInstanceStatus
	config := configArg.(types.NetworkInstanceConfig)
	status := lookupNetworkInstanceStatus(ctx, key)
	if status != nil {
		log.Functionf("handleNetworkInstanceModify(%s)\n", key)
		status.ChangeInProgress = types.ChangeInProgressTypeModify
		pub.Publish(status.Key(), *status)
		doNetworkInstanceModify(ctx, config, status)
		niUpdateNIprobing(ctx, status)
		status.ChangeInProgress = types.ChangeInProgressTypeNone
		publishNetworkInstanceStatus(ctx, status)
		log.Functionf("handleNetworkInstanceModify(%s) done\n", key)
	} else {
		log.Fatalf("handleNetworkInstanceModify(%s) no status", key)
	}
}

func handleNetworkInstanceCreate(
	ctxArg interface{},
	key string,
	configArg interface{}) {

	ctx := ctxArg.(*zedrouterContext)
	config := configArg.(types.NetworkInstanceConfig)

	log.Functionf("handleNetworkInstanceCreate: (UUID: %s, name:%s)\n",
		key, config.DisplayName)

	pub := ctx.pubNetworkInstanceStatus
	status := types.NetworkInstanceStatus{
		NetworkInstanceConfig: config,
		NetworkInstanceInfo: types.NetworkInstanceInfo{
			IPAssignments: make(map[string]net.IP),
			VifMetricMap:  make(map[string]types.NetworkMetric),
		},
	}

	status.ChangeInProgress = types.ChangeInProgressTypeCreate
	ctx.networkInstanceStatusMap[status.UUID] = &status
	pub.Publish(status.Key(), status)

	status.PInfo = make(map[string]types.ProbeInfo)
	niUpdateNIprobing(ctx, &status)

	err := doNetworkInstanceCreate(ctx, &status)
	if err != nil {
		log.Errorf("doNetworkInstanceCreate(%s) failed: %s\n",
			key, err)
		log.Error(err)
		status.SetErrorNow(err.Error())
		status.ChangeInProgress = types.ChangeInProgressTypeNone
		publishNetworkInstanceStatus(ctx, &status)
		return
	}
	pub.Publish(status.Key(), status)

	if config.Activate {
		log.Functionf("handleNetworkInstanceCreate: Activating network instance")
		err := doNetworkInstanceActivate(ctx, &status)
		if err != nil {
			log.Errorf("doNetworkInstanceActivate(%s) failed: %s\n", key, err)
			log.Error(err)
			status.SetErrorNow(err.Error())
		} else {
			log.Functionf("Activated network instance %s %s", status.UUID, status.DisplayName)
			status.Activated = true
		}
	}

	status.ChangeInProgress = types.ChangeInProgressTypeNone
	publishNetworkInstanceStatus(ctx, &status)
	// Hooks for updating dependent objects
	checkAndRecreateAppNetwork(ctx, config.UUID)
	log.Functionf("handleNetworkInstanceCreate(%s) done\n", key)
}

func handleNetworkInstanceDelete(ctxArg interface{}, key string,
	configArg interface{}) {

	log.Functionf("handleNetworkInstanceDelete(%s)\n", key)
	ctx := ctxArg.(*zedrouterContext)
	pub := ctx.pubNetworkInstanceStatus
	status := lookupNetworkInstanceStatus(ctx, key)
	if status == nil {
		log.Functionf("handleNetworkInstanceDelete: unknown %s\n", key)
		return
	}
	status.ChangeInProgress = types.ChangeInProgressTypeDelete
	pub.Publish(status.Key(), *status)
	if status.Activated {
		doNetworkInstanceInactivate(ctx, status)
	}
	doNetworkInstanceDelete(ctx, status)
	delete(ctx.networkInstanceStatusMap, status.UUID)
	pub.Unpublish(status.Key())

	deleteNetworkInstanceMetrics(ctx, status.Key())
	log.Functionf("handleNetworkInstanceDelete(%s) done\n", key)
}

func doNetworkInstanceCreate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("NetworkInstance(%s-%s): NetworkType: %d, IpType: %d\n",
		status.DisplayName, status.UUID, status.Type, status.IpType)

	if err := doNetworkInstanceSanityCheck(ctx, status); err != nil {
		log.Errorf("NetworkInstance(%s-%s): Sanity Check failed: %s",
			status.DisplayName, status.UUID, err)
		return err
	}

	// Allocate bridgeNum.
	bridgeNum := bridgeNumAllocate(ctx, status.UUID)
	status.BridgeNum = bridgeNum
	bridgeMac := ""
	var bridgeName string
	var err error

	switch status.Type {
	case types.NetworkInstanceTypeLocal, types.NetworkInstanceTypeCloud:
		bridgeName = fmt.Sprintf("bn%d", bridgeNum)
		status.BridgeName = bridgeName
		if err, bridgeMac = doCreateBridge(bridgeName, bridgeNum, status); err != nil {
			return err
		}

	case types.NetworkInstanceTypeSwitch:
		if status.CurrentUplinkIntf == "" {
			// Create a local-only bridge
			bridgeName = fmt.Sprintf("bn%d", bridgeNum)
			status.BridgeName = bridgeName
			if err, bridgeMac = doCreateBridge(bridgeName, bridgeNum, status); err != nil {
				return err
			}
		} else {
			// Find bridge created by nim
			if bridgeName, bridgeMac, err = doLookupBridge(ctx, status); err != nil {
				return err
			}
			status.BridgeName = bridgeName
		}
	}

	// Get Ifindex of bridge and store it in network instance status
	bridgeLink, err := netlink.LinkByName(bridgeName)
	if err != nil {
		err = fmt.Errorf("doNetworkInstanceCreate: LinkByName(%s) failed: %v",
			bridgeName, err)
		log.Error(err)
		return err
	}
	status.BridgeIfindex = bridgeLink.Attrs().Index

	status.BridgeMac = bridgeMac
	publishNetworkInstanceStatus(ctx, status)

	log.Functionf("bridge created. BridgeMac: %s\n", bridgeMac)

	if err := setBridgeIPAddr(ctx, status); err != nil {
		return err
	}
	log.Functionf("IpAddress set for bridge\n")

	// Create a hosts directory for the new bridge
	// Directory is /run/zedrouter/hosts.${BRIDGENAME}
	hostsDirpath := runDirname + "/hosts." + bridgeName
	deleteHostsConfiglet(hostsDirpath, false)
	createHostsConfiglet(hostsDirpath,
		status.DnsNameToIPList)

	if status.BridgeIPAddr != "" {
		// XXX arbitrary name "router"!!
		addToHostsConfiglet(hostsDirpath, "router",
			[]string{status.BridgeIPAddr})
	}

	// Start clean
	deleteDnsmasqConfiglet(bridgeName)
	stopDnsmasq(bridgeName, false, false)

	if status.BridgeIPAddr != "" {
		dnsServers := types.GetDNSServers(*ctx.deviceNetworkStatus,
			status.CurrentUplinkIntf)
		ntpServers := types.GetNTPServers(*ctx.deviceNetworkStatus,
			status.CurrentUplinkIntf)
		createDnsmasqConfiglet(ctx, bridgeName,
			status.BridgeIPAddr, &status.NetworkInstanceConfig,
			hostsDirpath, status.BridgeIPSets,
			status.CurrentUplinkIntf, dnsServers, ntpServers)
		startDnsmasq(bridgeName)
	}

	// monitor the DNS and DHCP information
	log.Functionf("Creating %s at %s", "DNSMonitor", agentlog.GetMyStack())
	go DNSMonitor(bridgeName, bridgeNum, ctx, status)

	if status.IsIPv6() {
		// XXX do we need same logic as for IPv4 dnsmasq to not
		// advertize as default router? Might we need lower
		// radvd preference if isolated local network?
		restartRadvdWithNewConfig(bridgeName)
	}

	switch status.Type {
	case types.NetworkInstanceTypeCloud:
		err := vpnCreate(ctx, status)
		if err != nil {
			return err
		}
	default:
	}
	return nil
}

func doNetworkInstanceSanityCheck(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("Sanity Checking NetworkInstance(%s-%s): type:%d, IpType:%d\n",
		status.DisplayName, status.UUID, status.Type, status.IpType)

	err := checkNIphysicalPort(ctx, status)
	if err != nil {
		log.Error(err)
		return err
	}

	//  Check NetworkInstanceType
	switch status.Type {
	case types.NetworkInstanceTypeLocal:
		// Do nothing
	case types.NetworkInstanceTypeSwitch:
		// Do nothing
	case types.NetworkInstanceTypeCloud:
		// Do nothing
	default:
		err := fmt.Sprintf("Instance type %d not supported", status.Type)
		return errors.New(err)
	}

	if err := checkPortAvailable(ctx, status); err != nil {
		log.Errorf("checkPortAvailable failed: Port: %s, err:%s",
			status.CurrentUplinkIntf, err)
		return err
	}

	// IpType - Check for valid types
	switch status.IpType {
	case types.AddressTypeNone:
		// Do nothing
	case types.AddressTypeIPV4, types.AddressTypeIPV6,
		types.AddressTypeCryptoIPV4, types.AddressTypeCryptoIPV6:

		err := doNetworkInstanceSubnetSanityCheck(ctx, status)
		if err != nil {
			return err
		}

		if status.Gateway.IsUnspecified() {
			err := fmt.Sprintf("Gateway Unspecified: %+v\n",
				status.Gateway)
			return errors.New(err)
		}
		err = DoNetworkInstanceStatusDhcpRangeSanityCheck(status)
		if err != nil {
			return err
		}

	default:
		err := fmt.Sprintf("IpType %d not supported\n", status.IpType)
		return errors.New(err)
	}

	return nil
}

func doNetworkInstanceSubnetSanityCheck(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	// Mesh network instance with crypto V6 addressing will not need any
	// subnet specific configuration
	if (status.Subnet.IP == nil || status.Subnet.IP.IsUnspecified()) &&
		(status.IpType != types.AddressTypeCryptoIPV6) {
		err := fmt.Sprintf("Subnet Unspecified for %s-%s: %+v\n",
			status.Key(), status.DisplayName, status.Subnet)
		return errors.New(err)
	}

	// Verify Subnet doesn't overlap with other network instances
	for _, iterStatusEntry := range ctx.networkInstanceStatusMap {
		if status == iterStatusEntry {
			continue
		}

		// We check for overlapping subnets by checking the
		// SubnetAddr ( first address ) is not contained in the subnet of
		// any other NI and vice-versa ( Other NI Subnet addrs are not
		// contained in the current NI subnet)

		// Check if status.Subnet is contained in iterStatusEntry.Subnet
		if iterStatusEntry.Subnet.Contains(status.Subnet.IP) {
			errStr := fmt.Sprintf("Subnet(%s) SubnetAddr(%s) overlaps with another "+
				"network instance(%s-%s) Subnet(%s)\n",
				status.Subnet.String(), status.Subnet.IP.String(),
				iterStatusEntry.DisplayName, iterStatusEntry.UUID,
				iterStatusEntry.Subnet.String())
			return errors.New(errStr)
		}

		// Reverse check..Check if iterStatusEntry.Subnet is contained in status.subnet
		if status.Subnet.Contains(iterStatusEntry.Subnet.IP) {
			errStr := fmt.Sprintf("Another network instance(%s-%s) Subnet(%s) "+
				"overlaps with Subnet(%s)",
				iterStatusEntry.DisplayName, iterStatusEntry.UUID,
				iterStatusEntry.Subnet.String(),
				status.Subnet.String())
			return errors.New(errStr)
		}
	}
	return nil
}

// DoDhcpRangeSanityCheck
// 1) Must always be Unspecified
// 2) It should be a subset of Subnet
func DoNetworkInstanceStatusDhcpRangeSanityCheck(
	status *types.NetworkInstanceStatus) error {
	// For Mesh type network instance with Crypto V6 addressing, no dhcp-range
	// will be specified.
	if status.DhcpRange.Start == nil || status.DhcpRange.Start.IsUnspecified() {
		err := fmt.Sprintf("DhcpRange Start Unspecified: %+v\n",
			status.DhcpRange.Start)
		return errors.New(err)
	}
	if !status.Subnet.Contains(status.DhcpRange.Start) {
		err := fmt.Sprintf("DhcpRange Start(%s) not within Subnet(%s)\n",
			status.DhcpRange.Start.String(), status.Subnet.String())
		return errors.New(err)
	}
	if status.DhcpRange.End == nil || status.DhcpRange.End.IsUnspecified() {
		err := fmt.Sprintf("DhcpRange End Unspecified: %+v\n",
			status.DhcpRange.Start)
		return errors.New(err)
	}
	if !status.Subnet.Contains(status.DhcpRange.End) {
		err := fmt.Sprintf("DhcpRange End(%s) not within Subnet(%s)\n",
			status.DhcpRange.End.String(), status.Subnet.String())
		return errors.New(err)
	}
	return nil
}

func doNetworkInstanceModify(ctx *zedrouterContext,
	config types.NetworkInstanceConfig,
	status *types.NetworkInstanceStatus) {

	log.Functionf("doNetworkInstanceModify: key %s\n", config.UUID)
	if config.Type != status.Type {
		log.Functionf("doNetworkInstanceModify: key %s\n", config.UUID)
		// We do not allow Type to change.

		err := fmt.Errorf("Changing Type of NetworkInstance from %d to %d is not supported", status.Type, config.Type)
		log.Error(err)
		status.SetErrorNow(err.Error())
	}

	err := checkNIphysicalPort(ctx, status)
	if err != nil {
		log.Error(err)
		status.SetErrorNow(err.Error())
		return
	}

	if config.Logicallabel != status.Logicallabel {
		err := fmt.Errorf("Changing Logicallabel in NetworkInstance is not yet supported: from %s to %s",
			status.Logicallabel, config.Logicallabel)
		log.Error(err)
		status.SetErrorNow(err.Error())
		return
	}

	if config.Activate && !status.Activated {
		err := doNetworkInstanceActivate(ctx, status)
		if err != nil {
			log.Errorf("doNetworkInstanceActivate(%s) failed: %s\n",
				config.Key(), err)
			log.Error(err)
			status.SetErrorNow(err.Error())
		} else {
			status.Activated = true
		}
	} else if status.Activated && !config.Activate {
		doNetworkInstanceInactivate(ctx, status)
		status.Activated = false
	}
}

func checkNIphysicalPort(ctx *zedrouterContext, status *types.NetworkInstanceStatus) error {
	// check the NI have the valid physical port binding to
	label := status.Logicallabel
	if label != "" && !strings.EqualFold(label, "uplink") &&
		!strings.EqualFold(label, "freeuplink") {
		ifname := types.LogicallabelToIfName(ctx.deviceNetworkStatus, label)
		devPort := ctx.deviceNetworkStatus.GetPortByIfName(ifname)
		if devPort == nil {
			err := fmt.Sprintf("Network Instance port %s does not exist", label)
			return errors.New(err)
		}
	}
	return nil
}

// getSwitchNetworkInstanceUsingIfname
//		This function assumes if a port used by networkInstance of type SWITCH
//		is not shared by other switch network instances.
func getSwitchNetworkInstanceUsingIfname(
	ctx *zedrouterContext,
	ifname string) (status *types.NetworkInstanceStatus) {

	pub := ctx.pubNetworkInstanceStatus
	items := pub.GetAll()

	for _, st := range items {
		status := st.(types.NetworkInstanceStatus)
		ifname2 := types.LogicallabelToIfName(ctx.deviceNetworkStatus,
			status.Logicallabel)
		if ifname2 != ifname {
			log.Functionf("getSwitchNetworkInstanceUsingIfname: NI (%s) not using %s",
				status.DisplayName, ifname)
			continue
		}

		if status.Type != types.NetworkInstanceTypeSwitch {
			log.Functionf("getSwitchNetworkInstanceUsingIfname: networkInstance (%s) "+
				"not of type (%d) switch\n",
				status.DisplayName, status.Type)
			continue
		}
		// Found Status using the Port.
		log.Functionf("getSwitchNetworkInstanceUsingIfname: networkInstance (%s) using "+
			"logicallabel: %s, ifname: %s, type: %d\n",
			status.DisplayName, status.Logicallabel, ifname, status.Type)

		return &status
	}
	return nil
}

// haveSwitchNetworkInstances returns true if we have one or more switch
// network instances
func haveSwitchNetworkInstances(ctx *zedrouterContext) bool {
	sub := ctx.subNetworkInstanceConfig
	items := sub.GetAll()

	for _, c := range items {
		config := c.(types.NetworkInstanceConfig)
		if config.Type == types.NetworkInstanceTypeSwitch {
			return true
		}
	}
	return false
}

func restartDnsmasq(ctx *zedrouterContext, status *types.NetworkInstanceStatus) {

	log.Functionf("restartDnsmasq(%s) ipsets %v\n",
		status.BridgeName, status.BridgeIPSets)
	bridgeName := status.BridgeName
	stopDnsmasq(bridgeName, false, true)

	hostsDirpath := runDirname + "/hosts." + bridgeName
	// XXX arbitrary name "router"!!
	addToHostsConfiglet(hostsDirpath, "router",
		[]string{status.BridgeIPAddr})

	// Use existing BridgeIPSets
	dnsServers := types.GetDNSServers(*ctx.deviceNetworkStatus,
		status.CurrentUplinkIntf)
	ntpServers := types.GetNTPServers(*ctx.deviceNetworkStatus,
		status.CurrentUplinkIntf)
	createDnsmasqConfiglet(ctx, bridgeName, status.BridgeIPAddr,
		&status.NetworkInstanceConfig, hostsDirpath, status.BridgeIPSets,
		status.CurrentUplinkIntf, dnsServers, ntpServers)
	createHostDnsmasqFile(ctx, bridgeName)
	startDnsmasq(bridgeName)
}

func createHostDnsmasqFile(ctx *zedrouterContext, bridge string) {
	pub := ctx.pubAppNetworkStatus
	items := pub.GetAll()
	for _, st := range items {
		status := st.(types.AppNetworkStatus)
		for _, ulStatus := range status.UnderlayNetworkList {
			if strings.Compare(bridge, ulStatus.Bridge) != 0 {
				continue
			}
			addhostDnsmasq(bridge, ulStatus.Mac,
				ulStatus.AllocatedIPAddr, status.UUIDandVersion.UUID.String())
			log.Functionf("createHostDnsmasqFile:(%s) mac=%s, IP=%s\n", bridge, ulStatus.Mac, ulStatus.AllocatedIPAddr)
		}
	}
}

// Returns an IP address as a string, or "" if not found.
func lookupOrAllocateIPv4(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus,
	mac net.HardwareAddr) (string, error) {

	log.Functionf("lookupOrAllocateIPv4(%s-%s): mac:%s\n",
		status.DisplayName, status.Key(), mac.String())
	// Lookup to see if it exists
	if ip, ok := status.IPAssignments[mac.String()]; ok {
		log.Functionf("found Ip addr ( %s) for mac(%s)\n",
			ip.String(), mac.String())
		return ip.String(), nil
	}

	log.Functionf("bridgeName %s Subnet %v range %v-%v\n",
		status.BridgeName, status.Subnet,
		status.DhcpRange.Start.String(), status.DhcpRange.End.String())

	if status.DhcpRange.Start == nil {
		if status.Type == types.NetworkInstanceTypeSwitch {
			log.Functionf("%s-%s switch means no bridgeIpAddr",
				status.DisplayName, status.Key())
			return "", nil
		}
		log.Fatalf("%s-%s: nil DhcpRange.Start",
			status.DisplayName, status.Key())
	}

	// Starting guess based on number allocated
	allocated := uint(len(status.IPAssignments))
	if status.Gateway != nil {
		// With Gateway present in network instance status,
		// we would have used that as our Bridge IP address and not
		// allocated new one. Since bridge IP address is also stored
		// as part of IPAssignments, the actual allocated IP address
		// numner is 1 less than the length of IPAssignments map size.
		allocated--
	}
	a := addToIP(status.DhcpRange.Start, allocated)
	for status.DhcpRange.End == nil ||
		bytes.Compare(a, status.DhcpRange.End) <= 0 {

		log.Functionf("lookupOrAllocateIPv4(%s) testing %s\n",
			mac.String(), a.String())
		if status.IsIpAssigned(a) {
			a = addToIP(a, 1)
			continue
		}
		log.Functionf("lookupOrAllocateIPv4(%s) found free %s\n",
			mac.String(), a.String())

		recordIPAssignment(ctx, status, a, mac.String())
		return a.String(), nil
	}
	errStr := fmt.Sprintf("lookupOrAllocateIPv4(%s) no free address in DhcpRange",
		status.Key())
	return "", errors.New(errStr)
}

// recordIPAssigment updates status and publishes the result
func recordIPAssignment(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus, ip net.IP, mac string) {

	status.IPAssignments[mac] = ip
	// Publish the allocation
	publishNetworkInstanceStatus(ctx, status)
}

// Add to an IPv4 address
func addToIP(ip net.IP, addition uint) net.IP {
	addr := ip.To4()
	if addr == nil {
		log.Fatalf("addIP: not an IPv4 address %s", ip.String())
	}
	val := uint(addr[0])<<24 + uint(addr[1])<<16 +
		uint(addr[2])<<8 + uint(addr[3])
	val += addition
	val0 := byte((val >> 24) & 0xFF)
	val1 := byte((val >> 16) & 0xFF)
	val2 := byte((val >> 8) & 0xFF)
	val3 := byte(val & 0xFF)
	return net.IPv4(val0, val1, val2, val3)
}

// releaseIPv4
//	XXX TODO - This should be a method in NetworkInstanceSm
func releaseIPv4FromNetworkInstance(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus,
	mac net.HardwareAddr) error {

	log.Functionf("releaseIPv4(%s)\n", mac.String())
	// Lookup to see if it exists
	if _, ok := status.IPAssignments[mac.String()]; !ok {
		errStr := fmt.Sprintf("releaseIPv4: not found %s for %s",
			mac.String(), status.Key())
		log.Error(errStr)
		return errors.New(errStr)
	}
	delete(status.IPAssignments, mac.String())
	publishNetworkInstanceStatus(ctx, status)
	return nil
}

func getPrefixLenForBridgeIP(
	status *types.NetworkInstanceStatus) int {
	var prefixLen int
	if status.Subnet.IP != nil {
		prefixLen, _ = status.Subnet.Mask.Size()
	} else if status.IsIPv6() {
		prefixLen = 128
	} else {
		prefixLen = 24
	}
	return prefixLen
}

func doConfigureIpAddrOnInterface(
	ipAddr string,
	prefixLen int,
	link netlink.Link) error {

	ipAddr = fmt.Sprintf("%s/%d", ipAddr, prefixLen)

	//    ip addr add ${ipAddr}/N dev ${bridgeName}
	addr, err := netlink.ParseAddr(ipAddr)
	if err != nil {
		errStr := fmt.Sprintf("ParseAddr %s failed: %s", ipAddr, err)
		log.Errorln(errStr)
		return errors.New(errStr)
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		errStr := fmt.Sprintf("AddrAdd %s failed: %s", ipAddr, err)
		log.Errorln(errStr)
		return errors.New(errStr)
	}
	return nil
}

func setBridgeIPAddr(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("setBridgeIPAddr(%s-%s)\n",
		status.DisplayName, status.Key())

	if status.BridgeName == "" {
		// Called too early
		log.Functionf("setBridgeIPAddr: don't yet have a bridgeName for %s\n",
			status.UUID)
		return nil
	}

	// Get the linux interface with the attributes.
	// This is used to add an IP Address below.
	link, _ := netlink.LinkByName(status.BridgeName)
	if link == nil {
		// XXX..Why would this fail? Should this be Fatal instead??
		errStr := fmt.Sprintf("Failed to get link for Bridge %s", status.BridgeName)
		return errors.New(errStr)
	}
	log.Functionf("Bridge: %s, Link: %+v\n", status.BridgeName, link)

	var ipAddr string
	var err error

	// Assign the gateway Address as the bridge IP address
	var bridgeMac net.HardwareAddr

	switch link.(type) {
	case *netlink.Bridge:
		// XXX always true?
		bridgeLink := link.(*netlink.Bridge)
		bridgeMac = bridgeLink.HardwareAddr
	default:
		// XXX - Same here.. Should be Fatal??
		errStr := fmt.Sprintf("Not a bridge %s",
			status.BridgeName)
		return errors.New(errStr)
	}
	if status.Gateway != nil {
		ipAddr = status.Gateway.String()
		status.IPAssignments[bridgeMac.String()] = status.Gateway
	}
	log.Functionf("BridgeMac: %s, ipAddr: %s\n",
		bridgeMac.String(), ipAddr)
	status.BridgeIPAddr = ipAddr
	publishNetworkInstanceStatus(ctx, status)
	log.Functionf("Published NetworkStatus. BridgeIpAddr: %s\n",
		status.BridgeIPAddr)

	if status.BridgeIPAddr == "" {
		log.Functionf("Does not yet have a bridge IP address for %s\n",
			status.Key())
		return nil
	}

	prefixLen := getPrefixLenForBridgeIP(status)
	if err = doConfigureIpAddrOnInterface(ipAddr, prefixLen, link); err != nil {
		log.Errorf("Failed to configure IPAddr on Interface\n")
		return err
	}

	// Create new radvd configuration and restart radvd if ipv6
	if status.IsIPv6() {
		log.Functionf("Restart Radvd\n")
		restartRadvdWithNewConfig(status.BridgeName)
	}
	return nil
}

// updateBridgeIPAddr
// 	Called a bridge service has been added/updated/deleted
func updateBridgeIPAddr(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	log.Functionf("updateBridgeIPAddr(%s)\n", status.Key())

	old := status.BridgeIPAddr
	err := setBridgeIPAddr(ctx, status)
	if err != nil {
		log.Functionf("updateBridgeIPAddr: %s\n", err)
		return
	}
	if status.BridgeIPAddr != old && status.BridgeIPAddr != "" {
		log.Functionf("updateBridgeIPAddr(%s) restarting dnsmasq\n",
			status.Key())
		restartDnsmasq(ctx, status)
	}
}

// maybeUpdateBridgeIPAddr
// 	Find ifname as a bridge Port and see if it can be updated
func maybeUpdateBridgeIPAddr(
	ctx *zedrouterContext,
	ifname string) {

	status := getSwitchNetworkInstanceUsingIfname(ctx, ifname)
	if status == nil {
		return
	}
	log.Functionf("maybeUpdateBridgeIPAddr: found "+
		"NetworkInstance %s", status.DisplayName)

	if !status.Activated {
		log.Errorf("maybeUpdateBridgeIPAddr: "+
			"network instance %s not activated\n", status.DisplayName)
		return
	}
	updateBridgeIPAddr(ctx, status)
	return
}

// doNetworkInstanceActivate
func doNetworkInstanceActivate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("doNetworkInstanceActivate NetworkInstance key %s type %d\n",
		status.UUID, status.Type)

	// Check that Port is either "uplink", "freeuplink", or
	// an existing port name assigned to domO/zedrouter.
	// A Bridge only works with a single logicallabel interface.
	// Management ports are not allowed to be part of Switch networks.
	err := checkPortAvailable(ctx, status)
	if err != nil {
		log.Errorf("checkPortAvailable failed: CurrentUplinkIntf: %s, err:%s",
			status.CurrentUplinkIntf, err)
		return err
	}

	// Get a list of IfNames to the ones we have an ifIndex for.
	if status.Type == types.NetworkInstanceTypeSwitch {
		// switched NI is not probed and does not have a CurrentUplinkIntf
		status.IfNameList = getIfNameListForLLOrIfname(ctx, status.Logicallabel)
	} else {
		status.IfNameList = getIfNameListForLLOrIfname(ctx, status.CurrentUplinkIntf)
	}
	log.Functionf("IfNameList: %+v", status.IfNameList)
	switch status.Type {
	case types.NetworkInstanceTypeSwitch:
		err = bridgeActivate(ctx, status)
		if err != nil {
			updateBridgeIPAddr(ctx, status)
		}
	case types.NetworkInstanceTypeLocal:
		err = natActivate(ctx, status)
		if err == nil {
			err = createServer4(ctx, status.BridgeIPAddr,
				status.BridgeName)
		}

	case types.NetworkInstanceTypeCloud:
		err = vpnActivate(ctx, status)
		if err == nil {
			err = createServer4(ctx, status.BridgeIPAddr,
				status.BridgeName)
		}

	default:
		errStr := fmt.Sprintf("doNetworkInstanceActivate: NetworkInstance %d not yet supported",
			status.Type)
		err = errors.New(errStr)
	}
	status.ProgUplinkIntf = status.CurrentUplinkIntf
	// setup the ACLs for the bridge
	// Here we explicitly adding the iptables rules, to the bottom of the
	// rule chains, which are tied to the Linux bridge itself and not the
	//  rules for any specific domU vifs.
	aclArgs := types.AppNetworkACLArgs{IsMgmt: false, BridgeName: status.BridgeName,
		BridgeIP: status.BridgeIPAddr, NIType: status.Type, UpLinks: status.IfNameList}
	handleNetworkInstanceACLConfiglet("-A", aclArgs)
	return err
}

// getIfNameListForLLorIfname takes a logicallabel or a ifname
// Get a list of IfNames to the ones we have an ifIndex for.
// In the case where the port maps to multiple underlying ports
// (For Ex: uplink), only include ports that have an ifindex.
//	If there is no such port with ifindex, then retain the whole list.
//	NetworkInstance creation will fail when programming default routes
//  and iptable rules in that case - and that should be fine.
func getIfNameListForLLOrIfname(
	ctx *zedrouterContext,
	llOrIfname string) []string {

	ifNameList := labelToIfNames(ctx, llOrIfname)
	log.Functionf("ifNameList: %+v", ifNameList)

	filteredList := make([]string, 0)
	for _, ifName := range ifNameList {
		dnsPort := ctx.deviceNetworkStatus.GetPortByIfName(ifName)
		if dnsPort != nil {
			// XXX - We have a bug in MakeDeviceNetworkStatus where we are allowing
			//	a device without the corresponding linux interface. We can
			//	remove this check for ifindex here when the MakeDeviceStatus
			//	is fixed.
			// XXX That bug has been fixed. Retest without this code?
			ifIndex, err := IfnameToIndex(log, ifName)
			if err == nil {
				log.Functionf("ifName %s, ifindex: %d added to filteredList",
					ifName, ifIndex)
				filteredList = append(filteredList, ifName)
			} else {
				log.Functionf("ifIndex not found for ifName(%s) - err: %s",
					ifName, err.Error())
			}
		} else {
			log.Functionf("DeviceNetworkStatus not found for ifName(%s)",
				ifName)
		}
	}
	if len(filteredList) > 0 {
		log.Functionf("filteredList: %+v", filteredList)
		return filteredList
	}
	log.Functionf("ifname or ifindex not found for any interface for logicallabel(%s)."+
		"Returning the unfiltered list: %+v", llOrIfname, ifNameList)
	return ifNameList
}

func doNetworkInstanceInactivate(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	log.Functionf("doNetworkInstanceInactivate NetworkInstance key %s type %d\n",
		status.UUID, status.Type)

	bridgeInactivateforNetworkInstance(ctx, status)
	switch status.Type {
	case types.NetworkInstanceTypeLocal:
		natInactivate(ctx, status, false)
		deleteServer4(ctx, status.BridgeIPAddr, status.BridgeName)
	case types.NetworkInstanceTypeCloud:
		vpnInactivate(ctx, status)
		deleteServer4(ctx, status.BridgeIPAddr, status.BridgeName)
	}

	return
}

func doNetworkInstanceDelete(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	log.Functionf("doNetworkInstanceDelete NetworkInstance key %s type %d\n",
		status.UUID, status.Type)

	// Anything to do except the inactivate already done?
	switch status.Type {
	case types.NetworkInstanceTypeSwitch:
		// Nothing to do.
	case types.NetworkInstanceTypeLocal:
		natDelete(status)
	case types.NetworkInstanceTypeCloud:
		vpnDelete(ctx, status)
	default:
		log.Errorf("NetworkInstance(%s-%s): Type %d not yet supported",
			status.DisplayName, status.UUID, status.Type)
	}

	doBridgeAclsDelete(ctx, status)
	if status.BridgeName != "" {
		stopDnsmasq(status.BridgeName, false, false)

		if status.IsIPv6() {
			stopRadvd(status.BridgeName, true)
		}
		DNSStopMonitor(status.BridgeNum)
	}
	networkInstanceBridgeDelete(ctx, status)
}

func lookupNetworkInstanceConfig(ctx *zedrouterContext, key string) *types.NetworkInstanceConfig {

	sub := ctx.subNetworkInstanceConfig
	c, _ := sub.Get(key)
	if c == nil {
		return nil
	}
	config := c.(types.NetworkInstanceConfig)
	return &config
}

func lookupNetworkInstanceStatus(ctx *zedrouterContext, key string) *types.NetworkInstanceStatus {
	pub := ctx.pubNetworkInstanceStatus
	st, _ := pub.Get(key)
	if st == nil {
		return nil
	}
	status := st.(types.NetworkInstanceStatus)
	return &status
}

func lookupNetworkInstanceMetrics(ctx *zedrouterContext, key string) *types.NetworkInstanceMetrics {
	pub := ctx.pubNetworkInstanceMetrics
	st, _ := pub.Get(key)
	if st == nil {
		return nil
	}
	status := st.(types.NetworkInstanceMetrics)
	return &status
}

func createNetworkInstanceMetrics(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus,
	nms *types.NetworkMetrics) *types.NetworkInstanceMetrics {

	niMetrics := types.NetworkInstanceMetrics{
		UUIDandVersion: status.UUIDandVersion,
		DisplayName:    status.DisplayName,
		Type:           status.Type,
	}
	netMetrics := types.NetworkMetrics{}
	netMetric := status.UpdateNetworkMetrics(log, nms)
	status.UpdateBridgeMetrics(log, nms, netMetric)

	netMetrics.MetricList = []types.NetworkMetric{*netMetric}
	niMetrics.NetworkMetrics = netMetrics
	niMetrics.ProbeMetrics = getNIProbeMetric(ctx, status)
	switch status.Type {
	case types.NetworkInstanceTypeCloud:
		if strongSwanVpnStatusGet(ctx, status, &niMetrics) {
			publishNetworkInstanceStatus(ctx, status)
		}
	default:
	}

	return &niMetrics
}

// this is periodic metrics handler
func publishNetworkInstanceMetricsAll(ctx *zedrouterContext) {
	pub := ctx.pubNetworkInstanceStatus
	niList := pub.GetAll()
	if niList == nil {
		return
	}
	nms := getNetworkMetrics(ctx)
	for _, ni := range niList {
		status := ni.(types.NetworkInstanceStatus)
		netMetrics := createNetworkInstanceMetrics(ctx, &status, &nms)
		publishNetworkInstanceMetrics(ctx, netMetrics)
	}
}

func deleteNetworkInstanceMetrics(ctx *zedrouterContext, key string) {
	pub := ctx.pubNetworkInstanceMetrics
	if metrics := lookupNetworkInstanceMetrics(ctx, key); metrics != nil {
		pub.Unpublish(metrics.Key())
	}
}

func publishNetworkInstanceStatus(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	copyProbeStats(ctx, status)
	ctx.networkInstanceStatusMap[status.UUID] = status
	pub := ctx.pubNetworkInstanceStatus
	pub.Publish(status.Key(), *status)
}

func publishNetworkInstanceMetrics(ctx *zedrouterContext,
	status *types.NetworkInstanceMetrics) {

	pub := ctx.pubNetworkInstanceMetrics
	pub.Publish(status.Key(), *status)
}

// ==== Bridge

func bridgeActivate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("bridgeActivate(%s)\n", status.DisplayName)
	if !strings.HasPrefix(status.BridgeName, "bn") {
		log.Noticef("bridgeActivate(%s) %s ignored",
			status.DisplayName, status.BridgeName)
		return nil
	}

	bridgeLink, err := findBridge(status.BridgeName)
	if err != nil {
		errStr := fmt.Sprintf("findBridge(%s) failed %s",
			status.BridgeName, err)
		return errors.New(errStr)
	}
	// Find logicallabel for first in list
	if len(status.IfNameList) == 0 {
		errStr := fmt.Sprintf("IfNameList empty for %s",
			status.BridgeName)
		return errors.New(errStr)
	}
	ifname := status.IfNameList[0]
	alink, _ := netlink.LinkByName(ifname)
	if alink == nil {
		errStr := fmt.Sprintf("Unknown Logicallabel %s, %s",
			status.Logicallabel, ifname)
		return errors.New(errStr)
	}
	// Make sure it is up
	//    ip link set ${logicallabel} up
	if err := netlink.LinkSetUp(alink); err != nil {
		errStr := fmt.Sprintf("LinkSetUp on %s ifname %s failed: %s",
			status.Logicallabel, ifname, err)
		return errors.New(errStr)
	}
	// ip link set ${logicallabel} master ${bridge_name}
	if err := netlink.LinkSetMaster(alink, bridgeLink); err != nil {
		errStr := fmt.Sprintf("LinkSetMaster %s ifname %s bridge %s failed: %s",
			status.Logicallabel, ifname, status.BridgeName, err)
		return errors.New(errStr)
	}
	log.Functionf("bridgeActivate: added %s ifname %s to bridge %s\n",
		status.Logicallabel, ifname, status.BridgeName)
	return nil
}

// bridgeInactivateforNetworkInstance deletes any bnX bridge but not
// others created by nim
func bridgeInactivateforNetworkInstance(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	log.Functionf("bridgeInactivateforNetworkInstance(%s) %s",
		status.DisplayName, status.BridgeName)
	if !strings.HasPrefix(status.BridgeName, "bn") {
		log.Noticef("bridgeInactivateforNetworkInstance(%s) %s ignored",
			status.DisplayName, status.BridgeName)
		return
	}
	// Find logicallabel
	if len(status.IfNameList) == 0 {
		errStr := fmt.Sprintf("IfNameList empty for %s",
			status.BridgeName)
		log.Errorln(errStr)
		return
	}
	ifname := status.IfNameList[0]
	alink, _ := netlink.LinkByName(ifname)
	if alink == nil {
		errStr := fmt.Sprintf("Unknown logicallabel %s, %s",
			status.Logicallabel, ifname)
		log.Errorln(errStr)
		return
	}
	// ip link set ${logicallabel} nomaster
	if err := netlink.LinkSetNoMaster(alink); err != nil {
		errStr := fmt.Sprintf("LinkSetNoMaster %s ifname %s failed: %s",
			status.Logicallabel, ifname, err)
		log.Functionln(errStr)
		return
	}
	log.Functionf("bridgeInactivateforNetworkInstance: removed %s ifname %s from bridge\n",
		status.Logicallabel, ifname)
}

// ==== Nat

// XXX need to redo this when MgmtPorts/FreeMgmtPorts changes?
func natActivate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("natActivate(%s)\n", status.DisplayName)
	subnetStr := status.Subnet.String()

	// status.IfNameList should not have more than one interface name.
	// Put a check anyway.
	// XXX Remove the loop below in future when we have reasonable stability in code.
	if len(status.IfNameList) > 1 {
		errStr := fmt.Sprintf("Network instance can have ONE interface active at the most,"+
			" but we have %d active interfaces.", len(status.IfNameList))
		log.Errorf(errStr)
		err := errors.New(errStr)
		return err
	}
	for _, a := range status.IfNameList {
		log.Functionf("Adding iptables rules for %s \n", a)
		err := iptables.IptableCmd(log, "-t", "nat", "-A", "POSTROUTING", "-o", a,
			"-s", subnetStr, "-j", "MASQUERADE")
		if err != nil {
			log.Errorf("IptableCmd failed: %s", err)
			return err
		}
		err = PbrRouteAddAll(status.BridgeName, a)
		if err != nil {
			log.Errorf("PbrRouteAddAll for Bridge(%s) and interface %s failed. "+
				"Err: %s", status.BridgeName, a, err)
			return err
		}
		devicenetwork.AddGatewaySourceRule(log, status.Subnet,
			net.ParseIP(status.BridgeIPAddr), devicenetwork.PbrNatOutGatewayPrio)
		devicenetwork.AddSourceRule(log, status.BridgeIfindex, status.Subnet, true, devicenetwork.PbrNatOutPrio)
		devicenetwork.AddInwardSourceRule(log, status.BridgeIfindex, status.Subnet, true, devicenetwork.PbrNatInPrio)
	}
	return nil
}

func natInactivate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus, inActivateOld bool) {

	log.Functionf("natInactivate(%s)\n", status.DisplayName)
	subnetStr := status.Subnet.String()
	var oldUplinkIntf string
	if inActivateOld {
		// XXX Should we instead use status.ProgUplinkIntf
		oldUplinkIntf = status.PrevUplinkIntf
	} else {
		oldUplinkIntf = status.CurrentUplinkIntf
	}
	err := iptables.IptableCmd(log, "-t", "nat", "-D", "POSTROUTING", "-o", oldUplinkIntf,
		"-s", subnetStr, "-j", "MASQUERADE")
	if err != nil {
		log.Errorf("natInactivate: iptableCmd failed %s\n", err)
	}
	devicenetwork.DelGatewaySourceRule(log, status.Subnet,
		net.ParseIP(status.BridgeIPAddr), devicenetwork.PbrNatOutGatewayPrio)
	devicenetwork.DelSourceRule(log, status.BridgeIfindex, status.Subnet, true, devicenetwork.PbrNatOutPrio)
	devicenetwork.DelInwardSourceRule(log, status.BridgeIfindex, status.Subnet, true, devicenetwork.PbrNatInPrio)
	err = PbrRouteDeleteAll(status.BridgeName, oldUplinkIntf)
	if err != nil {
		log.Errorf("natInactivate: PbrRouteDeleteAll failed %s\n", err)
	}
}

func natDelete(status *types.NetworkInstanceStatus) {

	log.Functionf("natDelete(%s)\n", status.DisplayName)
}

func lookupNetworkInstanceStatusByBridgeName(ctx *zedrouterContext,
	bridgeName string) *types.NetworkInstanceStatus {

	pub := ctx.pubNetworkInstanceStatus
	items := pub.GetAll()
	for _, st := range items {
		status := st.(types.NetworkInstanceStatus)
		if status.BridgeName == bridgeName {
			return &status
		}
	}
	return nil
}

func networkInstanceAddressType(ctx *zedrouterContext, bridgeName string) int {
	ipVer := 0
	instanceStatus := lookupNetworkInstanceStatusByBridgeName(ctx, bridgeName)
	if instanceStatus != nil {
		switch instanceStatus.IpType {
		case types.AddressTypeIPV4, types.AddressTypeCryptoIPV4:
			ipVer = 4
		case types.AddressTypeIPV6, types.AddressTypeCryptoIPV6:
			ipVer = 6
		}
		return ipVer
	}
	return ipVer
}

func lookupNetworkInstanceStatusByAppIP(ctx *zedrouterContext,
	ip net.IP) *types.NetworkInstanceStatus {

	pub := ctx.pubNetworkInstanceStatus
	items := pub.GetAll()
	for _, st := range items {
		status := st.(types.NetworkInstanceStatus)
		for _, a := range status.IPAssignments {
			if ip.Equal(a) {
				return &status
			}
		}
	}
	return nil
}

// ==== Vpn
func vpnCreate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {
	if status.OpaqueConfig == "" {
		return errors.New("Vpn network instance create, invalid config")
	}
	return strongswanNetworkInstanceCreate(ctx, status)
}

func vpnActivate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {
	if status.OpaqueConfig == "" {
		return errors.New("Vpn network instance activate, invalid config")
	}
	return strongswanNetworkInstanceActivate(ctx, status)
}

func vpnInactivate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	strongswanNetworkInstanceInactivate(ctx, status)
}

func vpnDelete(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	strongswanNetworkInstanceDestroy(ctx, status)
}

func strongswanNetworkInstanceCreate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("Vpn network instance create: %s\n", status.DisplayName)

	// parse and structure the config
	vpnConfig, err := strongSwanConfigGet(ctx, status)
	if err != nil {
		log.Warnf("Vpn network instance create: %v\n", err.Error())
		return err
	}

	// stringify and store in status
	bytes, err := json.Marshal(vpnConfig)
	if err != nil {
		log.Errorf("Vpn network instance create: %v\n", err.Error())
		return err
	}

	status.OpaqueStatus = string(bytes)
	if err := strongSwanVpnCreate(vpnConfig); err != nil {
		log.Errorf("Vpn network instance create: %v\n", err.Error())
		return err
	}
	return nil
}

func strongswanNetworkInstanceDestroy(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	log.Functionf("Vpn network instance delete: %s\n", status.DisplayName)
	vpnConfig, err := strongSwanVpnStatusParse(status.OpaqueStatus)
	if err != nil {
		log.Warnf("Vpn network instance delete: %v\n", err.Error())
	}

	if err := strongSwanVpnDelete(vpnConfig); err != nil {
		log.Warnf("Vpn network instance delete: %v\n", err.Error())
	}
}

func strongswanNetworkInstanceActivate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("Vpn network instance activate: %s\n", status.DisplayName)
	vpnConfig, err := strongSwanVpnStatusParse(status.OpaqueStatus)
	if err != nil {
		log.Warnf("Vpn network instance activate: %v\n", err.Error())
		return err
	}

	if err := strongSwanVpnActivate(vpnConfig); err != nil {
		log.Errorf("Vpn network instance activate: %v\n", err.Error())
		return err
	}
	return nil
}

func strongswanNetworkInstanceInactivate(ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) {

	log.Functionf("Vpn network instance inactivate: %s\n", status.DisplayName)
	vpnConfig, err := strongSwanVpnStatusParse(status.OpaqueStatus)
	if err != nil {
		log.Warnf("Vpn network instance inactivate: %v\n", err.Error())
	}

	if err := strongSwanVpnInactivate(vpnConfig); err != nil {
		log.Warnf("Vpn network instance inactivate: %v\n", err.Error())
	}
}

// labelToIfNames
//	XXX - Probably should move this to ZedRouter.go as a method
//		of zedRouterContext
// Expand the generic names, and return the interface names.
// Does not verify the existence of the logicallabels/interfaces
func labelToIfNames(ctx *zedrouterContext, llOrIfname string) []string {
	if strings.EqualFold(llOrIfname, "uplink") {
		return types.GetMgmtPortsAny(*ctx.deviceNetworkStatus, 0)
	}
	if strings.EqualFold(llOrIfname, "freeuplink") {
		return types.GetMgmtPortsFree(*ctx.deviceNetworkStatus, 0)
	}
	ifname := types.LogicallabelToIfName(ctx.deviceNetworkStatus, llOrIfname)
	if len(ifname) == 0 {
		return []string{}
	}
	return []string{ifname}
}

func vifNameToBridgeName(ctx *zedrouterContext, vifName string) string {

	pub := ctx.pubNetworkInstanceStatus
	instanceItems := pub.GetAll()
	for _, st := range instanceItems {
		status := st.(types.NetworkInstanceStatus)
		if status.IsVifInBridge(vifName) {
			return status.BridgeName
		}
	}
	return ""
}

// Get All ifindices for the Network Instances which are using ifname
func getAllNIindices(ctx *zedrouterContext, ifname string) []int {

	var indicies []int
	pub := ctx.pubNetworkInstanceStatus
	if pub == nil {
		return indicies
	}
	instanceItems := pub.GetAll()
	for _, st := range instanceItems {
		status := st.(types.NetworkInstanceStatus)
		if !status.IsUsingIfName(ifname) {
			continue
		}
		if status.BridgeName == "" {
			continue
		}
		link, err := netlink.LinkByName(status.BridgeName)
		if err != nil {
			errStr := fmt.Sprintf("LinkByName(%s) failed: %s",
				status.BridgeName, err)
			log.Errorln(errStr)
			continue
		}
		indicies = append(indicies, link.Attrs().Index)
	}
	return indicies
}

// checkAndReprogramNetworkInstances handles changes to CurrentUplinkIntf
// when NeedIntfUpdate is set.
func checkAndReprogramNetworkInstances(ctx *zedrouterContext) {
	pub := ctx.pubNetworkInstanceStatus
	instanceItems := pub.GetAll()

	for _, instance := range instanceItems {
		status := instance.(types.NetworkInstanceStatus)

		if !status.NeedIntfUpdate {
			continue
		}
		if status.ProgUplinkIntf == status.CurrentUplinkIntf {
			log.Functionf("checkAndReprogramNetworkInstances: Uplink (%s) has not changed"+
				" for network instance %s",
				status.CurrentUplinkIntf, status.DisplayName)
			continue
		}

		log.Functionf("checkAndReprogramNetworkInstances: Changing Uplink to %s from %s for "+
			"network instance %s", status.CurrentUplinkIntf, status.PrevUplinkIntf,
			status.DisplayName)
		doNetworkInstanceFallback(ctx, &status)
	}
}

func doNetworkInstanceFallback(
	ctx *zedrouterContext,
	status *types.NetworkInstanceStatus) error {

	log.Functionf("doNetworkInstanceFallback NetworkInstance key %s type %d\n",
		status.UUID, status.Type)

	var err error
	// Get a list of IfNames to the ones we have an ifIndex for.
	status.IfNameList = getIfNameListForLLOrIfname(ctx, status.CurrentUplinkIntf)
	publishNetworkInstanceStatus(ctx, status)
	log.Functionf("IfNameList: %+v", status.IfNameList)

	switch status.Type {
	case types.NetworkInstanceTypeLocal:
		if !status.Activated {
			return nil
		}
		natInactivate(ctx, status, true)
		err = natActivate(ctx, status)
		if err != nil {
			log.Errorf("doNetworkInstanceFallback: %s", err)
		}
		status.ProgUplinkIntf = status.CurrentUplinkIntf

		// Use dns server received from DHCP for the current uplink
		bridgeName := status.BridgeName
		hostsDirpath := runDirname + "/hosts." + bridgeName
		deleteOnlyDnsmasqConfiglet(bridgeName)
		stopDnsmasq(bridgeName, false, false)

		if status.BridgeIPAddr != "" {
			dnsServers := types.GetDNSServers(*ctx.deviceNetworkStatus,
				status.CurrentUplinkIntf)
			ntpServers := types.GetNTPServers(*ctx.deviceNetworkStatus,
				status.CurrentUplinkIntf)
			createDnsmasqConfiglet(ctx, bridgeName,
				status.BridgeIPAddr, &status.NetworkInstanceConfig,
				hostsDirpath, status.BridgeIPSets,
				status.CurrentUplinkIntf, dnsServers, ntpServers)
			startDnsmasq(bridgeName)
		}

		// Go through the list of all application connected to this network instance
		// and clear conntrack flows corresponding to them.
		apps := ctx.pubAppNetworkStatus.GetAll()
		// Find all app instances that use this network and purge flows
		// that correspond to these applications.
		for _, app := range apps {
			appNetworkStatus := app.(types.AppNetworkStatus)
			for i := range appNetworkStatus.UnderlayNetworkList {
				ulStatus := &appNetworkStatus.UnderlayNetworkList[i]
				if uuid.Equal(ulStatus.Network, status.UUID) {
					config := lookupAppNetworkConfig(ctx, appNetworkStatus.Key())
					ipsets := compileAppInstanceIpsets(ctx, config.UnderlayNetworkList)
					ulConfig := &config.UnderlayNetworkList[i]
					// This should take care of re-programming any ACL rules that
					// use input match on uplinks.
					// XXX no change in config
					// XXX forcing a change
					doAppNetworkModifyUnderlayNetwork(
						ctx, &appNetworkStatus, ulConfig, ulConfig, ulStatus, ipsets, true)
				}
			}
			publishAppNetworkStatus(ctx, &appNetworkStatus)
		}
	case types.NetworkInstanceTypeSwitch:
		// NA for switch network instance.
	case types.NetworkInstanceTypeCloud:
		// XXX Add support for Cloud network instance
		if status.Activated {
			vpnInactivate(ctx, status)
		}
		vpnDelete(ctx, status)
		vpnCreate(ctx, status)
		if status.Activated {
			vpnActivate(ctx, status)
		}
		status.ProgUplinkIntf = status.CurrentUplinkIntf

		// Use dns server received from DHCP for the current uplink
		bridgeName := status.BridgeName
		hostsDirpath := runDirname + "/hosts." + bridgeName
		deleteOnlyDnsmasqConfiglet(bridgeName)
		stopDnsmasq(bridgeName, false, false)

		if status.BridgeIPAddr != "" {
			dnsServers := types.GetDNSServers(*ctx.deviceNetworkStatus,
				status.CurrentUplinkIntf)
			ntpServers := types.GetNTPServers(*ctx.deviceNetworkStatus,
				status.CurrentUplinkIntf)
			createDnsmasqConfiglet(ctx, bridgeName,
				status.BridgeIPAddr, &status.NetworkInstanceConfig,
				hostsDirpath, status.BridgeIPSets,
				status.CurrentUplinkIntf, dnsServers, ntpServers)
			startDnsmasq(bridgeName)
		}

		// Go through the list of all application connected to this network instance
		// and clear conntrack flows corresponding to them.
		apps := ctx.pubAppNetworkStatus.GetAll()
		// Find all app instances that use this network and purge flows
		// that correspond to these applications.
		for _, app := range apps {
			appNetworkStatus := app.(types.AppNetworkStatus)
			for i := range appNetworkStatus.UnderlayNetworkList {
				ulStatus := &appNetworkStatus.UnderlayNetworkList[i]
				if uuid.Equal(ulStatus.Network, status.UUID) {
					config := lookupAppNetworkConfig(ctx, appNetworkStatus.Key())
					ipsets := compileAppInstanceIpsets(ctx, config.UnderlayNetworkList)
					ulConfig := &config.UnderlayNetworkList[i]
					// This should take care of re-programming any ACL rules that
					// use input match on uplinks.
					// XXX no change in config
					doAppNetworkModifyUnderlayNetwork(
						ctx, &appNetworkStatus, ulConfig, ulConfig, ulStatus, ipsets, true)
				}
			}
			publishAppNetworkStatus(ctx, &appNetworkStatus)
		}
	}
	status.NeedIntfUpdate = false
	publishNetworkInstanceStatus(ctx, status)
	return err
}

// uplinkToPhysdev checks if the ifname is a bridge and if so it
// prepends a "k" to the name (assuming that ifname exists)
// If any issues it returns the argument ifname
func uplinkToPhysdev(ifname string) string {

	link, err := netlink.LinkByName(ifname)
	if err != nil {
		err = fmt.Errorf("uplinkToPhysdev LinkByName(%s) failed: %v",
			ifname, err)
		log.Error(err)
		return ifname
	}
	linkType := link.Type()
	if linkType != "bridge" {
		log.Functionf("uplinkToPhysdev(%s) not a bridge", ifname)
		return ifname
	}

	kernIfname := "k" + ifname
	_, err = netlink.LinkByName(kernIfname)
	if err != nil {
		err = fmt.Errorf("uplinkToPhysdev(%s) %s does not exist: %v",
			ifname, kernIfname, err)
		log.Error(err)
		return ifname
	}
	log.Functionf("uplinkToPhysdev found %s", kernIfname)
	return kernIfname
}
