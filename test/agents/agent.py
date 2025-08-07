from dotenv import load_dotenv
from livekit.agents import (
    Agent,
    AgentSession,
    JobContext,
    WorkerOptions,
    cli,
)
from livekit.plugins import deepgram, elevenlabs, openai, silero


load_dotenv(dotenv_path=".env", override=True)


async def entrypoint(ctx: JobContext):
    await ctx.connect()

    agent0 = Agent(
        instructions="You hosting a podcast, and you will be having a conversation with your guest."
                     " The audio from this conversation will be streamed to live listeners."
                     " Ask engaging questions and keep the conversation flowing.",
        turn_detection="vad"
    )
    agent1 = Agent(
        instructions="You are a guest on a podcast."
                     " The audio from this conversation will be streamed to live listeners."
                     " Choose a field of study and expertise, and talk about recent developments in that field.",
        turn_detection="vad",
    )
    session0 = AgentSession(
        vad=silero.VAD.load(),
        stt=deepgram.STT(model="nova-3"),
        llm=openai.LLM(model="gpt-4o-mini"),
        tts=elevenlabs.TTS(),
    )
    session1 = AgentSession(
        vad=silero.VAD.load(),
        stt=deepgram.STT(model="nova-3"),
        llm=openai.LLM(model="gpt-4o-mini"),
        tts=elevenlabs.TTS(),
    )

    await session0.start(agent=agent0, room=ctx.room)
    await session1.start(agent=agent1, room=ctx.room)
    await session0.generate_reply(instructions="Greet your guest and ask them about their field of study.")


if __name__ == "__main__":
    cli.run_app(WorkerOptions(entrypoint_fnc=entrypoint, agent_name="egress-integration"))
