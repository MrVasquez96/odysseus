"""Odysseus voice agent — LiveKit bridge for real-time voice chat.

Connects to a LiveKit room, uses configurable STT/LLM/TTS backends for voice,
and saves transcripts back to the Odysseus chat session so voice and text
messages share the same conversation history.

Supports fully self-hosted operation via OpenAI-compatible local LLM endpoints
(Ollama, vLLM, llama.cpp, etc.) and local STT/TTS, or cloud providers if
configured.

Environment variables:
  VOICE_LLM_ENDPOINT   - OpenAI-compatible base URL for the LLM (default: Ollama)
  VOICE_LLM_MODEL      - Model name for the LLM (default: from Odysseus config)
  VOICE_LLM_API_KEY    - API key if needed (default: "not-needed" for local)
  VOICE_STT_ENDPOINT   - OpenAI-compatible Whisper endpoint (optional, uses local faster-whisper otherwise)
  VOICE_TTS_ENDPOINT   - OpenAI-compatible TTS endpoint (optional)
  VOICE_TTS_MODEL      - TTS model name (default: "tts-1")
  VOICE_TTS_VOICE      - TTS voice (default: "alloy")
  GOOGLE_API_KEY        - If set, uses Google Gemini native audio instead (cloud)
  VOICE_MODE           - "local" (default) or "google"
"""

import json
import logging
import os 
import httpx
from livekit import agents, rtc
from livekit.agents import Agent, AgentSession
from livekit.agents.voice.room_io import RoomOptions


logger = logging.getLogger(__name__)

def internal_api_base() -> str:
    """Base URL for in-process loopback calls to Odysseus's own API.

    Agent tools and background jobs reach admin-gated routes by calling the
    running server over HTTP. Resolution order:
      1. ODYSSEUS_INTERNAL_BASE  - explicit override (e.g. behind a TLS proxy).
      2. APP_PORT                - http://127.0.0.1:$APP_PORT (docker-compose).
      3. Fallback http://127.0.0.1:7000 - legacy default.

    127.0.0.1 (not "localhost") avoids IPv6/DNS ambiguity for a strictly-local
    call. Without this, loopback tools fail with "All connection attempts
    failed" whenever the server is not on port 7000.
    """
    override = os.environ.get("ODYSSEUS_INTERNAL_BASE")
    if override:
        return override.rstrip("/")
    return f"http://127.0.0.1:{os.environ.get('APP_PORT', '7000')}"

# ── Voice Configuration ────────────────────────────────────────────────────
VOICE_MODE = os.getenv("VOICE_MODE", "local")  # "local" or "google"

# Local LLM settings (OpenAI-compatible endpoint)
VOICE_LLM_ENDPOINT = os.getenv("VOICE_LLM_ENDPOINT", "")
VOICE_LLM_MODEL = os.getenv("VOICE_LLM_MODEL", "")
VOICE_LLM_API_KEY = os.getenv("VOICE_LLM_API_KEY", "not-needed")

# Local STT settings
VOICE_STT_ENDPOINT = os.getenv("VOICE_STT_ENDPOINT", "")
VOICE_STT_MODEL = os.getenv("VOICE_STT_MODEL", "Systran/faster-whisper-small")

# Local TTS settings
VOICE_TTS_ENDPOINT = os.getenv("VOICE_TTS_ENDPOINT", "")
VOICE_TTS_MODEL = os.getenv("VOICE_TTS_MODEL", "tts-1")
VOICE_TTS_VOICE = os.getenv("VOICE_TTS_VOICE", "alloy")

# Google Gemini settings (cloud fallback)
GOOGLE_VOICE_MODEL = os.getenv("VOICE_MODEL", "gemini-2.5-flash-native-audio-preview-12-2025")
GOOGLE_VOICE_NAME = os.getenv("VOICE_NAME", "Aoede")

# Internal API base for saving transcripts
ODYSSEUS_BASE = os.getenv("ODYSSEUS_INTERNAL_BASE", "http://127.0.0.1:7000")
INTERNAL_TOKEN_PATH = os.getenv("INTERNAL_TOKEN_PATH", "/app/data/.internal_token")

OLLAMA_BASE_URL = os.getenv("OLLAMA_BASE_URL", None)
 

DEFAULT_INSTRUCTIONS= (
  "You are Odysseus, a helpful AI assistant. You are in a voice conversation. "
  "Be conversational, concise, and helpful. Speak naturally as if having a "
  "real-time conversation. Do not use markdown or special formatting in your "
  "responses — speak in plain language."
)

def _mem_text_instructions(username:str, mem_text: str) -> str:
  return (
    "You are Odysseus, a helpful AI assistant in a voice conversation. "
    "Be conversational, concise, and helpful. Speak naturally.\n\n"
    f"# Memories about {username}\n{mem_text}"
)

MEMORY_LOAD_INSTRUCTIONS = _mem_text_instructions
def _read_internal_token() -> str:
    """Read the shared internal token for Go API calls."""
    try:
        with open(INTERNAL_TOKEN_PATH) as f:
            return f.read().strip()
    except FileNotFoundError:
        return ""


def _save_transcript(session_id: str, role: str, content: str):
    """Save a voice transcript to the Odysseus chat session via Go API."""
    if not content.strip():
        return
    token = _read_internal_token()
    try:
        httpx.post(
            f"{internal_api_base()}/api/voice/transcript",
            json={
                "session_id": session_id,
                "role": role,
                "content": content,
                "source": "voice",
            },
            headers={
                "X-Internal-Token": token,
                "Content-Type": "application/json",
            },
            timeout=10,
        )
    except Exception as e:
        logger.warning("Failed to save transcript: %s", e)


def _resolve_llm_endpoint() -> str:
    """Resolve the LLM endpoint from voice config or Odysseus settings."""
    if VOICE_LLM_ENDPOINT:
        return VOICE_LLM_ENDPOINT

    # Try Ollama default
    if OLLAMA_BASE_URL:
        return OLLAMA_BASE_URL + "/v1" if not OLLAMA_BASE_URL.endswith("/v1") else OLLAMA_BASE_URL

    # Fall back to host.docker.internal (common Docker Ollama setup)
    return "http://host.docker.internal:11434/v1"


def _resolve_llm_model() -> str:
    """Resolve the LLM model from voice config or Odysseus settings."""
    if VOICE_LLM_MODEL:
        return VOICE_LLM_MODEL

    # Try to get default model from Odysseus
    try:
        token = _read_internal_token()
        resp = httpx.get(
            f"{internal_api_base()}/api/default-chat",
            headers={"X-Internal-Token": token},
            timeout=5,
        )
        if resp.status_code == 200:
            data = resp.json()
            model = data.get("model", "")
            if model:
                return model
    except Exception:
        pass

    return "llama3.2"  # sensible default for Ollama


def _build_local_session(instructions: str | None) -> AgentSession:
    """Build an AgentSession using local OpenAI-compatible endpoints."""
    from livekit.plugins import openai, silero

    llm_endpoint = _resolve_llm_endpoint()
    llm_model = _resolve_llm_model()

    logger.info("Voice LLM: %s model=%s", llm_endpoint, llm_model)

    llm = openai.LLM(
        base_url=llm_endpoint,
        api_key=VOICE_LLM_API_KEY,
        model=llm_model,
    )

    # STT: use OpenAI-compat endpoint if configured, else fall back to
    # Silero VAD + local faster-whisper (which requires faster_whisper pip pkg)
    if VOICE_STT_ENDPOINT:
        logger.info("Voice STT: OpenAI-compat at %s", VOICE_STT_ENDPOINT)
        stt = openai.STT(
            base_url=VOICE_STT_ENDPOINT,
            api_key=VOICE_LLM_API_KEY,
            model=VOICE_STT_MODEL,
        )
    else:
        # Use Silero VAD — lightweight, runs locally, no GPU needed.
        # The pipeline will use STT from whichever plugin is available.
        logger.info("Voice STT: Silero VAD + default STT pipeline")
        stt = None  # AgentSession handles VAD-based pipeline

    # TTS: use OpenAI-compat endpoint if configured
    if VOICE_TTS_ENDPOINT:
        logger.info("Voice TTS: OpenAI-compat at %s", VOICE_TTS_ENDPOINT)
        tts = openai.TTS(
            base_url=VOICE_TTS_ENDPOINT,
            api_key=VOICE_LLM_API_KEY,
            model=VOICE_TTS_MODEL,
            voice=VOICE_TTS_VOICE,
        )
    else:
        tts = None

    session_kwargs = {"llm": llm}
    if stt:
        session_kwargs["stt"] = stt
    if tts:
        session_kwargs["tts"] = tts
    else:
        logger.warning(
            "No TTS endpoint configured (VOICE_TTS_ENDPOINT). "
            "The agent can hear and think but won't speak back. "
            "Set VOICE_TTS_ENDPOINT to an OpenAI-compatible TTS server "
            "(e.g. openedai-speech, Piper, or Kokoro) for voice output."
        )

    # Add VAD if available
    try:
        session_kwargs["vad"] = silero.VAD.load()
    except Exception as e:
        logger.warning("Could not load Silero VAD: %s", e)

    return AgentSession(**session_kwargs)


def _build_google_session(instructions: str | None) -> AgentSession:
    """Build an AgentSession using Google Gemini native audio."""
    from google.genai import types
    from livekit.plugins import google

    return AgentSession(
        llm=google.realtime.RealtimeModel(
            model=GOOGLE_VOICE_MODEL,
            voice=GOOGLE_VOICE_NAME,
            thinking_config=types.ThinkingConfig(thinking_budget=0),
            temperature=0.8,
        ),
    )

class OdysseusAgent(Agent):
    """Voice agent with Odysseus system prompt."""

    def __init__(self, instructions: str = "") -> None:
        
        super().__init__(instructions=instructions or DEFAULT_INSTRUCTIONS)


async def entrypoint(ctx: agents.JobContext):
    await ctx.connect()

    # Wait for the user participant so we reliably get their metadata.
    participant = await ctx.wait_for_participant()
    session_id = None
    username = None

    if participant and participant.metadata:
        try:
            meta = json.loads(participant.metadata)
            session_id = meta.get("session_id")
            username = meta.get("username")
        except (json.JSONDecodeError, TypeError):
            pass

    if session_id:
        logger.info("Voice session for user=%s, session=%s", username, session_id)
    else:
        logger.warning("No session_id in participant metadata — transcripts won't be saved")

    # Load memories for context if available
    instructions = None
    if username and session_id:
        try:
            token = _read_internal_token()
            resp = httpx.get(
                f"{internal_api_base()}/api/memories",
                params={"limit": 50},
                headers={"X-Internal-Token": token},
                timeout=10,
            )
            if resp.status_code == 200:
                memories = resp.json()
                if memories:
                    mem_text = "\n".join(
                        f"- {m.get('text', '')}" for m in memories[:30]
                    )
                    instructions = MEMORY_LOAD_INSTRUCTIONS(username, mem_text)
        except Exception as e:
            logger.warning("Failed to load memories for voice context: %s", e)

    # Build session based on configured mode
    mode = VOICE_MODE
    google_key = os.getenv("GOOGLE_API_KEY", "")

    # Auto-detect: use Google if key is set and mode is "google", else local
    if mode == "google" and google_key:
        logger.info("Voice mode: Google Gemini native audio")
        session = _build_google_session(instructions)
    else:
        if mode == "google" and not google_key:
            logger.warning("VOICE_MODE=google but GOOGLE_API_KEY not set — falling back to local")
        logger.info("Voice mode: local (OpenAI-compatible endpoints)")
        session = _build_local_session(instructions)

    # Track transcripts for saving
    if session_id:
        _wire_transcript_saving(session, ctx.room, session_id)

    await session.start(
        room=ctx.room,
        agent=OdysseusAgent(instructions=instructions or ""),
        room_options=RoomOptions(video_input=False),
    )

    # Greeting
    await session.generate_reply(
        instructions="Greet the user briefly. Say something like 'Hey, how can I help?'"
    )


def _wire_transcript_saving(session: AgentSession, room: rtc.Room, session_id: str):
    """Wire up event handlers to save voice transcripts to the chat session."""

    @room.on("transcription_received")
    def on_transcription(ev):
        """Handle transcription events from LiveKit."""
        if not ev.segments:
            return
        for seg in ev.segments:
            if seg.final:
                is_agent = ev.participant and ev.participant.identity and "agent" in ev.participant.identity.lower()
                role = "assistant" if is_agent else "user"
                _save_transcript(session_id, role, seg.text)


if __name__ == "__main__":
    agents.cli.run_app(agents.WorkerOptions(entrypoint_fnc=entrypoint))
