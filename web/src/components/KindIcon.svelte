<script lang="ts">
  import { Cable, Wifi, RadioTower, Usb, Bluetooth, Network, Shield } from '@lucide/svelte'
  let { kind, size = 18 }: { kind: string; size?: number } = $props()
  const icons: Record<string, any> = {
    ethernet: Cable, wifi: Wifi, cellular: RadioTower, usb: Usb, bluetooth: Bluetooth, vpn: Shield,
  }
  const Icon = $derived(icons[kind] ?? Network)
</script>

<Icon {size} strokeWidth={2} />
