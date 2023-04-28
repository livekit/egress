package errors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFatalError(t *testing.T) {
	assert.False(t, IsFatal(ErrNoConfig))
	assert.True(t, IsFatal(Fatal(ErrNoConfig)))
	assert.Equal(t, ErrNoConfig, Fatal(ErrNoConfig).(*FatalError).Unwrap())
}
