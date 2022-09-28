package sdk

import (
	"go.uber.org/atomic"
)

// a single synchronizer is shared between audio and video writers
// used for creating PTS
type synchronizer struct {
	startTime atomic.Int64
	endTime   atomic.Int64
	delay     atomic.Int64
}

func (c *synchronizer) GetOrSetStartTime(t int64) int64 {
	if c.startTime.CAS(0, t) {
		return t
	}

	startTime := c.startTime.Load()
	c.delay.CAS(0, t-startTime)
	return startTime
}

func (c *synchronizer) GetStartTime() int64 {
	return c.startTime.Load()
}

func (c *synchronizer) SetEndTime(t int64) {
	c.endTime.Store(t)
}

func (c *synchronizer) GetEndTime() int64 {
	return c.endTime.Load()
}

func (c *synchronizer) GetDelay() int64 {
	return c.delay.Load()
}
