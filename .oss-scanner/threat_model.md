# LiveKit Egress threat model

`github.com/livekit/egress` records and streams LiveKit rooms. It subscribes to
participants' tracks, or renders a web page in headless Chrome, encodes the
result with GStreamer through cgo, and writes it to files, HLS segments,
images, RTMP/SRT/WebSocket streams, and cloud storage uploads.

One binary runs in two roles. The long-lived service (`egress`) takes start
requests from the RPC bus, serves the built-in room-composite templates on
localhost, and starts a handler subprocess (`egress run-handler`) for each
egress. For web sources the handler launches Xvfb and Chrome and loads a
PulseAudio sink with `pactl` (the PulseAudio daemon itself is started outside
the binary). For track sources it joins the room through the Go SDK. It then runs the GStreamer
pipeline and uploads the output. A fault in the service affects every egress
on the host. A fault in a handler affects one egress.

## Scope and trust boundaries

Audit the code on `main`. Prioritise:

- `pkg/config/`: request validation and the pipeline config built from it:
  web and custom base URLs (`isHttp`), output URLs per scheme (`urls.go`,
  `output*.go`), filename templating and cleaning (`clean`,
  `replaceAndClean`), and storage config, including request credentials,
  custom endpoints and proxies, and the S3 assume-role external-ID checks
  (`storage.go`).
- `pkg/pipeline/sink/uploader/`: the storage-prefix boundary and the
  choice of backend, local or cloud.
- `pkg/pipeline/source/`: the web source (Xvfb, PulseAudio, the Chrome flags,
  navigation, and the `START_RECORDING`/`END_RECORDING` console signals) and
  the SDK source (subscribing, RTP depacketizing, gap filling, and
  `{publisher_identity}` substitution).
- `pkg/pipeline/builder/`, `pkg/pipeline/sink/`, `pkg/gstreamer/`: the
  pipeline graph, segment and playlist writing, and image and stream sinks.
- `pkg/server/`, `pkg/service/`, `pkg/handler/`, `pkg/ipc/conn.go`: request
  intake, the process manager, the template and debug servers, and IPC
  between the service and its handlers.
- `template-default/`, `template-sdk/`: the pages Chrome loads for room
  composites, which hold a room token and read their parameters from the URL.

Out of scope: generated code (`pkg/ipc/*.pb.go`, `pkg/service/servicefakes`),
the integration harness under `test/`, `build/`, and `magefile.go`. Out of
scope also are bugs inside upstream dependencies: Chrome, GStreamer and its
plugins, pion, the Go SDK, and `livekit/storage`. Report how this repository
uses them, for example a Chrome flag or a storage key it should not pass, but
not a bug that reproduces without this code.

Untrusted input:

- **API callers (hold the API key and secret).** Every field of an egress
  request: web URLs, custom template base URLs, layouts, file paths and
  filename templates, stream and WebSocket URLs, segment and image settings,
  and upload destinations with their credentials, endpoints, proxies, and
  assume-role ARNs and external IDs. API callers are trusted to record their
  own rooms into their own storage. They are not trusted with the egress host,
  its local network, operator-configured storage credentials, or other
  callers' storage prefixes and egresses.
- **Web content.** Everything Chrome loads from a caller's URL, including
  redirects, subresources, and script. It runs in Chrome on the egress host,
  and by default without Chrome's sandbox (`enable_chrome_sandbox` is off).
- **Room participants (hold only a room token, or none).** The media they
  publish (Opus, H.264, VP8, VP9, and the RTP/RTCP around it) is depacketized
  in Go and decoded by GStreamer. Their identities and names can flow into
  filenames through `{publisher_identity}`, and into the composite template
  page.
- **Remote endpoints.** Responses from stream servers, WebSocket peers, and
  storage endpoints that a caller chose.

Trusted: the operator's config file (including `storage`, `insecure`,
`enable_chrome_sandbox`, `disallow_local_storage`, and the configured S3
assume-role key and external ID), the API secret, the redis message bus and the
start requests that arrive over it after the LiveKit server has authenticated
the caller, and the handler subprocesses and their IPC sockets.

LiveKit server (`livekit/livekit`) is enrolled separately, and
`livekit/protocol` has its own threat model. Report bugs in request validation, pipeline setup, and uploads
here. Report bugs in egress authorization or the shared request types in those
repositories.

## Security properties

- **Web sources:** a caller URL must start as `http` or `https`. Rendering it
  must not give the page local file reads, control of Chrome beyond the page,
  or a way to read or change state through the egress host's own listeners
  (template server, health, Prometheus, debug handler, IPC sockets).
- **File paths:** a caller's file path or filename template, after
  substitution of room names and participant identities, must stay inside the
  configured storage prefix, and inside the local storage directory when no
  upload is configured. It must not overwrite files outside the output
  location, including on the handler's temporary directory.
- **Storage credentials:** a request must not make the egress use operator
  storage credentials or the operator's assume-role key for a destination or
  role the operator did not configure. Callers' credentials must not appear in
  logs, errors, or the `EgressInfo` that the service reports.
- **Isolation:** one egress must not read, overwrite, or end another egress's
  output, media, or session, whether through shared temporary paths, handler
  IDs, or the template server.
- **Participant media:** malformed RTP or codec payloads from a participant
  must not crash the service, hang it, or exhaust memory, and should end at
  most the egress that subscribed to them.
- **Tokens:** the room token that arrives with a request must go only to the
  SDK and to the template page at the request's base URL, and must not appear
  in logs at default levels.

## Severity guidance

- **Critical:** code execution on the egress host from web content,
  participant media, or a request field, caused by this repository's code or
  its Chrome or pipeline setup. Also critical: using one caller's request to
  write into, or read from, another caller's storage.
- **High:** reading local files or internal service responses through a web
  source, or exfiltrating them into a recording; escaping the storage prefix
  or local storage directory; using operator storage credentials for a
  caller-chosen destination; leaking upload credentials or room tokens in
  output the caller can see; a crash or hang of the service process from one
  request or one participant.
- **Medium:** a handler-only crash or unbounded resource use from participant
  media; reaching internal network addresses through a web source, stream
  output, or storage endpoint without returning their contents; credentials or
  tokens in logs.
- **Low / not a finding:** behaviour that needs operator config (`insecure`,
  debug and Prometheus ports, local storage on a single-tenant host), or the
  API secret used within its own project and storage. Not a finding either: a
  caller exhausting its own resources, bugs in generated or test code, or an
  upstream Chrome or GStreamer bug that reproduces without this code.

## Testing

The checkout is at `/src`. Go modules and the build cache are in the image, and
`GOPROXY=off` and `GOSUMDB=off` prevent downloads. GStreamer is installed
system-wide. Google Chrome, Xvfb, and PulseAudio are installed, the built
templates are in `cmd/server/templates/`, and the binary is at
`/usr/local/bin/egress`.

These are the package tests CI runs. They need no redis, room server, or
Chrome. Without network, `TestValidateUrl` and `TestGetUrl` in `pkg/config`
fail; everything else passes. CI's integration build runs them with
`-tags deadlock`:

```sh
cd /src
go test -count=1 -timeout 10m ./pkg/...
go test -count=1 -timeout 10m -tags deadlock ./pkg/...
```

Upload tests in `pkg/pipeline/sink/uploader` skip unless real bucket
credentials are set. The integration suite in `test/` (build tag
`integration`) needs a LiveKit server, media samples, and network storage, so
it does not run here. For request-validation and path bugs, write a Go test in
`pkg/config`. For web-source behaviour, start PulseAudio
(`pulseaudio -D --exit-idle-time=-1 --disallow-exit`) and run Chrome directly
(`google-chrome --headless=new --no-sandbox <url>`) against a local file or
server, with the flags in `pkg/pipeline/source/web.go`. A local redis is
installed (`redis-server --daemonize yes`) for running the service itself.

## Reports

State the attacker's role (API caller, web content, or room participant), the
request field or media path, the process affected (service, handler, or
Chrome), and the demonstrated impact. Provide a minimal reproducer, preferably
a Go test in the affected package or the egress request that triggers it.
