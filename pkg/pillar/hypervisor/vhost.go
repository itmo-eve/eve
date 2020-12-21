// Copyright (c) 2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package hypervisor

import (
	"fmt"
	"os/exec"

	"github.com/lf-edge/eve/pkg/pillar/types"
)

// VhostCreate - Create vhost fabric for volume
func VhostCreate(status types.DiskStatus) (string, error) {
	var x = GenerateWWN(status.DisplayName)
	var wwn = x.DeviceWWN()
	var wwnNexus = x.NexusWWN()

	var targetRoot = fmt.Sprintf(`/hostfs/sys/kernel/config/target/core/fileio_0/%v`, status.DisplayName)
	var vhostRoot = fmt.Sprintf(`/hostfs/sys/kernel/config/target/vhost/%v/tpgt_1/lun/lun_0`, wwn)
	if err := os.MkdirAll(vhostRoot, 0755); err != nil {
		logError(fmt.Sprintf("Error create catalog in sysfs for vhost filio [%v]", err))
		return err
	}

	var controlPath = filepath.Join(targetRoot, "control")
	if err := ioutil.WriteFile(controlPath, []byte("scsi_host_id=1,scsi_channel_id=0,scsi_target_id=0,scsi_lun_id=0"), 0660); err != nil {
		logError(fmt.Sprintf("Error set control: %v", err))
		return err
	}

	var nexusPath = fmt.Sprintf(`/hostfs/sys/kernel/config/target/vhost/%v/tpgt_1/nexus`, wwn)
	if err := ioutil.WriteFile(nexusPath, []byte(wwnNexus), 0660); err != nil {
		logError(fmt.Sprintf("Error set control: %v", err))
		return err
	}

	var script = fmt.Sprintf(`cd /hostfs/sys/kernel/config/target/vhost/%v/tpgt_1/lun/lun_0 && ln -s ../../../../../core/fileio_0/%v/ .`, wwn, status.DisplayName)
	if err := exec.Command("bash", "-c", script).Run(); err != nil {
		logError("Failed to execute command [%s]: %v", cmd, err)
	}

	logError("Create vhost for %v, wwn %v", status.DisplayName, wwn)
	return wwn, nil
}
