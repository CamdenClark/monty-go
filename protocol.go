package monty

import (
	"fmt"
	"sort"
	"time"
)

type requestKind int

const (
	reqConfigure     requestKind = 1
	reqInstall       requestKind = 2
	reqFeed          requestKind = 3
	reqResumeCall    requestKind = 4
	reqResumeName    requestKind = 5
	reqResumeFutures requestKind = 6
	reqDump          requestKind = 7
	reqLoad          requestKind = 8
	reqReset         requestKind = 9
	reqShutdown      requestKind = 10
)

type request struct {
	kind requestKind
	body []byte
}

func (r request) encode() []byte { return fieldMessage(int(r.kind), r.body) }

type configureWire struct {
	scriptName        string
	limits            ResourceLimits
	typeCheck         bool
	typeCheckStubs    *string
	typeCheckFormat   TypeCheckFormat
	typeCheckColor    bool
	assertAnnotations *uint32
}

func configureRequest(x configureWire) request {
	b := fieldString(1, x.scriptName)
	if limits := encodeLimits(x.limits); len(limits) > 0 {
		b = append(b, fieldMessage(2, limits)...)
	}
	b = append(b, fieldBool(3, x.typeCheck)...)
	if x.typeCheckStubs != nil {
		b = append(b, fieldString(4, *x.typeCheckStubs)...)
	}
	b = append(b, fieldString(5, Version)...)
	if x.assertAnnotations != nil {
		b = append(b, fieldVarint(6, uint64(*x.assertAnnotations))...)
	}
	b = append(b, fieldVarint(7, uint64(x.typeCheckFormat.wire()))...)
	b = append(b, fieldBool(8, x.typeCheckColor)...)
	b = append(b, fieldVarint(9, protocolVersion)...)
	return request{reqConfigure, b}
}

func encodeLimits(x ResourceLimits) []byte {
	b := []byte{}
	if x.MaxDuration > 0 {
		b = append(b, fieldVarint(1, uint64(x.MaxDuration/time.Microsecond))...)
	}
	if x.MaxMemory > 0 {
		b = append(b, fieldVarint(2, x.MaxMemory)...)
	}
	if x.GCInterval > 0 {
		b = append(b, fieldVarint(3, x.GCInterval)...)
	}
	if x.MaxRecursionDepth > 0 {
		b = append(b, fieldVarint(4, x.MaxRecursionDepth)...)
	}
	return b
}

func feedRequest(code string, inputs map[string]Value, skip bool) (request, error) {
	b := fieldString(1, code)
	names := make([]string, 0, len(inputs))
	for k := range inputs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		value, err := encodeValue(inputs[name])
		if err != nil {
			return request{}, fmt.Errorf("input %q: %w", name, err)
		}
		named := append(fieldString(1, name), fieldMessage(2, value)...)
		b = append(b, fieldMessage(2, named)...)
	}
	b = append(b, fieldBool(3, skip)...)
	return request{reqFeed, b}, nil
}

type resumeResult struct {
	value      Value
	err        *RaisedException
	future     *uint32
	notFound   *string
	notHandled bool
}

func encodeResumeResult(x resumeResult) ([]byte, error) {
	if x.err != nil {
		return fieldMessage(2, encodeRaised(*x.err)), nil
	}
	if x.future != nil {
		return fieldVarint(3, uint64(*x.future)), nil
	}
	if x.notFound != nil {
		return fieldString(4, *x.notFound), nil
	}
	if x.notHandled {
		return fieldMessage(5, nil), nil
	}
	v, err := encodeValue(x.value)
	if err != nil {
		return nil, err
	}
	return fieldMessage(1, v), nil
}
func resumeCallRequest(callID uint32, result resumeResult) (request, error) {
	r, err := encodeResumeResult(result)
	if err != nil {
		return request{}, err
	}
	return request{reqResumeCall, append(fieldVarint(1, uint64(callID)), fieldMessage(2, r)...)}, nil
}
func resumeNameValueRequest(value Value) (request, error) {
	v, err := encodeValue(value)
	if err != nil {
		return request{}, err
	}
	return request{reqResumeName, fieldMessage(1, v)}, nil
}
func resumeNameUndefinedRequest() request { return request{reqResumeName, fieldMessage(2, nil)} }

type futureWireResult struct {
	callID uint32
	result resumeResult
}

func resumeFuturesRequest(results []futureWireResult) (request, error) {
	b := []byte{}
	for _, x := range results {
		r, err := encodeResumeResult(x.result)
		if err != nil {
			return request{}, err
		}
		item := append(fieldVarint(1, uint64(x.callID)), fieldMessage(2, r)...)
		b = append(b, fieldMessage(1, item)...)
	}
	return request{reqResumeFutures, b}, nil
}
func encodeRaised(x RaisedException) []byte {
	b := fieldString(1, x.Type)
	if x.Message != "" {
		b = append(b, fieldString(2, x.Message)...)
	}
	for _, f := range x.Frames {
		sf := fieldString(1, f.Filename)
		sf = append(sf, fieldMessage(2, append(fieldVarint(1, uint64(f.Line)), fieldVarint(2, uint64(f.Column))...))...)
		sf = append(sf, fieldMessage(3, append(fieldVarint(1, uint64(f.EndLine)), fieldVarint(2, uint64(f.EndColumn))...))...)
		if f.FrameName != "" {
			sf = append(sf, fieldString(4, f.FrameName)...)
		}
		if f.PreviewLine != "" {
			sf = append(sf, fieldString(5, f.PreviewLine)...)
		}
		sf = append(sf, fieldBool(6, f.HideCaret)...)
		sf = append(sf, fieldBool(7, f.HideFrameName)...)
		b = append(b, fieldMessage(3, sf)...)
	}
	return b
}

type eventKind int

const (
	eventPrint          eventKind = 1
	eventFunctionCall   eventKind = 2
	eventOSCall         eventKind = 3
	eventNameLookup     eventKind = 4
	eventResolveFutures eventKind = 5
	eventComplete       eventKind = 6
	eventError          eventKind = 7
	eventTypingError    eventKind = 8
	eventDump           eventKind = 9
	eventOK             eventKind = 10
	eventFatal          eventKind = 11
	eventShutdownDump   eventKind = 12
)

type childEvent struct {
	kind                 eventKind
	body                 []byte
	totalExecutionMicros uint64
	maxDurationMicros    *uint64
	restoredScriptName   *string
}

func decodeEvent(data []byte) (childEvent, error) {
	var e childEvent
	err := parseFields(data, func(f wireField) error {
		if f.tag >= 1 && f.tag <= 12 {
			if e.kind != 0 {
				return fmt.Errorf("child event has multiple kinds")
			}
			e.kind = eventKind(f.tag)
			e.body = f.bytes
		} else {
			switch f.tag {
			case 20:
				e.totalExecutionMicros = f.varint
			case 21:
				v := f.varint
				e.maxDurationMicros = &v
			case 22:
				v := string(f.bytes)
				e.restoredScriptName = &v
			}
		}
		return nil
	})
	if err != nil {
		return e, err
	}
	if e.kind == 0 {
		return e, fmt.Errorf("child event has no kind")
	}
	return e, nil
}

type printEvent struct {
	Stream PrintStream
	Text   string
}

func decodePrint(data []byte) printEvent {
	var x printEvent
	_ = parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			x.Stream = PrintStream(f.varint)
		}
		if f.tag == 2 {
			x.Text = string(f.bytes)
		}
		return nil
	})
	return x
}

type callEvent struct {
	Name       string
	Args       []Value
	Kwargs     Dict
	CallID     uint32
	MethodCall bool
	OS         bool
}

func decodeFunctionCall(data []byte) (callEvent, error) {
	var x callEvent
	err := parseFields(data, func(f wireField) error {
		switch f.tag {
		case 1:
			x.Name = string(f.bytes)
		case 2:
			v, e := decodeValue(f.bytes)
			if e != nil {
				return e
			}
			x.Args = append(x.Args, v)
		case 3:
			var p Pair
			if e := parseFields(f.bytes, func(k wireField) error {
				v, e := decodeValue(k.bytes)
				if e != nil {
					return e
				}
				if k.tag == 1 {
					p.Key = v
				} else if k.tag == 2 {
					p.Value = v
				}
				return nil
			}); e != nil {
				return e
			}
			x.Kwargs = append(x.Kwargs, p)
		case 4:
			x.CallID = uint32(f.varint)
		case 5:
			x.MethodCall = f.varint != 0
		}
		return nil
	})
	return x, err
}
func decodeOSCall(data []byte) (callEvent, error) {
	var x callEvent
	x.OS = true
	err := parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			x.CallID = uint32(f.varint)
			return nil
		}
		switch f.tag {
		case 2:
			x.Name = "Path.exists"
			x.Args = []Value{string(f.bytes)}
		case 3:
			x.Name = "Path.is_file"
			x.Args = []Value{string(f.bytes)}
		case 4:
			x.Name = "Path.is_dir"
			x.Args = []Value{string(f.bytes)}
		case 5:
			x.Name = "Path.is_symlink"
			x.Args = []Value{string(f.bytes)}
		case 6:
			x.Name = "Path.read_text"
			x.Args = []Value{string(f.bytes)}
		case 7:
			x.Name = "Path.read_bytes"
			x.Args = []Value{string(f.bytes)}
		case 8:
			x.Name = "Path.stat"
			x.Args = []Value{string(f.bytes)}
		case 9:
			x.Name = "Path.iterdir"
			x.Args = []Value{string(f.bytes)}
		case 10:
			x.Name = "Path.resolve"
			x.Args = []Value{string(f.bytes)}
		case 11:
			x.Name = "Path.absolute"
			x.Args = []Value{string(f.bytes)}
		case 12:
			x.Name = "Path.unlink"
			x.Args = []Value{string(f.bytes)}
		case 13:
			x.Name = "Path.rmdir"
			x.Args = []Value{string(f.bytes)}
		case 14, 15:
			var p, d string
			_ = parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					p = string(v.bytes)
				}
				if v.tag == 2 {
					d = string(v.bytes)
				}
				return nil
			})
			if f.tag == 14 {
				x.Name = "Path.write_text"
			} else {
				x.Name = "Path.append_text"
			}
			x.Args = []Value{p, d}
		case 16, 17:
			var p string
			var d []byte
			_ = parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					p = string(v.bytes)
				}
				if v.tag == 2 {
					d = append([]byte(nil), v.bytes...)
				}
				return nil
			})
			if f.tag == 16 {
				x.Name = "Path.write_bytes"
			} else {
				x.Name = "Path.append_bytes"
			}
			x.Args = []Value{p, d}
		case 18:
			var p, m string
			_ = parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					p = string(v.bytes)
				}
				if v.tag == 2 {
					m = string(v.bytes)
				}
				return nil
			})
			x.Name = "open"
			x.Args = []Value{p, m}
		case 19:
			var p string
			var parents, exist bool
			_ = parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					p = string(v.bytes)
				}
				if v.tag == 2 {
					parents = v.varint != 0
				}
				if v.tag == 3 {
					exist = v.varint != 0
				}
				return nil
			})
			x.Name = "Path.mkdir"
			x.Args = []Value{p, parents, exist}
		case 20:
			var a, b string
			_ = parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					a = string(v.bytes)
				}
				if v.tag == 2 {
					b = string(v.bytes)
				}
				return nil
			})
			x.Name = "Path.rename"
			x.Args = []Value{a, b}
		case 21:
			var key string
			var def Value
			var decErr error
			_ = parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					key = string(v.bytes)
				}
				if v.tag == 2 {
					def, decErr = decodeValue(v.bytes)
				}
				return decErr
			})
			if decErr != nil {
				return decErr
			}
			x.Name = "os.getenv"
			x.Args = []Value{key, def}
		case 22:
			x.Name = "os.environ"
		case 23:
			x.Name = "date.today"
		case 24:
			var tz Value = nil
			_ = parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					tz = decodeTimeZone(v.bytes)
				}
				return nil
			})
			x.Name = "datetime.now"
			x.Args = []Value{tz}
		}
		return nil
	})
	return x, err
}

func decodeComplete(data []byte) (Value, error) {
	var value Value
	found := false
	err := parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			v, e := decodeValue(f.bytes)
			if e != nil {
				return e
			}
			value = v
			found = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("complete event missing value")
	}
	return value, nil
}
func decodeRaised(data []byte) (RaisedException, error) {
	var x RaisedException
	err := parseFields(data, func(f wireField) error {
		switch f.tag {
		case 1:
			x.Type = string(f.bytes)
		case 2:
			x.Message = string(f.bytes)
		case 3:
			var sf StackFrame
			if e := parseFields(f.bytes, func(v wireField) error {
				switch v.tag {
				case 1:
					sf.Filename = string(v.bytes)
				case 2:
					_ = parseFields(v.bytes, func(l wireField) error {
						if l.tag == 1 {
							sf.Line = uint32(l.varint)
						}
						if l.tag == 2 {
							sf.Column = uint32(l.varint)
						}
						return nil
					})
				case 3:
					_ = parseFields(v.bytes, func(l wireField) error {
						if l.tag == 1 {
							sf.EndLine = uint32(l.varint)
						}
						if l.tag == 2 {
							sf.EndColumn = uint32(l.varint)
						}
						return nil
					})
				case 4:
					sf.FrameName = string(v.bytes)
				case 5:
					sf.PreviewLine = string(v.bytes)
				case 6:
					sf.HideCaret = v.varint != 0
				case 7:
					sf.HideFrameName = v.varint != 0
				}
				return nil
			}); e != nil {
				return e
			}
			x.Frames = append(x.Frames, sf)
		}
		return nil
	})
	return x, err
}
func decodeErrorEvent(data []byte) (RaisedException, error) {
	var x RaisedException
	found := false
	err := parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			v, e := decodeRaised(f.bytes)
			if e != nil {
				return e
			}
			x = v
			found = true
		}
		return nil
	})
	if err == nil && !found {
		err = fmt.Errorf("error event missing exception")
	}
	return x, err
}
func decodeStringField(data []byte, tag int) string {
	var s string
	_ = parseFields(data, func(f wireField) error {
		if f.tag == tag {
			s = string(f.bytes)
		}
		return nil
	})
	return s
}
func decodeUint32List(data []byte, tag int) []uint32 {
	var out []uint32
	_ = parseFields(data, func(f wireField) error {
		if f.tag == tag {
			out = append(out, uint32(f.varint))
		}
		return nil
	})
	return out
}
func decodeBytesField(data []byte, tag int) []byte {
	var out []byte
	_ = parseFields(data, func(f wireField) error {
		if f.tag == tag {
			out = append([]byte(nil), f.bytes...)
		}
		return nil
	})
	return out
}
