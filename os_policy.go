package monty

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
	"time"
)

// DateTimeSource selects the clock used by date.today, datetime.now and time.time.
type DateTimeSource string

const (
	DateTimeSystem   DateTimeSource = "system"
	DateTimeCallHost DateTimeSource = "call_host"
)

// SleepMode selects how time.sleep and asyncio.sleep are serviced.
type SleepMode string

const (
	SleepSystem   SleepMode = "system"
	SleepCallHost SleepMode = "call_host"
	SleepZero     SleepMode = "zero"
)

// RandomSource selects the initial source of random's state.
type RandomSource string

const (
	RandomSystem   RandomSource = "system"
	RandomCallHost RandomSource = "call_host"
)

// ProcessTimeMode selects what time.process_time and time.thread_time report.
type ProcessTimeMode string

const (
	ProcessTimeZero    ProcessTimeMode = "zero"
	ProcessTimeElapsed ProcessTimeMode = "elapsed"
)

// UnlimitedSleep disables the cap on one system sleep. Use it through
// OSPolicy.SleepSystemMax; MaxTotalSleep and context cancellation still apply.
const UnlimitedSleep time.Duration = -1

// OSPolicy configures a session's clocks, time zone, sleeps and initial random
// state. Zero values keep Monty's defaults: system time, UTC, system sleeps
// capped at ten seconds, OS entropy and a zero process clock.
type OSPolicy struct {
	DateTime DateTimeSource
	// FixedDateTime freezes the clock and cannot be combined with DateTime.
	FixedDateTime *time.Time
	// TimeZone is "utc" or an IANA name. FixedTimeZone is an alternative offset.
	TimeZone      string
	FixedTimeZone *TimeZone
	Sleep         SleepMode
	// SleepSystemMax overrides the per-call cap, including zero for no waiting.
	// It only applies to system sleeps. UnlimitedSleep disables the cap.
	SleepSystemMax *time.Duration
	Random         RandomSource
	// RandomSeed replaces Random with an explicit Python seed: a Go integer,
	// *big.Int, finite float, string or []byte. Zero and empty seeds are valid.
	RandomSeed  any
	ProcessTime ProcessTimeMode
}

func encodeOSPolicy(x OSPolicy) ([]byte, time.Duration, error) {
	var b []byte
	cap := 10 * time.Second
	if x.FixedDateTime != nil {
		if x.DateTime != "" {
			return nil, 0, fmt.Errorf("OSPolicy.FixedDateTime cannot be combined with DateTime")
		}
		fixed := append(fieldVarint(1, uint64(x.FixedDateTime.Unix())), fieldVarint(2, uint64(x.FixedDateTime.Nanosecond()/1000))...)
		b = append(b, fieldMessage(3, fixed)...)
	} else {
		switch x.DateTime {
		case "", DateTimeSystem:
		case DateTimeCallHost:
			b = append(b, fieldMessage(2, nil)...)
		default:
			return nil, 0, fmt.Errorf("unknown OSPolicy.DateTime %q", x.DateTime)
		}
	}
	if x.FixedTimeZone != nil {
		if x.TimeZone != "" {
			return nil, 0, fmt.Errorf("OSPolicy.FixedTimeZone cannot be combined with TimeZone")
		}
		if x.FixedTimeZone.OffsetSeconds < -86399 || x.FixedTimeZone.OffsetSeconds > 86399 {
			return nil, 0, fmt.Errorf("OSPolicy.FixedTimeZone offset must be within 86399 seconds of UTC")
		}
		b = append(b, fieldMessage(4, fieldMessage(3, encodeTimeZone(*x.FixedTimeZone)))...)
	} else if x.TimeZone != "" {
		zone := fieldMessage(1, nil)
		if x.TimeZone != "utc" {
			zone = fieldString(2, x.TimeZone)
		}
		b = append(b, fieldMessage(4, zone)...)
	}
	var sleep []byte
	switch x.Sleep {
	case "", SleepSystem:
		if x.SleepSystemMax != nil {
			cap = *x.SleepSystemMax
			if cap < 0 && cap != UnlimitedSleep {
				return nil, 0, fmt.Errorf("OSPolicy.SleepSystemMax must be non-negative or UnlimitedSleep")
			}
			micros := uint64(math.MaxUint64)
			if cap != UnlimitedSleep {
				cap = cap.Truncate(time.Microsecond)
				micros = uint64(cap / time.Microsecond)
			}
			sleep = fieldMessage(1, fieldVarint(1, micros))
		}
	case SleepCallHost, SleepZero:
		if x.SleepSystemMax != nil {
			return nil, 0, fmt.Errorf("OSPolicy.SleepSystemMax only applies to system sleeps")
		}
		tag := 2
		if x.Sleep == SleepZero {
			tag = 3
		}
		sleep = fieldMessage(tag, nil)
	default:
		return nil, 0, fmt.Errorf("unknown OSPolicy.Sleep %q", x.Sleep)
	}
	if sleep != nil {
		b = append(b, fieldMessage(5, sleep)...)
	}
	if x.RandomSeed != nil {
		if x.Random != "" {
			return nil, 0, fmt.Errorf("OSPolicy.RandomSeed cannot be combined with Random")
		}
		seed, err := encodeRandomSeed(x.RandomSeed)
		if err != nil {
			return nil, 0, err
		}
		b = append(b, fieldMessage(8, seed)...)
	} else {
		switch x.Random {
		case "", RandomSystem:
		case RandomCallHost:
			b = append(b, fieldMessage(7, nil)...)
		default:
			return nil, 0, fmt.Errorf("unknown OSPolicy.Random %q", x.Random)
		}
	}
	switch x.ProcessTime {
	case "", ProcessTimeZero:
	case ProcessTimeElapsed:
		b = append(b, fieldMessage(10, nil)...)
	default:
		return nil, 0, fmt.Errorf("unknown OSPolicy.ProcessTime %q", x.ProcessTime)
	}
	return b, cap, nil
}

func encodeRandomSeed(value any) ([]byte, error) {
	var integer *big.Int
	switch x := value.(type) {
	case *big.Int:
		if x == nil {
			return nil, fmt.Errorf("OSPolicy.RandomSeed cannot be a nil *big.Int")
		}
		integer = x
	case big.Int:
		integer = &x
	case []byte:
		return fieldBytes(4, x), nil
	default:
		v := reflect.ValueOf(value)
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			integer = big.NewInt(v.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			integer = new(big.Int).SetUint64(v.Uint())
		case reflect.Float32, reflect.Float64:
			f := v.Float()
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, fmt.Errorf("OSPolicy.RandomSeed float must be finite")
			}
			return fieldFixed64(2, math.Float64bits(f)), nil
		case reflect.String:
			return fieldString(3, v.String()), nil
		default:
			return nil, fmt.Errorf("unsupported OSPolicy.RandomSeed type %T", value)
		}
	}
	// RandomSeed integers use signed, little-endian two's complement, unlike
	// value-arena big integers, which use a sign and big-endian magnitude.
	size := (integer.BitLen() + 8) / 8
	if size == 0 {
		size = 1
	}
	z := new(big.Int).Set(integer)
	if z.Sign() < 0 {
		z.Add(z, new(big.Int).Lsh(big.NewInt(1), uint(size*8)))
	}
	bytes := make([]byte, size)
	z.FillBytes(bytes)
	for i, j := 0, len(bytes)-1; i < j; i, j = i+1, j-1 {
		bytes[i], bytes[j] = bytes[j], bytes[i]
	}
	return fieldBytes(1, bytes), nil
}
