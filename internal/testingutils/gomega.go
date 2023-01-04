package testingutils

import (
	"testing"

	"github.com/onsi/gomega"
)

func NewGomegaWithT(t *testing.T) *gomega.WithT {
	g := gomega.NewWithT(t)
	g.Fail = func(message string, _ ...int) {
		t.Helper()
		t.Logf("\n%s", message)
		t.Fail()
	}
	return g
}
