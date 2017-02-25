package main

import (
	"bytes"
	"fmt"
	"syscall"
	"text/template"

	"github.com/Sirupsen/logrus"
	"github.com/godbus/dbus"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
)

const sdpTpl = `
<?xml version="1.0" encoding="UTF-8" ?>
<record>
	<attribute id="0x0001">
		<sequence>
			<uuid value="0x1124" />
		</sequence>
	</attribute>
	<attribute id="0x0004">
		<sequence>
			<sequence>
				<uuid value="0x0100" />
				<uint16 value="0x0011" />
			</sequence>
			<sequence>
				<uuid value="0x0011" />
			</sequence>
		</sequence>
	</attribute>
	<attribute id="0x0005">
		<sequence>
			<uuid value="0x1002" />
		</sequence>
	</attribute>
	<attribute id="0x0006">
		<sequence>
			<uint16 value="0x656e" />
			<uint16 value="0x006a" />
			<uint16 value="0x0100" />
		</sequence>
	</attribute>
	<attribute id="0x0009">
		<sequence>
			<sequence>
				<uuid value="0x1124" />
				<uint16 value="0x0100" />
			</sequence>
		</sequence>
	</attribute>
	<attribute id="0x000d">
		<sequence>
			<sequence>
				<sequence>
					<uuid value="0x0100" />
					<uint16 value="0x0013" />
				</sequence>
				<sequence>
					<uuid value="0x0011" />
				</sequence>
			</sequence>
		</sequence>
	</attribute>
	<attribute id="0x0100">
		<text value="Raspberry Pi Virtual Keyboard" />
	</attribute>
	<attribute id="0x0101">
		<text value="USB > BT Keyboard" />
	</attribute>
	<attribute id="0x0102">
		<text value="Raspberry Pi" />
	</attribute>
	<attribute id="0x0200">
		<uint16 value="0x0100" />
	</attribute>
	<attribute id="0x0201">
		<uint16 value="0x0111" />
	</attribute>
	<attribute id="0x0202">
		<uint8 value="0x40" />
	</attribute>
	<attribute id="0x0203">
		<uint8 value="0x00" />
	</attribute>
	<attribute id="0x0204">
		<boolean value="true" />
	</attribute>
	<attribute id="0x0205">
		<boolean value="true" />
	</attribute>
	<attribute id="0x0206">
		<sequence>
			<sequence>
				<uint8 value="0x22" />
				<text encoding="hex" value="{{.HIDDesc}}" />
			</sequence>
		</sequence>
	</attribute>
	<attribute id="0x0207">
		<sequence>
			<sequence>
				<uint16 value="0x0409" />
				<uint16 value="0x0100" />
			</sequence>
		</sequence>
	</attribute>
	<attribute id="0x020b">
		<uint16 value="0x0100" />
	</attribute>
	<attribute id="0x020c">
		<uint16 value="0x0c80" />
	</attribute>
	<attribute id="0x020d">
		<boolean value="false" />
	</attribute>
	<attribute id="0x020e">
		<boolean value="true" />
	</attribute>
	<attribute id="0x020f">
		<uint16 value="0x0640" />
	</attribute>
	<attribute id="0x0210">
		<uint16 value="0x0320" />
	</attribute>
</record>
`

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

func (p *HidProfile) Register(desc string) error {
	callback := make(chan *dbus.Call, 1)

	tpl, err := template.New("sdp").Parse(sdpTpl)
	if err != nil {
		return err
	}

	sdp := bytes.NewBuffer(nil)
	if err := tpl.Execute(sdp, struct{ HIDDesc string }{desc}); err != nil {
		return err
	}

	opts := map[string]dbus.Variant{
		"PSM": dbus.MakeVariant(uint16(PSMCTRL)),
		"RequireAuthentication": dbus.MakeVariant(true),
		"RequireAuthorization":  dbus.MakeVariant(true),
		"ServiceRecord":         dbus.MakeVariant(sdp.String()),
	}

	if err = p.bus.Object("org.bluez", "/org/bluez").Go(
		"org.bluez.ProfileManager1.RegisterProfile",
		0, callback, p.path, p.uid, opts,
	).Err; err != nil {
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
		syscall.Close(int(fd))
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
