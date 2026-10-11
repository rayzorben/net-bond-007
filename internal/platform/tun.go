//go:build linux || darwin || windows

package platform

import (
	"fmt"
	"net"
	"sync"

	"golang.zx2c4.com/wireguard/tun"
)

// tunOffset leaves headroom in front of each packet. Linux needs 10 bytes
// for the virtio-net header when GSO/GRO offloads are active.
const tunOffset = 16

// Tun is the virtual adapter the OS routes traffic into.
type Tun struct {
	dev   tun.Device
	name  string
	index int
	mtu   int

	wmu  sync.Mutex
	wbuf [][]byte
}

// OpenTun creates the adapter. On macOS the name is assigned by the kernel
// (utunN); on Windows the Wintun driver is extracted next to the executable.
func OpenTun(name string, mtu int) (*Tun, error) {
	if err := prepareTunDriver(); err != nil {
		return nil, err
	}
	dev, err := tun.CreateTUN(TunName(name), mtu)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", name, err)
	}
	real, err := dev.Name()
	if err != nil {
		dev.Close()
		return nil, err
	}
	t := &Tun{dev: dev, name: real, mtu: mtu, wbuf: [][]byte{make([]byte, tunOffset+65535)}}
	if ni, err := net.InterfaceByName(real); err == nil {
		t.index = ni.Index
	}
	return t, nil
}

func (t *Tun) Name() string { return t.name }
func (t *Tun) Index() int   { return t.index }
func (t *Tun) Close() error { return t.dev.Close() }

// ReadLoop delivers every outbound packet to fn until the device closes.
func (t *Tun) ReadLoop(fn func(pkt []byte)) error {
	n := t.dev.BatchSize()
	bufs := make([][]byte, n)
	sizes := make([]int, n)
	for i := range bufs {
		bufs[i] = make([]byte, tunOffset+65535)
	}
	for {
		cnt, err := t.dev.Read(bufs, sizes, tunOffset)
		for i := 0; i < cnt; i++ {
			fn(bufs[i][tunOffset : tunOffset+sizes[i]])
		}
		if err != nil {
			if err == tun.ErrTooManySegments {
				continue
			}
			return err
		}
	}
}

// Write injects one inbound packet into the OS.
func (t *Tun) Write(pkt []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	b := t.wbuf[0][:tunOffset+len(pkt)]
	copy(b[tunOffset:], pkt)
	t.wbuf[0] = b
	_, err := t.dev.Write(t.wbuf, tunOffset)
	t.wbuf[0] = t.wbuf[0][:cap(t.wbuf[0])]
	return err
}
