// Copyright (c) 2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package volumemgr

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/lf-edge/eve/pkg/pillar/types"
)

// TargetBlockCreate - Create fileio target for volume
func TargetBlockCreate(status types.VolumeStatus) error {

	var targetRoot = filepath.Join("/hostfs/sys/kernel/config/target/core/iblock_0/", status.DisplayName)
	if err := os.MkdirAll(targetRoot, 0755); err != nil {
		return fmt.Errorf("Error create catalog in sysfs for target blockio: %v", err)
	}

	var controlPath = filepath.Join(targetRoot, "control")
	var data = fmt.Sprintf("dev=/dev/mapper/thin-volume,udev_path=/dev/mapper/thin-volume,readonly=0,wwn=%s",  status.VolumeID.String())
	if err := ioutil.WriteFile(controlPath, []byte(data), 0660); err != nil {
		return fmt.Errorf("Error set control: %v", err)
	}

	var vpdUnitSerial = filepath.Join(targetRoot, "wwn", "vpd_unit_serial")
	if err := ioutil.WriteFile(vpdUnitSerial, []byte(status.VolumeID.String()), 0660); err != nil {
		return fmt.Errorf("Error set UUID for target: %v", err)
	}

	var udevPath = filepath.Join(targetRoot, "udev_path")
	if err := ioutil.WriteFile(udevPath, []byte("/dev/mapper/thin-volume"), 0660); err != nil {
		return fmt.Errorf("Error set udev_path for target %v", err)
	}

	var enablePath = filepath.Join(targetRoot, "enable")
	if err := ioutil.WriteFile(enablePath, []byte("1"), 0660); err != nil {
		return fmt.Errorf("Error set enable target fileIO: %v", err)
	}

	return nil
}
