package query

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// version is the version of the query protocol. It represents Gamespy Query Protocol version 4.
var Header = [2]byte{0xfe, 0xfd}

// splitNum is the split number set in a response before the Information is written. It is conventionally
// written as a string 'splitnum' terminated by a null byte, so we do this too.
var splitNum = [9]byte{'S', 'P', 'L', 'I', 'T', 'N', 'U', 'M', 0x00}

// playerKey is the key under which players are typically stored.
var playerKey = [...]byte{0x00, 0x01, 'p', 'l', 'a', 'y', 'e', 'r', '_', 0x00}

const (
	queryTypeHandshake   = 0x09
	queryTypeInformation = 0x00
)

// request is a packet sent by the client to the server. It is first used to request the handshake, and after
// that to request the information.
type request struct {
	// RequestType is the type of the request. It is either queryTypeHandshake or queryTypeInformation,
	// with queryTypeHandshake being sent first and queryTypeInformation being sent in response to the
	// queryTypeHandshake.
	RequestType byte
	// SequenceNumber is a sequence number identifying the request. Typically, this is a timestamp, but it is
	// merely used to match request with response, so the actual value it holds isn't relevant.
	SequenceNumber int32
	// ResponseNumber is the number sent in the response following a handshake request. It only requires being
	// set if RequestType is queryTypeInformation.
	ResponseNumber int32

	Token int32
}

// response is a packet sent by the server to the client. It is sent in response to a request, with either a
// response indicating the handshake was successful or the actual information of the query.
type response struct {
	// ResponseType is the RequestType of the request that the packet is a response to. It is either
	// queryTypeHandshake, which holds simply a number for the next request, or queryTypeInformation, which
	// holds the information of the server.
	ResponseType byte
	// SequenceNumber is the SequenceNumber sent in the request packet. Typically, this is a timestamp, but it
	// is merely used to match request with response, so the actual value it holds isn't relevant.
	SequenceNumber int32
	// ResponseNumber is a number sent only if ResponseType is queryTypeHandshake. The request packet holds
	// this number in the next request.
	ResponseNumber int32
	// Information is a list of all information of the server. It is sent only if ResponseType is
	// queryTypeInformation.
	Information map[string]string

	Players []string
}

// Unmarshal ...
func (pk *request) Unmarshal(r io.Reader) error {
	if err := binary.Read(r, binary.BigEndian, &pk.RequestType); err != nil {
		return err
	}
	if err := binary.Read(r, binary.BigEndian, &pk.SequenceNumber); err != nil {
		return err
	}
	if pk.RequestType == queryTypeInformation {
		if err := binary.Read(r, binary.BigEndian, &pk.ResponseNumber); err != nil {
			return err
		}
		p := make([]byte, 4)
		_, err := r.Read(p)
		return err
	} else if pk.RequestType != queryTypeHandshake {
		return fmt.Errorf("unknown request type %X", pk.RequestType)
	}
	return nil
}

// Marshal ...
func (pk *response) Marshal(w io.Writer) {
	_ = binary.Write(w, binary.BigEndian, pk.ResponseType)
	_ = binary.Write(w, binary.BigEndian, pk.SequenceNumber)
	if pk.ResponseType == queryTypeHandshake {
		v := []byte(fmt.Sprint(pk.ResponseNumber))
		if len(v) != 12 {
			// Pad the response number to 12 bytes.
			v = append(v, make([]byte, 12-len(v))...)
		}
		_, _ = w.Write(v)
	} else {
		_, _ = w.Write(splitNum[:])
		_ = binary.Write(w, binary.BigEndian, byte(0x80)) // Number of packets, but in our case always 0x80.
		_ = binary.Write(w, binary.BigEndian, byte(0))    // Unused.
		values := make([][]byte, 0, len(pk.Information)*2)
		for key, val := range pk.Information {
			values = append(values, []byte(key))
			values = append(values, []byte(val))
		}
		values = append(values, playerKey[:])
		for _, player := range pk.Players {
			values = append(values, []byte(player))
		}
		values = append(values, []byte{0x00})

		// Join all keys and values together using a null byte.
		_, _ = w.Write(bytes.Join(values, []byte{0x00}))
	}
}
