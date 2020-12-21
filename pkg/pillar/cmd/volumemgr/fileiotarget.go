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

// TargetCreate - Create fileio target for volume
func TargetCreate(status types.VolumeStatus) error {

	var targetRoot = filepath.Join("/hostfs/sys/kernel/config/target/core/fileio_0/", status.DisplayName)
	if err := os.MkdirAll(targetRoot, 0755); err != nil {
		log.Error(fmt.Sprintf("Error create catalog in sysfs for target filio [%v]", err))
		return err
	}

	var controlPath = filepath.Join(targetRoot, "control")
	var data = fmt.Sprintf("fd_dev_name=%s,fd_dev_size=%d,fd_buffered_io=1", status.PathName(), status.CurrentSize)
	if err := ioutil.WriteFile(controlPath, []byte(data), 0660); err != nil {
		log.Error(fmt.Sprintf("Error set control: %v", err))
		return err
	}

	var bsPath = filepath.Join(targetRoot, "attrib", "block_size")
	if err := ioutil.WriteFile(bsPath, []byte("4096"), 0660); err != nil {
		log.Error(fmt.Sprintf("Error set bs: %v", err))
		return err
	}

	var vpdUnitSerial = filepath.Join(targetRoot, "wwn", "vpd_unit_serial")
	if err := ioutil.WriteFile(vpdUnitSerial, []byte(status.VolumeID.String()), 0660); err != nil {
		log.Error(fmt.Sprintf("Error set UUID: %v", err))
		return err
	}

	var udevPath = filepath.Join(targetRoot, "udev_path")
	if err := ioutil.WriteFile(udevPath, []byte(status.PathName()), 0660); err != nil {
		log.Error(fmt.Sprintf("Error set udev %v", err))
		return err
	}

	var enablePath = filepath.Join(targetRoot, "enable")
	if err := ioutil.WriteFile(enablePath, []byte("1"), 0660); err != nil {
		log.Error(fmt.Sprintf("Error set enable: %v", err))
		return err
	}

	log.Functionf(fmt.Sprintf("Created target fileIO for [%v]:[%v] size=[%v] UUID:%s", status.DisplayName, status.PathName(), status.MaxVolSize, status.VolumeID))

	return nil
}
