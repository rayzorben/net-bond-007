package com.rayzorben.bond007.relay

import java.util.concurrent.atomic.AtomicLong

/** Live relay state, written by the service and read by the screen. */
object Status {
    @Volatile var running = false
    @Volatile var wifi = false
    @Volatile var cellular = false
    @Volatile var router: String? = null
    @Volatile var server: String? = null
    @Volatile var lastAckMs = 0L
    @Volatile var refused: String? = null
    @Volatile var error: String? = null

    // Phone health, refreshed with every RELAY_HELLO.
    @Volatile var battery = -1
    @Volatile var charging = false
    @Volatile var tempC = Double.NaN
    @Volatile var thermal = 0
    @Volatile var signal = -1
    @Volatile var carrier = ""

    val upBytes = AtomicLong()
    val downBytes = AtomicLong()
    val upPackets = AtomicLong()
    val downPackets = AtomicLong()
    val dropped = AtomicLong()

    fun reset() {
        wifi = false; cellular = false; router = null; server = null
        lastAckMs = 0; refused = null; error = null
        upBytes.set(0); downBytes.set(0); upPackets.set(0); downPackets.set(0); dropped.set(0)
        History.clear()
    }

    enum class Tone { OK, WARN, BAD, IDLE }

    fun state(now: Long = System.currentTimeMillis()): Pair<String, Tone> = when {
        !running -> "Stopped" to Tone.IDLE
        !wifi -> "Waiting for Wi-Fi" to Tone.WARN
        !cellular -> "Waiting for mobile data" to Tone.WARN
        refused != null -> "Router refused: $refused" to Tone.BAD
        now - lastAckMs > 6000 -> "Looking for the router" to Tone.WARN
        server == null -> "Finding the server" to Tone.WARN
        else -> "Relaying" to Tone.OK
    }
}

/** Per-second throughput samples for the sparklines (process lifetime). */
object History {
    const val SIZE = 60
    val up = FloatArray(SIZE)
    val down = FloatArray(SIZE)
    var count = 0
        private set
    private var head = 0
    private var lastUp = -1L
    private var lastDown = 0L
    private var lastAt = 0L

    @Synchronized
    fun clear() {
        up.fill(0f); down.fill(0f); count = 0; head = 0; lastUp = -1
    }

    /** Records one sample; returns the current rates in bits/s. */
    @Synchronized
    fun sample(now: Long): Pair<Float, Float> {
        val u = Status.upBytes.get()
        val d = Status.downBytes.get()
        if (lastUp < 0 || u < lastUp || now <= lastAt) {
            lastUp = u; lastDown = d; lastAt = now
            return 0f to 0f
        }
        val secs = (now - lastAt) / 1000f
        val ur = (u - lastUp) * 8 / secs
        val dr = (d - lastDown) * 8 / secs
        lastUp = u; lastDown = d; lastAt = now
        up[head] = ur; down[head] = dr
        head = (head + 1) % SIZE
        if (count < SIZE) count++
        return ur to dr
    }

    /** Oldest first. */
    @Synchronized
    fun series(src: FloatArray): FloatArray {
        val out = FloatArray(count)
        for (i in 0 until count) out[i] = src[(head - count + i + SIZE) % SIZE]
        return out
    }
}
