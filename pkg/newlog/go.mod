module github.com/lf-edge/eve/pkg/newlog

go 1.15

require (
	github.com/euank/go-kmsg-parser v2.0.0+incompatible
	github.com/golang/protobuf v1.4.3
	github.com/google/go-cmp v0.5.4
	github.com/google/go-containerregistry v0.4.0 // indirect
	github.com/lf-edge/eve/api/go v0.0.0-20210209052624-0ef611252120
	github.com/lf-edge/eve/pkg/pillar v0.0.0-20210209052624-0ef611252120
	github.com/sirupsen/logrus v1.7.0
	github.com/vishvananda/netlink v1.1.0 // indirect
	github.com/vishvananda/netns v0.0.0-20210104183010-2eb08e3e575f // indirect
	golang.org/x/sys v0.0.0-20210124154548-22da62e12c0c // indirect
)

replace github.com/lf-edge/eve/api/go => ../../api/go

replace github.com/lf-edge/eve/pkg/pillar => ../pillar
