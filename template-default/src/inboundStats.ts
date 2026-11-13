/**
 * Copyright 2026 LiveKit, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { Room } from 'livekit-client';

const INBOUND_STATS_LOG = 'INBOUND_RTP_STATS';
const INBOUND_STATS_INTERVAL = 5000;

interface JitterBufferTotals {
  delay: number;
  targetDelay: number;
  minimumDelay: number;
  emitted: number;
}

const round = (v: number | undefined) => (v === undefined ? undefined : Math.round(v * 10) / 10);

// Mean ms per emitted audio sample or video frame since the previous read.
const averageMs = (total: number | undefined, prev: number, emitted: number) =>
  total === undefined || emitted <= 0 ? undefined : round(((total - prev) / emitted) * 1000);

export function startInboundStatsLogging(room: Room): () => void {
  const previous = new Map<string, JitterBufferTotals>();
  let sampling = false;

  const sample = async () => {
    const reads = [];
    const present = new Set<string>();
    for (const p of Array.from(room.remoteParticipants.values())) {
      for (const pub of Array.from(p.trackPublications.values())) {
        if (pub.track) {
          const identity = p.identity;
          present.add(pub.trackSid);
          reads.push(
            pub.track
              .getRTCStatsReport()
              .catch(() => undefined)
              .then((report) => ({ identity, pub, report })),
          );
        }
      }
    }
    const results = await Promise.all(reads);

    for (const { identity, pub, report } of results) {
      report?.forEach((s) => {
        if (s.type !== 'inbound-rtp') {
          return;
        }
        const stats = s as RTCInboundRtpStreamStats;

        const zero = { delay: 0, targetDelay: 0, minimumDelay: 0, emitted: 0 };
        let prev = previous.get(pub.trackSid) ?? zero;
        if ((stats.jitterBufferEmittedCount ?? 0) < prev.emitted) {
          prev = zero;
        }
        const emitted = (stats.jitterBufferEmittedCount ?? 0) - prev.emitted;
        previous.set(pub.trackSid, {
          delay: stats.jitterBufferDelay ?? 0,
          targetDelay: stats.jitterBufferTargetDelay ?? 0,
          minimumDelay: stats.jitterBufferMinimumDelay ?? 0,
          emitted: stats.jitterBufferEmittedCount ?? 0,
        });

        const line = {
          participant: identity,
          trackSid: pub.trackSid,
          trackSource: pub.source,
          kind: stats.kind,
          sampledAt: round(stats.timestamp),
          estimatedPlayoutTimestamp: round(stats.estimatedPlayoutTimestamp),
          // Sender clock time now playing minus local sample time; audio minus video is the A/V playout offset.
          playoutClockMs:
            stats.estimatedPlayoutTimestamp === undefined
              ? undefined
              : round(stats.estimatedPlayoutTimestamp - stats.timestamp),
          jitterBufferMs: averageMs(stats.jitterBufferDelay, prev.delay, emitted),
          jitterBufferTargetMs: averageMs(stats.jitterBufferTargetDelay, prev.targetDelay, emitted),
          jitterBufferMinimumMs: averageMs(stats.jitterBufferMinimumDelay, prev.minimumDelay, emitted),
          jitterBufferEmitted: emitted,
          jitterMs: stats.jitter === undefined ? undefined : round(stats.jitter * 1000),
          packetsReceived: stats.packetsReceived,
          packetsLost: stats.packetsLost,
          framesDecoded: stats.framesDecoded,
          framesDropped: stats.framesDropped,
          freezeCount: stats.freezeCount,
          totalFreezesDuration: round(stats.totalFreezesDuration),
        };
        console.log(INBOUND_STATS_LOG, JSON.stringify(line));
      });
    }

    for (const sid of Array.from(previous.keys())) {
      if (!present.has(sid)) {
        previous.delete(sid);
      }
    }
  };

  const interval = setInterval(() => {
    if (sampling) {
      return;
    }
    sampling = true;
    sample()
      .catch(() => {})
      .finally(() => {
        sampling = false;
      });
  }, INBOUND_STATS_INTERVAL);

  return () => clearInterval(interval);
}
