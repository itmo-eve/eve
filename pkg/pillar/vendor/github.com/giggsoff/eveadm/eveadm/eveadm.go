package eveadm

import "github.com/lf-edge/eve/pkg/pillar/pubsub"
import "github.com/giggsoff/eveadm/cmd"

func Run(ps *pubsub.PubSub) {
	cmd.Execute()
}
