package ltsm

import (
	"testing"
)

// stubImports is a minimal ModuleImports implementation for testing.
type stubImports struct{}

func (s *stubImports) Import_a(p0 uint32)                                                    {}
func (s *stubImports) Import_b(p0, p1, p2, p3, p4, p5, p6, p7 uint32)                        {}
func (s *stubImports) Import_c(p0, p1 uint32, p2 float64)                                    {}
func (s *stubImports) Import_d(p0, p1, p2 uint32)                                            {}
func (s *stubImports) Import_e(p0, p1, p2, p3 uint32) uint32                                 { return 0 }
func (s *stubImports) Import_f(p0 uint32) uint32                                             { return 0 }
func (s *stubImports) Import_g(p0, p1, p2, p3, p4, p5, p6 uint32)                            {}
func (s *stubImports) Import_h(p0, p1, p2, p3, p4 uint32)                                    {}
func (s *stubImports) Import_i(p0, p1, p2, p3, p4, p5, p6, p7, p8, p9, p10, p11, p12 uint32) {}
func (s *stubImports) Import_j(p0 uint32)                                                    {}
func (s *stubImports) Import_k(p0, p1, p2, p3, p4, p5 uint32)                                {}
func (s *stubImports) Import_l(p0, p1 uint32) uint32                                         { return 0 }
func (s *stubImports) Import_m()                                                             {}
func (s *stubImports) Import_n(p0, p1, p2 uint32)                                            {}
func (s *stubImports) Import_o(p0, p1, p2 uint32) uint32                                     { return 0 }
func (s *stubImports) Import_p(p0 uint32) uint32                                             { return 0 }
func (s *stubImports) Import_q(p0 uint32) uint32                                             { return 0 }
func (s *stubImports) Import_r(p0, p1, p2, p3 uint32) uint32                                 { return 0 }
func (s *stubImports) Import_s(p0, p1, p2 uint32) uint32                                     { return 0 }
func (s *stubImports) Import_t(p0, p1 uint32)                                                {}
func (s *stubImports) Import_u(p0, p1, p2 uint32)                                            {}
func (s *stubImports) Import_v(p0, p1, p2 uint32, p3, p4 uint64)                             {}
func (s *stubImports) Import_w() uint32                                                      { return 0 }
func (s *stubImports) Import_x() uint32                                                      { return 0 }
func (s *stubImports) Import_y(p0 uint32)                                                    {}
func (s *stubImports) Import_z(p0, p1, p2 uint32) float64                                    { return 0 }
func (s *stubImports) Import_A(p0, p1 uint32) uint32                                         { return 0 }
func (s *stubImports) Import_B(p0, p1, p2 uint32)                                            {}
func (s *stubImports) Import_C(p0 uint32) uint32                                             { return 0 }
func (s *stubImports) Import_D(p0 uint32, p1 uint64, p2, p3 uint32) uint32                   { return 0 }
func (s *stubImports) Import_E(p0, p1, p2, p3 uint32) uint32                                 { return 0 }
func (s *stubImports) Import_F(p0, p1, p2 uint32) uint32                                     { return 0 }
func (s *stubImports) Import_G(p0, p1, p2, p3 uint32) uint32                                 { return 0 }
func (s *stubImports) Import_H(p0, p1, p2 uint32)                                            {}
func (s *stubImports) Import_I(p0, p1 uint32)                                                {}
func (s *stubImports) Import_J(p0, p1, p2, p3, p4 uint32)                                    {}
func (s *stubImports) Import_K(p0, p1 uint32)                                                {}
func (s *stubImports) Import_L(p0, p1, p2 uint32)                                            {}
func (s *stubImports) Import_M(p0 uint32) uint32                                             { return 0 }
func (s *stubImports) Import_N(p0, p1, p2, p3, p4, p5 uint32)                                {}

// TestTranspiledMix66 validates the transpiled mix function matches the hand-written Mix66.
func TestTranspiledMix66(t *testing.T) {
	mod := NewModule(&stubImports{})

	// Set up test data in module memory
	xAddr := uint32(8000000)
	yAddr := uint32(8001000)
	dstAddr := uint32(8002000)

	// Create test inputs (base-8 digits, 0..7)
	var x, y [66]byte
	for i := 0; i < 66; i++ {
		x[i] = byte((i * 3) % 8)
		y[i] = byte((i * 5) % 8)
	}

	// Copy inputs to module memory
	copy(mod.mem[xAddr:], x[:])
	copy(mod.mem[yAddr:], y[:])

	// Run the hand-written Mix66 for reference output
	var refOut [66]byte
	Mix66(refOut[:], x[:], y[:], 0)

	// Run the transpiled mix function
	mod.f156(0, xAddr, yAddr, dstAddr)

	// Read transpiled output from module memory
	var transOut [66]byte
	copy(transOut[:], mod.mem[dstAddr:dstAddr+66])

	// Compare
	for i := 0; i < 66; i++ {
		if transOut[i] != refOut[i] {
			t.Errorf("mismatch at index %d: transpiled=%d, reference=%d", i, transOut[i], refOut[i])
		}
	}

	t.Logf("transpiled mix output matches hand-written Mix66: %v", transOut[:10])
}
