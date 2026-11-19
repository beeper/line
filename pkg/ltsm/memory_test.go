package ltsm

import "testing"

func TestRuntimeNativeMemoryAllocation(t *testing.T) {
	rt, err := NewRuntime()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	const size = 64 * 1024
	first, second := rt.AllocMemory(size), rt.AllocMemory(size)
	if first == 0 || second == 0 || uint64(first)+size > uint64(len(rt.ModuleMem())) || uint64(second)+size > uint64(len(rt.ModuleMem())) {
		t.Fatalf("invalid native allocations: %d, %d", first, second)
	}
	if first < second+size && second < first+size {
		t.Fatal("native allocations overlap")
	}
	rt.ModuleMem()[first] = 42
	rt.ModuleMem()[second] = 17
	if rt.ModuleMem()[first] != 42 {
		t.Fatal("writing one allocation corrupted another")
	}
	rt.FreeMemory(first)
	rt.FreeMemory(second)
}

func TestRuntimeNativeMemoryExhaustionAndReuse(t *testing.T) {
	rt, err := NewRuntime()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	const size = 64 * 1024
	var allocated []uint32
	for range len(rt.ModuleMem()) / size {
		ptr := rt.AllocMemory(size)
		if ptr == 0 {
			break
		}
		allocated = append(allocated, ptr)
	}
	if len(allocated) == 0 || rt.AllocMemory(size) != 0 {
		t.Fatal("native allocator did not report fixed-heap exhaustion")
	}
	for _, ptr := range allocated {
		rt.FreeMemory(ptr)
	}
	// Coalescing the freed blocks must make a large real allocation possible.
	ptr := rt.AllocMemory(uint32(len(allocated) * size))
	if ptr == 0 {
		t.Fatal("freed native memory was not reusable")
	}
	rt.FreeMemory(ptr)
}
