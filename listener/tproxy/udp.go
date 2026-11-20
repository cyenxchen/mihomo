package tproxy

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/common/pool"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/transport/socks5"

	"golang.org/x/exp/slices"
)

// ErrTransparentUDPPermission distinguishes socket configuration from UDP bind failures.
var ErrTransparentUDPPermission = errors.New("permission denied configuring transparent UDP")

type UDPListener struct {
	packetConn net.PacketConn
	addr       string
}

// RawAddress implements C.Listener
func (l *UDPListener) RawAddress() string {
	return l.addr
}

// Address implements C.Listener
func (l *UDPListener) Address() string {
	return l.packetConn.LocalAddr().String()
}

// Close implements C.Listener
func (l *UDPListener) Close() error {
	return l.packetConn.Close()
}

func NewUDP(addr string, tunnel C.Tunnel, additions ...inbound.Addition) (*UDPListener, error) {
	if len(additions) == 0 {
		additions = []inbound.Addition{
			inbound.WithInName("DEFAULT-TPROXY"),
			inbound.WithSpecialRules(""),
		}
	}
	l, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}

	rl := &UDPListener{
		packetConn: l,
		addr:       addr,
	}

	c := l.(*net.UDPConn)

	rc, err := c.SyscallConn()
	if err == nil {
		err = setsockopt(rc, addr)
		if errors.Is(err, os.ErrPermission) {
			err = fmt.Errorf("%w: %w", ErrTransparentUDPPermission, err)
		}
	}
	if err != nil {
		_ = l.Close()
		return nil, err
	}

	go func() {
		oob := make([]byte, 1024)
		for {
			buf := pool.Get(pool.UDPBufferSize)
			n, oobn, _, lAddr, err := c.ReadMsgUDPAddrPort(buf, oob)
			if err != nil {
				pool.Put(buf)
				if errors.Is(err, net.ErrClosed) {
					break
				}
				continue
			}

			rAddr, err := getOrigDst(oob[:oobn])
			if err != nil {
				pool.Put(buf)
				continue
			}

			dscp, _ := getDSCP(oob[:oobn])
			additions := append(slices.Clip(additions), inbound.WithDSCP(dscp)) // don't change outside additions

			if rAddr.Addr().Is4() {
				// try to unmap 4in6 address
				lAddr = netip.AddrPortFrom(lAddr.Addr().Unmap(), lAddr.Port())
			}
			handlePacketConn(l, tunnel, buf[:n], lAddr, rAddr, additions...)
		}
	}()

	return rl, nil
}

func handlePacketConn(pc net.PacketConn, tunnel C.Tunnel, buf []byte, lAddr, rAddr netip.AddrPort, additions ...inbound.Addition) {
	target := socks5.AddrFromStdAddrPort(rAddr)
	pkt := &packet{
		pc:        pc,
		lAddr:     lAddr,
		buf:       buf,
		tunnel:    tunnel,
		additions: additions,
	}
	tunnel.HandleUDPPacket(inbound.NewPacket(target, pkt, C.TPROXY, additions...))
}
