package main

import (
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"

	"golang.org/x/sys/unix"
)

type socklen uint32

type rawSockaddrL2 struct {
	Family uint16
	Psm    uint16
	Bdaddr [6]uint8
}

var addrlen = socklen(unsafe.Sizeof(rawSockaddrL2{}))

// Represents L2CAP socket address
type sockaddrL2 struct {
	PSM    uint16
	Bdaddr [6]uint8
	raw    rawSockaddrL2
}

func (sa *sockaddrL2) sockaddr() (unsafe.Pointer, socklen, error) {
	sa.raw.Family = unix.AF_BLUETOOTH
	sa.raw.Psm = uint16(sa.PSM)
	sa.raw.Bdaddr = sa.Bdaddr

	return unsafe.Pointer(&sa.raw), socklen(unsafe.Sizeof(rawSockaddrL2{})), nil
}

func (sa *sockaddrL2) String() string {
	return fmt.Sprintf("[PSM: %d, Bdaddr: %v]", sa.PSM, sa.Bdaddr)
}

const (
	PSMCTRL = 0x11
	PSMINTR = 0x13
	BUFSIZE = 1024

	FDBITS = 32
)

var mu sync.Mutex

// type fdSet struct {
// Bits [32]int32
// }

// func setFd(fd int, fdset *fdSet) {
// mask := uint(1) << (uint(fd) % uint(FDBITS))
// fdset.Bits[fd/FDBITS] |= int32(mask)
// }

// func isSetFd(fd int, fdset *fdSet) bool {
// mask := uint(1) << (uint(fd) % uint(FDBITS))
// return ((fdset.Bits[fd/FDBITS] & int32(mask)) != 0)
// }

// Bluetooth represents a bluetooth socket connection
type Bluetooth struct {
	fd     int
	family int
	proto  int
	typ    int
	saddr  sockaddrL2

	block bool
	mu    sync.Mutex
}

// SetBlocking sets socket to blocking mode(true) or Non-blocking mode(false)
func (bt *Bluetooth) SetBlocking(block bool) error {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	dFlgPtr, _, err := unix.Syscall(unix.SYS_FCNTL, uintptr(bt.fd), unix.F_GETFL, 0)

	if err != 0 {
		return errors.Wrap(err, "failed in SetBlocking")
	}

	var delayFlag uint
	if block {
		delayFlag = uint(dFlgPtr) & (^(uint)(unix.O_NONBLOCK))
	} else {
		delayFlag = uint(dFlgPtr) | ((uint)(unix.O_NONBLOCK))
	}

	_, _, err = unix.Syscall(unix.SYS_FCNTL, uintptr(bt.fd), unix.F_SETFL, uintptr(delayFlag))
	if err != 0 {
		return errors.Wrap(err, "failed in SetBlocking")
	}

	return nil
}

// NewBluetoothSocket creates L2CAP socket wrapper with given file descriptor
// This file descriptor is provided by BlueZ DBus interface
// e.g. org.bluez.Profile1.NewConnection()
func NewBluetoothSocket(fd int) (*Bluetooth, error) {
	bt := &Bluetooth{
		fd:     fd,
		family: unix.AF_BLUETOOTH,
		typ:    unix.SOCK_SEQPACKET,
		proto:  unix.BTPROTO_L2CAP,
		block:  false,
	}

	var rsa rawSockaddrL2

	_, _, err := unix.RawSyscall(
		unix.SYS_GETSOCKNAME,
		uintptr(fd),
		uintptr(unsafe.Pointer(&rsa)),
		uintptr(unsafe.Pointer(&addrlen)),
	)

	if int(err) != 0 {
		unix.Close(fd)
		return nil, errors.Wrap(err, "failed in getsocketname")
	}

	bt.saddr = sockaddrL2{
		PSM:    rsa.Psm,
		Bdaddr: rsa.Bdaddr,
	}

	logrus.WithField("sockname", bt.saddr).Debugln("New socket created")

	return bt, nil
}

// ListenBluetooth creates L2CAP socket and lets it listen on given PSM
func ListenBluetooth(psm uint, bklen int, block bool) (*Bluetooth, error) {
	mu.Lock()
	defer mu.Unlock()

	bt := &Bluetooth{
		family: unix.AF_BLUETOOTH,
		typ:    unix.SOCK_SEQPACKET, // RFCOMM = SOCK_STREAM, L2CAP = SOCK_SEQPACKET, HCI = SOCK_RAW
		proto:  unix.BTPROTO_L2CAP,
		block:  block,
	}

	fd, err := unix.Socket(bt.family, bt.typ, bt.proto)
	if err != nil {
		return nil, errors.Wrap(err, "socket could not be created")
	}
	logrus.Debugln("Socket created")

	bt.fd = fd
	unix.CloseOnExec(bt.fd)

	if err := bt.SetBlocking(block); err != nil {
		bt.Close()
		return nil, err
	}
	logrus.Debugln("Socket blocking mode set")

	// because L2CAP socket address struct does not exist in golang's standard libs
	// must be binded by using very low-level operations
	bt.saddr = sockaddrL2{
		PSM:    uint16(psm),
		Bdaddr: [6]uint8{0},
	}

	saddr, saddrlen, err := bt.saddr.sockaddr()
	if err != nil {
		return nil, err
	}

	_, _, sysErr := unix.Syscall(
		unix.SYS_BIND,
		uintptr(bt.fd),
		uintptr(saddr),
		uintptr(saddrlen),
	)
	if int(sysErr) != 0 {
		bt.Close()
		return nil, sysErr
	}

	logrus.Debugln("Socket binded")

	if err := unix.Listen(bt.fd, bklen); err != nil {
		bt.Close()
		return nil, err
	}

	logrus.Debugln("Socket is listening")

	return bt, nil
}

// Accept accepts on listening socket and return received connection
func (bt *Bluetooth) Accept() (*Bluetooth, error) {
	mu.Lock()
	defer mu.Unlock()

	var nFd int
	var rAddr *sockaddrL2

	// setFd(bt.fd, &fdSet{Bits: [32]int32{0}})

	for {
		var raddr rawSockaddrL2

		rFd, _, err := unix.Syscall(
			unix.SYS_ACCEPT,
			uintptr(bt.fd),
			uintptr(unsafe.Pointer(&raddr)),
			uintptr(unsafe.Pointer(&addrlen)),
		)

		if err != 0 {
			switch err {
			case syscall.EAGAIN:
				time.Sleep(1 * time.Millisecond)
				continue
			case syscall.ECONNABORTED:
				continue
			}
			unix.Close(int(rFd))
			return nil, err
		}

		nFd = int(rFd)
		rAddr = &sockaddrL2{
			PSM:    raddr.Psm,
			Bdaddr: raddr.Bdaddr,
		}
		break
	}

	logrus.Debugln("Remote Address Info", rAddr)

	rbt := &Bluetooth{
		family: bt.family,
		typ:    bt.typ,
		proto:  bt.proto,
		block:  bt.block,
		fd:     nFd,
		saddr:  *rAddr,
	}

	unix.CloseOnExec(nFd)
	logrus.Debugln("Accept closeonexec")

	if err := rbt.SetBlocking(false); err != nil {
		bt.Close()
		rbt.Close()
		return nil, err
	}
	logrus.Debugln("Accepted Socket could set blocking mode")

	return rbt, nil
}

func getPointer(b []byte) unsafe.Pointer {
	if len(b) > 0 {
		return unsafe.Pointer(&b[0])
	}
	var _zero uintptr
	return unsafe.Pointer(&_zero)
}

func (bt *Bluetooth) Read(b []byte) (int, error) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	// setFd(bt.fd, &fdSet{Bits: [32]int32{0}})

	for {
		r, _, err := unix.Syscall(
			unix.SYS_READ,
			uintptr(bt.fd),
			uintptr(getPointer(b)),
			uintptr(len(b)),
		)

		if err == 0 {
			return int(r), nil
		}

		if err == syscall.EAGAIN {
			time.Sleep(1 * time.Millisecond)
			continue
		}

		return -1, err
	}
}

func (bt *Bluetooth) Write(d []byte) (int, error) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	// setFd(bt.fd, &fdSet{Bits: [32]int32{0}})

	r, _, err := unix.Syscall(
		unix.SYS_WRITE,
		uintptr(bt.fd),
		uintptr(getPointer(d)),
		uintptr(len(d)),
	)

	if err != 0 {
		return -1, err
	}

	return int(r), nil
}

// Close closes the socket
func (bt *Bluetooth) Close() error {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	if bt.fd <= 0 {
		return unix.EINVAL
	}

	return unix.Close(bt.fd)
}
