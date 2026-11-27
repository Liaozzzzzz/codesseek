package embedder

import (
	"encoding/binary"
	"fmt"
	"os"
)

// Minimal ONNX protobuf inspection. It reads only the graph input/output names
// and their declared dimensions, so the embedder can discover the model
// contract instead of guessing it:
//
//	ModelProto.graph       = field 7
//	GraphProto.input       = field 11
//	GraphProto.output      = field 12
//	ValueInfoProto.name    = field 1, .type = field 2
//	TypeProto.tensor_type  = field 1
//	Tensor.elem_type       = field 1, .shape = field 2
//	TensorShapeProto.dim   = field 1
//	Dimension.dim_value    = field 1, .dim_param = field 2

type tensorInfo struct {
	name string
	dims []string
	elem int
}

type onnxGraph struct {
	inputs  []tensorInfo
	outputs []tensorInfo
}

func inspectONNX(path string) (onnxGraph, error) {
	var g onnxGraph
	data, err := os.ReadFile(path)
	if err != nil {
		return g, err
	}
	err = walkProtoFields(data, func(num, wire int, payload []byte, _ uint64) error {
		if num != 7 || wire != 2 { // ModelProto.graph
			return nil
		}
		return walkProtoFields(payload, func(gnum, gwire int, gpayload []byte, _ uint64) error {
			if gwire != 2 {
				return nil
			}
			switch gnum {
			case 11:
				ti, err := parseValueInfo(gpayload)
				if err != nil {
					return err
				}
				g.inputs = append(g.inputs, ti)
			case 12:
				ti, err := parseValueInfo(gpayload)
				if err != nil {
					return err
				}
				g.outputs = append(g.outputs, ti)
			}
			return nil
		})
	})
	return g, err
}

func parseValueInfo(b []byte) (tensorInfo, error) {
	var ti tensorInfo
	err := walkProtoFields(b, func(num, wire int, payload []byte, _ uint64) error {
		if wire != 2 {
			return nil
		}
		switch num {
		case 1:
			ti.name = string(payload)
		case 2:
			return walkProtoFields(payload, func(tnum, twire int, tpayload []byte, _ uint64) error {
				if tnum == 1 && twire == 2 { // tensor_type
					return parseTensorType(tpayload, &ti)
				}
				return nil
			})
		}
		return nil
	})
	return ti, err
}

func parseTensorType(b []byte, ti *tensorInfo) error {
	return walkProtoFields(b, func(num, wire int, payload []byte, u uint64) error {
		switch {
		case num == 1 && wire == 0:
			ti.elem = int(u)
		case num == 2 && wire == 2:
			return walkProtoFields(payload, func(snum, swire int, spayload []byte, _ uint64) error {
				if snum == 1 && swire == 2 {
					return parseProtoDimension(spayload, ti)
				}
				return nil
			})
		}
		return nil
	})
}

func parseProtoDimension(b []byte, ti *tensorInfo) error {
	dim := "?"
	return walkProtoFields(b, func(num, wire int, payload []byte, u uint64) error {
		switch {
		case num == 1 && wire == 0:
			dim = fmt.Sprintf("%d", u)
		case num == 2 && wire == 2:
			dim = string(payload)
		}
		return nil
	}, func() { ti.dims = append(ti.dims, dim) })
}

// walkProtoFields iterates protobuf fields. If done is non-nil it is called
// after the whole message has been walked (used to collect repeated dims).
func walkProtoFields(b []byte, fn func(num, wire int, payload []byte, u uint64) error, done ...func()) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return fmt.Errorf("bad protobuf key")
		}
		b = b[n:]
		num := int(key >> 3)
		wire := int(key & 7)
		switch wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return fmt.Errorf("bad protobuf varint")
			}
			b = b[n:]
			if err := fn(num, wire, nil, v); err != nil {
				return err
			}
		case 1:
			if len(b) < 8 {
				return fmt.Errorf("truncated fixed64")
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || int(l) > len(b)-n {
				return fmt.Errorf("bad protobuf length")
			}
			b = b[n:]
			payload := b[:l]
			b = b[l:]
			if err := fn(num, wire, payload, 0); err != nil {
				return err
			}
		case 5:
			if len(b) < 4 {
				return fmt.Errorf("truncated fixed32")
			}
			b = b[4:]
		default:
			return fmt.Errorf("unsupported protobuf wire type %d", wire)
		}
	}
	for _, d := range done {
		d()
	}
	return nil
}
