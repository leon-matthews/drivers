package mcp2515

import (
	"errors"
	"testing"
)

// fakeOutputPin records the chip select level and reports the falling edge.
type fakeOutputPin struct {
	high  bool
	onLow func()
}

func (p *fakeOutputPin) Set(level bool) {
	if !level && p.high && p.onLow != nil {
		p.onLow()
	}
	p.high = level
}

// fakeSPI models the register file of the controller.
//
// It decodes only the instructions that the mode functions use.
type fakeSPI struct {
	t     *testing.T
	cs    *fakeOutputPin
	regs  [128]byte
	frame []byte
	ops   int
}

// beginFrame starts a new instruction when the chip select goes low.
func (s *fakeSPI) beginFrame() { s.frame = s.frame[:0] }

func (s *fakeSPI) Transfer(value byte) (byte, error) {
	s.t.Helper()
	s.record()
	s.frame = append(s.frame, value)
	switch {
	case s.frame[0] == mcpWrite && len(s.frame) == 3:
		s.writeRegister(s.frame[1], s.frame[2])
	case s.frame[0] == mcpBitMod && len(s.frame) == 4:
		addr, mask, data := s.frame[1], s.frame[2], s.frame[3]
		s.writeRegister(addr, (s.regs[addr]&^mask)|(data&mask))
	}
	return 0, nil
}

func (s *fakeSPI) Tx(w, r []byte) error {
	s.t.Helper()
	s.record()
	if w != nil || len(s.frame) < 2 || s.frame[0] != mcpRead {
		return nil
	}
	for i := range r {
		addr := int(s.frame[1]) + i
		if addr >= len(s.regs) {
			break
		}
		r[i] = s.regs[addr]
	}
	return nil
}

func (s *fakeSPI) record() {
	s.t.Helper()
	if s.cs.high {
		s.t.Error("chip select was high during a transfer")
	}
	s.ops++
}

// writeRegister keeps OPMOD in CANSTAT equal to REQOP in CANCTRL, as the controller does.
func (s *fakeSPI) writeRegister(addr, value byte) {
	s.regs[addr] = value
	if addr == mcpCANCTRL {
		s.regs[mcpCANSTAT] = (s.regs[mcpCANSTAT] &^ modeMask) | (value & modeMask)
	}
}

func newTestDevice(t *testing.T) (*Device, *fakeSPI) {
	t.Helper()
	cs := &fakeOutputPin{high: true}
	bus := &fakeSPI{t: t, cs: cs}
	cs.onLow = bus.beginFrame

	return New(bus, cs), bus
}

func TestSetModeRejectsModesYouCannotRequest(t *testing.T) {
	for _, mode := range []Mode{modeConfig, modePowerUp, 0x10, 0xa0} {
		d, bus := newTestDevice(t)
		if err := d.SetMode(mode); !errors.Is(err, ErrInvalidParameter) {
			t.Errorf("SetMode(%#02x) error = %v, want %v", byte(mode), err, ErrInvalidParameter)
		}
		if bus.ops != 0 {
			t.Errorf("SetMode(%#02x) made %d SPI operations, want 0", byte(mode), bus.ops)
		}
	}
}

func TestSetModeRequestsTheMode(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
	}{
		{"normal", ModeNormal},
		{"sleep", ModeSleep},
		{"loopback", ModeLoopback},
		{"listen only", ModeListenOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, bus := newTestDevice(t)
			if err := d.SetMode(tt.mode); err != nil {
				t.Fatalf("SetMode(%#02x) error = %v", byte(tt.mode), err)
			}
			if got := Mode(bus.regs[mcpCANCTRL] & modeMask); got != tt.mode {
				t.Errorf("CANCTRL REQOP = %#02x, want %#02x", byte(got), byte(tt.mode))
			}
		})
	}
}

func TestModeReportsTheControllerState(t *testing.T) {
	d, bus := newTestDevice(t)
	bus.regs[mcpCANSTAT] = modePowerUp

	got, err := d.Mode()
	if err != nil {
		t.Fatalf("Mode() error = %v", err)
	}
	if got != modePowerUp {
		t.Errorf("Mode() = %#02x, want %#02x for a controller that is not configured", byte(got), modePowerUp)
	}
}

// A request for sleep must keep the mode to return to, because sleep is temporary.
func TestSetModeSleepKeepsTheModeToReturnTo(t *testing.T) {
	d, _ := newTestDevice(t)
	if err := d.SetMode(ModeLoopback); err != nil {
		t.Fatalf("SetMode(ModeLoopback) error = %v", err)
	}
	if err := d.SetMode(ModeSleep); err != nil {
		t.Fatalf("SetMode(ModeSleep) error = %v", err)
	}
	if d.mcpMode != ModeLoopback {
		t.Errorf("mcpMode = %#02x, want %#02x", byte(d.mcpMode), byte(ModeLoopback))
	}
}

// A controller that is asleep needs the wake sequence before it accepts a new mode.
func TestSetModeWakesTheController(t *testing.T) {
	d, bus := newTestDevice(t)
	bus.regs[mcpCANSTAT] = modeSleep

	if err := d.SetMode(ModeNormal); err != nil {
		t.Fatalf("SetMode(ModeNormal) error = %v", err)
	}
	if got := Mode(bus.regs[mcpCANCTRL] & modeMask); got != ModeNormal {
		t.Errorf("CANCTRL REQOP = %#02x, want %#02x", byte(got), byte(ModeNormal))
	}
}
