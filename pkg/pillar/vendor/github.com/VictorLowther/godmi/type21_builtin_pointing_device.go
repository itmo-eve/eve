/*
* File Name:	type21_builtin_pointing_device.go
* Description:
* Author:	Chapman Ou <ochapman.cn@gmail.com>
* Created:	2014-08-19
 */
package godmi

import (
	"fmt"
	"strconv"
)

type BuiltinPointingDeviceType byte

func (b BuiltinPointingDeviceType) String() string {
	return safeLookup(byte(b)-1,
		"Other",
		"Unknown",
		"Mouse",
		"Track Ball",
		"Track Point",
		"Glide Point",
		"Touch Pad",
		"Touch Screen",
		"Optical Sensor",
	)
}

type BuiltinPointingDeviceInterface byte

func (b BuiltinPointingDeviceInterface) String() string {
	if b < 0xA0 {
		return safeLookup(byte(b)-1,
			"Other", // 0x01h
			"Unknown",
			"Serial",
			"PS/2",
			"Infrared",
			"HP-HIL",
			"Bus mouse",
			"ADB (Apple Desktop Bus)", // 0x08h
		)
	}
	return safeLookup(byte(b)-0xA0,
		"Bus mouse DB-9", // 0xA0h
		"Bus mouse micro-DIN",
		"USB", // 0xA2h
	)
}

type BuiltinPointingDevice struct {
	infoCommon
	Type            BuiltinPointingDeviceType
	Interface       BuiltinPointingDeviceInterface
	NumberOfButtons byte
}

func (b BuiltinPointingDevice) String() string {
	return fmt.Sprintf("Built-in Pointing Device\n"+
		"\tType: %s\n"+
		"\tInterface: %s\n"+
		"\tNumber of Buttons: %d",
		b.Type,
		b.Interface,
		b.NumberOfButtons,
	)
}

func newBuiltinPointingDevice(h dmiHeader) dmiTyper {
	data := h.data()
	bi := &BuiltinPointingDevice{
		Type:            BuiltinPointingDeviceType(data[0x04]),
		Interface:       BuiltinPointingDeviceInterface(data[0x05]),
		NumberOfButtons: data[0x06],
	}
	BuiltinPointingDevices = append(BuiltinPointingDevices, bi)
	return bi
}

var BuiltinPointingDevices []*BuiltinPointingDevice

func GetBuiltinPointingDevice() string {
	var ret string
	for i, v := range BuiltinPointingDevices {
		ret += "\nBuiltin Pointing Devices information index:" + strconv.Itoa(i) + "\n" + v.String()
	}
	return ret
}

func init() {
	addTypeFunc(SMBIOSStructureTypeBuilt_inPointingDevice, newBuiltinPointingDevice)
}
