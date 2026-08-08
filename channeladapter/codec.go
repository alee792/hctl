package channeladapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

type wireEnvelope struct {
	ProtocolVersion int             `json:"protocol_version"`
	ID              string          `json:"id"`
	Kind            Kind            `json:"kind"`
	CorrelationID   string          `json:"correlation_id,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

// MarshalFrame validates and encodes one frame without its trailing newline.
func MarshalFrame(envelope Envelope, direction Direction) ([]byte, error) {
	if err := ValidateEnvelope(envelope, direction); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(envelope.Payload)
	if err != nil {
		return nil, errors.New("cannot encode channel-adapter payload")
	}
	wire := wireEnvelope{ProtocolVersion: envelope.ProtocolVersion, ID: envelope.ID, Kind: envelope.Payload.frameKind(), CorrelationID: envelope.CorrelationID, Payload: payload}
	data, err := json.Marshal(wire)
	if err != nil || len(data) > MaxFrameBytes {
		return nil, fmt.Errorf("channel-adapter frame exceeds %d bytes", MaxFrameBytes)
	}
	return data, nil
}

// DecodeFrame strictly decodes one complete JSON frame without a newline.
func DecodeFrame(data []byte, direction Direction) (Envelope, error) {
	if len(data) == 0 || len(data) > MaxFrameBytes || !utf8.Valid(data) {
		return Envelope{}, errors.New("channel-adapter frame must be bounded UTF-8 JSON")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return Envelope{}, err
	}
	var wire wireEnvelope
	if err := decodeStrict(data, &wire); err != nil {
		return Envelope{}, fmt.Errorf("decode channel-adapter frame: %w", err)
	}
	payload, err := decodePayload(wire.Kind, wire.Payload)
	if err != nil {
		return Envelope{}, err
	}
	envelope := Envelope{ProtocolVersion: wire.ProtocolVersion, ID: wire.ID, CorrelationID: wire.CorrelationID, Payload: payload}
	if err := ValidateEnvelope(envelope, direction); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func decodePayload(kind Kind, data []byte) (Payload, error) {
	var payload Payload
	switch kind {
	case KindHello:
		payload = &Hello{}
	case KindInitialize:
		payload = &Initialize{}
	case KindReady:
		payload = &Ready{}
	case KindInboundMessage:
		payload = &InboundMessage{}
	case KindControlRequest:
		payload = &ControlRequest{}
	case KindControlResult:
		payload = &ControlResult{}
	case KindEventAck:
		payload = &EventAck{}
	case KindActivity:
		payload = &Activity{}
	case KindDelivery:
		payload = &Delivery{}
	case KindDeliveryResult:
		payload = &DeliveryResult{}
	case KindInteractionRequest:
		payload = &InteractionRequest{}
	case KindInteractionCancel:
		payload = &InteractionCancel{}
	case KindInteractionResult:
		payload = &InteractionResult{}
	case KindAttachmentFetch:
		payload = &AttachmentFetch{}
	case KindAttachmentChunk:
		payload = &AttachmentChunk{}
	case KindAttachmentDeliver:
		payload = &AttachmentDeliver{}
	case KindAttachmentResult:
		payload = &AttachmentResult{}
	case KindConnection:
		payload = &Connection{}
	case KindDiagnostic:
		payload = &Diagnostic{}
	case KindShutdown:
		payload = &Shutdown{}
	case KindShutdownComplete:
		payload = &ShutdownComplete{}
	default:
		return nil, fmt.Errorf("channel-adapter frame kind %q is unsupported", kind)
	}
	if err := decodeStrict(data, payload); err != nil {
		return nil, fmt.Errorf("decode channel-adapter %s payload: %w", kind, err)
	}
	return payload, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON document")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				if keyErr != nil {
					return keyErr
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("channel-adapter frame contains a duplicate object key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON document")
	}
	return nil
}

type Decoder struct {
	reader        *bufio.Reader
	maxFrameBytes int
}

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{reader: bufio.NewReaderSize(reader, MaxAttachmentChunkBytes), maxFrameBytes: MaxFrameBytes}
}

// SetMaxFrameBytes narrows the decoder after protocol negotiation. It must be
// called only between reads.
func (decoder *Decoder) SetMaxFrameBytes(maximum int) error {
	if maximum < 1 || maximum > decoder.maxFrameBytes {
		return errors.New("channel-adapter frame limit is invalid")
	}
	decoder.maxFrameBytes = maximum
	return nil
}

// Read reads one newline-delimited frame without ever retaining more than the
// frame bound. Oversized and unterminated lines are fatal protocol errors.
func (decoder *Decoder) Read(direction Direction) (Envelope, error) {
	var frame []byte
	for {
		part, err := decoder.reader.ReadSlice('\n')
		if len(frame)+len(part) > decoder.maxFrameBytes+1 {
			return Envelope{}, fmt.Errorf("channel-adapter frame exceeds %d bytes", decoder.maxFrameBytes)
		}
		frame = append(frame, part...)
		if err == nil {
			frame = frame[:len(frame)-1]
			if len(frame) > 0 && frame[len(frame)-1] == '\r' {
				return Envelope{}, errors.New("channel-adapter frames must use LF delimiters")
			}
			return DecodeFrame(frame, direction)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(frame) > 0 {
			return Envelope{}, errors.New("channel-adapter frame is not newline terminated")
		}
		return Envelope{}, err
	}
}

type Encoder struct{ writer io.Writer }

func NewEncoder(writer io.Writer) *Encoder { return &Encoder{writer: writer} }

func (encoder *Encoder) Write(envelope Envelope, direction Direction) error {
	data, err := MarshalFrame(envelope, direction)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	for len(data) > 0 {
		written, writeErr := encoder.writer.Write(data)
		if writeErr != nil {
			return writeErr
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func MarshalOperationResult(result OperationResult) ([]byte, error) {
	if err := ValidateOperationResult(result); err != nil {
		return nil, err
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > MaxOperationResultBytes {
		return nil, errors.New("channel-adapter operation result is too large")
	}
	return data, nil
}

func DecodeOperationResult(data []byte) (OperationResult, error) {
	if len(data) == 0 || len(data) > MaxOperationResultBytes || !utf8.Valid(data) {
		return OperationResult{}, errors.New("channel-adapter operation result must be bounded UTF-8 JSON")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return OperationResult{}, err
	}
	var result OperationResult
	if err := decodeStrict(data, &result); err != nil {
		return OperationResult{}, err
	}
	if err := ValidateOperationResult(result); err != nil {
		return OperationResult{}, err
	}
	return result, nil
}
