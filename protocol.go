package monty

import (
	"fmt"
	"math"
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
	reqAbortFeed     requestKind = 11
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
	b = append(b, fieldString(5, RuntimeVersion)...)
	if x.assertAnnotations != nil {
		b = append(b, fieldVarint(6, uint64(*x.assertAnnotations))...)
	}
	b = append(b, fieldVarint(7, uint64(x.typeCheckFormat.wire()))...)
	b = append(b, fieldBool(8, x.typeCheckColor)...)
	b = append(b, fieldVarint(9, protocolVersion)...)
	// Preserve per-line print callbacks with upstream print debouncing.
	b = append(b, fieldVarint(10, 0)...)
	return request{reqConfigure, b}
}

func encodeLimits(x ResourceLimits) []byte {
	b := []byte{}
	if x.MaxFeedDuration > 0 {
		b = append(b, fieldVarint(6, uint64(x.MaxFeedDuration/time.Microsecond))...)
	}
	if x.MaxTurnDuration > 0 {
		b = append(b, fieldVarint(7, uint64(x.MaxTurnDuration/time.Microsecond))...)
	}
	if x.MaxSuspensions > 0 {
		b = append(b, fieldVarint(5, x.MaxSuspensions)...)
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
	a := arenaEncoder{}
	for _, name := range names {
		value, err := a.add(inputs[name])
		if err != nil {
			return request{}, fmt.Errorf("input %q: %w", name, err)
		}
		named := append(fieldString(1, name), fieldVarint(2, uint64(value))...)
		b = append(b, fieldMessage(2, named)...)
	}
	b = append(b, fieldMessage(3, a.encode())...)
	b = append(b, fieldBool(4, skip)...)
	return request{reqFeed, b}, nil
}

type resumeResult struct {
	value      Value
	err        *RaisedException
	future     *uint32
	notFound   *string
	notHandled bool
}

func encodeResumeResultArena(x resumeResult, a *arenaEncoder) ([]byte, error) {
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
	v, err := a.add(x.value)
	if err != nil {
		return nil, err
	}
	return fieldVarint(1, uint64(v)), nil
}
func resumeCallRequest(callID uint32, result resumeResult) (request, error) {
	a := arenaEncoder{}
	r, err := encodeResumeResultArena(result, &a)
	if err != nil {
		return request{}, err
	}
	return request{reqResumeCall, append(fieldVarint(1, uint64(callID)), append(fieldMessage(2, r), fieldMessage(3, a.encode())...)...)}, nil
}
func resumeNameValueRequest(value Value) (request, error) {
	a := arenaEncoder{}
	v, err := a.add(value)
	if err != nil {
		return request{}, err
	}
	return request{reqResumeName, append(fieldMessage(1, a.encode()), fieldVarint(2, uint64(v))...)}, nil
}
func resumeNameUndefinedRequest() request { return request{reqResumeName, fieldMessage(3, nil)} }

type futureWireResult struct {
	callID uint32
	result resumeResult
}

func resumeFuturesRequest(results []futureWireResult) (request, error) {
	b := []byte{}
	a := arenaEncoder{}
	for _, x := range results {
		r, err := encodeResumeResultArena(x.result, &a)
		if err != nil {
			return request{}, err
		}
		item := append(fieldVarint(1, uint64(x.callID)), fieldMessage(2, r)...)
		b = append(b, fieldMessage(1, item)...)
	}
	b = append(b, fieldMessage(2, a.encode())...)
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
	maxSuspensions       *uint64
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
			case 22:
				v := f.varint
				e.maxSuspensions = &v
			case 23:
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
	Position   SourceRange
	Name       string
	Args       []Value
	Kwargs     Dict
	CallID     uint32
	MethodCall bool
	ObjectID   *[16]byte
	OS         bool
}

func decodeFunctionCall(data []byte) (callEvent, error) {
	x := callEvent{Name: decodeStringField(data, 1), Position: decodeSourceRange(decodeBytesField(data, 8))}
	values, err := messageArena(data, 7)
	if err != nil {
		return x, err
	}
	ids, err := indexes(data, 2)
	if err != nil {
		return x, err
	}
	for _, i := range ids {
		v, err := arenaRef(values, i)
		if err != nil {
			return x, err
		}
		x.Args = append(x.Args, v)
	}
	err = parseFields(data, func(f wireField) error {
		switch f.tag {
		case 3:
			d, err := arenaPairs(fieldMessage(1, f.bytes), values)
			if err != nil {
				return err
			}
			x.Kwargs = append(x.Kwargs, d...)
		case 4:
			x.CallID = uint32(f.varint)
		case 5:
			id, err := decodeUUID(f.bytes)
			if err != nil {
				return err
			}
			x.ObjectID = &id
			x.MethodCall = true
		}
		return nil
	})
	return x, err
}
func decodeOSCall(data []byte) (callEvent, error) {
	var x callEvent
	x.OS = true
	x.Position = decodeSourceRange(decodeBytesField(data, 52))
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
					values, err := messageArena(data, 50)
					if err != nil {
						return err
					}
					def, decErr = arenaRef(values, v.varint)
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
		case 25:
			var size uint64
			if err := parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					size = v.varint
				}
				return nil
			}); err != nil {
				return err
			}
			x.Name = "os.urandom"
			x.Args = []Value{size}
		case 26:
			x.Name = "time.time"
			x.Args = []Value{decodeStringField(f.bytes, 1)}
		case 27, 28, 29, 30:
			var seconds float64
			if err := parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					seconds = math.Float64frombits(v.fixed64)
				}
				return nil
			}); err != nil {
				return err
			}
			if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
				return fmt.Errorf("invalid sleep duration")
			}
			names := map[int]string{27: "time.sleep", 28: "asyncio.sleep", 29: "system.sleep", 30: "system.async_sleep"}
			x.Name = names[f.tag]
			x.Args = []Value{seconds}
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
	values, err := messageArena(data, 2)
	if err != nil {
		return nil, err
	}
	var i uint64
	err = parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			i = f.varint
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return arenaRef(values, i)
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
	ids, _ := indexes(data, tag)
	out := []uint32{}
	for _, i := range ids {
		out = append(out, uint32(i))
	}
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

func decodePrintSegments(data []byte) []printEvent {
	var out []printEvent
	_ = parseFields(data, func(f wireField) error {
		if f.tag == 1 && f.type_ == 2 {
			out = append(out, decodePrint(f.bytes))
		}
		return nil
	})
	return out
}

func decodeSourceRange(data []byte) SourceRange {
	x := SourceRange{Filename: decodeStringField(data, 1)}
	_ = parseFields(data, func(f wireField) error {
		if f.tag == 2 {
			x.Start = uint32(f.varint)
		}
		if f.tag == 3 {
			x.End = uint32(f.varint)
		}
		return nil
	})
	return x
}
