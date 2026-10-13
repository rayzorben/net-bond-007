package com.rayzorben.bond007.relay

import android.net.Network
import android.util.Log
import org.json.JSONObject
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.SocketAddress
import kotlin.concurrent.thread

private const val TAG = "Bond007Relay"
private const val MAX_FRAME = 65535 + 16

/**
 * Relay copies Bond007 frames between the router (over Wi-Fi) and the
 * Bond007 server (over cellular). It never looks inside bond frames: the
 * router's bond engine treats this phone as one more link, and the server
 * sees that link arrive from the carrier's address.
 *
 *     router link socket --Wi-Fi--> lan :7008 --> cell --cellular--> server
 *     router link socket <--Wi-Fi-- lan :7008 <-- cell <--cellular-- server
 *
 * Both sockets are pinned to their network with Network.bindSocket. That
 * matters: the phone's Wi-Fi is the bonded LAN, so an unpinned cellular socket
 * would send relay traffic back into the tunnel it is meant to carry.
 */
class Relay(
    private val settings: Settings,
    private val telemetry: () -> JSONObject,
) {
    private val lock = Any()
    private var lan: DatagramSocket? = null
    private var lanNet: Network? = null
    private var cell: DatagramSocket? = null
    private var cellNet: Network? = null

    @Volatile private var router: InetAddress? = null
    @Volatile private var routerReturn: SocketAddress? = null // the router's link socket
    @Volatile private var serverSpec: String? = null // "host:port" from RELAY_ACK
    @Volatile private var server: InetSocketAddress? = null // resolved over cellular
    @Volatile private var closed = false
    private var announcer: Thread? = null

    fun start() {
        announcer = thread(name = "relay-hello", isDaemon = true) { announceLoop() }
    }

    fun close() {
        closed = true
        announcer?.interrupt()
        synchronized(lock) {
            lan?.close(); lan = null; lanNet = null
            cell?.close(); cell = null; cellNet = null
        }
    }

    fun onWifi(net: Network, gateway: InetAddress?) = synchronized(lock) {
        if (closed) return
        val target = settings.routerHost.takeIf { it.isNotBlank() }
            ?.let { runCatching { net.getByName(it) }.getOrNull() } ?: gateway
        router = target
        Status.router = target?.hostAddress
        if (net == lanNet && lan != null) return
        lan?.close()
        try {
            val s = DatagramSocket(null)
            s.reuseAddress = true
            s.bind(InetSocketAddress(settings.relayPort))
            net.bindSocket(s)
            lan = s; lanNet = net
            Status.wifi = true
            thread(name = "relay-lan", isDaemon = true) { lanLoop(s) }
            Log.i(TAG, "Wi-Fi up, router $target, listening on :${settings.relayPort}")
        } catch (e: Exception) {
            lan = null; lanNet = null; Status.wifi = false
            Status.error = "Wi-Fi socket: ${e.message}"
            Log.w(TAG, "Wi-Fi socket failed", e)
        }
    }

    fun onWifiLost(net: Network) = synchronized(lock) {
        if (net != lanNet) return
        lan?.close(); lan = null; lanNet = null
        router = null; routerReturn = null
        Status.wifi = false; Status.router = null
        Log.i(TAG, "Wi-Fi lost")
    }

    fun onCellular(net: Network) = synchronized(lock) {
        if (closed || (net == cellNet && cell != null)) return
        cell?.close()
        try {
            val s = DatagramSocket()
            net.bindSocket(s)
            cell = s; cellNet = net
            server = null // re-resolve on this network (NAT64 prefixes differ)
            Status.server = null
            Status.cellular = true
            thread(name = "relay-cell", isDaemon = true) { cellLoop(s) }
            Log.i(TAG, "cellular up")
        } catch (e: Exception) {
            cell = null; cellNet = null; Status.cellular = false
            Status.error = "cellular socket: ${e.message}"
            Log.w(TAG, "cellular socket failed", e)
        }
    }

    fun onCellularLost(net: Network) = synchronized(lock) {
        if (net != cellNet) return
        cell?.close(); cell = null; cellNet = null
        Status.cellular = false
        Log.i(TAG, "cellular lost")
    }

    /** Router -> server. Only frames from the router are forwarded. */
    private fun lanLoop(s: DatagramSocket) {
        val buf = ByteArray(MAX_FRAME)
        val inP = DatagramPacket(buf, buf.size)
        val outP = DatagramPacket(buf, 0)
        while (!closed) {
            inP.setData(buf, 0, buf.size)
            try {
                s.receive(inP)
            } catch (e: Exception) {
                break // socket closed: Wi-Fi changed or relay stopped
            }
            val n = inP.length
            if (inP.address != router) {
                Status.dropped.incrementAndGet(); continue
            }
            if (Wire.type(buf, n) == Wire.TYPE_RELAY_ACK) {
                runCatching { onAck(Wire.body(buf, n)) }
                continue
            }
            routerReturn = inP.socketAddress
            val c = cell
            val dst = server
            if (c == null || dst == null) {
                Status.dropped.incrementAndGet(); continue
            }
            outP.setData(buf, 0, n)
            outP.socketAddress = dst
            try {
                c.send(outP)
                Status.upBytes.addAndGet(n.toLong()); Status.upPackets.incrementAndGet()
            } catch (e: Exception) {
                Status.dropped.incrementAndGet()
            }
        }
    }

    /** Server -> router. Only frames from the server are forwarded. */
    private fun cellLoop(s: DatagramSocket) {
        val buf = ByteArray(MAX_FRAME)
        val inP = DatagramPacket(buf, buf.size)
        val outP = DatagramPacket(buf, 0)
        while (!closed) {
            inP.setData(buf, 0, buf.size)
            try {
                s.receive(inP)
            } catch (e: Exception) {
                break
            }
            val n = inP.length
            val l = lan
            val back = routerReturn
            if (inP.socketAddress != server || l == null || back == null) {
                Status.dropped.incrementAndGet(); continue
            }
            outP.setData(buf, 0, n)
            outP.socketAddress = back
            try {
                l.send(outP)
                Status.downBytes.addAndGet(n.toLong()); Status.downPackets.incrementAndGet()
            } catch (e: Exception) {
                Status.dropped.incrementAndGet()
            }
        }
    }

    private fun onAck(j: JSONObject) {
        Status.lastAckMs = System.currentTimeMillis()
        if (!j.optBoolean("ok")) {
            Status.refused = j.optString("reason", "refused")
            return
        }
        Status.refused = null
        val spec = j.optString("server")
        if (spec.isNotEmpty() && spec != serverSpec) {
            serverSpec = spec
            server = null
            Status.server = null
        }
    }

    /** Sends RELAY_HELLO every 2 s and resolves the server over cellular. */
    private fun announceLoop() {
        while (!closed) {
            try {
                resolveServer()
                val s = lan
                val r = router
                if (s != null && r != null) {
                    val body = telemetry()
                        .put("token", settings.token)
                        .put("name", settings.name)
                        .put("version", BuildConfig.VERSION_NAME)
                        .put("cellular", cell != null)
                        .put("upBytes", Status.upBytes.get())
                        .put("downBytes", Status.downBytes.get())
                    val b = Wire.control(Wire.TYPE_RELAY_HELLO, body)
                    s.send(DatagramPacket(b, b.size, InetSocketAddress(r, settings.routerPort)))
                }
                Status.error = null
            } catch (e: InterruptedException) {
                return
            } catch (e: Exception) {
                Status.error = e.message
            }
            try {
                Thread.sleep(2000)
            } catch (e: InterruptedException) {
                return
            }
        }
    }

    private fun resolveServer() {
        val spec = serverSpec ?: return
        val net = cellNet ?: return
        if (server != null) return
        val i = spec.lastIndexOf(':')
        require(i > 0) { "bad server address $spec" }
        val host = spec.substring(0, i).removePrefix("[").removeSuffix("]")
        val port = spec.substring(i + 1).toInt()
        // Resolve on the cellular network so IPv6-only carriers synthesize a
        // NAT64 address for an IPv4 server.
        val addr = net.getAllByName(host).first()
        server = InetSocketAddress(addr, port)
        Status.server = "${addr.hostAddress}:$port"
        Log.i(TAG, "server $spec -> ${Status.server}")
    }
}
