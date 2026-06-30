package ltsm

import (
	"testing"
)

func TestDebugMemoryStrings(t *testing.T) {
	// Check strings at addresses used in embind registrations
	addrs := []uint32{
		// From Import_i (class registration) first call
		4724, 4752, 4796, 7096, 8180, 4812, 1403,
		// From Import_g (static method registration) calls
		2480, 1374, 4824, 4828, 4836,
		// More method names
		1416, 1427, 1437, 1448, 1461, 1480, 1501,
		2541, 2558, 2684, 2685, 2697,
		// Module property name at 2304
		2304,
		// Error string at 1212
		1212,
	}
	for _, addr := range addrs {
		if int(addr) < len(memInit) {
			var s []byte
			for i := addr; i < uint32(len(memInit)) && i < addr+100; i++ {
				if memInit[i] == 0 {
					break
				}
				s = append(s, memInit[i])
			}
			t.Logf("addr %5d: %q", addr, string(s))
		}
	}
}

func TestDebugStaticMethodRegistrations(t *testing.T) {
	_, imp := initModule(t)

	// Log all static method invoker details
	for name, ci := range imp.classByName {
		for mname, mi := range ci.StaticMethods {
			t.Logf("%s.%s: invokerIdx=%d, fnIdx=%d, argCount=%d, argTypes=%v",
				name, mname, mi.InvokerIdx, mi.Context, mi.ArgCount, mi.ArgTypes)
		}
		for mname, mi := range ci.Methods {
			t.Logf("%s.%s (instance): invokerIdx=%d, context=%d, argCount=%d, argTypes=%v",
				name, mname, mi.InvokerIdx, mi.Context, mi.ArgCount, mi.ArgTypes)
		}
		for i, ctor := range ci.Constructors {
			t.Logf("%s.ctor[%d]: invokerIdx=%d, rawCtorIdx=%d, argCount=%d, argTypes=%v",
				name, i, ctor.InvokerIdx, ctor.RawCtorIdx, ctor.ArgCount, ctor.ArgTypes)
		}
	}
}
