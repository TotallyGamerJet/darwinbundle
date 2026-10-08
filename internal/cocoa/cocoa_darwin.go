//go:build darwin

// Package cocoa is the small part of the Objective-C runtime that package
// keychain needs: building NSString, NSData and NSDictionary values, reading
// bytes back out, and running work inside an autorelease pool.
//
// Nothing here uses cgo. purego resolves symbols with dlopen and dlsym and
// implements the C calling convention in assembly, so a program importing
// package keychain still builds with CGO_ENABLED=0 and still cross-compiles.
//
// Together with package keychain this is the only place in the module that
// imports purego.
package cocoa

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// The frameworks mapped before any class lookup. objc.GetClass sees only
// symbols from images that are already loaded, so this has to come first.
const (
	fwFoundation = "/System/Library/Frameworks/Foundation.framework/Foundation"
	fwSecurity   = "/System/Library/Frameworks/Security.framework/Security"
)

var (
	loadOnce sync.Once
	loadErr  error
)

// Load maps the Foundation and Security frameworks. It is safe to call from
// many goroutines and does its work once.
func Load() error {
	loadOnce.Do(func() {
		for _, path := range []string{fwFoundation, fwSecurity} {
			if _, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
				loadErr = fmt.Errorf("cocoa: loading %s: %w", path, err)
				return
			}
		}
	})
	return loadErr
}

// Selectors used often enough that caching them avoids hashing the name in the
// runtime on every message send.
var (
	selRelease        = objc.RegisterName("release")
	selAutorelease    = objc.RegisterName("autorelease")
	selStringWithUTF8 = objc.RegisterName("stringWithUTF8String:")
	selLength         = objc.RegisterName("length")
	selBytes          = objc.RegisterName("bytes")
	selDataWithBytes  = objc.RegisterName("dataWithBytes:length:")
	selAddObject      = objc.RegisterName("addObject:")
	selArrayWithCap   = objc.RegisterName("arrayWithCapacity:")
	selNumberWithBool = objc.RegisterName("numberWithBool:")
	selDictWithCap    = objc.RegisterName("dictionaryWithCapacity:")
	selSetObject      = objc.RegisterName("setObject:forKey:")
)

// Class handles, resolved on first use so that Load runs first.
var (
	classOnce     sync.Once
	clsNSString   objc.Class
	clsNSData     objc.Class
	clsNSNumber   objc.Class
	clsNSMutArray objc.Class
	clsNSMutDict  objc.Class
)

func initClasses() {
	classOnce.Do(func() {
		clsNSString = objc.GetClass("NSString")
		clsNSData = objc.GetClass("NSData")
		clsNSNumber = objc.GetClass("NSNumber")
		clsNSMutArray = objc.GetClass("NSMutableArray")
		clsNSMutDict = objc.GetClass("NSMutableDictionary")
	})
}

// String converts a Go string to an autoreleased NSString, or 0 if it cannot be
// carried: invalid UTF-8, which +stringWithUTF8String: rejects by returning nil,
// or an interior NUL, which the C string API would silently treat as the end.
func String(s string) objc.ID {
	initClasses()
	if strings.IndexByte(s, 0) >= 0 {
		return 0
	}
	// A NUL-terminated copy is required; Go strings are not NUL-terminated.
	b := make([]byte, len(s)+1)
	copy(b, s)
	id := objc.ID(clsNSString).Send(selStringWithUTF8, unsafe.Pointer(&b[0]))
	runtime.KeepAlive(b)
	return id
}

// Data wraps a Go byte slice in an autoreleased NSData. The bytes are copied, so
// the caller's slice may be reused immediately.
func Data(b []byte) objc.ID {
	initClasses()
	if len(b) == 0 {
		return objc.ID(clsNSData).Send(selDataWithBytes, unsafe.Pointer(nil), uint64(0))
	}
	id := objc.ID(clsNSData).Send(selDataWithBytes, unsafe.Pointer(&b[0]), uint64(len(b)))
	runtime.KeepAlive(b)
	return id
}

// GoBytes copies an NSData into a Go slice.
func GoBytes(id objc.ID) []byte {
	if id == 0 {
		return nil
	}
	n := objc.Send[uint64](id, selLength)
	if n == 0 {
		return nil
	}
	p := objc.Send[unsafe.Pointer](id, selBytes)
	if p == nil {
		return nil
	}
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(p), n))
	return out
}

// Bool boxes a bool as an autoreleased NSNumber.
func Bool(v bool) objc.ID {
	initClasses()
	return objc.ID(clsNSNumber).Send(selNumberWithBool, v)
}

// Array builds an autoreleased NSArray from already-bridged objects.
func Array(items []objc.ID) objc.ID {
	initClasses()
	arr := objc.ID(clsNSMutArray).Send(selArrayWithCap, uint64(len(items)))
	for _, it := range items {
		if it != 0 {
			arr.Send(selAddObject, it)
		}
	}
	return arr
}

// Dict builds an autoreleased NSMutableDictionary from alternating keys and
// values. It is toll-free bridged to the CFDictionaryRef the Security functions
// expect, so it is passed to them as is.
func Dict(pairs ...objc.ID) objc.ID {
	initClasses()
	d := objc.ID(clsNSMutDict).Send(selDictWithCap, uint64(len(pairs)/2))
	for i := 0; i+1 < len(pairs); i += 2 {
		d.Send(selSetObject, pairs[i+1], pairs[i])
	}
	return d
}

// Count returns an NSArray's length.
func Count(array objc.ID) uint64 {
	return objc.Send[uint64](array, objc.RegisterName("count"))
}

// ObjectAt returns an NSArray's element at index i.
func ObjectAt(array objc.ID, i uint64) objc.ID {
	return array.Send(objc.RegisterName("objectAtIndex:"), i)
}

// Release decrements an object's reference count.
func Release(id objc.ID) {
	if id != 0 {
		id.Send(selRelease)
	}
}

// Autorelease adds an object to the current autorelease pool.
func Autorelease(id objc.ID) objc.ID {
	if id == 0 {
		return 0
	}
	return id.Send(selAutorelease)
}

var (
	poolPush func() unsafe.Pointer
	poolPop  func(unsafe.Pointer)
	poolOnce sync.Once
)

func initPool() {
	poolOnce.Do(func() {
		purego.RegisterLibFunc(&poolPush, purego.RTLD_DEFAULT, "objc_autoreleasePoolPush")
		purego.RegisterLibFunc(&poolPop, purego.RTLD_DEFAULT, "objc_autoreleasePoolPop")
	})
}

// WithPool runs fn inside an autorelease pool. Every goroutine that touches the
// Objective-C runtime must go through this, or the objects it creates leak for
// the life of the process.
//
// The goroutine is locked to its OS thread for the duration, and that is not a
// precaution — it is required for correctness. Autorelease pools are
// thread-local: pushing returns a token that belongs to the calling thread's
// pool stack, and popping must hand it back to that same thread. A goroutine has
// no such affinity; the scheduler may move it to another thread at any
// preemption point, a function call being enough. Without the lock a pool can
// be pushed on one thread and popped on another, which corrupts the other
// thread's pool stack and surfaces later, somewhere unrelated, as an
// over-released object or an "Attempt to use unknown class" from the runtime.
//
// LockOSThread nests, so nested calls behave correctly.
func WithPool(fn func()) {
	initPool()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	p := poolPush()
	defer poolPop(p)
	fn()
}
