// Copyright (c) 2019-2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package downloader

import (
	"github.com/lf-edge/eve/pkg/pillar/types"
)

func handleDatastoreConfigCreate(ctxArg interface{}, key string,
	configArg interface{}) {
	handleDatastoreConfigImpl(ctxArg, key, configArg)
	notifyResolveConfigByNewDatastoreConfig(ctxArg, key, configArg)
}

func handleDatastoreConfigModify(ctxArg interface{}, key string,
	configArg interface{}, oldConfigArg interface{}) {
	handleDatastoreConfigImpl(ctxArg, key, configArg)
}

func handleDatastoreConfigImpl(ctxArg interface{}, key string,
	configArg interface{}) {

	ctx := ctxArg.(*downloaderContext)
	config := configArg.(types.DatastoreConfig)
	log.Functionf("handleDatastoreConfigImpl for %s", key)
	checkAndUpdateDownloadableObjects(ctx, config.UUID)
	log.Functionf("handleDatastoreConfigImpl for %s, done", key)
}

func handleDatastoreConfigDelete(ctxArg interface{}, key string,
	configArg interface{}) {
	ctx := ctxArg.(*downloaderContext)
	config := configArg.(types.DatastoreConfig)
	cipherBlock := config.CipherBlockStatus
	ctx.pubCipherBlockStatus.Unpublish(cipherBlock.Key())
	log.Functionf("handleDatastoreConfigDelete for %s", key)
}

//notifyResolveConfigByNewDatastoreConfig fires modify handler for ResolveConfig
//we need to call it in case of no DatastoreConfig found
func notifyResolveConfigByNewDatastoreConfig(ctxArg interface{}, key string,
	configArg interface{}) {

	ctx := ctxArg.(*downloaderContext)
	config := configArg.(types.DatastoreConfig)
	log.Functionf("notifyResolveConfigByNewDatastoreConfig for %s", key)
	resolveConfigs := ctx.subResolveConfig.GetAll()
	for _, v := range resolveConfigs {
		cfg := v.(types.ResolveConfig)
		if cfg.DatastoreID == config.UUID {
			//on this step we have ResolveConfig and we inside handleDatastoreConfigCreate
			resHandler.modify(ctxArg, cfg.Key(), cfg, cfg)
		}
	}
	log.Functionf("notifyResolveConfigByNewDatastoreConfig for %s, done", key)
}
