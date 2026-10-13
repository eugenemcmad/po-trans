package app

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package on leaked goroutines.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
