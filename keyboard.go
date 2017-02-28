package main

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
	HIDPHEADERTRANSMASK = 0xf0

	HIDPTRANSHANDSHAKE   = 0x00
	HIDPTRANSSETPROTOCOL = 0x60
	HIDPTRANSDATA        = 0xa0

	HIDPHSHKSUCCESSFUL = 0x00
	HIDPHSHKERRUNKNOWN = 0x0e
)

func getFirstKeyboard() (kb hid.Device, found bool) {
	hid.UsbWalk(func(d hid.Device) {
		if found {
			return
		}

		// Protofol 1 for keyboard, 2 for mouse
		if d.Info().Protocol == 1 {
			kb = d
			found = true
		}
	})

	return
}

type Client struct {
	Dev   dbus.ObjectPath
	Sintr *Bluetooth
	Sctrl *Bluetooth
}

type Keyboard struct {
	sync.Mutex
	client *Client
	dev    hid.Device
	sdp    string
	stop   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
}

func (kb *Keyboard) Desc() string {
	return kb.sdp
}

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
		dev:  dev,
		stop: make(chan struct{}),
		sdp:  hex.EncodeToString(desc),
	}, nil
}

func (kb *Keyboard) Client() *Client {
	kb.Lock()
	defer kb.Unlock()
	return kb.client
}

func (kb *Keyboard) HandleHID() {
	kb.wg.Add(1)
	defer kb.wg.Done()
	defer kb.dev.Close()

	for {
		select {
		case <-kb.stop:
			return
		default:
		}

		// Set timeout to 1 second, so read does not block forever
		state, err := kb.dev.Read(-1, time.Second)
		if err != nil {
			// connection timeout is normal when the keyboard is idle
			logrus.WithError(err).Debugln("Error in read")
			// TODO: handle fatal error like device disconnection
			continue
		}

		client := kb.Client()

		if client == nil {
			continue
		}

		if _, err := client.Sintr.Write(append([]byte{0xA1}, state...)); err != nil {
			logrus.WithError(err).Errorln("Error in write")
			continue
		}
	}
}

func (kb *Keyboard) Stop() {
	kb.Disconnect(kb.Client())
	kb.once.Do(func() {
		close(kb.stop)
		kb.wg.Wait()
		logrus.Infoln("Keyboard stopped")
	})
}

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

	return nil
}

func (kb *Keyboard) HandleHandshake() {
	kb.wg.Add(1)
	defer kb.wg.Done()

	for {
		select {
		case <-kb.stop:
			return
		default:
		}

		client := kb.Client()
		if client == nil {
			time.Sleep(time.Second)
			continue
		}

		r := make([]byte, BUFSIZE)
		d, err := client.Sctrl.Read(r)

		if err != nil || d < 1 {
			logrus.WithError(err).WithField("read", d).
				Errorln("Failed to read from sctrl")
			kb.Disconnect(client)
			continue
		}

		hsk := []byte{HIDPTRANSHANDSHAKE}
		msgTyp := r[0] & HIDPHEADERTRANSMASK

		switch {
		case (msgTyp & HIDPTRANSSETPROTOCOL) != 0:
			logrus.Debugln("handshake set protocol")
			hsk[0] |= HIDPHSHKSUCCESSFUL
			if _, err := client.Sctrl.Write(hsk); err != nil {
				logrus.WithError(err).Debugln("handshake set protocol failed")
			}
		case (msgTyp & HIDPTRANSDATA) != 0:
			logrus.Debugln("handshake data")
		default:
			logrus.Debugln("unknown handshake message")
			hsk[0] |= HIDPHSHKERRUNKNOWN
			client.Sctrl.Write(hsk)
		}
	}
}

func (kb *Keyboard) Disconnect(client *Client) error {
	kb.Lock()
	defer kb.Unlock()

	if client == nil || client.Dev != kb.client.Dev {
		return nil
	}

	logrus.WithField("dev", client.Dev).Infoln("Disconnecting")

	defer func() {
		kb.client = nil
	}()

	if err := client.Sctrl.Close(); err != nil {
		return err
	}

	return client.Sintr.Close()
}
