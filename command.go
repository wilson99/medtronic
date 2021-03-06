package medtronic

import (
	"bytes"
	"fmt"
	"log"
	"time"

	"github.com/ecc1/medtronic/packet"
)

const (
	maxPacketSize   = 70   // excluding CRC byte
	historyPageSize = 1024 // including CRC16
)

var (
	pumpPrefix []byte
)

func initPumpPrefix() {
	if len(pumpPrefix) != 0 {
		return
	}
	pumpPrefix = append([]byte{packet.Pump}, PumpAddress()...)
}

// Command represents a pump command.
type Command byte

//go:generate stringer -type Command

const (
	ack Command = 0x06
	nak Command = 0x15
)

// NoResponseError indicates that no response to a command was received.
type NoResponseError Command

func (e NoResponseError) Error() string {
	return fmt.Sprintf("no response to %v", Command(e))
}

// NoResponse checks whether the pump has a NoResponseError.
func (pump *Pump) NoResponse() bool {
	_, ok := pump.Error().(NoResponseError)
	return ok
}

// InvalidCommandError indicates that the pump rejected a command as invalid.
type InvalidCommandError struct {
	Command   Command
	PumpError PumpError
}

// PumpError represents an error response from the pump.
type PumpError byte

//go:generate stringer -type PumpError

// Pump error codes.
const (
	CommandRefused           PumpError = 0x08
	MaxSettingExceeded       PumpError = 0x09
	BolusInProgress          PumpError = 0x0C
	InvalidHistoryPageNumber PumpError = 0x0D
)

func (e InvalidCommandError) Error() string {
	return fmt.Sprintf("%v error: %v", e.Command, e.PumpError)
}

// BadResponseError indicates an unexpected response to a command.
type BadResponseError struct {
	Command Command
	Data    []byte
}

func (e BadResponseError) Error() string {
	return fmt.Sprintf("unexpected response to %v: % X", e.Command, e.Data)
}

// BadResponse sets the pump's error state to a BadResponseError.
func (pump *Pump) BadResponse(cmd Command, data []byte) {
	pump.SetError(BadResponseError{Command: cmd, Data: data})
}

// pumpPacket constructs a packet
// with the specified command code and parameters.
// A command packet with no parameters is 7 bytes long:
//   device type (0xA7)
//   3 bytes of pump ID
//   command code
//   length of parameters (0)
//   CRC-8
// A command packet with parameters is 71 bytes long:
//   device type (0xA7)
//   3 bytes of pump ID
//   command code
//   length of parameters
//   64 bytes of parameters plus padding
//   CRC-8
func pumpPacket(cmd Command, params []byte) []byte {
	initPumpPrefix()
	var data []byte
	if len(params) == 0 {
		data = make([]byte, 6)
	} else {
		data = make([]byte, maxPacketSize)
	}
	copy(data, pumpPrefix)
	data[4] = byte(cmd)
	data[5] = byte(len(params))
	if len(params) != 0 {
		copy(data[6:], params)
	}
	return packet.Encode(data)
}

// Execute sends a command and parameters to the pump and returns its response.
// Commands with parameters require an initial exchange with no parameters,
// followed by an exchange with the actual arguments.
func (pump *Pump) Execute(cmd Command, params ...byte) []byte {
	if len(params) != 0 {
		pump.perform(cmd, ack, nil)
		if pump.NoResponse() {
			pump.SetError(fmt.Errorf("%v command not performed", cmd))
			return nil
		}
		return pump.perform(cmd, ack, params)
	}
	return pump.perform(cmd, cmd, nil)
}

// History pages are returned as a series of 65-byte fragments:
//   sequence number (1 to 16)
//   64 bytes of payload
// The caller must send an ACK to receive the next fragment
// or a NAK to have the current one retransmitted.
// The 0x80 bit is set in the sequence number of the final fragment.
// The page consists of the concatenated payloads.
// The final 2 bytes are the CRC-16 of the preceding data.

const (
	numFragments    = 16
	fragmentLength  = 65
	doneBit         = 1 << 7
	maxNAKs         = 10
	downloadTimeout = 150 * time.Millisecond
)

// Download requests the given history page from the pump.
func (pump *Pump) Download(cmd Command, page int) []byte {
	timeout := pump.Timeout()
	pump.SetTimeout(downloadTimeout)
	defer pump.SetTimeout(timeout)
	results := make([]byte, 0, historyPageSize)
	data := pump.Execute(cmd, byte(page))
	if pump.Error() != nil {
		return nil
	}
	retries := pump.Retries()
	pump.SetRetries(1)
	defer pump.SetRetries(retries)
	seq := 1
	for {
		payload, n := pump.checkFragment(page, data, seq)
		if pump.Error() != nil {
			return nil
		}
		if n == seq {
			results = append(results, payload...)
			seq++
		}
		if n == numFragments {
			return pump.checkPageCRC(page, results)
		}
		// Acknowledge the current fragment and receive the next.
		next := pump.perform(ack, cmd, nil)
		if pump.Error() != nil {
			if !pump.NoResponse() {
				return nil
			}
			next = pump.handleNoResponse(cmd, page, seq)
		}
		data = next
	}
}

// checkFragment verifies that a fragment has the expected sequence number
// and returns the payload.
func (pump *Pump) checkFragment(page int, data []byte, expected int) ([]byte, int) {
	if len(data) != fragmentLength {
		pump.SetError(fmt.Errorf("history page %d: unexpected fragment length (%d)", page, len(data)))
		return nil, 0
	}
	seqNum := int(data[0] &^ doneBit)
	if seqNum > expected {
		// Missed fragment.
		pump.SetError(fmt.Errorf("history page %d: received fragment %d instead of %d", page, seqNum, expected))
		return nil, 0
	}
	if seqNum < expected {
		// Skip duplicate responses.
		return nil, seqNum
	}
	// This is the next fragment.
	done := data[0]&doneBit != 0
	if (done && seqNum != numFragments) || (!done && seqNum == numFragments) {
		pump.SetError(fmt.Errorf("history page %d: unexpected final sequence number (%d)", page, seqNum))
		return nil, seqNum
	}
	return data[1:], seqNum
}

// handleNoResponse sends NAKs to request retransmission of the expected fragment.
func (pump *Pump) handleNoResponse(cmd Command, page int, expected int) []byte {
	for count := 0; count < maxNAKs; count++ {
		pump.SetError(nil)
		data := pump.perform(nak, cmd, nil)
		if pump.Error() == nil {
			seqNum := int(data[0] &^ doneBit)
			format := "history page %d: received fragment %d after %d NAK"
			if count != 0 {
				format += "s"
			}
			log.Printf(format, page, seqNum, count+1)
			return data
		}
		if !pump.NoResponse() {
			return nil
		}
	}
	pump.SetError(fmt.Errorf("history page %d: lost fragment %d", page, expected))
	return nil
}

// checkPageCRC verifies the history page CRC and returns the page data with the CRC removed.
func (pump *Pump) checkPageCRC(page int, data []byte) []byte {
	if len(data) != historyPageSize {
		pump.SetError(fmt.Errorf("history page %d: unexpected size (%d)", page, len(data)))
		return nil
	}
	dataCRC := twoByteUint(data[historyPageSize-2:])
	data = data[:historyPageSize-2]
	calcCRC := packet.CRC16(data)
	if calcCRC != dataCRC {
		pump.SetError(fmt.Errorf("history page %d: computed CRC %02X but received %02X", page, calcCRC, dataCRC))
		return nil
	}
	return data
}

func (pump *Pump) perform(cmd Command, resp Command, params []byte) []byte {
	if pump.Error() != nil {
		return nil
	}
	p := pumpPacket(cmd, params)
	maxTries := pump.retries
	if len(params) != 0 {
		// Don't attempt any state-changing commands more than once.
		maxTries = 1
	}
	for tries := 0; tries < maxTries; tries++ {
		pump.SetError(nil)
		response, rssi := pump.Radio.SendAndReceive(p, pump.Timeout())
		if pump.Error() != nil {
			continue
		}
		if len(response) == 0 {
			pump.SetError(NoResponseError(cmd))
			continue
		}
		data, err := packet.Decode(response)
		if err != nil {
			pump.SetError(err)
			continue
		}
		if pump.unexpected(cmd, resp, data) {
			return nil
		}
		logTries(cmd, tries)
		pump.rssi = rssi
		return data[5:]
	}
	if pump.Error() == nil {
		panic("perform")
	}
	return nil
}

func logTries(cmd Command, tries int) {
	if tries == 0 {
		return
	}
	r := "retries"
	if tries == 1 {
		r = "retry"
	}
	log.Printf("%v command required %d %s", cmd, tries, r)
}

func (pump *Pump) unexpected(cmd Command, resp Command, data []byte) bool {
	if len(data) < 6 {
		pump.BadResponse(cmd, data)
		return true
	}
	n := len(pumpPrefix)
	if !bytes.Equal(data[:n], pumpPrefix) {
		pump.BadResponse(cmd, data)
		return true
	}
	switch Command(data[n]) {
	case cmd:
		return false
	case resp:
		return false
	case ack:
		if cmd == wakeup {
			return false
		}
		pump.BadResponse(cmd, data)
		return true
	case nak:
		pump.SetError(InvalidCommandError{
			Command:   cmd,
			PumpError: PumpError(data[n+1]),
		})
		return true
	default:
		pump.BadResponse(cmd, data)
		return true
	}
}
