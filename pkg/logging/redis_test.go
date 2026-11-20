package logging

import "testing"

func TestIsRedisSentinelDiscoveryMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "selected sentinel",
			msg:  "sentinel: selected addr=10.0.1.62:26379 masterAddr=10.0.1.61:6379",
			want: true,
		},
		{
			name: "new master",
			msg:  "sentinel: new master=\"voice-redis\" addr=\"10.0.1.61:6379\"",
			want: true,
		},
		{
			name: "canceled parallel sentinel lookup",
			msg:  "sentinel: GetMasterAddrByName addr=10.0.1.63:26379, master=\"voice-redis\" failed: context canceled",
			want: true,
		},
		{
			name: "connection refused remains diagnostic",
			msg:  "sentinel: GetMasterAddrByName addr=10.0.1.63:26379, master=\"voice-redis\" failed: dial tcp 10.0.1.63:26379: connect: connection refused",
			want: false,
		},
		{
			name: "deadline exceeded remains diagnostic",
			msg:  "sentinel: GetMasterAddrByName addr=10.0.1.63:26379, master=\"voice-redis\" failed: context deadline exceeded",
			want: false,
		},
		{
			name: "non sentinel redis line remains diagnostic",
			msg:  "redis: unable to connect to redis: all sentinels specified in configuration are unreachable",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRedisSentinelDiscoveryMessage(tt.msg); got != tt.want {
				t.Fatalf("IsRedisSentinelDiscoveryMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}
