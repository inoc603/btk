package btk

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/godbus/dbus"
	"github.com/pkg/errors"
	"github.com/zserge/hid"
)

const (
	hidpHeaderTransMask = 0xf0

	hidpTransHandshake   = 0x00
	hidpTransSetProtocol = 0x60
	hidpTransData        = 0xa0

	hidpHshkSuccessful = 0x00
	hidpHshkErrUnknown = 0x0e

	protocolKeyboard = 1
	protocolMouse    = 2
)

func getFirstKeyboard() (kb hid.Device, found bool) {
	hid.UsbWalk(func(d hid.Device) {
		if found {
			return
		}

		if d.Info().Protocol == protocolKeyboard {
			kb = d
			found = true
		}
	})

	return
}

// Client represents a bluetooth client
type Client struct {
	Dev   dbus.ObjectPath
	Sintr *Bluetooth
	Sctrl *Bluetooth
	Done  chan struct{}
}

// Keyboard represents a HID keyboard
type Keyboard struct {
	sync.Mutex
	client *Client
	dev    hid.Device
	sdp    string
	once   sync.Once
}

// Desc returns the HID descriptor of the usb keyboard
func (kb *Keyboard) Desc() string {
	return kb.sdp
}

// NewKeyboard returns a new keyboard on the first usb keyboard connected.
func NewKeyboard() (*Keyboard, error) {
	dev, ok := getFirstKeyboard()
	if !ok {
		return nil, errors.New("no hid keyboard found")
	}

	if err := dev.Open(); err != nil {
		return nil, errors.Wrap(err, "failed to open hid device")
	}

	desc, err := dev.HIDReport()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get HID descriptor")
	}

	return &Keyboard{
		dev: dev,
		sdp: hex.EncodeToString(desc),
	}, nil
}

// Client returns the current bluetooth client of the keyboard
func (kb *Keyboard) Client() *Client {
	kb.Lock()
	defer kb.Unlock()
	return kb.client
}

// HandleHID starts a loop to read from the usb keyboard, it blocks until there's
// a fatal error reading from the keyboard, e.g. keyboard disconnection
func (kb *Keyboard) HandleHID() {
	defer kb.dev.Close()

	for {
		// Set timeout to 1 second, so read does not block forever
		state, err := kb.dev.Read(-1, time.Second)
		if err != nil {
			// connection timeout is normal when the keyboard is idle.
			// Although inspecting the error message is not a good
			// way to check the error, we'll get on with it to
			// prevent the too many debug log
			if err.Error() != "connection timed out" {
				logrus.WithError(err).Errorln("Error in read from keyboard")
			}
			// TODO: handle fatal error like device disconnection
			continue
		}

		logrus.WithField("state", state).Debugln("Keyboard input")

		client := kb.Client()
		if client == nil {
			continue
		}

		if _, err := client.Sintr.Write(append([]byte{0xA1}, state...)); err != nil {
			logrus.WithError(err).Errorln("Error in write to client")
			continue
		}
	}
}

// Stop close the usb keyboard
func (kb *Keyboard) Stop() {
	kb.once.Do(func() {
		// Violently close the usb keyboard, HandleHID() will exit on error
		kb.dev.Close()
		logrus.Warnln("Keyboard stopped")
	})
}

// Connect hooks up the given client with the usb keyboard, and start piping
// keypresses to the client. Will return an error if the keyboard is already
// in use
func (kb *Keyboard) Connect(client *Client) error {
	kb.Lock()
	defer kb.Unlock()
	// Only support one connection at a time, since controlling more than
	// one device with one keyboard is typically not what we want
	if kb.client != nil {
		return errors.New("keyboard in use")
	}

	kb.client = client

	if _, err := client.Sctrl.Write([]byte{0xA1, 0x13, 0x03}); err != nil {
		return errors.Wrap(err, "failed to send hello on ctrl 1")
	}

	if _, err := client.Sctrl.Write([]byte{0xA1, 0x13, 0x02}); err != nil {
		return errors.Wrap(err, "failed to send hello on ctrl 2")
	}

	go kb.handleHandshake()

	return nil
}

// handleHandshake handles bluetooth handshake messages, and it's also an
// indicator of client disconnection
func (kb *Keyboard) handleHandshake() {
	client := kb.client
	if client == nil {
		return
	}
	logger := logrus.WithField("client", client.Dev)
	logger.Debugln("Start handling handshake")

	for {
		select {
		case <-client.Done:
			logger.Debugln("Exit handling handshake")
			return
		default:
		}

		r := make([]byte, BUFSIZE)
		d, err := client.Sctrl.Read(r)

		if err != nil || d < 1 {
			// a read error means the client has disconnected
			logger.WithError(err).WithField("read", d).
				Errorln("Failed to read from sctrl")
			kb.Disconnect(client)
			continue
		}

		hsk := []byte{hidpTransHandshake}
		msgTyp := r[0] & hidpHeaderTransMask

		switch {
		case (msgTyp & hidpTransSetProtocol) != 0:
			logger.Debugln("handshake set protocol")
			hsk[0] |= hidpHshkSuccessful
			if _, err := client.Sctrl.Write(hsk); err != nil {
				logger.WithError(err).Debugln("handshake set protocol failed")
			}
		case (msgTyp & hidpTransData) != 0:
			logger.Debugln("handshake data")
		default:
			logger.Debugln("unknown handshake message")
			hsk[0] |= hidpHshkErrUnknown
			client.Sctrl.Write(hsk)
		}
	}
}

// Disconnect closes the connection to the given bluetooth client
// Currently this is just some cleanning up. It can't close the actual
// bluetooth connection, and will block on the attempt
// TODO: Find a way to close the connection
func (kb *Keyboard) Disconnect(client *Client) error {
	kb.Lock()
	defer kb.Unlock()

	if client == nil || client.Dev != kb.client.Dev {
		return nil
	}

	logrus.WithField("client", client.Dev).Infoln("Disconnecting")

	defer func() {
		close(kb.client.Done)
		kb.client = nil
	}()

	if err := client.Sctrl.Close(); err != nil {
		return err
	}

	return client.Sintr.Close()
}
