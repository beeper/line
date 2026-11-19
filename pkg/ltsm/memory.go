package ltsm

// AllocMemory allocates bytes from the module's native linear-memory heap and
// returns their offset in ModuleMem. It returns zero if the fixed heap cannot
// satisfy the allocation. Like the other low-level Runtime methods, callers
// must serialize access to a runtime.
//
// This complements ModuleMem for resource-pressure fixtures: tests can reserve
// real native allocations without repeatedly decoding cryptographic keys. It
// does not replace the allocator, resize the heap, or synthesize an abort.
func (rt *Runtime) AllocMemory(size uint32) uint32 {
	return rt.imp.malloc(size)
}

// FreeMemory releases an allocation made by AllocMemory on this runtime. The
// caller must not use the allocation afterwards or free it more than once.
func (rt *Runtime) FreeMemory(ptr uint32) {
	rt.imp.free(ptr)
}
