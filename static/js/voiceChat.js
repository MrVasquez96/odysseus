/**
 * voiceChat.js — LiveKit real-time voice integration for Odysseus.
 *
 * Adds a "Voice" button to the chat input bar. When connected, the user
 * can speak in real time and an AI voice agent responds via LiveKit.
 * Voice transcripts are saved to the same chat session so text and voice
 * messages share the same conversation thread.
 *
 * Live transcript segments appear in the chat as they are spoken, updating
 * word-by-word (partial at reduced opacity, final at full opacity).
 */

import sessionModule from './sessions.js';
import uiModule from './ui.js';
import { addMessage } from './chatRenderer.js';

// ── LiveKit SDK (loaded from CDN on first connect) ──────────────────
let _livekitLoaded = false;
let _livekitLoadPromise = null;

function _ensureLiveKitSDK() {
    if (_livekitLoaded) return Promise.resolve();
    if (_livekitLoadPromise) return _livekitLoadPromise;
    _livekitLoadPromise = new Promise((resolve, reject) => {
        const s = document.createElement('script');
        s.src = 'https://cdn.jsdelivr.net/npm/livekit-client@2/dist/livekit-client.umd.min.js';
        const existing = document.querySelector('script[nonce]');
        if (existing) s.nonce = existing.nonce;
        s.onload = () => { _livekitLoaded = true; resolve(); };
        s.onerror = () => reject(new Error('Failed to load LiveKit SDK'));
        document.head.appendChild(s);
    });
    return _livekitLoadPromise;
}

// ── State ────────────────────────────────────────────────────────────
let _room = null;
let _muted = false;
let _voiceEnabled = null; // null = unknown, true/false = checked
let _agentSpeaking = false;

// Live transcript segments: segId → DOM element
const _transcriptSegments = new Map();

// ── DOM references (set in init) ────────────────────────────────────
let _voiceBtn = null;
let _voiceStatus = null;
let _voiceDisconnect = null;
let _voiceMute = null;
let _voiceBar = null;

// ── Check if voice is available ─────────────────────────────────────
async function checkVoiceStatus() {
    try {
        const res = await fetch('/api/voice/status', { credentials: 'same-origin' });
        if (res.ok) {
            const data = await res.json();
            _voiceEnabled = data.enabled === true;
        } else {
            _voiceEnabled = false;
        }
    } catch {
        _voiceEnabled = false;
    }
    _updateUI();
    return _voiceEnabled;
}

// ── Connect to LiveKit room ─────────────────────────────────────────
async function connect() {
    if (_room) return; // already connected

    let sessionId = typeof sessionModule.getCurrentSessionId === 'function'
        ? sessionModule.getCurrentSessionId()
        : sessionModule.currentSessionId;

    // Auto-create a session if none exists
    if (!sessionId) {
        try {
            // Materialize pending chat if one exists (user picked a model)
            if (sessionModule.hasPendingChat?.()) {
                const ok = await sessionModule.materializePendingSession();
                if (ok) sessionId = sessionModule.getCurrentSessionId();
            }
            // Still no session — create one from the default chat config
            if (!sessionId) {
                let dc = window.__odysseusDefaultChat || null;
                if (!dc) {
                    try {
                        const dcRes = await fetch('/api/default-chat');
                        if (dcRes.ok) dc = await dcRes.json();
                    } catch (_) {}
                }
                if (dc?.endpoint_url && dc?.model) {
                    sessionModule.createDirectChat(dc.endpoint_url, dc.model, dc.endpoint_id);
                    const ok = await sessionModule.materializePendingSession();
                    if (ok) sessionId = sessionModule.getCurrentSessionId();
                }
            }
            // Last resort — create a bare session via POST /api/session
            if (!sessionId) {
                try {
                    const fd = new FormData();
                    fd.append('name', 'Voice ' + new Date().toLocaleTimeString());
                    const res = await fetch('/api/session', { method: 'POST', body: fd });
                    if (res.ok) {
                        const payload = await res.json();
                        if (payload?.id) {
                            sessionId = payload.id;
                            if (sessionModule.loadSessions) await sessionModule.loadSessions().catch(() => {});
                        }
                    }
                } catch (_) {}
            }
        } catch (e) {
            console.error('Voice: failed to auto-create session:', e);
        }
        if (!sessionId) {
            uiModule.showToast?.('Could not create a chat session — select a model first', 'error');
            return;
        }
    }

    _setConnecting(true);

    try {
        await _ensureLiveKitSDK();
        const LK = window.LivekitClient;
        if (!LK) throw new Error('LiveKit SDK not available');

        const tokenRes = await fetch('/api/voice/token', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ session_id: sessionId }),
        });
        if (!tokenRes.ok) {
            const err = await tokenRes.json().catch(() => ({}));
            throw new Error(err.error || 'Failed to get voice token');
        }
        const { token, url } = await tokenRes.json();

        _room = new LK.Room({
            adaptiveStream: true,
            dynacast: true,
        });

        _room.on(LK.RoomEvent.Disconnected, () => {
            _cleanup();
            _updateUI();
        });

        // Attach agent audio output
        _room.on(LK.RoomEvent.TrackSubscribed, (track) => {
            if (track.kind === 'audio') {
                const el = track.attach();
                el.id = 'voice-agent-audio';
                document.body.appendChild(el);
            }
        });

        _room.on(LK.RoomEvent.TrackUnsubscribed, (track) => {
            if (track.kind === 'audio') {
                track.detach().forEach(el => el.remove());
            }
        });

        // ── Listening / Speaking indicator ──────────────────────────
        _room.on(LK.RoomEvent.ActiveSpeakersChanged, (speakers) => {
            const speaking = speakers.some(
                p => p.identity !== _room.localParticipant.identity
            );
            _setAgentSpeaking(speaking);
        });

        _room.on(LK.RoomEvent.ParticipantConnected, () => {
            _setAgentSpeaking(false);
        });

        // ── Live transcription ─────────────────────────────────────
        _room.on(LK.RoomEvent.TranscriptionReceived, (segments, participant) => {
            const isMe = participant?.identity === _room?.localParticipant?.identity;
            for (const seg of segments) {
                const text = seg.text?.trim();
                if (!text || text === '<noise>') continue;

                const role = isMe ? 'user' : 'assistant';
                _upsertTranscript(seg.id, role, text, seg.final);

                // Save final transcript to chat session
                if (seg.final) {
                    _saveTranscript(role, text);
                }
            }
        });

        await _room.connect(url, token);
        await _room.localParticipant.setMicrophoneEnabled(true);
        _muted = false;

    } catch (e) {
        console.error('Voice connect failed:', e);
        uiModule.showToast?.('Voice: ' + e.message, 'error');
        _cleanup();
    }

    _setConnecting(false);
    _updateUI();
}

// ── Disconnect ──────────────────────────────────────────────────────
async function disconnect() {
    if (!_room) return;
    await _room.disconnect();
    _cleanup();
    _updateUI();
}

// ── Toggle mute ─────────────────────────────────────────────────────
async function toggleMute() {
    if (!_room) return;
    _muted = !_muted;
    await _room.localParticipant.setMicrophoneEnabled(!_muted);
    _updateUI();
}

// ── Live Transcript Rendering ───────────────────────────────────────

function _upsertTranscript(segId, role, text, isFinal) {
    const chatHistory = document.getElementById('chat-history');
    if (!chatHistory) return;

    let el = _transcriptSegments.get(segId);
    if (!el) {
        // Create a new live transcript bubble
        el = document.createElement('div');
        el.className = 'voice-transcript-bubble voice-transcript-' + role;
        el.dataset.segId = segId;

        const label = document.createElement('span');
        label.className = 'voice-transcript-label';
        label.textContent = role === 'user' ? 'You' : 'Odysseus';
        el.appendChild(label);

        const content = document.createElement('span');
        content.className = 'voice-transcript-content';
        el.appendChild(content);

        chatHistory.appendChild(el);
        _transcriptSegments.set(segId, el);
    }

    // Update text content
    const content = el.querySelector('.voice-transcript-content');
    if (content) content.textContent = text;

    // Partial = dimmed, final = full opacity
    el.style.opacity = isFinal ? '1' : '0.55';

    if (isFinal) {
        el.classList.add('voice-transcript-final');
        _transcriptSegments.delete(segId);
    }

    // Auto-scroll
    chatHistory.scrollTop = chatHistory.scrollHeight;
}

function _saveTranscript(role, text) {
    if (!text?.trim()) return;
    const sessionId = typeof sessionModule.getCurrentSessionId === 'function'
        ? sessionModule.getCurrentSessionId()
        : sessionModule.currentSessionId;
    if (!sessionId) return;

    fetch('/api/voice/transcript', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            session_id: sessionId,
            role: role,
            content: text,
            source: 'voice',
        }),
    }).catch(e => console.warn('Voice transcript save failed:', e));
}

// ── Helpers ──────────────────────────────────────────────────────────

function _cleanup() {
    if (_room) {
        try { _room.disconnect(); } catch {}
        _room = null;
    }
    _muted = false;
    _agentSpeaking = false;
    _transcriptSegments.clear();
    document.querySelectorAll('#voice-agent-audio').forEach(el => el.remove());
}

function _setAgentSpeaking(speaking) {
    _agentSpeaking = speaking;
    if (!_voiceStatus) return;
    const dot = _voiceBar?.querySelector('.voice-status-dot');
    if (speaking) {
        _voiceStatus.textContent = 'Odysseus is speaking...';
        if (dot) dot.classList.add('speaking');
    } else {
        _voiceStatus.textContent = 'Listening...';
        if (dot) dot.classList.remove('speaking');
    }
}

let _connecting = false;
function _setConnecting(v) {
    _connecting = v;
    _updateUI();
}

function _updateUI() {
    if (!_voiceBtn) return;

    if (_voiceEnabled === false) {
        _voiceBtn.style.display = 'none';
        if (_voiceBar) _voiceBar.style.display = 'none';
        return;
    }
    _voiceBtn.style.display = '';

    const connected = !!_room;

    if (_connecting) {
        _voiceBtn.classList.add('connecting');
        _voiceBtn.title = 'Connecting...';
    } else {
        _voiceBtn.classList.remove('connecting');
        _voiceBtn.title = connected ? 'Voice connected' : 'Start voice chat';
    }

    _voiceBtn.classList.toggle('active', connected);

    if (_voiceBar) {
        _voiceBar.style.display = connected ? 'flex' : 'none';
    }
    if (_voiceMute) {
        _voiceMute.textContent = _muted ? 'Unmute' : 'Mute';
        _voiceMute.classList.toggle('muted', _muted);
    }
    if (connected && _voiceStatus) {
        _voiceStatus.textContent = _agentSpeaking ? 'Odysseus is speaking...' : 'Listening...';
    }
}

// ── Init ─────────────────────────────────────────────────────────────
function init() {
    const inputLeft = document.querySelector('.chat-input-left');
    if (!inputLeft) return;

    // Voice connect button
    _voiceBtn = document.createElement('button');
    _voiceBtn.type = 'button';
    _voiceBtn.className = 'input-icon-btn voice-chat-btn';
    _voiceBtn.id = 'voice-chat-btn';
    _voiceBtn.title = 'Start voice chat';
    _voiceBtn.setAttribute('aria-label', 'Voice chat');
    _voiceBtn.innerHTML = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/><line x1="12" y1="19" x2="12" y2="23"/><line x1="8" y1="23" x2="16" y2="23"/></svg>`;
    _voiceBtn.style.display = 'none';
    _voiceBtn.addEventListener('click', () => {
        if (_room) {
            disconnect();
        } else {
            connect();
        }
    });

    const shellBtn = document.getElementById('bash-toggle-btn');
    if (shellBtn && shellBtn.nextSibling) {
        inputLeft.insertBefore(_voiceBtn, shellBtn.nextSibling);
    } else {
        inputLeft.appendChild(_voiceBtn);
    }

    // Voice control bar (shown when connected)
    _voiceBar = document.createElement('div');
    _voiceBar.className = 'voice-control-bar';
    _voiceBar.style.display = 'none';
    _voiceBar.innerHTML = `
        <span class="voice-status-dot"></span>
        <span class="voice-status-text">Listening...</span>
        <button type="button" class="voice-ctrl-btn" id="voice-mute-btn">Mute</button>
        <button type="button" class="voice-ctrl-btn voice-disconnect-btn" id="voice-disconnect-btn">Disconnect</button>
    `;

    const chatInputBar = document.querySelector('.chat-input-bar');
    if (chatInputBar) {
        chatInputBar.parentElement.insertBefore(_voiceBar, chatInputBar);
    }

    _voiceMute = _voiceBar.querySelector('#voice-mute-btn');
    _voiceDisconnect = _voiceBar.querySelector('#voice-disconnect-btn');
    _voiceStatus = _voiceBar.querySelector('.voice-status-text');

    _voiceMute.addEventListener('click', toggleMute);
    _voiceDisconnect.addEventListener('click', disconnect);

    checkVoiceStatus();
}

// Auto-init on DOM ready
if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
} else {
    init();
}

export default { connect, disconnect, toggleMute, checkVoiceStatus };
