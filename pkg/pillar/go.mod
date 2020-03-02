module github.com/lf-edge/eve/pkg/pillar

go 1.12

require (
	github.com/Azure/azure-sdk-for-go v38.0.0+incompatible
	github.com/appc/docker2aci v0.17.2
	github.com/aws/aws-sdk-go v1.27.1
	github.com/eriknordmark/ipinfo v0.0.0-20190220084921-7ee0839158f9
	github.com/eriknordmark/netlink v0.0.0-20190912172510-3b6b45309321
	github.com/fsnotify/fsnotify v1.4.7
	github.com/giggsoff/eveadm v0.0.0-20200302143102-dadd342eb56b // indirect
	github.com/giggsoff/eveadm/eveadm v0.0.0-20200302143102-dadd342eb56b // indirect
	github.com/golang/protobuf v1.3.2
	github.com/google/go-cmp v0.4.0
	github.com/google/go-containerregistry v0.0.0-20200123184029-53ce695e4179
	github.com/google/go-tpm v0.1.1
	github.com/google/gopacket v1.1.16
	github.com/gorilla/websocket v1.4.0
	github.com/jackwakefield/gopac v1.0.2
	github.com/lf-edge/eve/api/go v0.0.0-00010101000000-000000000000
	github.com/pelletier/go-toml v1.6.0 // indirect
	github.com/pkg/sftp v1.10.0
	github.com/rackn/gohai v0.0.0-20190321191141-5053e7f1fa36
	github.com/satori/go.uuid v1.2.0
	github.com/shirou/gopsutil v0.0.0-20190323131628-2cbc9195c892
	github.com/sirupsen/logrus v1.4.2
	github.com/spf13/cast v1.3.1 // indirect
	github.com/spf13/cobra v0.0.6 // indirect
	github.com/spf13/jwalterweatherman v1.1.0 // indirect
	github.com/spf13/viper v1.6.2 // indirect
	github.com/stretchr/testify v1.4.0
	github.com/tatsushid/go-fastping v0.0.0-20160109021039-d7bb493dee3e
	golang.org/x/crypto v0.0.0-20191206172530-e9b2fee46413
	golang.org/x/net v0.0.0-20191004110552-13f9640d40b9
	golang.org/x/sys v0.0.0-20200302083256-062a44052db1 // indirect
	gopkg.in/ini.v1 v1.52.0 // indirect
	gopkg.in/mcuadros/go-syslog.v2 v2.3.0
	gopkg.in/yaml.v2 v2.2.8 // indirect
)

replace github.com/lf-edge/eve/api/go => ../../api/go

replace github.com/vishvananda/netlink/nl => github.com/eriknordmark/netlink/nl v0.0.0-20190903203740-41fa442996b8

replace github.com/vishvananda/netlink => github.com/eriknordmark/netlink v0.0.0-20190903203740-41fa442996b8

replace git.apache.org/thrift.git => github.com/apache/thrift v0.12.0

// this is because of a lower required version from github.com/appc/docker2aci . This conflicts (gently)
// with the requirements from github.com/google/go-containerregistry.
// REMOVE this as soon as docker2aci is done!
replace github.com/opencontainers/image-spec => github.com/opencontainers/image-spec v1.0.0-rc2

//Till we upstream ECDH TPM APIs
replace github.com/google/go-tpm => github.com/cshari-zededa/go-tpm v0.0.0-20200113112746-a8476c2d6eb3
