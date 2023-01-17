package pprof

import (
	"bytes"
	"context"
	"runtime/pprof"
	"time"

	"github.com/livekit/egress/pkg/errors"
)

const (
	cpuProfileName = "cpu"
	defaultTimeout = 30
)

func GetProfileData(ctx context.Context, profileName string, timeout int, debug int) (b []byte, err error) {
	switch profileName {
	case cpuProfileName:
		return GetCpuProfileData(ctx, timeout)
	default:
		return GetGenericProfileData(profileName, debug)
	}
}

func GetCpuProfileData(ctx context.Context, timeout int) (b []byte, err error) {
	if timeout == 0 {
		timeout = defaultTimeout
	}

	buf := &bytes.Buffer{}
	err = pprof.StartCPUProfile(buf)
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		// finish async in order not to block, since we will not use the results
		go pprof.StopCPUProfile()
		return nil, context.Canceled
	case <-time.After(time.Duration(timeout) * time.Second):
		// break
	}

	pprof.StopCPUProfile()

	return buf.Bytes(), nil
}

func GetGenericProfileData(profileName string, debug int) (b []byte, err error) {
	pp := pprof.Lookup(profileName)
	if pp == nil {
		return nil, errors.ErrProfileNotFound
	}

	buf := &bytes.Buffer{}

	err = pp.WriteTo(buf, debug)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
