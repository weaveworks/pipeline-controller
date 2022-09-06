package testingutils

import (
	"testing"

	Ω "github.com/onsi/gomega"
)

func NewGomegaWithT(t *testing.T) *Ω.WithT {
	g := Ω.NewGomegaWithT(t)
	g.Fail = func(message string, _ ...int) {
		t.Helper()
		t.Logf("\n%s", message)
		t.Fail()
	}
	return g
}
