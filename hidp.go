package main

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/Sirupsen/logrus"
	"github.com/godbus/dbus"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
)

type HidProfile struct {
	bus  *dbus.Conn
	path dbus.ObjectPath
	uid  string

	connIntr *Bluetooth

	connection    chan *Client
	disconnection chan *Client
}

func (p *HidProfile) UUID() string {
	return p.uid
}

func (p *HidProfile) Path() dbus.ObjectPath {
	return p.path
}

func (p *HidProfile) Connection() chan *Client {
	return p.connection
}

func (p *HidProfile) Disconnection() chan *Client {
	return p.disconnection
}

func (p *HidProfile) Export() error {
	return errors.Wrap(
		p.bus.Export(p, p.path, "org.bluez.Profile1"),
		"failed to export profile",
	)
}

func (p *HidProfile) Register(sdp string) error {
	callback := make(chan *dbus.Call, 1)

	opts := map[string]dbus.Variant{
		"PSM": dbus.MakeVariant(uint16(PSMCTRL)),
		"RequireAuthentication": dbus.MakeVariant(true),
		"RequireAuthorization":  dbus.MakeVariant(true),
		"ServiceRecord":         dbus.MakeVariant(sdp),
	}

	err := p.bus.Object("org.bluez", "/org/bluez").Go(
		"org.bluez.ProfileManager1.RegisterProfile",
		0, callback, p.path, p.uid, opts,
	).Err

	if err != nil {
		return err
	}

	return (<-callback).Err
}

func (p *HidProfile) Unregister() error {
	return p.bus.Object("org.bluez", "/org/bluez").Call(
		"org.bluez.ProfileManager1.UnregisterProfile",
		0, p.path,
	).Err
}

func NewHidProfile(path string) (*HidProfile, error) {
	connIntr, err := ListenBluetooth(PSMINTR, 1, false)
	if err != nil {
		return nil, errors.Wrap(err, "failed to listen bluetooth")
	}

	bus, err := dbus.SystemBus()
	if err != nil {
		return nil, errors.Wrap(err, "failed to connect system bus")
	}

	return &HidProfile{
		bus:           bus,
		path:          (dbus.ObjectPath)(path),
		connIntr:      connIntr,
		uid:           uuid.NewV4().String(),
		connection:    make(chan *Client),
		disconnection: make(chan *Client),
	}, nil
}

func (p *HidProfile) Release() *dbus.Error {
	logrus.Debugln("Release")
	return nil
}

func (p *HidProfile) NewConnection(dev dbus.ObjectPath, fd dbus.UnixFD, fdProps map[string]dbus.Variant) *dbus.Error {
	logrus.Debugln("NewConnection", dev, fd, fdProps)

	sintr, err := p.connIntr.Accept()
	if err != nil {
		logrus.WithError(err).Errorln("Accept failed")
		p.connIntr.Close()
		return dbus.NewError(fmt.Sprintf("Accept failed: %v", PSMINTR), []interface{}{err})
	}

	logrus.Infoln("New bluetooth connection")

	sctrl, err := NewBluetoothSocket(int(fd))
	if err != nil {
		logrus.WithError(err).Errorln("Failed to create bluetooth socket")
		unix.Close(int(fd))
		return dbus.NewError("failed to create bluetooth socket", []interface{}{err})
	}

	logrus.Infoln("New bluetooth socket created")

	p.connection <- &Client{dev, sintr, sctrl}

	return nil
}

func (p *HidProfile) RequestDisconnection(dev dbus.ObjectPath) *dbus.Error {
	logrus.WithField("device", dev).Infoln("Disconnection requested")

	p.disconnection <- &Client{Dev: dev}

	return nil
}

func (p *HidProfile) Close() {
	logrus.Infoln("Close HID profile")
}
