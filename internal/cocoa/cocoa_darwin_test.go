//go:build darwin

package cocoa

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ebitengine/purego/cstrings"
	"github.com/ebitengine/purego/objc"
)

func TestMain(m *testing.M) {
	if err := Load(); err != nil {
		panic(err)
	}
	m.Run()
}

func TestStringRoundTrips(t *testing.T) {
	WithPool(func() {
		for _, s := range []string{"", "plain", "é日本😀", strings.Repeat("x", 10_000)} {
			if got := cstrings.NSStringToString(String(s)); got != s {
				t.Errorf("round trip of %q gave %q", s[:min(len(s), 20)], got[:min(len(got), 20)])
			}
		}
	})
}

// A string NSString cannot carry yields 0, not a truncated or mangled value.
// An interior NUL would otherwise end the C string early without a word.
func TestStringRefusesWhatItCannotCarry(t *testing.T) {
	WithPool(func() {
		for name, s := range map[string]string{"a NUL": "a\x00b", "invalid UTF-8": "a\xffb"} {
			if id := String(s); id != 0 {
				t.Errorf("%s produced %v", name, id)
			}
		}
	})
}

func TestDataRoundTrips(t *testing.T) {
	WithPool(func() {
		for _, b := range [][]byte{nil, {}, {0}, []byte("hello"), bytes.Repeat([]byte{0xAB, 0}, 5000)} {
			got := GoBytes(Data(b))
			if len(b) == 0 {
				if len(got) != 0 {
					t.Errorf("empty data came back as %d bytes", len(got))
				}
				continue
			}
			if !bytes.Equal(got, b) {
				t.Errorf("round trip of %d bytes gave %d", len(b), len(got))
			}
		}
		if GoBytes(0) != nil {
			t.Error("GoBytes of nil was not nil")
		}
	})
}

func TestDataIsCopied(t *testing.T) {
	WithPool(func() {
		b := []byte("original")
		d := Data(b)
		copy(b, "mutated!")
		if got := string(GoBytes(d)); got != "original" {
			t.Errorf("the NSData follows the Go slice: %q", got)
		}
	})
}

func TestArrayAndDict(t *testing.T) {
	WithPool(func() {
		arr := Array([]objc.ID{String("a"), 0, String("b")}) // nil entries are skipped
		if n := Count(arr); n != 2 {
			t.Fatalf("Count = %d, want 2", n)
		}
		if got := cstrings.NSStringToString(ObjectAt(arr, 1)); got != "b" {
			t.Errorf("ObjectAt(1) = %q", got)
		}

		d := Dict(String("k1"), String("v1"), String("k2"), Bool(true))
		if n := objc.Send[uint64](d, objc.RegisterName("count")); n != 2 {
			t.Errorf("the dictionary holds %d entries, want 2", n)
		}
		v := d.Send(objc.RegisterName("objectForKey:"), String("k1"))
		if got := cstrings.NSStringToString(v); got != "v1" {
			t.Errorf("k1 = %q", got)
		}
	})
}

// Pools nest, and many goroutines can use their own at once. This is the case
// the thread-locking in WithPool exists for; run under -race.
func TestWithPoolNestsAndIsSafeAcrossGoroutines(t *testing.T) {
	done := make(chan struct{})
	for range 16 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 200 {
				WithPool(func() {
					WithPool(func() { _ = String("inner") })
					_ = Data([]byte("outer"))
				})
			}
		}()
	}
	for range 16 {
		<-done
	}
}

func TestReleaseAndAutoreleaseTolerateNil(t *testing.T) {
	Release(0)
	if Autorelease(0) != 0 {
		t.Error("Autorelease(0) was not 0")
	}
}
