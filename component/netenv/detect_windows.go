package netenv

import (
	"context"
	"encoding/binary"
	"net"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformDetectionSupported() bool { return true }
func detectPlatform(ctx context.Context, preferred string) (Snapshot, error) {
	size := uint32(16384)
	var buffer []byte
	var first *windows.IpAdapterAddresses
	for {
		buffer = make([]byte, size)
		first = (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_GATEWAYS, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return Snapshot{}, err
		}
		break
	}
	var selected *windows.IpAdapterAddresses
	gateway := ""
	best := uint32(^uint32(0))
	selectedV4 := false
	for a := first; a != nil; a = a.Next {
		name := windows.UTF16PtrToString(a.FriendlyName)
		if a.OperStatus != windows.IfOperStatusUp || a.TunnelType != 0 || (preferred != "" && name != preferred) || (preferred == "" && windowsInterfaceType(a.IfType) == "") {
			continue
		}
		g := ""
		v4 := false
		for entry := a.FirstGatewayAddress; entry != nil; entry = entry.Next {
			ip := entry.Address.IP()
			if ip == nil || ip.IsUnspecified() {
				continue
			}
			if g == "" || ip.To4() != nil {
				g = ip.String()
				v4 = ip.To4() != nil
			}
			if v4 {
				break
			}
		}
		if preferred != "" {
			selected = a
			gateway = g
			break
		}
		if g == "" {
			continue
		}
		metric := a.Ipv6Metric
		if v4 {
			metric = a.Ipv4Metric
		}
		if selected == nil || v4 && !selectedV4 || v4 == selectedV4 && metric < best {
			selected = a
			gateway = g
			best = metric
			selectedV4 = v4
		}
	}
	if selected == nil {
		return Snapshot{State: None}, nil
	}
	f := Fields{Type: windowsInterfaceType(selected.IfType), Interface: windows.UTF16PtrToString(selected.FriendlyName), Gateway: gateway}
	if ip := selected.Dhcpv4Server.IP(); ip != nil && !ip.IsUnspecified() {
		f.DHCPServer = ip.String()
	}
	f.GatewayMAC = windowsGatewayMAC(selected.IfIndex, gateway)
	if f.Type == "wifi" {
		f.SSID, f.BSSID = windowsWifi(windows.BytePtrToString(selected.AdapterName))
	}
	runtime.KeepAlive(buffer)
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return detectedSnapshot(f), nil
}

func windowsWifi(adapterName string) (string, string) {
	dll := windows.NewLazySystemDLL("wlanapi.dll")
	open, closeHandle, query, free := dll.NewProc("WlanOpenHandle"), dll.NewProc("WlanCloseHandle"), dll.NewProc("WlanQueryInterface"), dll.NewProc("WlanFreeMemory")
	for _, p := range []*windows.LazyProc{open, closeHandle, query, free} {
		if p.Find() != nil {
			return "", ""
		}
	}
	guid, err := windows.GUIDFromString(adapterName)
	if err != nil {
		return "", ""
	}
	var negotiated uint32
	var handle windows.Handle
	code, _, _ := open.Call(2, 0, uintptr(unsafe.Pointer(&negotiated)), uintptr(unsafe.Pointer(&handle)))
	if code != 0 {
		return "", ""
	}
	defer closeHandle.Call(uintptr(handle), 0)
	var size, opcodeType uint32
	var data *byte
	// wlan_intf_opcode_current_connection = 7.
	code, _, _ = query.Call(uintptr(handle), uintptr(unsafe.Pointer(&guid)), 7, 0, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&opcodeType)))
	if code != 0 || data == nil {
		return "", ""
	}
	defer free.Call(uintptr(unsafe.Pointer(data)))
	if size > 1<<20 {
		return "", ""
	}
	return parseWindowsWifi(unsafe.Slice(data, int(size)))
}

type forkIPNetRow struct {
	Address        [28]byte
	Index          uint32
	LUID           uint64
	Physical       [32]byte
	PhysicalLength uint32
	State          uint32
	Flags          uint8
	_              [3]byte
	Reachability   uint32
}

func windowsGatewayMAC(index uint32, gateway string) string {
	if gateway == "" {
		return ""
	}
	dll := windows.NewLazySystemDLL("iphlpapi.dll")
	get, free := dll.NewProc("GetIpNetTable2"), dll.NewProc("FreeMibTable")
	if get.Find() != nil || free.Find() != nil {
		return ""
	}
	var table unsafe.Pointer
	code, _, _ := get.Call(windows.AF_UNSPEC, uintptr(unsafe.Pointer(&table)))
	if code != 0 || table == nil {
		return ""
	}
	defer free.Call(uintptr(table))
	count := *(*uint32)(table)
	if count > 1<<20 {
		return ""
	}
	rows := unsafe.Slice((*forkIPNetRow)(unsafe.Add(table, 8)), int(count))
	for _, row := range rows {
		if row.Index != index || row.PhysicalLength != 6 {
			continue
		}
		var ip net.IP
		switch binary.LittleEndian.Uint16(row.Address[:2]) {
		case windows.AF_INET:
			ip = net.IP(row.Address[4:8])
		case windows.AF_INET6:
			ip = net.IP(row.Address[8:24])
		}
		if ip.String() == gateway {
			if mac := windowsNeighborMAC(net.HardwareAddr(row.Physical[:6]).String(), row.State); mac != "" {
				return mac
			}
		}
	}
	return ""
}
