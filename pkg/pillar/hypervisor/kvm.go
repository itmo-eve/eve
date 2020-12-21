// Copyright (c) 2017-2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package hypervisor

import (
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	zconfig "github.com/lf-edge/eve/api/go/config"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/sirupsen/logrus"
)

//TBD: Have a better way to calculate this number.
//For now it is based on some trial-and-error experiments
const qemuOverHead = int64(600 * 1024 * 1024)

const minUringKernelTag = uint64((5 << 16) | (4 << 8) | (72 << 0))

// We build device model around PCIe topology according to best practices
//    https://github.com/qemu/qemu/blob/master/docs/pcie.txt
// and
//    https://libvirt.org/pci-hotplug.html
// Thus the only PCI devices plugged directly into the root (pci.0) bus are:
//    00:01.0 cirrus-vga
//    00:02.0 pcie-root-port for QEMU XHCI Host Controller
//    00:03.0 virtio-serial for hvc consoles and serial communications with the domain
//    00:0x.0 pcie-root-port for block or network device #x (where x > 2)
//    00:0y.0 virtio-9p-pci
//
// This makes everything but 9P volumes be separated from root pci bus
// and effectively hang off the bus of its own:
//     01:00.0 QEMU XHCI Host Controller (behind pcie-root-port 00:02.0)
//     xx:00.0 block or network device #x (behind pcie-root-port 00:0x.0)
//
// It would be nice to figure out how to do the same with virtio-9p-pci
// eventually, but for now this is not a high priority.

const qemuConfTemplate = `# This file is automatically generated by domainmgr
[msg]
  timestamp = "on"

[machine]
  type = "{{.Machine}}"
  dump-guest-core = "off"
{{- if eq .Machine "virt" }}
  accel = "kvm:tcg"
  gic-version = "host"
{{- end -}}
{{- if ne .Machine "virt" }}
  accel = "kvm"
  vmport = "off"
  kernel-irqchip = "on"
{{- end -}}
{{- if .BootLoader }}
  firmware = "{{.BootLoader}}"
{{- end -}}
{{- if .Kernel }}
  kernel = "{{.Kernel}}"
{{- end -}}
{{- if .Ramdisk }}
  initrd = "{{.Ramdisk}}"
{{- end -}}
{{- if .DeviceTree }}
  dtb = "{{.DeviceTree}}"
{{- end -}}
{{- if .ExtraArgs }}
  append = "{{.ExtraArgs}}"
{{ end }}
{{if ne .Machine "virt" }}
[global]
  driver = "kvm-pit"
  property = "lost_tick_policy"
  value = "delay"

[global]
  driver = "ICH9-LPC"
  property = "disable_s3"
  value = "1"

[global]
  driver = "ICH9-LPC"
  property = "disable_s4"
  value = "1"

[rtc]
  base = "localtime"
  driftfix = "slew"

[device]
  driver = "intel-iommu"
  caching-mode = "on"
{{ end }}
[realtime]
  mlock = "off"

[chardev "charmonitor"]
  backend = "socket"
  path = "` + kvmStateDir + `{{.DisplayName}}/qmp"
  server = "on"
  wait = "off"

[mon "monitor"]
  chardev = "charmonitor"
  mode = "control"

[chardev "charlistener"]
  backend = "socket"
  path = "` + kvmStateDir + `{{.DisplayName}}/listener.qmp"
  server = "on"
  wait = "off"

[mon "listener"]
  chardev = "charlistener"
  mode = "control"

[memory]
  size = "{{.Memory}}"

[smp-opts]
  cpus = "{{.VCpus}}"
  sockets = "1"
  cores = "{{.VCpus}}"
  threads = "1"

[device]
  driver = "virtio-serial"
  addr = "3"

[chardev "charserial0"]
  backend = "socket"
  mux = "on"
  path = "` + kvmStateDir + `{{.DisplayName}}/cons"
  server = "on"
  wait = "off"
  logfile = "/dev/fd/1"
  logappend = "on"

[device]
  driver = "virtconsole"
  chardev = "charserial0"
  name = "org.lfedge.eve.console.0"

{{if .EnableVnc}}
[vnc "default"]
  vnc = "0.0.0.0:{{if .VncDisplay}}{{.VncDisplay}}{{else}}0{{end}}"
  to = "99"
{{- if .VncPasswd}}
  password = "on"
{{- end -}}
{{end}}
#[device "video0"]
#  driver = "qxl-vga"
#  ram_size = "67108864"
#  vram_size = "67108864"
#  vram64_size_mb = "0"
#  vgamem_mb = "16"
#  max_outputs = "1"
#  bus = "pcie.0"
#  addr = "0x1"
{{- if ne .Machine "virt" }}
[device "video0"]
  driver = "cirrus-vga"
  vgamem_mb = "16"
  bus = "pcie.0"
  addr = "0x1"
{{else}}
[device "video0"]
  driver = "ramfb"
{{end}}
[device "pci.2"]
  driver = "pcie-root-port"
  port = "12"
  chassis = "2"
  bus = "pcie.0"
  addr = "0x2"

[device "usb"]
  driver = "qemu-xhci"
  p2 = "15"
  p3 = "15"
  bus = "pci.2"
  addr = "0x0"
{{if ne .Machine "virt" }}
[device "input0"]
  driver = "usb-tablet"
  bus = "usb.0"
  port = "1"
{{else}}
[device "input0"]
  driver = "usb-kbd"
  bus = "usb.0"
  port = "1"

[device "input1"]
  driver = "usb-mouse"
  bus = "usb.0"
  port = "2"
{{end}}`

//   multidevs = "remap"
const qemuDiskTemplate = `
{{if eq .Devtype "cdrom"}}
[drive "drive-sata0-{{.DiskID}}"]
  file = "{{.FileLocation}}"
  format = "{{.Format | Fmt}}"
  if = "none"
  media = "cdrom"
  readonly = "on"

[device "sata0-{{.SATAId}}"]
  drive = "drive-sata0-{{.DiskID}}"
{{- if eq .Machine "virt"}}
  driver = "usb-storage"
{{else}}
  driver = "ide-cd"
  bus = "ide.{{.SATAId}}"
{{- end }}
{{else if eq .Devtype "9P"}}
[fsdev "fsdev{{.DiskID}}"]
  fsdriver = "local"
  security_model = "none"
  path = "{{.FileLocation}}"

[device "fs{{.DiskID}}"]
  driver = "virtio-9p-pci"
  fsdev = "fsdev{{.DiskID}}"
  mount_tag = "hostshare"
  addr = "{{.PCIId}}"
{{else}}
[device "pci.{{.PCIId}}"]
  driver = "pcie-root-port"
  port = "1{{.PCIId}}"
  chassis = "{{.PCIId}}"
  bus = "pcie.0"
  addr = "{{.PCIId}}"

[device "virtio-disk{{.DiskID}}"]
  driver = "vhost-scsi-pci"
  bus = "pci.{{.PCIId}}"
  addr = "0x0"
  wwpn = "{{.LunWWN}}"
{{end}}`

const qemuNetTemplate = `
[device "pci.{{.PCIId}}"]
  driver = "pcie-root-port"
  port = "1{{.PCIId}}"
  chassis = "{{.PCIId}}"
  bus = "pcie.0"
  multifunction = "on"
  addr = "{{.PCIId}}"

[netdev "hostnet{{.NetID}}"]
  type = "tap"
  ifname = "{{.Vif}}"
  br = "{{.Bridge}}"
  script = "/etc/xen/scripts/qemu-ifup"
  downscript = "no"

[device "net{{.NetID}}"]
  driver = "virtio-net-pci"
  netdev = "hostnet{{.NetID}}"
  mac = "{{.Mac}}"
  bus = "pci.{{.PCIId}}"
  addr = "0x0"
`

const qemuPciPassthruTemplate = `
[device]
  driver = "vfio-pci"
  host = "{{.PciShortAddr}}"
{{- if .Xvga }}
  x-vga = "on"
{{- end -}}
`
const qemuSerialTemplate = `
[chardev "charserial-usr{{.ID}}"]
  backend = "tty"
  path = "{{.SerialPortName}}"

[device "serial-usr{{.ID}}"]
  driver = "isa-serial"
  chardev = "charserial-usr{{.ID}}"
`

const qemuUsbHostTemplate = `
[device]
  driver = "usb-host"
  hostbus = "{{.UsbBus}}"
  hostaddr = "{{.UsbDevAddr}}"
`

const kvmStateDir = "/run/hypervisor/kvm/"
const sysfsPciDevices = "/sys/bus/pci/devices/"
const sysfsVfioPciBind = "/sys/bus/pci/drivers/vfio-pci/bind"
const sysfsPciDriversProbe = "/sys/bus/pci/drivers_probe"
const vfioDriverPath = "/sys/bus/pci/drivers/vfio-pci"

// KVM domains map 1-1 to anchor device model UNIX processes (qemu or firecracker)
// For every anchor process we maintain the following entry points in the
// /run/hypervisor/kvm/DOMAIN_NAME:
//    pid - contains PID of the anchor process
//    qmp - UNIX domain socket that allows us to talk to anchor process
//   cons - symlink to /dev/pts/X that allows us to talk to the serial console of the domain
// In addition to that, we also maintain DOMAIN_NAME -> PID mapping in kvmContext, so we don't
// have to look things up in the filesystem all the time (this also allows us to filter domains
// that may be created by others)
type kvmContext struct {
	ctrdContext
	// for now the following is statically configured and can not be changed per domain
	devicemodel  string
	dmExec       string
	dmArgs       []string
	dmCPUArgs    []string
	dmFmlCPUArgs []string
}

func newKvm() Hypervisor {
	ctrdCtx, err := initContainerd()
	if err != nil {
		logrus.Fatalf("couldn't initialize containerd (this should not happen): %v. Exiting.", err)
		return nil // it really never returns on account of above
	}
	// later on we may want to pass device model machine type in DomainConfig directly;
	// for now -- lets just pick a static device model based on the host architecture
	// "-cpu host",
	// -cpu IvyBridge-IBRS,ss=on,vmx=on,movbe=on,hypervisor=on,arat=on,tsc_adjust=on,mpx=on,rdseed=on,smap=on,clflushopt=on,sha-ni=on,umip=on,md-clear=on,arch-capabilities=on,xsaveopt=on,xsavec=on,xgetbv1=on,xsaves=on,pdpe1gb=on,3dnowprefetch=on,avx=off,f16c=off,hv_time,hv_relaxed,hv_vapic,hv_spinlocks=0x1fff
	switch runtime.GOARCH {
	case "arm64":
		return kvmContext{
			ctrdContext:  *ctrdCtx,
			devicemodel:  "virt",
			dmExec:       "/usr/lib/xen/bin/qemu-system-aarch64",
			dmArgs:       []string{"-display", "none", "-S", "-no-user-config", "-nodefaults", "-no-shutdown", "-overcommit", "mem-lock=on", "-overcommit", "cpu-pm=on", "-serial", "chardev:charserial0"},
			dmCPUArgs:    []string{"-cpu", "host"},
			dmFmlCPUArgs: []string{},
		}
	case "amd64":
		return kvmContext{
			//nolint:godox // FIXME: Removing "-overcommit", "mem-lock=on", "-overcommit" for now, revisit it later as part of resource partitioning
			ctrdContext:  *ctrdCtx,
			devicemodel:  "pc-q35-3.1",
			dmExec:       "/usr/lib/xen/bin/qemu-system-x86_64",
			dmArgs:       []string{"-display", "none", "-S", "-no-user-config", "-nodefaults", "-no-shutdown", "-serial", "chardev:charserial0", "-no-hpet"},
			dmCPUArgs:    []string{},
			dmFmlCPUArgs: []string{"-cpu", "host,hv_time,hv_relaxed,hv_vendor_id=eveitis,hypervisor=off,kvm=off"},
		}
	}
	return nil
}

func (ctx kvmContext) Name() string {
	return "kvm"
}

func (ctx kvmContext) Task(status *types.DomainStatus) types.Task {
	if status.VirtualizationMode == types.NOHYPER {
		return ctx.ctrdContext
	} else {
		return ctx
	}
}

func (ctx kvmContext) Setup(status types.DomainStatus, config types.DomainConfig, aa *types.AssignableAdapters, file *os.File) error {

	diskStatusList := status.DiskStatusList
	domainName := status.DomainName
	// first lets build the domain config
	if err := ctx.CreateDomConfig(domainName, config, diskStatusList, aa, file); err != nil {
		return logError("failed to build domain config: %v", err)
	}

	dmArgs := ctx.dmArgs
	if config.VirtualizationMode == types.FML {
		dmArgs = append(dmArgs, ctx.dmFmlCPUArgs...)
	} else {
		dmArgs = append(dmArgs, ctx.dmCPUArgs...)
	}

	os.MkdirAll(kvmStateDir+domainName, 0777)

	args := []string{ctx.dmExec}
	args = append(args, dmArgs...)
	args = append(args, "-name", domainName,
		"-readconfig", file.Name(),
		"-pidfile", kvmStateDir+domainName+"/pid")

	//nolint:godox // FIXME: Not passing domain config to LKTaskPrepare for disk performance improvement,
	// revisit it later as part of resource partitioning
	if err := ctx.ctrdClient.LKTaskPrepare(domainName, "xen-tools", nil, &status, qemuOverHead, args); err != nil {
		return logError("LKTaskPrepare failed for %s, (%v)", domainName, err)
	}

	return nil
}

func (ctx kvmContext) CreateDomConfig(domainName string, config types.DomainConfig, diskStatusList []types.DiskStatus,
	aa *types.AssignableAdapters, file *os.File) error {
	tmplCtx := struct {
		Machine string
		types.DomainConfig
	}{ctx.devicemodel, config}
	tmplCtx.Memory = (config.Memory + 1023) / 1024
	tmplCtx.DisplayName = domainName
	if config.VirtualizationMode == types.FML || config.VirtualizationMode == types.PV {
		//nolint:godox // FIXME XXX hack to reuce memory pressure of UEFI when we run containers on x86
		if config.IsContainer && runtime.GOARCH == "amd64" {
			tmplCtx.BootLoader = "/usr/lib/xen/boot/seabios.bin"
		} else {
			tmplCtx.BootLoader = "/usr/lib/xen/boot/ovmf.bin"
		}
	} else {
		tmplCtx.BootLoader = ""
	}
	if config.IsContainer {
		tmplCtx.Kernel = "/hostfs/boot/kernel"
		tmplCtx.Ramdisk = "/usr/lib/xen/boot/runx-initrd"
		tmplCtx.ExtraArgs = config.ExtraArgs + " console=hvc0 root=9p-kvm dhcp=1"
	}

	// render global device model settings
	t, _ := template.New("qemu").Parse(qemuConfTemplate)
	if err := t.Execute(file, tmplCtx); err != nil {
		return logError("can't write to config file %s (%v)", file.Name(), err)
	}

	// render disk device model settings
	diskContext := struct {
		Machine               string
		PCIId, DiskID, SATAId int
		AioType               string
		LunWWN                string
		types.DiskStatus
	}{Machine: ctx.devicemodel, PCIId: 4, DiskID: 0, SATAId: 0, AioType: "threads"}

	var osver []string
	var major, minor, patch uint64

	osver = strings.SplitN(getOsVersion(), ".", 3)

	if len(osver) >= 1 {
		major, _ = strconv.ParseUint(osver[0], 10, 8)
	}
	if len(osver) >= 2 {
		minor, _ = strconv.ParseUint(osver[1], 10, 8)
	}
	if len(osver) >= 3 {
		osver[2] = strings.Split(osver[2], "-")[0]
		patch, _ = strconv.ParseUint(osver[2], 10, 8)
	}

	if minUringKernelTag <= kernelVersionTag(major, minor, patch) {
		diskContext.AioType = "io_uring"
	}

	t, _ = template.New("qemuDisk").
		Funcs(template.FuncMap{"Fmt": func(f zconfig.Format) string { return strings.ToLower(f.String()) }}).
		Parse(qemuDiskTemplate)
	for _, ds := range diskStatusList {
		var err error

		if ds.Devtype == "" {
			continue
		}
		if ds.Devtype == "hdd" {
			if diskContext.LunWWN, err = VhostCreate(ds); err != nil {
				logError("Failed to create VHost fabric for %s: %v", ds.DisplayName, err)
			}
		}
		diskContext.DiskStatus = ds
		if err := t.Execute(file, diskContext); err != nil {
			return logError("can't write to config file %s (%v)", file.Name(), err)
		}
		if diskContext.Devtype == "cdrom" {
			diskContext.SATAId = diskContext.SATAId + 1
		} else {
			diskContext.PCIId = diskContext.PCIId + 1
		}
		diskContext.DiskID = diskContext.DiskID + 1
	}

	// render network device model settings
	netContext := struct {
		PCIId, NetID     int
		Mac, Bridge, Vif string
	}{PCIId: diskContext.PCIId, NetID: 0}
	t, _ = template.New("qemuNet").Parse(qemuNetTemplate)
	for _, net := range config.VifList {
		netContext.Mac = net.Mac
		netContext.Bridge = net.Bridge
		netContext.Vif = net.Vif
		if err := t.Execute(file, netContext); err != nil {
			return logError("can't write to config file %s (%v)", file.Name(), err)
		}
		netContext.PCIId = netContext.PCIId + 1
		netContext.NetID = netContext.NetID + 1
	}

	// Gather all PCI assignments into a single line
	var pciAssignments []typeAndPCI
	// Gather all USB assignments into a single line
	var usbAssignments []string
	// Gather all serial assignments into a single line
	var serialAssignments []string

	for _, adapter := range config.IoAdapterList {
		logrus.Debugf("processing adapter %d %s\n", adapter.Type, adapter.Name)
		list := aa.LookupIoBundleAny(adapter.Name)
		// We reserved it in handleCreate so nobody could have stolen it
		if len(list) == 0 {
			logrus.Fatalf("IoBundle disappeared %d %s for %s\n",
				adapter.Type, adapter.Name, domainName)
		}
		for _, ib := range list {
			if ib == nil {
				continue
			}
			if ib.UsedByUUID != config.UUIDandVersion.UUID {
				logrus.Fatalf("IoBundle not ours %s: %d %s for %s\n",
					ib.UsedByUUID, adapter.Type, adapter.Name,
					domainName)
			}
			if ib.PciLong != "" {
				logrus.Infof("Adding PCI device <%v>\n", ib.PciLong)
				tap := typeAndPCI{pciLong: ib.PciLong, ioType: ib.Type}
				pciAssignments = addNoDuplicatePCI(pciAssignments, tap)
			}
			if ib.Serial != "" {
				logrus.Infof("Adding serial <%s>\n", ib.Serial)
				serialAssignments = addNoDuplicate(serialAssignments, ib.Serial)
			}
			if ib.UsbAddr != "" {
				logrus.Infof("Adding USB host device <%s>\n", ib.UsbAddr)
				usbAssignments = addNoDuplicate(usbAssignments, ib.UsbAddr)
			}
		}
	}
	if len(pciAssignments) != 0 {
		pciPTContext := struct {
			PciShortAddr string
			Xvga         bool
		}{PciShortAddr: "", Xvga: false}

		t, _ = template.New("qemuPciPT").Parse(qemuPciPassthruTemplate)
		for _, pa := range pciAssignments {
			short := types.PCILongToShort(pa.pciLong)
			bootVgaFile := sysfsPciDevices + pa.pciLong + "/boot_vga"
			if _, err := os.Stat(bootVgaFile); err == nil {
				pciPTContext.Xvga = true
			}

			pciPTContext.PciShortAddr = short
			if err := t.Execute(file, pciPTContext); err != nil {
				return logError("can't write PCI Passthrough to config file %s (%v)", file.Name(), err)
			}
			pciPTContext.Xvga = false
		}
	}
	if len(serialAssignments) != 0 {
		serialPortContext := struct {
			SerialPortName string
			ID             int
		}{SerialPortName: "", ID: 0}

		t, _ = template.New("qemuSerial").Parse(qemuSerialTemplate)
		for id, serial := range serialAssignments {
			serialPortContext.SerialPortName = serial
			fmt.Printf("id for serial is %d\n", id)
			serialPortContext.ID = id
			if err := t.Execute(file, serialPortContext); err != nil {
				return logError("can't write serial assignment to config file %s (%v)", file.Name(), err)
			}
		}
	}
	if len(usbAssignments) != 0 {
		usbHostContext := struct {
			UsbBus     string
			UsbDevAddr string
			// Ports are dot-separated
		}{UsbBus: "", UsbDevAddr: ""}

		t, _ = template.New("qemuUsbHost").Parse(qemuUsbHostTemplate)
		for _, usbaddr := range usbAssignments {
			bus, port := usbBusPort(usbaddr)
			usbHostContext.UsbBus = bus
			usbHostContext.UsbDevAddr = port
			if err := t.Execute(file, usbHostContext); err != nil {
				return logError("can't write USB host device assignment to config file %s (%v)", file.Name(), err)
			}
		}
	}

	return nil
}

func waitForQmp(domainName string) error {
	maxDelay := time.Second * 10
	delay := time.Second
	var waited time.Duration
	for {
		logrus.Infof("waitForQmp for %s: waiting for %v", domainName, delay)
		if delay != 0 {
			time.Sleep(delay)
			waited += delay
		}
		if _, err := getQemuStatus(getQmpExecutorSocket(domainName)); err == nil {
			logrus.Infof("waitForQmp for %s, found file", domainName)
			return nil
		} else {
			if waited > maxDelay {
				// Give up
				logrus.Warnf("waitForQmp for %s: giving up", domainName)
				return logError("Qmp not found")
			}
			delay = 2 * delay
			if delay > time.Minute {
				delay = time.Minute
			}
		}
	}
}

func (ctx kvmContext) Start(domainName string, domainID int) error {
	logrus.Infof("starting KVM domain %s", domainName)
	if err := ctx.ctrdContext.Start(domainName, domainID); err != nil {
		logrus.Errorf("couldn't start task for domain %s: %v", domainName, err)
		return err
	}
	logrus.Infof("done launching qemu device model")
	if err := waitForQmp(domainName); err != nil {
		logrus.Errorf("Error waiting for Qmp for domain %s: %v", domainName, err)
		return err
	}
	logrus.Infof("done launching qemu device model")

	qmpFile := getQmpExecutorSocket(domainName)

	logrus.Debugf("starting qmpEventHandler")
	logrus.Infof("Creating %s at %s", "qmpEventHandler", agentlog.GetMyStack())
	go qmpEventHandler(getQmpListenerSocket(domainName), getQmpExecutorSocket(domainName))

	if err := execContinue(qmpFile); err != nil {
		return logError("failed to start domain that is stopped %v", err)
	}

	if status, err := getQemuStatus(qmpFile); err != nil || status != "running" {
		return logError("domain status is not running but %s after cont command returned %v", status, err)
	}
	return nil
}

func (ctx kvmContext) Stop(domainName string, domainID int, force bool) error {
	if err := execShutdown(getQmpExecutorSocket(domainName)); err != nil {
		return logError("Stop: failed to execute shutdown command %v", err)
	}
	return nil
}

func (ctx kvmContext) Delete(domainName string, domainID int) error {
	//Sending a stop signal to then domain before quitting. This is done to freeze the domain before quitting it.
	execStop(getQmpExecutorSocket(domainName))
	if err := execQuit(getQmpExecutorSocket(domainName)); err != nil {
		return logError("failed to execute quit command %v", err)
	}
	// we may want to wait a little bit here and actually kill qemu process if it gets wedged
	if err := os.RemoveAll(kvmStateDir + domainName); err != nil {
		return logError("failed to clean up domain state directory %s (%v)", domainName, err)
	}

	if err := ctx.ctrdContext.Stop(domainName, domainID, true); err != nil {
		return err
	}

	return ctx.ctrdContext.Delete(domainName, domainID)
}

func (ctx kvmContext) Info(domainName string, domainID int) (int, types.SwState, error) {
	// first we ask for the task status
	effectiveDomainID, effectiveDomainState, err := ctx.ctrdContext.Info(domainName, domainID)
	if err != nil || effectiveDomainState != types.RUNNING {
		return effectiveDomainID, effectiveDomainState, err
	}

	// if task us alive, we augment task status with finer grained details from qemu
	// lets parse the status according to https://github.com/qemu/qemu/blob/master/qapi/run-state.json#L8
	stateMap := map[string]types.SwState{
		"finish-migrate": types.PAUSED,
		"inmigrate":      types.PAUSING,
		"internal-error": types.BROKEN,
		"io-error":       types.BROKEN,
		"paused":         types.PAUSED,
		"postmigrate":    types.PAUSED,
		"prelaunch":      types.PAUSED,
		"restore-vm":     types.PAUSED,
		"running":        types.RUNNING,
		"save-vm":        types.PAUSED,
		"shutdown":       types.HALTING,
		"suspended":      types.PAUSED,
		"watchdog":       types.PAUSING,
		"guest-panicked": types.BROKEN,
		"colo":           types.PAUSED,
		"preconfig":      types.PAUSED,
	}
	res, err := getQemuStatus(getQmpExecutorSocket(domainName))
	if err != nil {
		return effectiveDomainID, types.BROKEN, logError("couldn't retrieve status for domain %s: %v", domainName, err)
	}

	if effectiveDomainState, matched := stateMap[res]; !matched {
		return effectiveDomainID, types.BROKEN, logError("domain %s reported to be in unexpected state %s", domainName, res)
	} else {
		return effectiveDomainID, effectiveDomainState, nil
	}
}

func (ctx kvmContext) PCIReserve(long string) error {
	logrus.Infof("PCIReserve long addr is %s", long)

	overrideFile := sysfsPciDevices + long + "/driver_override"
	driverPath := sysfsPciDevices + long + "/driver"
	unbindFile := driverPath + "/unbind"

	//Check if already bound to vfio-pci
	driverPathInfo, driverPathErr := os.Stat(driverPath)
	vfioDriverPathInfo, vfioDriverPathErr := os.Stat(vfioDriverPath)
	if driverPathErr == nil && vfioDriverPathErr == nil &&
		os.SameFile(driverPathInfo, vfioDriverPathInfo) {
		logrus.Infof("Driver for %s is already bound to vfio-pci, skipping unbind", long)
		return nil
	}

	//map vfio-pci as the driver_override for the device
	if err := ioutil.WriteFile(overrideFile, []byte("vfio-pci"), 0644); err != nil {
		return logError("driver_override failure for PCI device %s: %v",
			long, err)
	}

	//Unbind the current driver, whatever it is, if there is one
	if _, err := os.Stat(unbindFile); err == nil {
		if err := ioutil.WriteFile(unbindFile, []byte(long), 0644); err != nil {
			return logError("unbind failure for PCI device %s: %v",
				long, err)
		}
	}

	if err := ioutil.WriteFile(sysfsPciDriversProbe, []byte(long), 0644); err != nil {
		return logError("drivers_probe failure for PCI device %s: %v",
			long, err)
	}

	return nil
}

func (ctx kvmContext) PCIRelease(long string) error {
	logrus.Infof("PCIRelease long addr is %s", long)

	overrideFile := sysfsPciDevices + long + "/driver_override"
	unbindFile := sysfsPciDevices + long + "/driver/unbind"

	//Write Empty string, to clear driver_override for the device
	if err := ioutil.WriteFile(overrideFile, []byte("\n"), 0644); err != nil {
		logrus.Fatalf("driver_override failure for PCI device %s: %v",
			long, err)
	}

	//Unbind vfio-pci, if unbind file is present
	if _, err := os.Stat(unbindFile); err == nil {
		if err := ioutil.WriteFile(unbindFile, []byte(long), 0644); err != nil {
			logrus.Fatalf("unbind failure for PCI device %s: %v",
				long, err)
		}
	}

	//Write PCI DDDD:BB:DD.FF to /sys/bus/pci/drivers_probe,
	//as a best-effort to bring back original driver
	if err := ioutil.WriteFile(sysfsPciDriversProbe, []byte(long), 0644); err != nil {
		logrus.Fatalf("drivers_probe failure for PCI device %s: %v",
			long, err)
	}

	return nil
}

func usbBusPort(USBAddr string) (string, string) {
	ids := strings.SplitN(USBAddr, ":", 2)
	if len(ids) == 2 {
		return ids[0], ids[1]
	}
	return "", ""
}

func getQmpExecutorSocket(domainName string) string {
	return kvmStateDir + domainName + "/qmp"
}

func getQmpListenerSocket(domainName string) string {
	return kvmStateDir + domainName + "/listener.qmp"
}

func getOsVersion() string {
	var uname syscall.Utsname

	syscall.Uname(&uname)
	b := make([]rune, len(uname.Release[:]))

	for i, v := range uname.Release {
		b[i] = rune(v)
	}

	return string(b)
}

func kernelVersionTag(major uint64, minor uint64, patch uint64) uint64 {
	return (major << 16) | (minor << 8) | (patch << 0)
}
