package errors

import (
	"errors"
	"strings"
	"testing"

	"github.com/livekit/psrpc"
	"github.com/stretchr/testify/assert"
)

func TestFatalError(t *testing.T) {
	assert.False(t, IsFatal(ErrNoConfig))
	assert.True(t, IsFatal(Fatal(ErrNoConfig)))
	assert.Equal(t, ErrNoConfig, Fatal(ErrNoConfig).(*FatalError).Unwrap())
}

func TestErrArray(t *testing.T) {
	err1 := errors.New("error 1")
	err2 := errors.New("error 2")
	err3 := psrpc.NewErrorf(psrpc.NotFound, "error 3")
	err4 := psrpc.NewErrorf(psrpc.Internal, "error 4")

	errArray := &ErrArray{}
	assert.Nil(t, errArray.ToError())

	errArray.AppendErr(err1)
	assert.Equal(t, psrpc.Unknown, errArray.ToError().Code())
	assert.Equal(t, err1.Error(), errArray.ToError().Error())

	errArray.AppendErr(err2)
	assert.Equal(t, psrpc.Unknown, errArray.ToError().Code())
	assert.Equal(t, 2, len(strings.Split(errArray.ToError().Error(), "\n")))

	errArray.AppendErr(err3)
	assert.Equal(t, psrpc.NotFound, errArray.ToError().Code())
	assert.Equal(t, 3, len(strings.Split(errArray.ToError().Error(), "\n")))

	errArray.AppendErr(err4)
	assert.Equal(t, psrpc.NotFound, errArray.ToError().Code())
	assert.Equal(t, 4, len(strings.Split(errArray.ToError().Error(), "\n")))
}
