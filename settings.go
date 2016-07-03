package medtronic

import (
	"time"
)

const (
	Settings Command = 0xC0
)

type SettingsInfo struct {
	AutoOff              time.Duration
	InsulinAction        time.Duration
	InsulinConcentration int // 50 or 100
	MaxBolus             MilliUnits
	MaxBasal             MilliUnits
	RfEnabled            bool
	SelectedPattern      int
}

func (pump *Pump) Settings() SettingsInfo {
	// Format of response depends on the pump family.
	newer := pump.Family() >= 23
	data := pump.Execute(Settings)
	if pump.Error() != nil {
		return SettingsInfo{}
	}
	if newer {
		if len(data) < 26 || data[0] != 25 {
			pump.BadResponse(Settings, data)
			return SettingsInfo{}
		}
	} else {
		if len(data) < 22 || data[0] != 21 {
			pump.BadResponse(Settings, data)
			return SettingsInfo{}
		}
	}
	info := SettingsInfo{
		AutoOff:         time.Duration(data[1]) * time.Hour,
		MaxBolus:        byteToMilliUnits(data[6], false),
		SelectedPattern: int(data[12]),
		RfEnabled:       data[13] == 1,
		InsulinAction:   time.Duration(data[18]) * time.Hour,
	}
	switch data[10] {
	case 0:
		info.InsulinConcentration = 100
	case 1:
		info.InsulinConcentration = 50
	default:
		pump.BadResponse(Settings, data)
	}
	if newer {
		info.MaxBasal = twoByteMilliUnits(data[8:10], true)
	} else {
		info.MaxBasal = twoByteMilliUnits(data[7:9], false)
	}
	return info
}
