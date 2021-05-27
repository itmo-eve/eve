// Copyright (c) 2017-2021 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package zedagent

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/golang/protobuf/proto"
	zconfig "github.com/lf-edge/eve/api/go/config"
	"github.com/lf-edge/eve/api/go/profile"
	"github.com/lf-edge/eve/pkg/pillar/flextimer"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
)

const (
	defaultLocalProfileServerPort = "8888"
	savedLocalProfileFile         = "lastlocalprofile"
)

func getLocalProfileURL(localProfileServer string) (string, error) {
	localProfileURL := fmt.Sprintf("http://%s", localProfileServer)
	u, err := url.Parse(localProfileURL)
	if err != nil {
		return "", fmt.Errorf("url.Parse: %s", err)
	}
	if u.Port() == "" {
		localProfileURL = fmt.Sprintf("%s:%s", localProfileURL, defaultLocalProfileServerPort)
	}
	return fmt.Sprintf("%s/api/v1/local_profile", localProfileURL), nil
}

// Run a periodic fetch of the currentProfile
func localProfileTimerTask(handleChannel chan interface{},
	getconfigCtx *getconfigContext) {

	ctx := getconfigCtx.zedagentCtx
	iteration := 0
	localProfileServer := getconfigCtx.localProfileServer
	if localProfileServer != "" {
		localProfileURL, err := getLocalProfileURL(localProfileServer)
		if err != nil {
			log.Errorf("localProfileTimerTask: getLocalProfileURL: %s", err)
		} else {
			if err := getLocalProfileConfig(localProfileURL, iteration, getconfigCtx); err != nil {
				log.Errorf("localProfileTimerTask: %s", err)
			}
		}
		publishZedAgentStatus(getconfigCtx)
	}

	// use ConfigInterval as localProfileInterval
	localProfileInterval := ctx.globalConfig.GlobalValueInt(types.ConfigInterval)
	interval := time.Duration(localProfileInterval) * time.Second
	max := float64(interval)
	min := max * 0.3
	ticker := flextimer.NewRangeTicker(time.Duration(min),
		time.Duration(max))
	// Return handle to caller
	handleChannel <- ticker

	wdName := agentName + "currentProfile"

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	ctx.ps.StillRunning(wdName, warningTime, errorTime)
	ctx.ps.RegisterFileWatchdog(wdName)

	for {
		select {
		case <-ticker.C:
			localProfileServer = getconfigCtx.localProfileServer
			if localProfileServer != "" {
				start := time.Now()
				iteration++
				localProfileURL, err := getLocalProfileURL(localProfileServer)
				if err != nil {
					log.Errorf("getLocalProfileURL: %s", err)
				} else {
					if err := getLocalProfileConfig(localProfileURL, iteration, getconfigCtx); err != nil {
						log.Errorf("localProfileTimerTask getLocalProfileConfig: %s", err)
					}
				}
				ctx.ps.CheckMaxTimeTopic(wdName, "getLocalProfileConfig", start,
					warningTime, errorTime)
				publishZedAgentStatus(getconfigCtx)
			}

		case <-stillRunning.C:
		}
		ctx.ps.StillRunning(wdName, warningTime, errorTime)
	}
}

func validateAndSetLocalProfile(localProfileBytes []byte, getconfigCtx *getconfigContext) error {
	var localProfile = &profile.LocalProfile{}
	err := proto.Unmarshal(localProfileBytes, localProfile)
	if err != nil {
		return fmt.Errorf("validateAndSetLocalProfile: Unmarshalling failed: %v", err)
	}
	if localProfile.GetServerToken() != getconfigCtx.profileServerToken {
		// send something to ledmanager ??
		return fmt.Errorf("validateAndSetLocalProfile: missamtch ServerToken for loacl profile server")
	}
	getconfigCtx.currentProfile = localProfile.GetLocalProfile()
	writeReceivedProtoMessageLocalProfile(localProfileBytes)
	publishZedAgentStatus(getconfigCtx)
	filterAndPublishAppInstancesWithCurrentProfile(getconfigCtx)
	return nil
}

// read saved local profile in case of particular reboot reason
// if it is success will not read again on the next call
func readSavedLocalProfile(getconfigCtx *getconfigContext) error {
	if !getconfigCtx.readSavedLocalProfile && !getconfigCtx.localProfileReceived {
		localProfile, err := readSavedProtoMessage(
			getconfigCtx.zedagentCtx.globalConfig.GlobalValueInt(types.StaleConfigTime),
			filepath.Join(checkpointDirname, savedLocalProfileFile), false)
		if err != nil {
			return fmt.Errorf("lastlocalprofile: %v", err)
		}
		if localProfile != nil {
			log.Function("Using saved local profile")
			getconfigCtx.readSavedLocalProfile = true
			return validateAndSetLocalProfile(localProfile, getconfigCtx)
		}
	}
	return nil
}

// getLocalProfileConfig connects to local profile server to fetch current profile
func getLocalProfileConfig(url string, iteration int, getconfigCtx *getconfigContext) error {

	log.Tracef("getLocalProfileConfig(%s, %d)", url, iteration)

	resp, contents, err := zedcloud.SendLocal(zedcloudCtx, url, 0, nil)
	if err != nil {
		if err := readSavedLocalProfile(getconfigCtx); err != nil {
			return err
		}
		return fmt.Errorf("SendLocal: %s", err)
	}
	if resp.StatusCode == http.StatusOK {
		if err := validateProtoMessage(url, resp); err != nil {
			// send something to ledmanager ???
			return fmt.Errorf("getLocalProfileConfig: resp header error: %s", err)
		}
		getconfigCtx.localProfileReceived = true
		return validateAndSetLocalProfile(contents, getconfigCtx)
	}
	return fmt.Errorf("getLocalProfileConfig: wrong response status code: %d", resp.StatusCode)
}

// filterAndPublishAppInstancesWithCurrentProfile check all app instances with currentProfile and set activate state
func filterAndPublishAppInstancesWithCurrentProfile(getconfigCtx *getconfigContext) {
	pub := getconfigCtx.pubAppInstanceConfig
	items := pub.GetAll()
	for _, c := range items {
		config := c.(types.AppInstanceConfig)
		oldActivate := config.Activate
		filterAppInstanceWithCurrentProfile(&config, getconfigCtx)
		if oldActivate != config.Activate {
			log.Functionf("filterAppInstancesWithLocalProfile: change activate state for %s from %t to %t",
				config.Key(), oldActivate, config.Activate)
			if err := pub.Publish(config.Key(), config); err != nil {
				log.Errorf("failed to publish: %s", err)
			}
		}
	}
}

func filterAppInstanceWithCurrentProfile(config *types.AppInstanceConfig, getconfigCtx *getconfigContext) {
	if getconfigCtx.currentProfile == "" {
		log.Functionf("filterAppInstanceWithCurrentProfile(%s): empty current", config.Key())
		// if currentProfile is empty set activate state from controller
		config.Activate = config.ControllerActivateState
		return
	}
	if len(config.ProfileList) == 0 {
		log.Functionf("filterAppInstanceWithCurrentProfile(%s): empty ProfileList", config.Key())
		//we have no profile in list so we should use activate state from the controller
		config.Activate = config.ControllerActivateState
		return
	}
	config.Activate = false
	for _, p := range config.ProfileList {
		if p == getconfigCtx.currentProfile {
			log.Functionf("filterAppInstanceWithCurrentProfile(%s): profile form list (%s) match current (%s)",
				config.Key(), p, getconfigCtx.currentProfile)
			// activate app if currentProfile is inside ProfileList
			config.Activate = true
			return
		}
	}
	log.Functionf("filterAppInstanceWithCurrentProfile(%s): no match with current (%s)",
		config.Key(), getconfigCtx.currentProfile)
}

func writeReceivedProtoMessageLocalProfile(contents []byte) {
	writeProtoMessage(savedLocalProfileFile, contents)
}

func parseProfile(ctx *getconfigContext, config *zconfig.EdgeDevConfig) {
	log.Functionf("parseProfile start: globalProfile: %s currentProfile: %s", ctx.globalProfile, ctx.currentProfile)
	ctx.globalProfile = config.GlobalProfile
	ctx.localProfileServer = config.LocalProfileServer
	ctx.profileServerToken = config.ProfileServerToken
	if config.LocalProfileServer == "" {
		// if we do not use LocalProfileServer set profile to GlobalProfile
		ctx.currentProfile = config.GlobalProfile
	} else {
		// try to read saved local profile actual app may be loaded later
		if err := readSavedLocalProfile(ctx); err != nil {
			log.Functionf("parseConfig readSavedLocalProfile: %s", err)
		}
	}
	log.Functionf("parseProfile done globalProfile: %s currentProfile: %s", ctx.globalProfile, ctx.currentProfile)
}
