// Copyright (c) 2021 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/google/go-cmp/cmp"
	"github.com/lf-edge/eve/pkg/pillar/base"
)

// BaseOSMgrStatus : for sending from baseosmgr
type BaseOSMgrStatus struct {
	Name                string
	BaseosUpdateCounter uint32 // Current BaseosUpdateCounter from baseosmgr
}

// Key :
func (status BaseOSMgrStatus) Key() string {
	return status.Name
}

// LogCreate :
func (status BaseOSMgrStatus) LogCreate(logBase *base.LogObject) {
	logObject := base.NewLogObject(logBase, base.BaseOSMgrStatusLogType, status.Name,
		nilUUID, status.LogKey())
	if logObject == nil {
		return
	}
	logObject.Noticef("Baseosmgr status create")
}

// LogModify :
func (status BaseOSMgrStatus) LogModify(logBase *base.LogObject, old interface{}) {
	logObject := base.EnsureLogObject(logBase, base.BaseOSMgrStatusLogType, status.Name,
		nilUUID, status.LogKey())

	oldStatus, ok := old.(BaseOSMgrStatus)
	if !ok {
		logObject.Clone().Fatalf("LogModify: Old object interface passed is not of BaseOSMgrStatus type")
	}
	// XXX remove?
	logObject.CloneAndAddField("diff", cmp.Diff(oldStatus, status)).
		Noticef("Baseosmgr status modify")
}

// LogDelete :
func (status BaseOSMgrStatus) LogDelete(logBase *base.LogObject) {
	logObject := base.EnsureLogObject(logBase, base.BaseOSMgrStatusLogType, status.Name,
		nilUUID, status.LogKey())
	logObject.Noticef("Baseosmgr status delete")

	base.DeleteLogObject(logBase, status.LogKey())
}

// LogKey :
func (status BaseOSMgrStatus) LogKey() string {
	return string(base.BaseOSMgrStatusLogType) + "-" + status.Key()
}
