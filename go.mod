module github.com/livekit/egress

go 1.21

toolchain go1.21.4

require (
	cloud.google.com/go/storage v1.31.0
	github.com/Azure/azure-storage-blob-go v0.15.0
	github.com/aliyun/aliyun-oss-go-sdk v2.2.7+incompatible
	github.com/aws/aws-sdk-go v1.44.296
	github.com/chromedp/cdproto v0.0.0-20230625224106-7fafe342e117
	github.com/chromedp/chromedp v0.9.1
	github.com/frostbyte73/core v0.0.9
	github.com/go-gst/go-glib v0.0.0-20230906175327-b2d34240bcb4
	github.com/go-gst/go-gst v0.0.0-20231009181223-aa872b0f6c0c
	github.com/go-logr/logr v1.3.0
	github.com/googleapis/gax-go/v2 v2.12.0
	github.com/gorilla/websocket v1.5.0
	github.com/livekit/livekit-server v1.5.1-0.20231026153736-8b16db227070
	github.com/livekit/mageutil v0.0.0-20230125210925-54e8a70427c1
	github.com/livekit/protocol v1.9.2-0.20231127214843-fd7066503fee
	github.com/livekit/psrpc v0.5.2
	github.com/livekit/server-sdk-go v1.1.1
	github.com/pion/rtp v1.8.3
	github.com/pion/webrtc/v3 v3.2.23
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.17.0
	github.com/stretchr/testify v1.8.4
	github.com/urfave/cli/v2 v2.25.7
	go.uber.org/atomic v1.11.0
	go.uber.org/zap v1.26.0
	google.golang.org/api v0.130.0
	google.golang.org/grpc v1.59.0
	google.golang.org/protobuf v1.31.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cloud.google.com/go v0.110.9 // indirect
	cloud.google.com/go/compute v1.23.2 // indirect
	cloud.google.com/go/compute/metadata v0.2.3 // indirect
	cloud.google.com/go/iam v1.1.4 // indirect
	github.com/Azure/azure-pipeline-go v0.2.3 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bep/debounce v1.2.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/chromedp/sysutil v1.0.0 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/eapache/channels v1.1.0 // indirect
	github.com/eapache/queue v1.1.0 // indirect
	github.com/elliotchance/orderedmap/v2 v2.2.0 // indirect
	github.com/gammazero/deque v0.2.1 // indirect
	github.com/go-jose/go-jose/v3 v3.0.1 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.1.0 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/google/s2a-go v0.1.4 // indirect
	github.com/google/uuid v1.3.1 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.2.5 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/jxskiss/base62 v1.1.0 // indirect
	github.com/klauspost/compress v1.17.2 // indirect
	github.com/klauspost/cpuid/v2 v2.2.5 // indirect
	github.com/lithammer/shortuuid/v4 v4.0.0 // indirect
	github.com/livekit/mediatransportutil v0.0.0-20231017082622-43f077b4e60e // indirect
	github.com/mackerelio/go-osstat v0.2.4 // indirect
	github.com/magefile/mage v1.15.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-ieproxy v0.0.1 // indirect
	github.com/mattn/go-pointer v0.0.1 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/nats-io/nats.go v1.31.0 // indirect
	github.com/nats-io/nkeys v0.4.6 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pion/datachannel v1.5.5 // indirect
	github.com/pion/dtls/v2 v2.2.7 // indirect
	github.com/pion/ice/v2 v2.3.11 // indirect
	github.com/pion/interceptor v0.1.25 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/mdns v0.0.8 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.12 // indirect
	github.com/pion/sctp v1.8.9 // indirect
	github.com/pion/sdp/v3 v3.0.6 // indirect
	github.com/pion/srtp/v2 v2.0.18 // indirect
	github.com/pion/stun v0.6.1 // indirect
	github.com/pion/transport/v2 v2.2.4 // indirect
	github.com/pion/turn/v2 v2.1.4 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_model v0.4.1-0.20230718164431-9a2bf3000d16 // indirect
	github.com/prometheus/common v0.44.0 // indirect
	github.com/prometheus/procfs v0.11.1 // indirect
	github.com/redis/go-redis/v9 v9.3.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/thoas/go-funk v0.9.3 // indirect
	github.com/twitchtv/twirp v8.1.3+incompatible // indirect
	github.com/xrash/smetrics v0.0.0-20201216005158-039620a65673 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.15.0 // indirect
	golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa // indirect
	golang.org/x/mod v0.14.0 // indirect
	golang.org/x/net v0.18.0 // indirect
	golang.org/x/oauth2 v0.11.0 // indirect
	golang.org/x/sync v0.5.0 // indirect
	golang.org/x/sys v0.14.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	golang.org/x/xerrors v0.0.0-20220907171357-04be3eba64a2 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20231030173426-d783a09b4405 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20231016165738-49dd2c1f3d0b // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20231106174013-bbf56f31fb17 // indirect
)
