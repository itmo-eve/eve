// Copyright (c) 2017-2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package hypervisor

import (
	"errors"
	"fmt"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
	"github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	dom0Name = "Domain-0"
)

type typeAndPCI struct {
	pciLong string
	ioType  types.IoType
}

func addNoDuplicatePCI(list []typeAndPCI, tap typeAndPCI) []typeAndPCI {

	for _, t := range list {
		if t.pciLong == tap.pciLong {
			return list
		}
	}
	return append(list, tap)
}

func addNoDuplicate(list []string, add string) []string {

	for _, s := range list {
		if s == add {
			return list
		}
	}
	return append(list, add)
}

type xenContext struct {
	ctrdContext
}

func newXen() Hypervisor {
	ctrdCtx, err := initContainerd()
	if err != nil {
		logrus.Fatalf("couldn't initialize containerd (this should not happen): %v. Exiting.", err)
		return nil // it really never returns on account of above
	}
	return xenContext{ctrdContext: *ctrdCtx}
}

func (ctx xenContext) Name() string {
	return "xen"
}

func (ctx xenContext) Task(status *types.DomainStatus) types.Task {
	if status.VirtualizationMode == types.NOHYPER {
		return ctx.ctrdContext
	} else {
		return ctx
	}
}

func (ctx xenContext) Setup(status types.DomainStatus, config types.DomainConfig, aa *types.AssignableAdapters, file *os.File) error {
	// first lets build the domain config
	if err := ctx.CreateDomConfig(status.DomainName, config, status.DiskStatusList, aa, file); err != nil {
		return logError("failed to build domain config: %v", err)
	}

	spec, err := ctx.setupSpec(&status, &config, status.OCIConfigDir)
	if err != nil {
		return logError("failed to load OCI spec for domain %s: %v", status.DomainName, err)
	}
	if err = spec.AddLoader("/containers/services/xen-tools"); err != nil {
		return logError("failed to add xen hypervisor loader to domain %s: %v", status.DomainName, err)
	}

	// finally we can start it up
	spec.Get().Process.Args = []string{"/etc/xen/scripts/xen-start", status.DomainName, file.Name()}
	if err := spec.CreateContainer(true); err != nil {
		return logError("Failed to create container for task %s from %v: %v", status.DomainName, config, err)
	}

	return nil
}

func (ctx xenContext) CreateDomConfig(domainName string, config types.DomainConfig, diskStatusList []types.DiskStatus,
	aa *types.AssignableAdapters, file *os.File) error {
	xen_type := "pvh"
	rootDev := ""
	extra := config.ExtraArgs
	bootLoader := ""
	kernel := config.Kernel
	ramdisk := config.Ramdisk
	vif_type := "vif"
	xen_global := ""
	uuidStr := fmt.Sprintf("appuuid=%s ", config.UUIDandVersion.UUID)

	switch config.VirtualizationMode {
	case types.PV:
		xen_type = "pv"
		extra = "console=hvc0 " + uuidStr + config.ExtraArgs
		if kernel == "" {
			kernel = "/usr/lib/xen/boot/ovmf-pvh.bin"
		}
	case types.HVM:
		xen_type = "hvm"
	case types.LEGACY:
		xen_type = "hvm"
		vif_type = "ioemu"
		xen_global = "hdtype = \"ahci\"\n"
	case types.FML:
		xen_type = "hvm"
		vif_type = "ioemu"
		xen_global = "hdtype = \"ahci\"\nspoof_xen = 1\npci_permissive = 1\n"
	default:
		logrus.Errorf("Internal error: Unknown virtualizationMode %d",
			config.VirtualizationMode)
	}

	file.WriteString("# This file is automatically generated by domainmgr\n")
	file.WriteString(fmt.Sprintf("name = \"%s\"\n", domainName))
	file.WriteString(fmt.Sprintf("type = \"%s\"\n", xen_type))
	file.WriteString(fmt.Sprintf("uuid = \"%s\"\n",
		config.UUIDandVersion.UUID))
	file.WriteString(xen_global)

	if kernel != "" {
		file.WriteString(fmt.Sprintf("kernel = \"%s\"\n", kernel))
	}

	if ramdisk != "" {
		file.WriteString(fmt.Sprintf("ramdisk = \"%s\"\n", ramdisk))
	}

	if bootLoader != "" {
		file.WriteString(fmt.Sprintf("bootloader = \"%s\"\n",
			bootLoader))
	}
	if config.EnableVnc {
		if config.VirtualizationMode == types.PV {
			vncParams := []string{"vnc=1", "vnclisten=0.0.0.0"}
			if config.VncDisplay != 0 {
				vncParams = append(vncParams, fmt.Sprintf("vncdisplay=%d",
					config.VncDisplay))
			}
			if config.VncPasswd != "" {
				vncParams = append(vncParams, fmt.Sprintf("vncpasswd=%s",
					config.VncPasswd))
			}
			file.WriteString(fmt.Sprintf("vfb = ['%s']\n", strings.Join(vncParams, ", ")))
		} else {
			file.WriteString(fmt.Sprintf("vnc = 1\n"))
			file.WriteString(fmt.Sprintf("vnclisten = \"0.0.0.0\"\n"))
			file.WriteString(fmt.Sprintf("usb=1\n"))
			file.WriteString(fmt.Sprintf("usbdevice=[\"tablet\"]\n"))

			if config.VncDisplay != 0 {
				file.WriteString(fmt.Sprintf("vncdisplay = %d\n",
					config.VncDisplay))
			}
			if config.VncPasswd != "" {
				file.WriteString(fmt.Sprintf("vncpasswd = \"%s\"\n",
					config.VncPasswd))
			}
		}
	} else {
		if config.GPUConfig == "" {
			file.WriteString(fmt.Sprintf("nographic = 1\nvga=\"none\"\n"))
		}
		file.WriteString(fmt.Sprintf("vnc = 0\n"))
	}

	// Go from kbytes to mbytes
	kbyte2mbyte := func(kbyte int) int {
		return (kbyte + 1023) / 1024
	}
	file.WriteString(fmt.Sprintf("memory = %d\n",
		kbyte2mbyte(config.Memory)))
	if config.MaxMem != 0 {
		file.WriteString(fmt.Sprintf("maxmem = %d\n",
			kbyte2mbyte(config.MaxMem)))
	}
	vCpus := config.VCpus
	if vCpus == 0 {
		vCpus = 1
	}
	file.WriteString(fmt.Sprintf("vcpus = %d\n", vCpus))
	maxCpus := config.MaxCpus
	if maxCpus == 0 {
		maxCpus = vCpus
	}
	file.WriteString(fmt.Sprintf("maxcpus = %d\n", maxCpus))
	if config.CPUs != "" {
		file.WriteString(fmt.Sprintf("cpus = \"%s\"\n", config.CPUs))
	}
	if config.DeviceTree != "" {
		file.WriteString(fmt.Sprintf("device_tree = \"%s\"\n",
			config.DeviceTree))
	}
	dtString := ""
	for _, dt := range config.DtDev {
		if dtString != "" {
			dtString += ","
		}
		dtString += fmt.Sprintf("\"%s\"", dt)
	}
	if dtString != "" {
		file.WriteString(fmt.Sprintf("dtdev = [%s]\n", dtString))
	}
	// Note that qcow2 images might have partitions hence xvda1 by default
	if rootDev != "" {
		file.WriteString(fmt.Sprintf("root = \"%s\"\n", rootDev))
	}
	if extra != "" {
		file.WriteString(fmt.Sprintf("extra = \"%s\"\n", extra))
	}
	// XXX Should one be able to disable the serial console? Would need
	// knob in manifest

	var serialAssignments []string
	serialAssignments = append(serialAssignments, "pty")

	// Always prefer CDROM vdisk over disk
	file.WriteString(fmt.Sprintf("boot = \"%s\"\n", "dc"))

	var diskStrings []string
	var p9Strings []string
	for i, ds := range diskStatusList {
		switch ds.Devtype {
		case "":
		case "9P":
			p9Strings = append(p9Strings,
				fmt.Sprintf("'tag=share_dir,security_model=none,path=%s'", ds.FileLocation))
		default:
			access := "rw"
			if ds.ReadOnly {
				access = "ro"
			}
			oneDisk := fmt.Sprintf("'%s,%s,%s,%s'",
				ds.FileLocation, strings.ToLower(ds.Format.String()), ds.Vdev, access)
			logrus.Debugf("Processing disk %d: %s\n", i, oneDisk)
			diskStrings = append(diskStrings, oneDisk)
		}
	}
	if len(diskStrings) > 0 {
		file.WriteString(fmt.Sprintf("disk = [%s]\n", strings.Join(diskStrings, ",")))
	}
	if len(p9Strings) > 0 {
		file.WriteString(fmt.Sprintf("p9 = [%s]\n", strings.Join(p9Strings, ",")))
	}

	vifString := ""
	for _, net := range config.VifList {
		oneVif := fmt.Sprintf("'bridge=%s,vifname=%s,mac=%s,type=%s'",
			net.Bridge, net.Vif, net.Mac, vif_type)
		if vifString == "" {
			vifString = oneVif
		} else {
			vifString = vifString + ", " + oneVif
		}
	}
	file.WriteString(fmt.Sprintf("vif = [%s]\n", vifString))

	imString := ""
	for _, im := range config.IOMem {
		if imString != "" {
			imString += ","
		}
		imString += fmt.Sprintf("\"%s\"", im)
	}
	if imString != "" {
		file.WriteString(fmt.Sprintf("iomem = [%s]\n", imString))
	}

	// Gather all PCI assignments into a single line
	// Also irqs, ioports, and serials
	// irqs and ioports are used if we are pv; serials if hvm
	var pciAssignments []typeAndPCI
	var irqAssignments []string
	var ioportsAssignments []string
	var usbAssignments []string

	for _, irq := range config.IRQs {
		irqString := fmt.Sprintf("%d", irq)
		irqAssignments = addNoDuplicate(irqAssignments, irqString)
	}
	for _, adapter := range config.IoAdapterList {
		logrus.Debugf("configToXenCfg processing adapter %d %s\n",
			adapter.Type, adapter.Name)
		list := aa.LookupIoBundleAny(adapter.Name)
		// We reserved it in handleCreate so nobody could have stolen it
		if len(list) == 0 {
			logrus.Fatalf("configToXencfg IoBundle disappeared %d %s for %s\n",
				adapter.Type, adapter.Name, domainName)
		}
		for _, ib := range list {
			if ib == nil {
				continue
			}
			if ib.UsedByUUID != config.UUIDandVersion.UUID {
				logrus.Fatalf("configToXencfg IoBundle not ours %s: %d %s for %s\n",
					ib.UsedByUUID, adapter.Type, adapter.Name,
					domainName)
			}
			if ib.PciLong != "" {
				tap := typeAndPCI{pciLong: ib.PciLong, ioType: ib.Type}
				pciAssignments = addNoDuplicatePCI(pciAssignments, tap)
			}
			if ib.Irq != "" && config.VirtualizationMode == types.PV {
				logrus.Infof("Adding irq <%s>\n", ib.Irq)
				irqAssignments = addNoDuplicate(irqAssignments,
					ib.Irq)
			}
			if ib.Ioports != "" && config.VirtualizationMode == types.PV {
				logrus.Infof("Adding ioport <%s>\n", ib.Ioports)
				ioportsAssignments = addNoDuplicate(ioportsAssignments, ib.Ioports)
			}
			if ib.Serial != "" && (config.VirtualizationMode == types.HVM || config.VirtualizationMode == types.FML || config.VirtualizationMode == types.LEGACY) {
				logrus.Infof("Adding serial <%s>\n", ib.Serial)
				serialAssignments = addNoDuplicate(serialAssignments, ib.Serial)
			}
			if ib.UsbAddr != "" && (config.VirtualizationMode == types.HVM || config.VirtualizationMode == types.LEGACY || config.VirtualizationMode == types.PV) {
				logrus.Infof("Adding USB <%s>\n", ib.UsbAddr)
				usbAssignments = addNoDuplicate(usbAssignments, ib.UsbAddr)
			}
		}
	}
	if len(pciAssignments) != 0 {
		logrus.Infof("PCI assignments %v\n", pciAssignments)
		cfg := fmt.Sprintf("pci = [ ")
		for i, pa := range pciAssignments {
			if i != 0 {
				cfg = cfg + ", "
			}
			short := types.PCILongToShort(pa.pciLong)
			// USB controller are subject to legacy USB support from
			// some BIOS. Use relaxed to get past that.
			if pa.ioType == types.IoUSB {
				cfg = cfg + fmt.Sprintf("'%s,rdm_policy=relaxed'",
					short)
			} else {
				cfg = cfg + fmt.Sprintf("'%s'", short)
			}
		}
		cfg = cfg + "]"
		logrus.Debugf("Adding pci config <%s>\n", cfg)
		file.WriteString(fmt.Sprintf("%s\n", cfg))
	}
	irqString := ""
	for _, irq := range irqAssignments {
		if irqString != "" {
			irqString += ","
		}
		irqString += irq
	}
	if irqString != "" {
		file.WriteString(fmt.Sprintf("irqs = [%s]\n", irqString))
	}
	ioportString := ""
	for _, ioports := range ioportsAssignments {
		if ioportString != "" {
			ioportString += ","
		}
		ioportString += ioports
	}
	if ioportString != "" {
		file.WriteString(fmt.Sprintf("ioports = [%s]\n", ioportString))
	}
	serialString := ""
	for _, serial := range serialAssignments {
		if serialString != "" {
			serialString += ","
		}
		serialString += "'" + serial + "'"
	}
	if serialString != "" {
		file.WriteString(fmt.Sprintf("serial = [%s]\n", serialString))
	}
	if len(usbAssignments) != 0 {
		logrus.Infof("USB assignments %v\n", usbAssignments)
		cfg := fmt.Sprintf("usbctrl = ['type=auto, version=2, ports=%d']\n", 6)
		cfg += fmt.Sprintf("usbdev = [")
		for i, UsbAddr := range usbAssignments {
			if i > 0 {
				cfg = cfg + ", "
			}
			bus, addr := usbBusPort(UsbAddr)
			cfg = cfg + fmt.Sprintf("'hostbus=%s,hostaddr=%s,controller=0,port=%d'", bus, addr, i)
		}
		cfg = cfg + "]\n"
		logrus.Debugf("Adding pci config <%s>\n", cfg)
		file.WriteString(fmt.Sprintf("%s\n", cfg))
	}

	// XXX log file content: logrus.Infof("Created %s: %s
	return nil
}

func (ctx xenContext) Stop(domainName string, domainID int, force bool) error {
	logrus.Infof("xlShutdown %s %d\n", domainName, domainID)
	args := []string{
		"xl",
		"shutdown",
		domainName,
	}
	if force {
		args = append(args, "-F")
	}
	ctrdCtx, done := ctx.ctrdClient.CtrNewUserServicesCtx()
	defer done()
	stdOut, stdErr, err := ctx.ctrdClient.CtrExec(ctrdCtx, domainName, args)
	if err != nil {
		logrus.Errorln("xl shutdown failed ", err)
		logrus.Errorln("xl shutdown output ", stdOut, stdErr)
		return fmt.Errorf("xl shutdown failed: %s %s", stdOut, stdErr)
	}
	logrus.Infof("xl shutdown done\n")
	return nil
}

func (ctx xenContext) Delete(domainName string, domainID int) (result error) {
	// regardless of happens to everything else, we have to try and delete the task
	defer func() {
		if err := ctx.ctrdContext.Delete(domainName, domainID); err != nil {
			result = fmt.Errorf("%w; couldn't delete task %s: %v", result, domainName, err)
		}
	}()

	logrus.Infof("xlDestroy %s %d\n", domainName, domainID)
	ctrdSystemCtx, done := ctx.ctrdClient.CtrNewSystemServicesCtx()
	defer done()
	stdOut, stdErr, err := ctx.ctrdClient.CtrSystemExec(ctrdSystemCtx, "xen-tools",
		[]string{"xl", "destroy", domainName})
	if err != nil {
		logrus.Errorln("xl destroy failed ", err)
		logrus.Errorln("xl destroy output ", stdOut, stdErr)
		return fmt.Errorf("xl destroy failed: %s %s", stdOut, stdErr)
	}

	logrus.Infof("xl destroy done %s %d\n", domainName, domainID)
	return nil
}

func (ctx xenContext) Info(domainName string, domainID int) (int, types.SwState, error) {
	// first we ask for the task status
	effectiveDomainID, effectiveDomainState, err := ctx.ctrdContext.Info(domainName, domainID)
	if err != nil || effectiveDomainState != types.RUNNING {
		return effectiveDomainID, effectiveDomainState, err
	}

	// if task is alive, we augment task status with finer grained details from xl info
	logrus.Debugf("xlStatus %s %d\n", domainName, domainID)

	status, err := ioutil.ReadFile("/run/tasks/" + domainName)
	if err != nil {
		logrus.Errorf("couldn't read task status file: %v", err)
		status = []byte("running") // assigning default state as we weren't able to read status file
	}
	logrus.Debugf("task %s has status %v\n", domainName, status)

	stateMap := map[string]types.SwState{
		"running": types.RUNNING,
		"paused":  types.PAUSED,
		"halting": types.HALTING,
	}
	effectiveDomainState, matched := stateMap[strings.TrimSpace(string(status))]
	if !matched {
		return effectiveDomainID, types.BROKEN, fmt.Errorf("info: domain %s reported to be in unexpected state %v",
			domainName, string(status))
	}

	return effectiveDomainID, effectiveDomainState, nil
}

func (ctx xenContext) PCIReserve(long string) error {
	logrus.Infof("pciAssignableAdd %s\n", long)
	ctrdSystemCtx, done := ctx.ctrdClient.CtrNewSystemServicesCtx()
	defer done()
	stdOut, stdErr, err := ctx.ctrdClient.CtrSystemExec(ctrdSystemCtx, "xen-tools",
		[]string{"xl", "pci-assignable-add", long})
	if err != nil {
		errStr := fmt.Sprintf("xl pci-assignable-add failed: %s %s", stdOut, stdErr)
		logrus.Errorln(errStr)
		return errors.New(errStr)
	}
	logrus.Infof("xl pci-assignable-add done\n")
	return nil
}

func (ctx xenContext) PCIRelease(long string) error {
	logrus.Infof("pciAssignableRemove %s\n", long)
	ctrdSystemCtx, done := ctx.ctrdClient.CtrNewSystemServicesCtx()
	defer done()
	stdOut, stdErr, err := ctx.ctrdClient.CtrSystemExec(ctrdSystemCtx, "xen-tools",
		[]string{"xl", "pci-assignable-rem", "-r", long})
	if err != nil {
		errStr := fmt.Sprintf("xl pci-assignable-rem failed: %s %s", stdOut, stdErr)
		logrus.Errorln(errStr)
		return errors.New(errStr)
	}
	logrus.Infof("xl pci-assignable-rem done\n")
	return nil
}

func (ctx xenContext) GetHostCPUMem() (types.HostMemory, error) {
	ctrdSystemCtx, done := ctx.ctrdClient.CtrNewSystemServicesCtx()
	defer done()
	xlInfo, stderr, err := ctx.ctrdClient.CtrSystemExec(ctrdSystemCtx, "xen-tools",
		[]string{"xl", "info"})
	if err != nil {
		logrus.Errorf("xl info failed %s %s falling back on Dom0 stats: %v", xlInfo, stderr, err)
		return selfDomCPUMem()
	}

	splitXlInfo := strings.Split(xlInfo, "\n")

	dict := make(map[string]string, len(splitXlInfo)-1)
	for _, str := range splitXlInfo {
		res := strings.SplitN(str, ":", 2)
		if len(res) == 2 {
			dict[strings.TrimSpace(res[0])] = strings.TrimSpace(res[1])
		}
	}

	hm := types.HostMemory{}
	hm.TotalMemoryMB, err = strconv.ParseUint(dict["total_memory"], 10, 64)
	if err != nil {
		logrus.Errorf("Failed parsing total_memory: %s", err)
		hm.TotalMemoryMB = 0
	}
	hm.FreeMemoryMB, err = strconv.ParseUint(dict["free_memory"], 10, 64)
	if err != nil {
		logrus.Errorf("Failed parsing free_memory: %s", err)
		hm.FreeMemoryMB = 0
	}

	// Note that this is the set of physical CPUs which is different
	// than the set of CPUs assigned to dom0
	var ncpus uint64
	ncpus, err = strconv.ParseUint(dict["nr_cpus"], 10, 32)
	if err != nil {
		logrus.Errorln("error while converting ncpus to int: ", err)
		ncpus = 0
	}
	hm.Ncpus = uint32(ncpus)
	if false {
		// debug code to compare Xen and fallback
		// XXX remove debug code
		hm2, err := selfDomCPUMem()
		logrus.Infof("XXX xen %+v fallback %+v (%v)", hm, hm2, err)
	}
	return hm, nil
}

func (ctx xenContext) GetDomsCPUMem() (map[string]types.DomainMetric, error) {
	count := 0
	counter := 0
	ctrdSystemCtx, done := ctx.ctrdClient.CtrNewSystemServicesCtx()
	defer done()
	xentopInfo, _, _ := ctx.ctrdClient.CtrSystemExec(ctrdSystemCtx, "xen-tools",
		[]string{"xentop", "-b", "-d", "1", "-i", "2", "-f"})

	splitXentopInfo := strings.Split(xentopInfo, "\n")
	splitXentopInfoLength := len(splitXentopInfo)
	var i int
	var start int

	for i = 0; i < splitXentopInfoLength; i++ {

		str := splitXentopInfo[i]
		re := regexp.MustCompile(" ")

		spaceRemovedsplitXentopInfo := re.ReplaceAllLiteralString(str, "")
		matched, err := regexp.MatchString("NAMESTATECPU.*", spaceRemovedsplitXentopInfo)

		if err != nil {
			logrus.Debugf("MatchString failed: %s", err)
		} else if matched {

			count++
			logrus.Debugf("string matched: %s", str)
			if count == 2 {
				start = i + 1
				logrus.Debugf("value of i: %d", start)
			}
		}
	}

	length := splitXentopInfoLength - 1 - start
	finalOutput := make([][]string, length)

	for j := start; j < splitXentopInfoLength-1; j++ {
		finalOutput[j-start] = strings.Fields(strings.TrimSpace(splitXentopInfo[j]))
	}

	cpuMemoryStat := make([][]string, length)

	for i := range cpuMemoryStat {
		cpuMemoryStat[i] = make([]string, 20)
	}

	// Need to treat "no limit" as one token
	for f := 0; f < length; f++ {

		// First name and state
		out := 0
		counter++
		cpuMemoryStat[f][counter] = finalOutput[f][out]
		out++
		counter++
		cpuMemoryStat[f][counter] = finalOutput[f][out]
		out++
		for ; out < len(finalOutput[f]); out++ {

			if finalOutput[f][out] == "no" {

			} else if finalOutput[f][out] == "limit" {
				counter++
				cpuMemoryStat[f][counter] = "no limit"
			} else {
				counter++
				cpuMemoryStat[f][counter] = finalOutput[f][out]
			}
		}
		counter = 0
	}
	logrus.Debugf("ExecuteXentopCmd return %+v", cpuMemoryStat)

	// first we get all the task stats from containerd, and we update
	// the ones that have a Xen domain associated with them
	dmList, err := ctx.ctrdContext.GetDomsCPUMem()
	if len(cpuMemoryStat) != 0 {
		dmList = parseCPUMemoryStat(cpuMemoryStat, dmList)
	} else if err != nil {
		dmList = fallbackDomainMetric()
	}
	// finally add host entry to dmList
	if false {
		// debug code to compare Xen and fallback
		logrus.Infof("XXX reported DomainMetric %+v", dmList)
		dmList = fallbackDomainMetric()
		logrus.Infof("XXX fallback DomainMetric %+v", dmList)
	}
	return dmList, nil
}

// Returns cpuTotal, usedMemory, availableMemory, usedPercentage
func parseCPUMemoryStat(cpuMemoryStat [][]string, dmList map[string]types.DomainMetric) map[string]types.DomainMetric {
	result := dmList
	if result == nil {
		result = make(map[string]types.DomainMetric)
	}

	for _, stat := range cpuMemoryStat {
		if len(stat) <= 2 {
			continue
		}
		domainname := strings.TrimSpace(stat[1])
		if len(stat) <= 6 {
			continue
		}
		logrus.Debugf("lookupCPUMemoryStat for %s %d elem: %+v",
			domainname, len(stat), stat)
		cpuTotal, err := strconv.ParseUint(stat[3], 10, 0)
		if err != nil {
			logrus.Errorf("ParseUint(%s) failed: %s",
				stat[3], err)
			cpuTotal = 0
		}
		// This is in kbytes
		totalMemory, err := strconv.ParseUint(stat[5], 10, 0)
		if err != nil {
			logrus.Errorf("ParseUint(%s) failed: %s",
				stat[5], err)
			totalMemory = 0
		}
		totalMemory = roundFromKbytesToMbytes(totalMemory)
		usedMemoryPercent, err := strconv.ParseFloat(stat[6], 10)
		if err != nil {
			logrus.Errorf("ParseFloat(%s) failed: %s",
				stat[6], err)
			usedMemoryPercent = 0
		}
		usedMemory := (float64(totalMemory) * (usedMemoryPercent)) / 100
		availableMemory := float64(totalMemory) - usedMemory

		dm := types.DomainMetric{
			CPUTotal:          cpuTotal,
			UsedMemory:        uint32(usedMemory),
			AvailableMemory:   uint32(availableMemory),
			UsedMemoryPercent: float64(usedMemoryPercent),
		}
		result[domainname] = dm
	}
	return result
}

// First approximation for a host without Xen
// XXX Assumes that all of the used memory in the host is overhead the same way dom0 is
// overhead, which is completely incorrect when running containers
func fallbackDomainMetric() map[string]types.DomainMetric {
	dmList := make(map[string]types.DomainMetric)
	vm, err := mem.VirtualMemory()
	if err != nil {
		return dmList
	}
	var usedMemoryPercent float64
	if vm.Total != 0 {
		usedMemoryPercent = float64(100 * (vm.Total - vm.Available) / vm.Total)
	}
	total := roundFromBytesToMbytes(vm.Total)
	available := roundFromBytesToMbytes(vm.Available)
	dm := types.DomainMetric{
		UsedMemory:        uint32(total - available),
		AvailableMemory:   uint32(available),
		UsedMemoryPercent: usedMemoryPercent,
	}
	// Ask for one total entry
	cpuStat, err := cpu.Times(false)
	if err != nil {
		return dmList
	}
	for _, cpu := range cpuStat {
		dm.CPUTotal = uint64(cpu.Total())
		break
	}
	dmList[dom0Name] = dm
	return dmList
}
