// Code generated by "stringer -type TempBasalType"; DO NOT EDIT.

package medtronic

import "strconv"

const _TempBasalType_name = "AbsolutePercent"

var _TempBasalType_index = [...]uint8{0, 8, 15}

func (i TempBasalType) String() string {
	if i >= TempBasalType(len(_TempBasalType_index)-1) {
		return "TempBasalType(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _TempBasalType_name[_TempBasalType_index[i]:_TempBasalType_index[i+1]]
}
