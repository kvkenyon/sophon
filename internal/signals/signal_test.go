package signals

import (
	"errors"
	"testing"
)

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition(SignalOpen, SignalResolved); err != nil {
		t.Fatalf("open -> resolved: %v", err)
	}
	for _, transition := range [][2]SignalStatus{
		{SignalOpen, SignalOpen},
		{SignalResolved, SignalOpen},
		{SignalResolved, SignalResolved},
	} {
		if err := ValidateTransition(transition[0], transition[1]); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("%s -> %s error = %v", transition[0], transition[1], err)
		}
	}
}
