// embind.go implements the Emscripten embind host functions for the transpiled Module.
// It captures type/class registrations during module initialization (fP + fT)
// and provides method invocation for calling embind-wrapped C++ class methods.
package ltsm

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ErrAbort identifies a fatal abort from the transpiled LTSM runtime.
var ErrAbort = errors.New("ltsm: WASM abort called")

// TypeInfo describes a registered embind type.
type TypeInfo struct {
	RawType uint32
	Name    string
	Kind    string // "void","bool","int","float","bigint","string","wstring","emval","memory_view","class","class_ptr"
	Size    int
}

// ClassInfo describes a registered embind class.
type ClassInfo struct {
	Name            string
	RawType         uint32
	RawPtrType      uint32
	RawConstPtrType uint32
	BaseClassRaw    uint32
	DestructorIdx   uint32
	Constructors    []*CtorInfo
	Methods         map[string]*MethodInfo
	StaticMethods   map[string]*MethodInfo
}

// CtorInfo describes a class constructor.
type CtorInfo struct {
	ArgCount   int
	ArgTypes   []uint32 // [returnType, argType1, argType2, ...]
	InvokerIdx uint32
	RawCtorIdx uint32
}

// MethodInfo describes a class method (instance or static).
type MethodInfo struct {
	Name       string
	ArgCount   int
	ArgTypes   []uint32 // [returnType, thisType, argType1, ...]
	InvokerIdx uint32
	Context    uint32
}

// EmvalTable is a handle table for passing Go values through WASM.
type EmvalTable struct {
	mu      sync.Mutex
	values  []any
	refCnts []int
	free    []uint32
}

func newEmvalTable() *EmvalTable {
	t := &EmvalTable{
		values:  make([]any, 5),
		refCnts: make([]int, 5),
	}
	// Reserved handles
	t.values[0] = struct{}{}       // undefined
	t.values[1] = nil              // null
	t.values[2] = true             // true
	t.values[3] = false            // false
	t.values[4] = map[string]any{} // empty object
	t.refCnts[0] = 1
	t.refCnts[1] = 1
	t.refCnts[2] = 1
	t.refCnts[3] = 1
	t.refCnts[4] = 1
	return t
}

func (t *EmvalTable) ToHandle(val any) uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var idx uint32
	if len(t.free) > 0 {
		idx = t.free[len(t.free)-1]
		t.free = t.free[:len(t.free)-1]
		t.values[idx] = val
		t.refCnts[idx] = 1
	} else {
		idx = uint32(len(t.values))
		t.values = append(t.values, val)
		t.refCnts = append(t.refCnts, 1)
	}
	return idx
}

func (t *EmvalTable) ToValue(handle uint32) any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if int(handle) >= len(t.values) {
		return nil
	}
	return t.values[handle]
}

func (t *EmvalTable) SetValue(handle uint32, val any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if int(handle) < len(t.values) {
		t.values[handle] = val
	}
}

func (t *EmvalTable) IncRef(handle uint32) {
	if handle < 5 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if int(handle) < len(t.refCnts) {
		t.refCnts[handle]++
	}
}

func (t *EmvalTable) DecRef(handle uint32) {
	if handle < 5 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if int(handle) < len(t.refCnts) {
		t.refCnts[handle]--
		if t.refCnts[handle] <= 0 {
			t.values[handle] = nil
			t.refCnts[handle] = 0
			t.free = append(t.free, handle)
		}
	}
}

// Imports implements ModuleImports for the transpiled WASM Module.
// It handles embind registrations and provides the runtime environment.
type Imports struct {
	mod         *Module
	types       map[uint32]*TypeInfo
	classes     map[uint32]*ClassInfo
	classByName map[string]*ClassInfo
	emval       *EmvalTable

	// File descriptor state for /dev/urandom emulation
	urandomFD uint32
	nextFD    uint32
}

// NewImports creates a new Imports instance bound to the given Module.
func NewImports() *Imports {
	return &Imports{
		types:       make(map[uint32]*TypeInfo),
		classes:     make(map[uint32]*ClassInfo),
		classByName: make(map[string]*ClassInfo),
		emval:       newEmvalTable(),
		nextFD:      42,
	}
}

// SetModule sets the Module reference (must be called before running).
func (imp *Imports) SetModule(m *Module) {
	imp.mod = m
}

// readCStr reads a null-terminated C string from module memory.
func (imp *Imports) readCStr(ptr uint32) string {
	mem := imp.mod.mem
	var buf []byte
	for i := ptr; i < uint32(len(mem)); i++ {
		if mem[i] == 0 {
			break
		}
		buf = append(buf, mem[i])
	}
	return string(buf)
}

// malloc allocates memory using the module's V export (malloc).
func (imp *Imports) malloc(size uint32) uint32 {
	return imp.mod.fV(size)
}

// free frees memory using the module's R export (free).
func (imp *Imports) free(ptr uint32) {
	imp.mod.fR(ptr)
}

// writeStdString writes a Go string as Emscripten std::string in WASM memory.
// Format: [u32 length][data bytes][null terminator]
func (imp *Imports) writeStdString(s string) uint32 {
	data := []byte(s)
	size := uint32(4 + len(data) + 1)
	ptr := imp.malloc(size)
	binary.LittleEndian.PutUint32(imp.mod.mem[ptr:], uint32(len(data)))
	copy(imp.mod.mem[ptr+4:], data)
	imp.mod.mem[ptr+4+uint32(len(data))] = 0
	return ptr
}

// readU32 reads a uint32 from module memory.
func (imp *Imports) readU32(ptr uint32) uint32 {
	return binary.LittleEndian.Uint32(imp.mod.mem[ptr:])
}

// writeU32 writes a uint32 to module memory.
func (imp *Imports) writeU32(ptr, val uint32) {
	binary.LittleEndian.PutUint32(imp.mod.mem[ptr:], val)
}

// --- ModuleImports Implementation ---

func (imp *Imports) Import_a(p0 uint32) { // _emval_decref
	imp.emval.DecRef(p0)
}

func (imp *Imports) Import_b(p0, p1, p2, p3, p4, p5, p6, p7 uint32) { // __embind_register_class_function
	classRawType := p0
	methodNamePtr := p1
	argCount := int(p2)
	rawArgTypesPtr := p3
	// p4 = invokerSignaturePtr (unused for now)
	invokerIdx := p5
	context := p6
	// p7 = isPureVirtual

	methodName := imp.readCStr(methodNamePtr)
	argTypes := make([]uint32, argCount)
	for i := 0; i < argCount; i++ {
		argTypes[i] = imp.readU32(rawArgTypesPtr + uint32(i)*4)
	}

	ci := imp.classes[classRawType]
	if ci == nil {
		return
	}
	if ci.Methods == nil {
		ci.Methods = make(map[string]*MethodInfo)
	}
	ci.Methods[methodName] = &MethodInfo{
		Name:       methodName,
		ArgCount:   argCount,
		ArgTypes:   argTypes,
		InvokerIdx: invokerIdx,
		Context:    context,
	}
}

func (imp *Imports) Import_c(p0, p1 uint32, p2 float64) { // __embind_register_constant
	// Constants registered during init - we don't need them
}

func (imp *Imports) Import_d(p0, p1, p2 uint32) { // __embind_register_memory_view
	rawType := p0
	// p1 = dataTypeIndex
	name := imp.readCStr(p2)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "memory_view", Size: 0}
}

func (imp *Imports) Import_e(p0, p1, p2, p3 uint32) uint32 { // _emval_new
	// Constructs a new object: new Constructor(args...)
	// p0 = constructorHandle, p1 = argCount, p2 = argTypesAddr, p3 = argsAddr
	ctor := imp.emval.ToValue(p0)
	ctorName, _ := ctor.(string)
	switch ctorName {
	case "Uint8Array", "Int8Array":
		if p1 >= 1 {
			argTypeID := imp.readU32(p2)
			ti := imp.types[argTypeID]
			if ti != nil && ti.Kind == "memory_view" {
				// Memory view wire format: {size, dataPtr}
				size := imp.readU32(p3)
				dataPtr := imp.readU32(p3 + 4)
				if size > 0 && dataPtr > 0 {
					out := make([]byte, size)
					copy(out, imp.mod.mem[dataPtr:dataPtr+size])
					return imp.emval.ToHandle(out)
				}
				return imp.emval.ToHandle(make([]byte, 0))
			}
			if ti != nil && (ti.Kind == "int" || ti.Kind == "bigint") {
				// new Uint8Array(length)
				argVal := imp.readU32(p3)
				return imp.emval.ToHandle(make([]byte, argVal))
			}
		}
		return imp.emval.ToHandle(make([]byte, 0))
	default:
		return imp.emval.ToHandle(make([]byte, 0))
	}
}

func (imp *Imports) Import_f(p0 uint32) uint32 { // _emval_get_global
	name := imp.readCStr(p0)
	// Return the name itself as a constructor marker (mirrors Module[name] = name)
	// The name is used by _emval_new to determine what type to construct.
	return imp.emval.ToHandle(name)
}

func (imp *Imports) Import_g(p0, p1, p2, p3, p4, p5, p6 uint32) { // __embind_register_class_class_function
	classRawType := p0
	methodNamePtr := p1
	argCount := int(p2)
	rawArgTypesPtr := p3
	// p4 = invokerSignaturePtr
	invokerIdx := p5
	fnIdx := p6

	methodName := imp.readCStr(methodNamePtr)
	argTypes := make([]uint32, argCount)
	for i := 0; i < argCount; i++ {
		argTypes[i] = imp.readU32(rawArgTypesPtr + uint32(i)*4)
	}

	ci := imp.classes[classRawType]
	if ci == nil {
		return
	}
	if ci.StaticMethods == nil {
		ci.StaticMethods = make(map[string]*MethodInfo)
	}
	ci.StaticMethods[methodName] = &MethodInfo{
		Name:       methodName,
		ArgCount:   argCount,
		ArgTypes:   argTypes,
		InvokerIdx: invokerIdx,
		Context:    fnIdx,
	}
}

func (imp *Imports) Import_h(p0, p1, p2, p3, p4 uint32) { // __embind_register_integer
	rawType := p0
	name := imp.readCStr(p1)
	size := int(p2)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "int", Size: size}
}

func (imp *Imports) Import_i(p0, p1, p2, p3, p4, p5, p6, p7, p8, p9, p10, p11, p12 uint32) { // __embind_register_class
	rawType := p0
	rawPtrType := p1
	rawConstPtrType := p2
	baseClassRawType := p3
	// p4 = getActualTypeSignature ("ii")
	// p5 = getActualType (func table idx)
	// p6 = upcastSignature ("ii")
	// p7 = upcast (func table idx)
	// p8 = downcastSignature ("ii")
	// p9 = downcast (func table idx)
	namePtr := p10
	// p11 = destructorSignature ("vi")
	destructorIdx := p12

	name := imp.readCStr(namePtr)

	ci := &ClassInfo{
		Name:            name,
		RawType:         rawType,
		RawPtrType:      rawPtrType,
		RawConstPtrType: rawConstPtrType,
		BaseClassRaw:    baseClassRawType,
		DestructorIdx:   destructorIdx,
		Methods:         make(map[string]*MethodInfo),
		StaticMethods:   make(map[string]*MethodInfo),
	}
	imp.classes[rawType] = ci
	imp.classByName[name] = ci

	// Register pointer types
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "class", Size: 4}
	imp.types[rawPtrType] = &TypeInfo{RawType: rawPtrType, Name: name + "*", Kind: "class_ptr", Size: 4}
	imp.types[rawConstPtrType] = &TypeInfo{RawType: rawConstPtrType, Name: name + " const*", Kind: "class_ptr", Size: 4}
}

func (imp *Imports) Import_j(p0 uint32) { // _emval_incref
	imp.emval.IncRef(p0)
}

func (imp *Imports) Import_k(p0, p1, p2, p3, p4, p5 uint32) { // __embind_register_class_constructor
	classRawType := p0
	argCount := int(p1)
	rawArgTypesPtr := p2
	// p3 = invokerSignaturePtr
	invokerIdx := p4
	rawCtorIdx := p5

	argTypes := make([]uint32, argCount)
	for i := 0; i < argCount; i++ {
		argTypes[i] = imp.readU32(rawArgTypesPtr + uint32(i)*4)
	}

	ci := imp.classes[classRawType]
	if ci == nil {
		return
	}
	ci.Constructors = append(ci.Constructors, &CtorInfo{
		ArgCount:   argCount,
		ArgTypes:   argTypes,
		InvokerIdx: invokerIdx,
		RawCtorIdx: rawCtorIdx,
	})
}

func (imp *Imports) Import_l(p0, p1 uint32) uint32 { // _emval_take_value
	typeID := p0
	ptr := p1

	ti := imp.types[typeID]
	if ti == nil {
		return imp.emval.ToHandle(imp.readU32(ptr))
	}

	switch ti.Kind {
	case "memory_view":
		// Wire format: {size:u32, dataPtr:u32}
		size := imp.readU32(ptr)
		dataPtr := imp.readU32(ptr + 4)
		data := make([]byte, size)
		copy(data, imp.mod.mem[dataPtr:dataPtr+size])
		return imp.emval.ToHandle(data)
	case "int", "bigint":
		switch ti.Size {
		case 1:
			return imp.emval.ToHandle(uint32(imp.mod.mem[ptr]))
		case 2:
			return imp.emval.ToHandle(uint32(binary.LittleEndian.Uint16(imp.mod.mem[ptr:])))
		case 4:
			return imp.emval.ToHandle(imp.readU32(ptr))
		case 8:
			return imp.emval.ToHandle(binary.LittleEndian.Uint64(imp.mod.mem[ptr:]))
		}
	case "class", "class_ptr":
		return imp.emval.ToHandle(imp.readU32(ptr))
	}
	return imp.emval.ToHandle(imp.readU32(ptr))
}

func (imp *Imports) Import_m() { // _abort
	panic(ErrAbort)
}

func (imp *Imports) Import_n(p0, p1, p2 uint32) { // __embind_register_std_wstring
	rawType := p0
	// p1 = charSize
	name := imp.readCStr(p2)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "wstring", Size: int(p1)}
}

func (imp *Imports) Import_o(p0, p1, p2 uint32) uint32 { // EM_JS dispatcher
	// EM_JS dispatcher: funcIdx selects the function, sigAddr points to
	// the signature string, argsAddr points to the stack-packed arguments.
	// Known EM_JS functions:
	//   1456932: throw error
	//   1456985: init getRandomValues (return 0=success)
	//   1457725: getRandomValues(ptr, len) — fill with random bytes
	funcIdx := p0
	sigAddr := p1
	argsAddr := p2

	// Read signature string from memory
	sig := imp.readCStr(sigAddr)

	// Parse args according to signature
	var args []uint32
	pos := argsAddr >> 2 // byte offset to i32 index
	for _, c := range sig {
		switch c {
		case 'i': // i32
			val := imp.readU32(pos * 4)
			args = append(args, val)
			pos++
		default: // skip double-width args
			if pos&1 != 0 {
				pos++
			}
			args = append(args, imp.readU32(pos*4))
			pos += 2
		}
	}

	switch funcIdx {
	case 1456932: // throw error
		var errType, errMsg string
		if len(args) >= 1 {
			errType = imp.readCStr(args[0])
		}
		if len(args) >= 2 {
			errMsg = imp.readCStr(args[1])
		}
		panic(fmt.Sprintf("ltsm: EM_JS error: %s: %s", errType, errMsg))

	case 1456985: // init getRandomValues
		return 0

	case 1457725: // getRandomValues(ptr, len)
		if len(args) >= 2 {
			ptr := args[0]
			length := args[1]
			io.ReadFull(rand.Reader, imp.mod.mem[ptr:ptr+length])
		}
		return 0
	}
	return 0
}

func (imp *Imports) Import_p(p0 uint32) uint32 { // __cxa_allocate_exception
	return imp.malloc(p0)
}

func (imp *Imports) Import_q(p0 uint32) uint32 { // fd_close
	return 0 // ENOSYS
}

func (imp *Imports) Import_r(p0, p1, p2, p3 uint32) uint32 { // fd_write
	// fd=p0, iovs=p1, iovsLen=p2, nwrittenPtr=p3
	// Route to stderr/stdout (silently consume)
	var totalWritten uint32
	for i := uint32(0); i < p2; i++ {
		iovPtr := p1 + i*8
		bufPtr := imp.readU32(iovPtr)
		bufLen := imp.readU32(iovPtr + 4)
		_ = bufPtr
		totalWritten += bufLen
	}
	imp.writeU32(p3, totalWritten)
	return 0
}

func (imp *Imports) Import_s(p0, p1, p2 uint32) uint32 { // __syscall_fcntl64
	return 0
}

func (imp *Imports) Import_t(p0, p1 uint32) { // __embind_register_std_string
	rawType := p0
	name := imp.readCStr(p1)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "string", Size: 4}
}

func (imp *Imports) Import_u(p0, p1, p2 uint32) { // __embind_register_float
	rawType := p0
	name := imp.readCStr(p1)
	size := int(p2)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "float", Size: size}
}

func (imp *Imports) Import_v(p0, p1, p2 uint32, p3, p4 uint64) { // __embind_register_bigint
	rawType := p0
	name := imp.readCStr(p1)
	size := int(p2)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "bigint", Size: size}
}

func (imp *Imports) Import_w() uint32 { // _emscripten_get_origin
	origin := "chrome-extension://ophjlpahpchlmihnnnihgmmeilfjmjjc"
	ptr := imp.malloc(uint32(len(origin) + 1))
	copy(imp.mod.mem[ptr:], []byte(origin))
	imp.mod.mem[ptr+uint32(len(origin))] = 0
	return ptr
}

func (imp *Imports) Import_x() uint32 { // _emval_new_array
	return imp.emval.ToHandle([]byte{})
}

func (imp *Imports) Import_y(p0 uint32) { // _emval_run_destructors
	// Run destructor list stored in emval
	val := imp.emval.ToValue(p0)
	if dl, ok := val.(*destructorList); ok {
		for _, ptr := range dl.ptrs {
			imp.free(ptr)
		}
	}
	imp.emval.DecRef(p0)
}

func (imp *Imports) Import_z(p0, p1, p2 uint32) float64 { // _emval_as
	handle := p0
	typeID := p1
	destructorsPtr := p2

	val := imp.emval.ToValue(handle)
	ti := imp.types[typeID]

	// Create empty destructors list handle
	emptyDtors := &destructorList{}
	dtorsHandle := imp.emval.ToHandle(emptyDtors)
	imp.writeU32(destructorsPtr, dtorsHandle)

	if ti != nil {
		switch ti.Kind {
		case "string":
			var data []byte
			switch v := val.(type) {
			case []byte:
				data = v
			case string:
				data = []byte(v)
			default:
				return 0
			}
			ptr := imp.writeStdString(string(data))
			imp.emval.DecRef(dtorsHandle)
			dtorsHandle = imp.emval.ToHandle(&destructorList{ptrs: []uint32{ptr}})
			imp.writeU32(destructorsPtr, dtorsHandle)
			return float64(ptr)
		case "memory_view":
			data, ok := val.([]byte)
			if !ok {
				return 0
			}
			dataPtr := imp.malloc(uint32(len(data)))
			copy(imp.mod.mem[dataPtr:], data)
			descPtr := imp.malloc(8)
			imp.writeU32(descPtr, uint32(len(data)))
			imp.writeU32(descPtr+4, dataPtr)
			imp.emval.DecRef(dtorsHandle)
			dtorsHandle = imp.emval.ToHandle(&destructorList{ptrs: []uint32{dataPtr, descPtr}})
			imp.writeU32(destructorsPtr, dtorsHandle)
			return float64(descPtr)
		case "int", "bigint":
			switch v := val.(type) {
			case []byte:
				// Array-like → return length
				return float64(len(v))
			default:
				return toFloat64(val)
			}
		case "float":
			return toFloat64(val)
		case "emval":
			return float64(handle)
		}
	}

	// Default: try numeric conversion
	return toFloat64(val)
}

func (imp *Imports) Import_A(p0, p1 uint32) uint32 { // _emval_get_property
	handle := p0
	propHandle := p1

	val := imp.emval.ToValue(handle)
	prop := imp.emval.ToValue(propHandle)

	// Handle []byte property access
	if data, ok := val.([]byte); ok {
		switch p := prop.(type) {
		case string:
			if p == "length" || p == "byteLength" {
				return imp.emval.ToHandle(uint32(len(data)))
			}
		case uint32:
			if int(p) < len(data) {
				return imp.emval.ToHandle(uint32(data[p]))
			}
			return imp.emval.ToHandle(uint32(0))
		case float64:
			idx := int(p)
			if idx < len(data) {
				return imp.emval.ToHandle(uint32(data[idx]))
			}
			return imp.emval.ToHandle(uint32(0))
		}
	}

	// Handle map[string]any property access
	if m, ok := val.(map[string]any); ok {
		if key, ok := prop.(string); ok {
			if v, exists := m[key]; exists {
				return imp.emval.ToHandle(v)
			}
		}
	}

	return imp.emval.ToHandle(nil)
}

func (imp *Imports) Import_B(p0, p1, p2 uint32) { // __cxa_throw
	panic(fmt.Sprintf("ltsm: C++ exception thrown (type=%d, ptr=%d)", p1, p0))
}

func (imp *Imports) Import_C(p0 uint32) uint32 { // emscripten_resize_heap
	return 0 // deny resize
}

func (imp *Imports) Import_D(p0 uint32, p1 uint64, p2, p3 uint32) uint32 { // fd_seek
	return 0 // stub
}

func (imp *Imports) Import_E(p0, p1, p2, p3 uint32) uint32 { // fd_read
	// Handle /dev/urandom reads
	if p0 == imp.urandomFD && imp.urandomFD != 0 {
		var totalRead uint32
		for i := uint32(0); i < p2; i++ {
			iovPtr := p1 + i*8
			bufPtr := imp.readU32(iovPtr)
			bufLen := imp.readU32(iovPtr + 4)
			io.ReadFull(rand.Reader, imp.mod.mem[bufPtr:bufPtr+bufLen])
			totalRead += bufLen
		}
		imp.writeU32(p3, totalRead)
		return 0
	}
	return 8 // EBADF
}

func (imp *Imports) Import_F(p0, p1, p2 uint32) uint32 { // __syscall_ioctl
	return 0
}

func (imp *Imports) Import_G(p0, p1, p2, p3 uint32) uint32 { // __syscall_openat
	path := imp.readCStr(p1)
	if strings.Contains(path, "urandom") {
		imp.urandomFD = imp.nextFD
		imp.nextFD++
		return imp.urandomFD
	}
	return 0xFFFFFFFF // -1
}

func (imp *Imports) Import_H(p0, p1, p2 uint32) { // emscripten_memcpy_big
	copy(imp.mod.mem[p0:p0+p2], imp.mod.mem[p1:p1+p2])
}

func (imp *Imports) Import_I(p0, p1 uint32) { // __embind_register_emval
	rawType := p0
	name := imp.readCStr(p1)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "emval", Size: 4}
}

func (imp *Imports) Import_J(p0, p1, p2, p3, p4 uint32) { // __embind_register_bool
	rawType := p0
	name := imp.readCStr(p1)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "bool", Size: int(p2)}
}

func (imp *Imports) Import_K(p0, p1 uint32) { // __embind_register_void
	rawType := p0
	name := imp.readCStr(p1)
	imp.types[rawType] = &TypeInfo{RawType: rawType, Name: name, Kind: "void", Size: 0}
}

func (imp *Imports) Import_L(p0, p1, p2 uint32) { // _emval_set_property
	handle := p0
	propHandle := p1
	valHandle := p2

	obj := imp.emval.ToValue(handle)
	prop := imp.emval.ToValue(propHandle)
	val := imp.emval.ToValue(valHandle)

	if data, ok := obj.([]byte); ok {
		idx := -1
		switch p := prop.(type) {
		case uint32:
			idx = int(p)
		case float64:
			idx = int(p)
		}
		if idx >= 0 {
			// Auto-grow slice
			for idx >= len(data) {
				data = append(data, 0)
			}
			if v, ok := val.(uint32); ok {
				data[idx] = byte(v)
			}
			imp.emval.SetValue(handle, data)
		}
	}

	if m, ok := obj.(map[string]any); ok {
		if key, ok := prop.(string); ok {
			m[key] = val
		}
	}
}

func (imp *Imports) Import_M(p0 uint32) uint32 { // _emval_get_module_property
	propName := imp.readCStr(p0)
	// In Emscripten, Module stores string constants for property access:
	// C++ val["length"] compiles to _emval_get_module_property("length") then
	// _emval_get_property(val, handle). Return the string itself as an emval handle.
	return imp.emval.ToHandle(propName)
}

func (imp *Imports) Import_N(p0, p1, p2, p3, p4, p5 uint32) { // __embind_register_function
	// Global function registration - store for later use
	_ = p0 // namePtr
	_ = p1 // argCount
	_ = p2 // rawArgTypesPtr
	_ = p3 // signaturePtr
	_ = p4 // invokerIdx
	_ = p5 // fnIdx
}

// destructorList holds WASM memory pointers to free during _emval_run_destructors.
type destructorList struct {
	ptrs []uint32
}

// --- Helper functions ---

func toFloat64(v any) float64 {
	switch val := v.(type) {
	case uint32:
		return float64(val)
	case int32:
		return float64(val)
	case uint64:
		return float64(val)
	case int64:
		return float64(val)
	case float32:
		return float64(val)
	case float64:
		return val
	case bool:
		if val {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// --- High-level embind method calling ---

// CallMethod calls an embind instance method on a C++ object.
func (imp *Imports) CallMethod(className, methodName string, thisPtr uint32, args ...uint32) (uint32, error) {
	ci := imp.classByName[className]
	if ci == nil {
		return 0, fmt.Errorf("ltsm: class %q not found", className)
	}

	mi := ci.Methods[methodName]
	if mi == nil {
		// Check base class
		if ci.BaseClassRaw != 0 {
			if baseCi := imp.classes[ci.BaseClassRaw]; baseCi != nil {
				mi = baseCi.Methods[methodName]
			}
		}
		if mi == nil {
			return 0, fmt.Errorf("ltsm: method %q not found on class %q", methodName, className)
		}
	}

	// Build call args: [context, thisPtr, arg1, arg2, ...]
	callArgs := make([]uint32, 0, 2+len(args))
	callArgs = append(callArgs, mi.Context, thisPtr)
	callArgs = append(callArgs, args...)

	return imp.callIndirect(mi.InvokerIdx, mi.ArgTypes, callArgs)
}

// CallStatic calls an embind static method.
func (imp *Imports) CallStatic(className, methodName string, args ...uint32) (uint32, error) {
	ci := imp.classByName[className]
	if ci == nil {
		return 0, fmt.Errorf("ltsm: class %q not found", className)
	}

	mi := ci.StaticMethods[methodName]
	if mi == nil {
		return 0, fmt.Errorf("ltsm: static method %q not found on class %q", methodName, className)
	}

	// Build call args: [fnIdx, arg1, arg2, ...]
	callArgs := make([]uint32, 0, 1+len(args))
	callArgs = append(callArgs, mi.Context)
	callArgs = append(callArgs, args...)

	return imp.callIndirect(mi.InvokerIdx, mi.ArgTypes, callArgs)
}

// Construct calls an embind class constructor.
func (imp *Imports) Construct(className string, args ...uint32) (uint32, error) {
	ci := imp.classByName[className]
	if ci == nil {
		return 0, fmt.Errorf("ltsm: class %q not found", className)
	}

	// Find constructor matching arg count (+1 for return type)
	for _, ctor := range ci.Constructors {
		if ctor.ArgCount == len(args)+1 {
			callArgs := make([]uint32, 0, 1+len(args))
			callArgs = append(callArgs, ctor.RawCtorIdx)
			callArgs = append(callArgs, args...)
			return imp.callIndirect(ctor.InvokerIdx, ctor.ArgTypes, callArgs)
		}
	}

	return 0, fmt.Errorf("ltsm: no constructor with %d args found for class %q", len(args), className)
}

// Destroy invokes the registered C++ destructor for an embind object.
func (imp *Imports) Destroy(className string, ptr uint32) error {
	if ptr == 0 {
		return nil
	}
	ci := imp.classByName[className]
	if ci == nil {
		return fmt.Errorf("ltsm: class %q not found", className)
	}
	if ci.DestructorIdx == 0 {
		return fmt.Errorf("ltsm: class %q has no destructor", className)
	}
	imp.mod.callIndirectT7(ci.DestructorIdx, ptr)
	return nil
}

// WriteEmvalBytes stores a byte slice in the emval table and returns the handle.
func (imp *Imports) WriteEmvalBytes(data []byte) uint32 {
	return imp.emval.ToHandle(data)
}

// ReadEmvalBytes reads a byte slice from an emval handle.
func (imp *Imports) ReadEmvalBytes(handle uint32) ([]byte, error) {
	val := imp.emval.ToValue(handle)
	if val == nil {
		return nil, fmt.Errorf("ltsm: emval handle %d is nil", handle)
	}
	switch v := val.(type) {
	case []byte:
		return v, nil
	default:
		return nil, fmt.Errorf("ltsm: emval handle %d has unexpected type %T", handle, val)
	}
}

// MarkSecureKeyExportable sets the exportable flag on a SecureKey C++ object.
// The LTSM module stores a flag at ptr+16 that controls whether exportKey() is allowed.
func (imp *Imports) MarkSecureKeyExportable(ptr uint32) {
	imp.mod.mem[ptr+16] = 1
}

// callIndirect dispatches to the appropriate callIndirectTN based on parameter count and types.
// Most embind methods use all-uint32 params, but some have uint64 (bigint) params or returns.
func (imp *Imports) callIndirect(invokerIdx uint32, argTypes []uint32, callArgs []uint32) (uint32, error) {
	m := imp.mod
	nArgs := len(callArgs)

	retType := imp.types[argTypes[0]]
	hasReturn := retType != nil && retType.Kind != "void"
	retBigint := retType != nil && retType.Kind == "bigint"

	// Check if any parameter is bigint (i64). argTypes[0] is return type, argTypes[1..] map
	// to callArgs[1..] (callArgs[0] is context, which has no argType entry).
	bigintPos := -1
	for i := 1; i < len(argTypes); i++ {
		t := imp.types[argTypes[i]]
		if t != nil && t.Kind == "bigint" {
			bigintPos = i // position in callArgs (context at 0 shifts everything by 0 since argTypes[1]=callArgs[1])
			break
		}
	}

	// Dispatch for functions with bigint parameters at position 7 (e.g. encryptV2 seq param)
	if bigintPos == 7 && nArgs == 9 {
		if hasReturn {
			// T36: (i32x7, i64, i32) -> i32  (e.g. encryptV2 invoker)
			return m.callIndirectT36(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3],
				callArgs[4], callArgs[5], callArgs[6], uint64(callArgs[7]), callArgs[8]), nil
		}
		// T26: (i32x7, i64, i32) -> void
		m.callIndirectT26(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3],
			callArgs[4], callArgs[5], callArgs[6], uint64(callArgs[7]), callArgs[8])
		return 0, nil
	}

	// Dispatch for functions with bigint return type
	if retBigint {
		switch nArgs {
		case 1:
			// T23: (i32) -> i64
			return uint32(m.callIndirectT23(invokerIdx, callArgs[0])), nil
		case 2:
			// T35: (i32, i32) -> i64  (e.g. getTimestamp invoker)
			return uint32(m.callIndirectT35(invokerIdx, callArgs[0], callArgs[1])), nil
		}
	}

	// Standard all-uint32 dispatch based on arg count + return type.
	switch {
	case hasReturn && nArgs == 1:
		return m.callIndirectT2(invokerIdx, callArgs[0]), nil
	case hasReturn && nArgs == 2:
		return m.callIndirectT1(invokerIdx, callArgs[0], callArgs[1]), nil
	case hasReturn && nArgs == 3:
		return m.callIndirectT0(invokerIdx, callArgs[0], callArgs[1], callArgs[2]), nil
	case hasReturn && nArgs == 4:
		return m.callIndirectT9(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3]), nil
	case hasReturn && nArgs == 5:
		return m.callIndirectT5(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3], callArgs[4]), nil
	case hasReturn && nArgs == 6:
		return m.callIndirectT12(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3], callArgs[4], callArgs[5]), nil
	case hasReturn && nArgs == 7:
		return m.callIndirectT10(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3], callArgs[4], callArgs[5], callArgs[6]), nil
	case hasReturn && nArgs == 8:
		return m.callIndirectT20(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3], callArgs[4], callArgs[5], callArgs[6], callArgs[7]), nil
	case hasReturn && nArgs == 9:
		return m.callIndirectT11(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3], callArgs[4], callArgs[5], callArgs[6], callArgs[7], callArgs[8]), nil
	case !hasReturn && nArgs == 0:
		m.callIndirectT15(invokerIdx)
		return 0, nil
	case !hasReturn && nArgs == 1:
		m.callIndirectT7(invokerIdx, callArgs[0])
		return 0, nil
	case !hasReturn && nArgs == 2:
		m.callIndirectT4(invokerIdx, callArgs[0], callArgs[1])
		return 0, nil
	case !hasReturn && nArgs == 3:
		m.callIndirectT3(invokerIdx, callArgs[0], callArgs[1], callArgs[2])
		return 0, nil
	case !hasReturn && nArgs == 4:
		m.callIndirectT6(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3])
		return 0, nil
	case !hasReturn && nArgs == 5:
		m.callIndirectT13(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3], callArgs[4])
		return 0, nil
	case !hasReturn && nArgs == 6:
		m.callIndirectT14(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3], callArgs[4], callArgs[5])
		return 0, nil
	case !hasReturn && nArgs == 8:
		m.callIndirectT19(invokerIdx, callArgs[0], callArgs[1], callArgs[2], callArgs[3], callArgs[4], callArgs[5], callArgs[6], callArgs[7])
		return 0, nil
	default:
		return 0, fmt.Errorf("ltsm: unsupported callIndirect with %d args (hasReturn=%v)", nArgs, hasReturn)
	}
}
