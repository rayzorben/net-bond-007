package com.rayzorben.bond007.relay

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.provider.Settings as SystemSettings
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import java.util.Locale

class MainActivity : Activity() {
    private val handler = Handler(Looper.getMainLooper())
    private lateinit var token: EditText
    private lateinit var router: EditText
    private lateinit var name: EditText
    private lateinit var status: TextView

    private var lastUp = 0L
    private var lastDown = 0L
    private var lastAt = 0L

    private val tick = object : Runnable {
        override fun run() {
            render()
            handler.postDelayed(this, 1000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        token = findViewById(R.id.token)
        router = findViewById(R.id.router)
        name = findViewById(R.id.name)
        status = findViewById(R.id.status)

        val s = Settings.load(this)
        token.setText(s.token)
        router.setText(s.routerHost)
        name.setText(s.name)

        findViewById<Button>(R.id.start).setOnClickListener {
            save()
            askNotifications()
            RelayService.start(this) // restarts with the new settings if already running
        }
        findViewById<Button>(R.id.stop).setOnClickListener { RelayService.stop(this) }
        findViewById<Button>(R.id.battery).setOnClickListener { askBatteryExemption() }
    }

    override fun onResume() {
        super.onResume()
        handler.post(tick)
    }

    override fun onPause() {
        handler.removeCallbacks(tick)
        super.onPause()
    }

    private fun save() {
        val old = Settings.load(this)
        Settings.save(
            this,
            old.copy(
                token = token.text.toString().trim(),
                routerHost = router.text.toString().trim(),
                name = name.text.toString().trim().ifEmpty { Build.MODEL },
            ),
        )
    }

    private fun askNotifications() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
    }

    private fun askBatteryExemption() {
        val pm = getSystemService(PowerManager::class.java)
        if (pm.isIgnoringBatteryOptimizations(packageName)) return
        startActivity(
            Intent(SystemSettings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)
                .setData(Uri.parse("package:$packageName"))
        )
    }

    private fun render() {
        val now = System.currentTimeMillis()
        val up = Status.upBytes.get()
        val down = Status.downBytes.get()
        val secs = if (lastAt == 0L) 0.0 else (now - lastAt) / 1000.0
        val upRate = if (secs > 0) (up - lastUp) * 8 / secs else 0.0
        val downRate = if (secs > 0) (down - lastDown) * 8 / secs else 0.0
        lastUp = up; lastDown = down; lastAt = now

        val ack = if (Status.lastAckMs == 0L) "never" else "${(now - Status.lastAckMs) / 1000}s ago"
        val exempt = getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)
        status.text = buildString {
            appendLine(Status.state(now))
            appendLine()
            appendLine("Wi-Fi:     ${if (Status.wifi) "up" else "down"}")
            appendLine("Cellular:  ${if (Status.cellular) "up" else "down"}")
            appendLine("Router:    ${Status.router ?: "-"} (last reply $ack)")
            appendLine("Server:    ${Status.server ?: "-"}")
            appendLine()
            appendLine("Up:    ${mbps(upRate)}  (${size(up)}, ${Status.upPackets.get()} pkts)")
            appendLine("Down:  ${mbps(downRate)}  (${size(down)}, ${Status.downPackets.get()} pkts)")
            appendLine("Dropped: ${Status.dropped.get()}")
            appendLine()
            appendLine("Battery optimisation: ${if (exempt) "off (good)" else "ON - tap the button below"}")
            Status.error?.let { appendLine("Error: $it") }
        }
    }

    private fun mbps(bps: Double) = String.format(Locale.US, "%.1f Mbps", bps / 1e6)

    private fun size(b: Long): String = when {
        b >= 1L shl 30 -> String.format(Locale.US, "%.2f GB", b / (1L shl 30).toDouble())
        b >= 1L shl 20 -> String.format(Locale.US, "%.1f MB", b / (1L shl 20).toDouble())
        else -> String.format(Locale.US, "%.0f KB", b / 1024.0)
    }
}
