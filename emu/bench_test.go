package emu

import "testing"

func loadTestBin(b *testing.B) *Machine {
	romPath, data := readFixtureROM(b)
	m := NewMachine("mtr1")
	if err := m.Load(data, romPath, nil, "mtr1"); err != nil {
		b.Fatal(err)
	}
	return m
}

func BenchmarkRun2M(b *testing.B) {
	m := loadTestBin(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Reset(true)
		if _, err := m.Run(RunOptions{Instructions: 2000000, Cycles: int64(^uint64(0) >> 1)}); err != nil {
			b.Fatal(err)
		}
	}
}
