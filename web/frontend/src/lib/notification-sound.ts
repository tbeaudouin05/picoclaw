type AudioContextCtor = typeof AudioContext

let audioCtx: AudioContext | null = null

function getAudioContextCtor(): AudioContextCtor | null {
  if (typeof window === "undefined") return null
  const w = window as Window & {
    AudioContext?: AudioContextCtor
    webkitAudioContext?: AudioContextCtor
  }
  return w.AudioContext ?? w.webkitAudioContext ?? null
}

export function playFinalResponseBeep(): void {
  try {
    const Ctor = getAudioContextCtor()
    if (!Ctor) return
    if (!audioCtx) {
      audioCtx = new Ctor()
    }
    if (audioCtx.state === "suspended") {
      void audioCtx.resume().catch(() => {})
    }

    const now = audioCtx.currentTime
    const osc = audioCtx.createOscillator()
    const gain = audioCtx.createGain()
    osc.type = "sine"
    osc.frequency.setValueAtTime(880, now)
    gain.gain.setValueAtTime(0, now)
    gain.gain.linearRampToValueAtTime(0.08, now + 0.01)
    gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.18)
    osc.connect(gain)
    gain.connect(audioCtx.destination)
    osc.start(now)
    osc.stop(now + 0.2)
  } catch {
    // Audio is best-effort — silently ignore failures (e.g. autoplay blocks).
  }
}
