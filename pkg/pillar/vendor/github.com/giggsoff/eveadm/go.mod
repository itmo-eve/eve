module github.com/giggsoff/eveadm

go 1.13

require (
	github.com/lf-edge/eve/pkg/pillar v0.0.0-20200301202154-704247b2b305
	github.com/mitchellh/go-homedir v1.1.0
	github.com/spf13/cobra v0.0.5
	github.com/spf13/viper v1.6.1
)

replace github.com/lf-edge/eve/api/go => github.com/lf-edge/eve/api/go v0.0.0-20200301202154-704247b2b305
