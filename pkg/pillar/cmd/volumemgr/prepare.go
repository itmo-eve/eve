// Copyright (c) 2021 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package volumemgr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lf-edge/edge-containers/pkg/registry"
	"github.com/lf-edge/eve/pkg/pillar/cas"
	"github.com/lf-edge/eve/pkg/pillar/diskmetrics"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zfs"
)

func getVolumeFilePathAndVSize(ctx *volumemgrContext, status types.VolumeStatus) (string, uint64, error) {

	puller := registry.Puller{
		Image: status.ReferenceName,
	}
	casClient, err := cas.NewCAS(casClientType)
	if err != nil {
		err = fmt.Errorf("getVolumeFilePathAndVSize: exception while initializing CAS client: %s", err.Error())
		return "", 0, err
	}
	defer casClient.CloseClient()
	ctrdCtx, done := casClient.CtrNewUserServicesCtx()
	defer done()

	resolver, err := casClient.Resolver(ctrdCtx)
	if err != nil {
		errStr := fmt.Sprintf("error getting CAS resolver: %v", err)
		log.Error(errStr)
		return "", 0, errors.New(errStr)
	}
	pathToFile := ""
	_, i, err := puller.Config(true, os.Stderr, resolver)
	if err != nil {
		errStr := fmt.Sprintf("error Config for ref %s: %v", status.ReferenceName, err)
		log.Error(errStr)
		return "", 0, errors.New(errStr)
	}
	if len(i.RootFS.DiffIDs) > 0 {
		// FIXME we expects root in the first layer for now
		b := i.RootFS.DiffIDs[0]
		// FIXME we need the proper way to extract file from content dir of containerd
		pathToFile = filepath.Join(types.ContainerdContentDir, "blobs", b.Algorithm().String(), b.Encoded())
	}

	if pathToFile == "" {
		errStr := fmt.Sprintf("no blobs to convert found for ref %s", status.ReferenceName)
		log.Error(errStr)
		return "", 0, errors.New(errStr)
	}

	vSize, err := diskmetrics.GetDiskVirtualSize(log, pathToFile)
	if err != nil {
		errStr := fmt.Sprintf("error GetDiskVirtualSize for file %s: %v", pathToFile, err)
		log.Error(errStr)
		return "", 0, errors.New(errStr)
	}
	if vSize > status.MaxVolSize {
		log.Warnf("Virtual size (%d) of volume (%s) is larger than provided MaxVolSize (%d). "+
			"Will use virtual size.", vSize, status.Key(), status.MaxVolSize)
	}
	if vSize < status.MaxVolSize {
		vSize = status.MaxVolSize
	}
	return pathToFile, vSize, nil
}

func prepareZVol(ctx *volumemgrContext, status types.VolumeStatus) error {
	_, vSize, err := getVolumeFilePathAndVSize(ctx, status)
	if err != nil {
		errStr := fmt.Sprintf("Error obtaining file for zvol at volume %s, error=%v",
			status.Key(), err)
		log.Error(errStr)
		return errors.New(errStr)
	}
	zVolName := status.ZVolName(types.VolumeZFSPool)
	if stdoutStderr, err := zfs.CreateVolumeDataset(log, zVolName, vSize, "on"); err != nil {
		errStr := fmt.Sprintf("Error creating zfs zvol at %s, error=%v, output=%s",
			zVolName, err, stdoutStderr)
		log.Error(errStr)
		return errors.New(errStr)
	}
	return nil
}

func prepareVolume(ctx *volumemgrContext, status types.VolumeStatus) error {
	log.Errorf("prepareVolume: %s", status.Key())
	if ctx.persistType != types.PersistZFS || status.IsContainer() {
		return nil
	}
	return prepareZVol(ctx, status)
}
