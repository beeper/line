package ltsm

import "fmt"

const (
	nativeGroupSuccess         = 1215432942
	nativeContextPointer       = 1458092
	nativeGroupDeriveAlgorithm = 2142031617
	nativeGroupHMACAlgorithm   = 403666397
	nativeGroupHMACParameter   = 105653898
	nativeGroupCipherAlgorithm = 1815715406
	nativeGroupCipherParameter = 1088723249
	nativeGroupKeyLabel        = 1413
	nativeGroupIVLabel         = 3197
	nativeSecureKeyVtable      = 7952
	nativeE2EEKeyVtable        = 5000
	nativeE2EEKeySize          = 40
)

func (rt *Runtime) unwrapGroupKey(channel uint32, encrypted []byte) (ret uint32, err error) {
	rt.imp.noteChannelState("E2EEChannel")
	m := rt.mod
	saved := m.g0
	if len(encrypted) > len(m.mem) {
		return 0, fmt.Errorf("ltsm: group key ciphertext exceeds native memory")
	}
	defer recoverError(&err, m, saved)
	frame := saved - 128
	m.g0 = frame
	clear(m.mem[frame:saved])
	derivedSlot := frame + 16
	unwrappedSlot := frame + 20
	temporary := frame + 24
	var heap, data uint32
	defer func() {
		recovered := recover()
		if recovered != nil {
			if _, ordinary := recovered.(wasmException); !ordinary {
				panic(recovered)
			}
		}
		m.g0 = frame
		for _, slot := range []uint32{derivedSlot, unwrappedSlot} {
			if ptr := m.i32Load(slot); ptr != 0 {
				m.i32Store(slot, 0)
				m.f73(ptr)
			}
		}
		m.f104(temporary)
		if heap != 0 {
			m.f132(heap)
			m.fR_0(heap)
		}
		if data != 0 {
			m.fR_0(data)
		}
		m.g0 = saved
		if recovered != nil {
			panic(recovered)
		}
	}()
	m.i32Store(frame+8, nativeGroupKeyLabel)
	m.i32Store(frame+12, 3)
	checkGroupStatus(m.f137(m.i32Load(channel+8), nativeGroupDeriveAlgorithm, frame, derivedSlot), "derive")
	prefix := rt.groupKeyPrefix(channel)
	ciphertext := make([]byte, 0, len(prefix)+len(encrypted))
	ciphertext = append(ciphertext, prefix...)
	ciphertext = append(ciphertext, encrypted...)
	data = m.f48(uint32(len(ciphertext)))
	copy(m.mem[data:data+uint32(len(ciphertext))], ciphertext)
	m.i32Store(frame+60, nativeGroupCipherParameter)
	checkGroupStatus(m.f206(m.i32Load(channel+4), data, uint32(len(ciphertext)), nativeGroupCipherAlgorithm, frame+60, m.i32Load(derivedSlot), unwrappedSlot), "unwrap")
	rt.adoptGroupKey(temporary, unwrappedSlot)
	heap = m.f48(nativeE2EEKeySize)
	clear(m.mem[heap : heap+nativeE2EEKeySize])
	m.f204(heap, temporary)
	m.i32Store(heap, nativeE2EEKeyVtable)
	m.i32Store8(heap+16, 0)
	m.i64Store(heap+24, 0)
	m.i64Store(heap+32, 0)
	ret = heap
	heap = 0
	return ret, nil
}

func (rt *Runtime) groupKeyPrefix(channel uint32) []byte {
	m := rt.mod
	saved := m.g0
	frame := saved - 32
	m.g0 = frame
	clear(m.mem[frame:saved])
	var data uint32
	defer func() {
		recovered := recover()
		if recovered != nil {
			if _, ordinary := recovered.(wasmException); !ordinary {
				panic(recovered)
			}
		}
		m.g0 = frame
		if ptr := m.i32Load(frame + 28); ptr != 0 {
			m.i32Store(frame+28, 0)
			m.f73(ptr)
		}
		if data != 0 {
			m.fR_0(data)
		}
		m.g0 = saved
		if recovered != nil {
			panic(recovered)
		}
	}()
	m.i32Store(frame+24, nativeGroupHMACParameter)
	checkGroupStatus(m.f230(m.i32Load(channel+4), nativeGroupHMACAlgorithm, frame+24, frame+28), "prefix init")
	hmac := m.i32Load(frame + 28)
	if hmac == 0 {
		panic(wasmException("ltsm: group prefix returned null"))
	}
	vtable := m.i32Load(hmac)
	checkGroupStatus(m.callIndirectT1(m.i32Load(vtable+12), hmac, m.i32Load(channel+8)), "prefix key")
	checkGroupStatus(m.callIndirectT0(m.i32Load(vtable+8), hmac, nativeGroupIVLabel, 2), "prefix update")
	checkGroupStatus(m.callIndirectT0(m.i32Load(vtable+16), hmac, 0, frame+20), "prefix size")
	size := m.i32Load(frame + 20)
	if size == 0 || size%2 != 0 || size > uint32(len(m.mem)) {
		panic(wasmException("ltsm: invalid group prefix size"))
	}
	data = m.f48(size)
	checkGroupStatus(m.callIndirectT0(m.i32Load(vtable+16), hmac, data, frame+20), "prefix digest")
	result := make([]byte, size/2)
	for i := uint32(0); i < size/2; i++ {
		result[i] = m.mem[data+i] ^ m.mem[data+i+size/2]
	}
	return result
}

func (rt *Runtime) adoptGroupKey(dst, rawSlot uint32) {
	m := rt.mod
	context := m.i32Load(nativeContextPointer)
	if context == 0 {
		panic(wasmException("ltsm: missing native group key context"))
	}
	status := m.f200(dst+4, dst+12)
	if m.i32Load(dst+4) == 0 {
		m.f135(context)
	}
	if status != 0 {
		panic(wasmException(fmt.Sprintf("ltsm: group key init failed: status %#x", status)))
	}
	if m.i32Load(dst+4) == 0 || m.i32Load(dst+12) == 0 {
		panic(wasmException("ltsm: group key init returned null owners"))
	}
	m.i32Store(dst, nativeSecureKeyVtable)
	m.i32Store(dst+8, m.i32Load(rawSlot))
	m.i32Store(rawSlot, 0)
}

func checkGroupStatus(status uint32, operation string) {
	if status != nativeGroupSuccess {
		panic(wasmException(fmt.Sprintf("ltsm: group key %s failed: status %#x", operation, status)))
	}
}
