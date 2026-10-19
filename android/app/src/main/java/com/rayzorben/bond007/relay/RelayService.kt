package com.rayzorben.bond007.relay

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiManager
import android.os.BatteryManager
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import android.telephony.TelephonyManager
import org.json.JSONObject
import java.net.Inet4Address
import java.net.InetAddress

/**
 * Foreground service that owns the relay: it keeps cellular data up while
 * Wi-Fi is connected, holds wake and Wi-Fi locks, and feeds network changes to
 * [Relay].
 */
class RelayService : Service() {
    companion object {
        private const val CHANNEL = "relay"
        private const val NOTIFICATION_ID = 7
        private const val ACTION_STOP = "com.rayzorben.bond007.relay.STOP"

        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, RelayService::class.java))
        }

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, RelayService::class.java).setAction(ACTION_STOP))
        }
    }

    private lateinit var cm: ConnectivityManager
    private var relay: Relay? = null
    private var wake: PowerManager.WakeLock? = null
    private val wifiLocks = mutableListOf<WifiManager.WifiLock>()

    /**
     * Plugging in while relaying wakes the screen, so keep-awake (which may be
     * limited to charging) takes over without unlocking: the relay screen
     * shows over the lock screen. Without this the phone stays asleep, or the
     * charging screensaver starts, and Wi-Fi naps.
     */
    private val powerReceiver = object : BroadcastReceiver() {
        override fun onReceive(ctx: Context, intent: Intent) {
            if (!Settings.load(ctx).keepAwake) return
            @Suppress("DEPRECATION")
            getSystemService(PowerManager::class.java)
                .newWakeLock(PowerManager.SCREEN_DIM_WAKE_LOCK or PowerManager.ACQUIRE_CAUSES_WAKEUP, "bond007:plugged")
                .acquire(3000)
        }
    }

    private val cellCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            relay?.onCellular(network)
        }

        override fun onLost(network: Network) {
            relay?.onCellularLost(network)
        }
    }

    private val wifiCallback = object : ConnectivityManager.NetworkCallback() {
        // Called after onAvailable and whenever the gateway changes.
        override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) {
            relay?.onWifi(network, gateway(lp))
        }

        override fun onLost(network: Network) {
            relay?.onWifiLost(network)
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }
        startForeground(NOTIFICATION_ID, notification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE)
        // Every start (re)builds the relay so changed settings apply.
        teardown()
        setup()
        return START_STICKY
    }

    private fun setup() {
        Status.reset()
        Status.running = true
        cm = getSystemService(ConnectivityManager::class.java)
        acquireLocks()
        registerReceiver(powerReceiver, IntentFilter(Intent.ACTION_POWER_CONNECTED), RECEIVER_NOT_EXPORTED)
        relay = Relay(Settings.load(this), ::telemetry).also { it.start() }

        // requestNetwork (not registerNetworkCallback) makes Android bring
        // cellular up and keep it up even though Wi-Fi is the default network.
        cm.requestNetwork(
            NetworkRequest.Builder()
                .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .build(),
            cellCallback,
        )
        // The router's Wi-Fi may have no validated internet (the bond is down),
        // so do not require the INTERNET capability for it.
        cm.registerNetworkCallback(
            NetworkRequest.Builder()
                .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                .removeCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .build(),
            wifiCallback,
        )
    }

    private fun teardown() {
        if (relay != null) {
            runCatching { cm.unregisterNetworkCallback(cellCallback) }
            runCatching { cm.unregisterNetworkCallback(wifiCallback) }
        }
        runCatching { unregisterReceiver(powerReceiver) }
        relay?.close()
        relay = null
        wake?.takeIf { it.isHeld }?.release()
        wifiLocks.forEach { if (it.isHeld) it.release() }
        wifiLocks.clear()
        Status.running = false
    }

    override fun onDestroy() {
        teardown()
        super.onDestroy()
    }

    private fun acquireLocks() {
        val pm = getSystemService(PowerManager::class.java)
        wake = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "bond007:relay").apply {
            setReferenceCounted(false); acquire()
        }
        // Without these, Wi-Fi power save adds 50-200 ms of jitter and the
        // router's scheduler rates this link badly. Low-latency mode only
        // applies while the app is in the foreground with the screen on;
        // high-perf covers the rest on the Android versions that still honour it.
        val wm = applicationContext.getSystemService(WifiManager::class.java)
        @Suppress("DEPRECATION")
        for (mode in listOf(WifiManager.WIFI_MODE_FULL_LOW_LATENCY, WifiManager.WIFI_MODE_FULL_HIGH_PERF)) {
            wm.createWifiLock(mode, "bond007:relay:$mode").apply {
                setReferenceCounted(false); acquire(); wifiLocks += this
            }
        }
    }

    private fun gateway(lp: LinkProperties): InetAddress? =
        lp.routes.firstOrNull { it.isDefaultRoute && it.gateway is Inet4Address }?.gateway

    /** Phone health for the router dashboard, sent with every RELAY_HELLO. */
    private fun telemetry(): JSONObject {
        val j = JSONObject()
        j.put("model", "${Build.MANUFACTURER} ${Build.MODEL}")
        registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))?.let { b ->
            val level = b.getIntExtra(BatteryManager.EXTRA_LEVEL, -1)
            val scale = b.getIntExtra(BatteryManager.EXTRA_SCALE, 100)
            if (level >= 0 && scale > 0) j.put("battery", level * 100 / scale)
            j.put("charging", b.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) != 0)
            val t = b.getIntExtra(BatteryManager.EXTRA_TEMPERATURE, Int.MIN_VALUE)
            if (t != Int.MIN_VALUE) j.put("tempC", t / 10.0)
        }
        val pm = getSystemService(PowerManager::class.java)
        j.put("thermal", pm.currentThermalStatus)
        Status.batteryExempt = pm.isIgnoringBatteryOptimizations(packageName)
        j.put("batteryExempt", Status.batteryExempt)
        j.put("awake", Status.awake && pm.isInteractive)
        runCatching {
            val tm = getSystemService(TelephonyManager::class.java)
            tm.signalStrength?.let { j.put("signal", it.level) } // 0..4 bars
            j.put("carrier", tm.networkOperatorName)
        }
        Status.battery = j.optInt("battery", -1)
        Status.charging = j.optBoolean("charging")
        Status.tempC = j.optDouble("tempC", Double.NaN)
        Status.thermal = j.optInt("thermal")
        Status.signal = j.optInt("signal", -1)
        Status.carrier = j.optString("carrier")
        return j
    }

    private fun notification(): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL, "Relay", NotificationManager.IMPORTANCE_LOW)
        )
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, RelayService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE
        )
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentTitle("Bond007 relay")
            .setContentText("Sharing cellular data with the router")
            .setOngoing(true)
            .setContentIntent(open)
            .addAction(Notification.Action.Builder(null, "Stop", stop).build())
            .build()
    }
}
