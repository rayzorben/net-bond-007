package client

import "runtime"

func osName() string {
	if isOpenWrt() {
		return "openwrt"
	}
	return runtime.GOOS
}
