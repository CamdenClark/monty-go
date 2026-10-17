package monty

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestCheckoutRejectsInvalidOptionsBeforeAcquiringWorker(t *testing.T) {
	fixed := time.Now()
	negative := -2 * time.Second
	cap := time.Second
	for name, option := range map[string]CheckoutOptions{
		"negative flush":          {PrintFlushInterval: -time.Second},
		"large flush":             {PrintFlushInterval: (time.Duration(math.MaxUint32) + 1) * time.Millisecond},
		"negative sleep budget":   {Limits: ResourceLimits{MaxTotalSleep: -time.Second}},
		"datetime":                {OSPolicy: OSPolicy{DateTime: "invalid"}},
		"fixed datetime conflict": {OSPolicy: OSPolicy{DateTime: DateTimeSystem, FixedDateTime: &fixed}},
		"timezone conflict":       {OSPolicy: OSPolicy{TimeZone: "utc", FixedTimeZone: &TimeZone{}}},
		"timezone offset":         {OSPolicy: OSPolicy{FixedTimeZone: &TimeZone{OffsetSeconds: 86400}}},
		"sleep mode":              {OSPolicy: OSPolicy{Sleep: "invalid"}},
		"sleep cap":               {OSPolicy: OSPolicy{SleepSystemMax: &negative}},
		"sleep cap mode":          {OSPolicy: OSPolicy{Sleep: SleepZero, SleepSystemMax: &cap}},
		"random mode":             {OSPolicy: OSPolicy{Random: "invalid"}},
		"random conflict":         {OSPolicy: OSPolicy{Random: RandomSystem, RandomSeed: 0}},
		"random nan":              {OSPolicy: OSPolicy{RandomSeed: math.NaN()}},
		"random infinity":         {OSPolicy: OSPolicy{RandomSeed: math.Inf(1)}},
		"random type":             {OSPolicy: OSPolicy{RandomSeed: true}},
		"process clock":           {OSPolicy: OSPolicy{ProcessTime: "invalid"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (&Monty{}).Checkout(context.Background(), option); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}

func TestOSPolicyUnlimitedSleep(t *testing.T) {
	cap := UnlimitedSleep
	wire, hostCap, err := encodeOSPolicy(OSPolicy{SleepSystemMax: &cap})
	if err != nil || hostCap != UnlimitedSleep {
		t.Fatalf("unlimited cap: %v, %v", hostCap, err)
	}
	system := decodeBytesField(decodeBytesField(wire, 5), 1)
	var max uint64
	if err := parseFields(system, func(f wireField) error { max = f.varint; return nil }); err != nil || max != math.MaxUint64 {
		t.Fatalf("unlimited wire cap: %d, %v", max, err)
	}
}

func TestLegacySnapshotWithoutSleepUsage(t *testing.T) {
	legacy := append([]byte(snapshotMagic), fieldBytes(1, []byte("state"))...)
	legacy = append(legacy, fieldString(2, RuntimeVersion)...)
	legacy = append(legacy, fieldVarint(3, 0)...)
	legacy = append(legacy, fieldVarint(4, 2)...)
	_, meta, err := decodeSnapshot(legacy)
	if err != nil || meta.hasSleepUsage || meta.sleepUsed != 0 || meta.suspensions != 2 {
		t.Fatalf("legacy snapshot: %#v, %v", meta, err)
	}
	bad := append(encodeSnapshot([]byte("state"), snapshotMetadata{}), fieldVarint(5, math.MaxUint64)...)
	if _, _, err := decodeSnapshot(bad); err == nil {
		t.Fatal("duplicate sleep metadata accepted")
	}
	if _, _, err := decodeSnapshot(append(legacy, fieldVarint(5, math.MaxUint64)...)); err == nil {
		t.Fatal("overflowing sleep usage accepted")
	}
}
