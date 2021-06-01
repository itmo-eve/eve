// Copyright (c) 2021 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package zedagent

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
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
func localProfileTimerTask(handleChannel chan interface{}, getconfigCtx *getconfigContext) {

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
	} else {
		cleanSavedProtoMessage(savedLocalProfileFile)
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
					} else {
						publishZedAgentStatus(getconfigCtx)
					}
				}
				ctx.ps.CheckMaxTimeTopic(wdName, "getLocalProfileConfig", start,
					warningTime, errorTime)
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
	if getconfigCtx.currentProfile != localProfile.GetLocalProfile() {
		log.Noticef("validateAndSetLocalProfile: profile changed from %s to %s",
			getconfigCtx.currentProfile, localProfile.GetLocalProfile())
		getconfigCtx.currentProfile = localProfile.GetLocalProfile()
		writeReceivedProtoMessageLocalProfile(localProfileBytes)
		publishZedAgentStatus(getconfigCtx)
	}
	return nil
}

// read saved local profile in case of particular reboot reason
// if it is success will not read again on the next call
func readSavedLocalProfile(getconfigCtx *getconfigContext) error {
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
	return nil
}

//prepareLocalProfileServerMap process configuration of network instances to find match with defined localServerURL
//returns the srcIP for the zero or more network instances on which the locaclProfileServer might be hosted
//based on a subnet inclusion match or hostname in dns records
//in form actual link (with hostname replaced) -> source IP
func prepareLocalProfileServerMap(localServerURL string, getconfigCtx *getconfigContext) (map[string]net.IP, error) {
	res := make(map[string]net.IP)

	netInstanses := getconfigCtx.pubNetworkInstanceConfig.GetAll()
	u, err := url.Parse(localServerURL)
	if err != nil {
		return nil, fmt.Errorf("checkAndPrepareLocalIP: url.Parse: %s", err)
	}
	localProfileServerHostname := u.Hostname()
	localProfileServerIP := net.ParseIP(localProfileServerHostname)
	for _, entry := range netInstanses {
		config := entry.(types.NetworkInstanceConfig)
		if localProfileServerIP != nil {
			//check if defined ip of localServer is on subnet
			if config.Subnet.Contains(localProfileServerIP) {
				res[localServerURL] = config.Gateway
			}
		} else {
			//check if defined host is in DNS records
			for _, el := range config.DnsNameToIPList {
				if el.HostName == localProfileServerHostname {
					for _, ip := range el.IPs {
						localServerURLReplaced := strings.Replace(localServerURL, localProfileServerHostname,
							ip.String(), 1)
						res[localServerURLReplaced] = config.Gateway
						log.Functionf(
							"prepareLocalProfileServerMap: will use url with replaced hostname: %s",
							localServerURLReplaced)
					}
				}
			}
		}
	}
	return res, nil
}

// getLocalProfileConfig connects to local profile server to fetch current profile
func getLocalProfileConfig(localServerURL string, iteration int, getconfigCtx *getconfigContext) error {

	log.Tracef("getLocalProfileConfig(%s, %d)", localServerURL, iteration)

	localServerMap, err := prepareLocalProfileServerMap(localServerURL, getconfigCtx)
	if err != nil {
		return fmt.Errorf("getLocalProfileConfig: prepareLocalProfileServerMap: %s", err)
	}

	if len(localServerMap) == 0 {
		return fmt.Errorf(
			"getLocalProfileConfig: cannot find any configured local subnets for localServerURL: %s",
			localServerURL)
	}

	var errList []string
	for link, srcIP := range localServerMap {
		resp, contents, err := zedcloud.SendLocal(zedcloudCtx, link, srcIP, 0, nil)
		if err != nil {
			errList = append(errList, fmt.Sprintf("SendLocal: %s", err))
			continue
		}
		if resp.StatusCode == http.StatusOK {
			if err := validateProtoMessage(link, resp); err != nil {
				// send something to ledmanager ???
				errList = append(errList, fmt.Sprintf("validateProtoMessage: resp header error: %s", err))
				continue
			}
			getconfigCtx.localProfileReceived = true
			err := validateAndSetLocalProfile(contents, getconfigCtx)
			if err != nil {
				errList = append(errList, fmt.Sprintf("validateAndSetLocalProfile: %s", err))
				continue
			} else {
				return nil
			}
		}
		errList = append(errList, fmt.Sprintf("getLocalProfileConfig: wrong response status code: %d",
			resp.StatusCode))
	}
	return fmt.Errorf("getLocalProfileConfig: all attempts failed: %s", strings.Join(errList, ";"))
}

func writeReceivedProtoMessageLocalProfile(contents []byte) {
	writeProtoMessage(savedLocalProfileFile, contents)
}

//parseProfile process local and global profile configuration
//must be called before processing of app instances from config
func parseProfile(ctx *getconfigContext, config *zconfig.EdgeDevConfig) {
	log.Functionf("parseProfile start: globalProfile: %s currentProfile: %s",
		ctx.globalProfile, ctx.currentProfile)
	ctx.globalProfile = config.GlobalProfile
	if ctx.localProfileServer != config.LocalProfileServer {
		log.Noticef("parseProfile: LocalProfileServer changed from %s to %s",
			ctx.localProfileServer, config.LocalProfileServer)
		ctx.localProfileServer = config.LocalProfileServer
		if ctx.localProfileReceived {
			// if we received local profile from another localProfileServer, remove it
			cleanSavedProtoMessage(savedLocalProfileFile)
		}
	}
	ctx.profileServerToken = config.ProfileServerToken
	if config.LocalProfileServer == "" {
		// if we do not use LocalProfileServer set profile to GlobalProfile
		ctx.currentProfile = config.GlobalProfile
	} else {
		if !ctx.readSavedLocalProfile && !ctx.localProfileReceived {
			// try to read saved local profile actual app may be loaded later
			if err := readSavedLocalProfile(ctx); err != nil {
				log.Functionf("parseConfig readSavedLocalProfile: %s", err)
			}
		}
	}
	publishZedAgentStatus(ctx)
	log.Functionf("parseProfile done globalProfile: %s currentProfile: %s",
		ctx.globalProfile, ctx.currentProfile)
}
