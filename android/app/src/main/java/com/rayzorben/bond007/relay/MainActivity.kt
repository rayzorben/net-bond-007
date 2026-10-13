package com.rayzorben.bond007.relay

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings as SystemSettings
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(Color.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(Color.TRANSPARENT),
        )
        super.onCreate(savedInstanceState)
        setContent {
            RelayTheme {
                RelayScreen(
                    isExempt = ::isExempt,
                    onStart = {
                        askNotifications()
                        RelayService.start(this) // restarts with new settings if already running
                    },
                    onStop = { RelayService.stop(this) },
                    onFixBattery = ::askBatteryExemption,
                )
            }
        }
    }

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
