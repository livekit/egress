# Pass-through integration test scenarios

Test scenarios for the H.264 video pass-through feature (branch
`feature/egress-h264-passthrough`). These are deliberately failure-focused:
without a decoder in the path there is no packet loss concealment, so a
corrupted bitstream reaches h264parse/mpegtsmux directly (the issue #723 error
class). Every scenario must degrade gracefully - a stalled playlist or a
crashed pipeline is a failure.

## Environment assumptions

- A local LiveKit stack running with host networking: LiveKit server
  (`ws://localhost:7880`, RTC UDP 50000-60000 by default), Ingress (WHIP on
  `http://localhost:8080/w`, RTC UDP 7885), Egress built from this branch with
  `enable_video_passthrough: true`. Adjust ports to your deployment.
- `lk` CLI on the host.
- `tc` (iproute2) on the host for netem, root required.
- Scripts require `LIVEKIT_API_KEY` and `LIVEKIT_API_SECRET` in the
  environment; `LIVEKIT_URL` defaults to `ws://localhost:7880`. Set
  `EGRESS_CONTAINER` to the egress container name for `measure-cpu.sh`.

## Scripts

| Script | Purpose |
|---|---|
| `publish.sh` | Synthetic OBS-like publisher: pre-encoded H.264 (2s fixed GOP, zerolatency) + Opus audio over WHIP bypass. Supports `pause`/`resume`/`stop` for mute/disconnect scenarios. |
| `egress.sh` | Starts a Participant egress with HLS segment output for the test room. `--transcode` adds an encoding preset, which forces the decode+encode fallback - same deployment, both modes. |
| `netem.sh` | Applies/removes packet loss on a UDP port range on `lo` (host networking → both legs cross loopback). |
| `measure-cpu.sh` | Samples the egress container CPU to CSV and prints the average. Run once in pass-through and once in `--transcode` mode for the 5.6 comparison. |
| `check-avsync.sh` | ffprobe audio/video first-PTS offset per segment, for the A/V sync scenario. |

## Scenarios

### 5.1 Packet loss publisher↔ingress

```bash
./publish.sh start
./egress.sh start
sudo ./netem.sh apply 7885 5%       # ingress RTC port
# observe 2-3 minutes, then:
sudo ./netem.sh clear
```

Pass criteria: egress handler log shows PLIs being sent and possibly dropped
buffers, but no pipeline error/crash; playlist keeps advancing (frozen or
degraded frames acceptable); egress survives until `lk egress stop`.

### 5.2 Packet loss SFU↔egress

Same as 5.1 but against the SFU RTC range:

```bash
sudo ./netem.sh apply 50000-60000 5%
```

Note: the SFU range is shared with any other room traffic on the stack - run
during a quiet window. Watch for `keyframe probe` PLI logs and jitter buffer
drops; same pass criteria.

### 5.3 Publisher mute mid-session (validates the freeze fallback)

```bash
./publish.sh start
./egress.sh start
sleep 60
./publish.sh pause          # SIGSTOP - RTP stops, appwriter inactivity fires
sleep 30
./publish.sh resume
```

Pass criteria: within ~2s of pause the handler logs `video freeze started`;
playlist keeps gaining segments (frozen frame) at the nominal cadence; on
resume, `video freeze ended` + `sending PLI to publisher`, live video returns
within one GOP + RTT. Player must not stall at any point.

### 5.4 Publisher disconnect/reconnect

```bash
./publish.sh stop           # hard disconnect
sleep 20
./publish.sh start          # new track, same identity
```

Pass criteria: on disconnect the freeze fallback holds the playlist; on
reconnect the new track is picked up (Participant egress resubscribes), freeze
ends, video resumes. The pipeline must not hang permanently even if the
publisher never returns (session limits still apply).

### 5.5 A/V sync: pass-through video + transcoded audio

Audio is always transcoded (Opus→AAC) while video passes through, so their
pipeline latencies differ. After running 5.1-5.4, download a window of
segments (start / after loss / after mute / after reconnect) and run:

```bash
./check-avsync.sh <segment-dir>
```

Pass criteria: audio/video first-PTS offset per segment stays stable (no
cumulative drift) and within ±100ms of the session's initial offset,
including after loss/mute events.

### 5.6 CPU comparison (the point of all this)

Two 30-minute runs with the same publisher settings:

```bash
./publish.sh start
./egress.sh start                        # pass-through
./measure-cpu.sh passthrough.csv 1800
./egress.sh stop

./egress.sh start --transcode           # preset forces decode+encode fallback
./measure-cpu.sh transcode.csv 1800
./egress.sh stop
```

Report the two averages. No redeploy needed between runs - the `--transcode`
egress carries an encoding preset, which the config gate treats as a
transcoding request.
