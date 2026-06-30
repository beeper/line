package ltsm

import "testing"

func TestMix66OutputInRange(t *testing.T) {
	// Input digits must be in 0..7 (base-8 digits).
	var x, y [66]byte
	for i := 0; i < 66; i++ {
		x[i] = byte((i * 3) % 8)
		y[i] = byte((i * 5) % 8)
	}

	var out [66]byte
	Mix66(out[:], x[:], y[:], 0)
	for i, b := range out {
		if b > 7 {
			t.Fatalf("out[%d]=%d out of range", i, b)
		}
	}
}
