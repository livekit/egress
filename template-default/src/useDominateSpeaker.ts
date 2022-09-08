import { Participant } from 'livekit-client';
import { useEffect, useState } from 'react';

export default function useDominateSpeaker(participants: Participant[]): Participant | undefined {
  const [dominantSpeaker, setDominateSpeaker] = useState<Participant>();

  useEffect(() => {
    if (dominantSpeaker) {
      if (dominantSpeaker.isSpeaking) {
        // no changes as long as current speaker is still speaking
        return;
      } if (dominantSpeaker.lastSpokeAt
        && Date.now() - dominantSpeaker.lastSpokeAt.getTime() > 5000) {
        return;
      }
    }
    let newDominateSpeaker: Participant | undefined = dominantSpeaker;
    for (const p of participants) {
      if (p.isSpeaking) {
        if (newDominateSpeaker === undefined || p.audioLevel > newDominateSpeaker.audioLevel) {
          newDominateSpeaker = p;
        }
      }
    }
    if (newDominateSpeaker !== dominantSpeaker) {
      setDominateSpeaker(newDominateSpeaker);
    }
  }, [participants]);

  return dominantSpeaker;
}
