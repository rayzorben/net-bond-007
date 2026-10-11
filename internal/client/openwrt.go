package client

import "os"

func isOpenWrt() bool {
	_, err := os.Stat("/etc/openwrt_release")
	return err == nil
}
