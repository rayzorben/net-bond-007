package com.rayzorben.bond007.relay

import org.json.JSONObject

/**
 * The slice of the Bond007 wire format (internal/proto/proto.go) the relay
 * needs. Every frame starts with an 8-byte header:
 *
 *     0xB7 | ver<<4|type | session(4) | link | flags
 *
 * The relay forwards DATA, PROBE, HELLO and every other bond frame untouched.
 * It only reads the two relay control types below, which travel between the
 * phone and the router and never reach the server.
 */
object Wire {
    const val MAGIC: Byte = 0xB7.toByte()
    const val VERSION = 1
    const val HEADER_LEN = 8

    // Relay control frames (types 1..9 are taken by internal/proto).
    const val TYPE_RELAY_HELLO = 10 // phone -> router: token + telemetry, every 2 s
    const val TYPE_RELAY_ACK = 11 // router -> phone: accepted or refused, server address

    /** Frame type, or -1 when b is not a Bond007 frame. */
    fun type(b: ByteArray, len: Int): Int {
        if (len < HEADER_LEN || b[0] != MAGIC || (b[1].toInt() shr 4) and 0x0f != VERSION) return -1
        return b[1].toInt() and 0x0f
    }

    /** A control frame with a JSON body (session 0, link 0). */
    fun control(type: Int, body: JSONObject): ByteArray {
        val json = body.toString().toByteArray(Charsets.UTF_8)
        val out = ByteArray(HEADER_LEN + json.size)
        out[0] = MAGIC
        out[1] = ((VERSION shl 4) or type).toByte()
        System.arraycopy(json, 0, out, HEADER_LEN, json.size)
        return out
    }

    fun body(b: ByteArray, len: Int): JSONObject =
        JSONObject(String(b, HEADER_LEN, len - HEADER_LEN, Charsets.UTF_8))
}
