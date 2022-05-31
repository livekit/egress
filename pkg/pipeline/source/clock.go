package source

import (
	"go.uber.org/atomic"
)

// a single clockSync is shared between audio and video writers
// used for creating PTS
type clockSync struct {
	startTime atomic.Int64
	endTime   atomic.Int64
	delay     atomic.Int64
}

func (c *clockSync) GetOrSetStartTime(t int64) int64 {
	if c.startTime.CAS(0, t) {
		return t
	}

	startTime := c.startTime.Load()
	c.delay.CAS(0, t-startTime)
	return startTime
}

func (c *clockSync) GetStartTime() int64 {
	return c.startTime.Load()
}

func (c *clockSync) SetEndTime(t int64) {
	c.endTime.Store(t)
}

func (c *clockSync) GetEndTime() int64 {
	return c.endTime.Load()
}

func (c *clockSync) GetDelay() int64 {
	return c.delay.Load()
}
