module github.com/livekit/egress

go 1.18

replace github.com/tinyzimmer/go-gst v0.2.32 => github.com/livekit/go-gst v0.0.0-20220603230042-cef031256427

require (
	cloud.google.com/go/storage v1.27.0
	github.com/Azure/azure-storage-blob-go v0.14.0
	github.com/aliyun/aliyun-oss-go-sdk v2.2.4+incompatible
	github.com/aws/aws-sdk-go v1.43.3
	github.com/chromedp/cdproto v0.0.0-20220208224320-6efb837e6bc2
	github.com/chromedp/chromedp v0.7.7
	github.com/googleapis/gax-go/v2 v2.6.0
	github.com/gorilla/websocket v1.5.0
	github.com/grafov/m3u8 v0.11.1
	github.com/livekit/livekit-server v1.3.3-0.20221227214332-5d3f64466700
	github.com/livekit/mageutil v0.0.0-20221221221243-f361fbe40290
	github.com/livekit/mediatransportutil v0.0.0-20221007030528-7440725c362b
	github.com/livekit/protocol v1.3.2-0.20221228042140-d4406dd27954
	github.com/livekit/psrpc v0.2.3
	github.com/livekit/server-sdk-go v1.0.6
	github.com/pion/rtcp v1.2.10
	github.com/pion/rtp v1.7.13
	github.com/pion/webrtc/v3 v3.1.50
	github.com/prometheus/client_golang v1.14.0
	github.com/stretchr/testify v1.8.1
	github.com/tinyzimmer/go-glib v0.0.25
	github.com/tinyzimmer/go-gst v0.2.32
	github.com/urfave/cli/v2 v2.23.7
	go.uber.org/atomic v1.10.0
	google.golang.org/api v0.102.0
	google.golang.org/grpc v1.52.0
	google.golang.org/protobuf v1.28.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cloud.google.com/go v0.105.0 // indirect
	cloud.google.com/go/compute v1.12.1 // indirect
	cloud.google.com/go/compute/metadata v0.2.1 // indirect
	cloud.google.com/go/iam v0.7.0 // indirect
	github.com/Azure/azure-pipeline-go v0.2.3 // indirect
	github.com/baiyubin/aliyun-sts-go-sdk v0.0.0-20180326062324-cfa1a18b161f // indirect
	github.com/benbjohnson/clock v1.3.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bep/debounce v1.2.1 // indirect
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/chromedp/sysutil v1.0.0 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/eapache/channels v1.1.0 // indirect
	github.com/eapache/queue v1.1.0 // indirect
	github.com/elliotchance/orderedmap/v2 v2.2.0 // indirect
	github.com/frostbyte73/go-throttle v0.0.0-20210621200530-8018c891361d // indirect
	github.com/gammazero/deque v0.1.0 // indirect
	github.com/go-logr/logr v1.2.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-redis/redis/v8 v8.11.5 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.1.0 // indirect
	github.com/golang/groupcache v0.0.0-20200121045136-8c9f03a8e57e // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/google/go-cmp v0.5.9 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.2.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/jxskiss/base62 v1.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lithammer/shortuuid/v3 v3.0.7 // indirect
	github.com/lithammer/shortuuid/v4 v4.0.0 // indirect
	github.com/livekit/rtcscore-go v0.0.0-20220815072451-20ee10ae1995 // indirect
	github.com/mackerelio/go-osstat v0.2.3 // indirect
	github.com/magefile/mage v1.14.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-ieproxy v0.0.1 // indirect
	github.com/mattn/go-pointer v0.0.1 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/nats-io/nats.go v1.21.0 // indirect
	github.com/nats-io/nkeys v0.3.0 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pion/datachannel v1.5.5 // indirect
	github.com/pion/dtls/v2 v2.1.5 // indirect
	github.com/pion/ice/v2 v2.2.12 // indirect
	github.com/pion/interceptor v0.1.12 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/mdns v0.0.5 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.8.5 // indirect
	github.com/pion/sdp/v3 v3.0.6 // indirect
	github.com/pion/srtp/v2 v2.0.10 // indirect
	github.com/pion/stun v0.3.5 // indirect
	github.com/pion/transport v0.14.1 // indirect
	github.com/pion/turn/v2 v2.0.9 // indirect
	github.com/pion/udp v0.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.37.0 // indirect
	github.com/prometheus/procfs v0.8.0 // indirect
	github.com/rogpeppe/go-internal v1.9.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/satori/go.uuid v1.2.0 // indirect
	github.com/thoas/go-funk v0.9.3 // indirect
	github.com/twitchtv/twirp v8.1.3+incompatible // indirect
	github.com/xrash/smetrics v0.0.0-20201216005158-039620a65673 // indirect
	go.opencensus.io v0.23.0 // indirect
	go.uber.org/goleak v1.1.12 // indirect
	go.uber.org/multierr v1.6.0 // indirect
	go.uber.org/zap v1.24.0 // indirect
	golang.org/x/crypto v0.2.0 // indirect
	golang.org/x/net v0.4.0 // indirect
	golang.org/x/oauth2 v0.0.0-20221014153046-6fdb5e3db783 // indirect
	golang.org/x/sync v0.1.0 // indirect
	golang.org/x/sys v0.3.0 // indirect
	golang.org/x/text v0.5.0 // indirect
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0 // indirect
	golang.org/x/xerrors v0.0.0-20220907171357-04be3eba64a2 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20221118155620-16455021b5e6 // indirect
	gopkg.in/square/go-jose.v2 v2.6.0 // indirect
)
