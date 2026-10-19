package com.rayzorben.bond007.relay

import android.Manifest
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.BatteryManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.os.SystemClock
import android.provider.Settings as SystemSettings
import android.view.MotionEvent
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.mutableStateOf

/** Seconds without a touch before keep-awake dims the screen to black. */
private const val STANDBY_AFTER_MS = 20_000L

class MainActivity : ComponentActivity() {
    private val handler = Handler(Looper.getMainLooper())
    private val standby = mutableStateOf(false)
    private var lastTouch = SystemClock.elapsedRealtime()
    private var resumed = false
    private var keepOn = false

    private val tick = object : Runnable {
        override fun run() {
            update()
            handler.postDelayed(this, 1000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(Color.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(Color.TRANSPARENT),
        )
        super.onCreate(savedInstanceState)
        setContent {
            RelayTheme {
                Box {
                    RelayScreen(
                        isExempt = ::isExempt,
                        onStart = {
                            askNotifications()
                            RelayService.start(this@MainActivity) // restarts with new settings if already running
                        },
                        onStop = { RelayService.stop(this@MainActivity) },
                        onFixBattery = ::askBatteryExemption,
                    )
                    if (standby.value) Standby()
                }
            }
        }
    }

    override fun onResume() {
        super.onResume()
        resumed = true
        lastTouch = SystemClock.elapsedRealtime()
        handler.post(tick)
    }

    override fun onPause() {
        super.onPause()
        resumed = false
        handler.removeCallbacks(tick)
        update()
    }

    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        lastTouch = SystemClock.elapsedRealtime()
        if (standby.value) {
            // The first touch only wakes the screen; it must not press anything.
            if (ev.actionMasked == MotionEvent.ACTION_DOWN) update()
            return true
        }
        return super.dispatchTouchEvent(ev)
    }

    /**
     * Keep-awake: while relaying with the app on screen, keep the screen on so
     * Android keeps the phone's Wi-Fi out of power save (the low-latency Wi-Fi
     * lock only works on screen), and dim it to black after a while to save
     * the battery.
     */
    private fun update() {
        Status.awake = resumed
        val s = Settings.load(this)
        val keep = resumed && Status.running && s.keepAwake && (!s.keepAwakeOnlyCharging || pluggedIn())
        if (keep != keepOn) {
            keepOn = keep
            if (keep) window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
            else window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        }
        val dim = keep && SystemClock.elapsedRealtime() - lastTouch > STANDBY_AFTER_MS
        if (dim != standby.value) {
            standby.value = dim
            window.attributes = window.attributes.apply {
                screenBrightness = if (dim) 0.01f else WindowManager.LayoutParams.BRIGHTNESS_OVERRIDE_NONE
            }
        }
    }

    private fun pluggedIn(): Boolean =
        (registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
            ?.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) ?: 0) != 0

    private fun isExempt() = getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)

    private fun askNotifications() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
    }

    private fun askBatteryExemption() {
        if (isExempt()) return
        startActivity(
            Intent(SystemSettings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).setData(Uri.parse("package:$packageName"))
        )
    }
}
