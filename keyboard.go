package main

import (
	"encoding/hex"
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
	dev    hid.Device
	sdp    string
	stop   chan struct{}
	client dbus.ObjectPath
	Sintr  *Bluetooth
	Sctrl  *Bluetooth
}

func (kb *Keyboard) SDP() string {
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

func (kb *Keyboard) Start() {
	for {
		select {
		case <-kb.stop:
			return
		default:
		}

		state, err := kb.dev.Read(-1, time.Second)
		if err != nil {
			logrus.WithError(err).Errorln("Error in read")
			continue
		}

		if kb.Sctrl == nil {
			continue
		}

		if _, err := kb.Sctrl.Write(append([]byte{0xA1}, state...)); err != nil {
			logrus.WithError(err).Errorln("Error in write")
			continue
		}
	}
}

func (kb *Keyboard) Stop() {
	kb.stop <- struct{}{}
}

func (kb *Keyboard) Connect(client *Client) error {
	// Only support one connection at a time, since controlling more than
	// one device with one keyboard is typically not what we want
	if kb.client != "" {
		return errors.New("keyboard in use")
	}

	kb.client = client.Dev
	kb.Sctrl = client.Sctrl
	kb.Sintr = client.Sintr

	if _, err := kb.Sctrl.Write([]byte{0xA1, 0x13, 0x03}); err != nil {
		return errors.Wrap(err, "failed to send hello on ctrl 1")
	}

	if _, err := kb.Sctrl.Write([]byte{0xA1, 0x13, 0x02}); err != nil {
		return errors.Wrap(err, "failed to send hello on ctrl 2")
	}

	return nil
}

func (kb *Keyboard) HandleEvent() {
	for {
		if kb.Sctrl == nil {
			time.Sleep(time.Second)
			continue
		}

		r := make([]byte, BUFSIZE)
		d, err := kb.Sctrl.Read(r)

		if err != nil || d < 1 {

		}

		hsk := []byte{HIDPTRANSHANDSHAKE}
		msgTyp := r[0] & HIDPHEADERTRANSMASK

		switch {
		case (msgTyp & HIDPTRANSSETPROTOCOL) != 0:
			logrus.Debugln("GoBt.procesCtrlEvent: handshake set protocol")
			hsk[0] |= HIDPHSHKSUCCESSFUL
			if _, err := kb.Sctrl.Write(hsk); err != nil {
				logrus.Debugln("GoBt.procesCtrlEvent: handshake set protocol: failure on reply")
			}
		case (msgTyp & HIDPTRANSDATA) != 0:
			logrus.Debugln("GoBt.procesCtrlEvent: handshake data")
		default:
			logrus.Debugln("GoBt.procesCtrlEvent: unknown handshake message")
			hsk[0] |= HIDPHSHKERRUNKNOWN
			kb.Sctrl.Write(hsk)
		}
	}
}

func (kb *Keyboard) Disconnect(client *Client) error {
	if client.Dev != kb.client {
		return nil
	}

	defer func() {
		kb.Sctrl = nil
		kb.Sintr = nil
		kb.client = ""
	}()

	if err := kb.Sctrl.Close(); err != nil {
		return err
	}

	return kb.Sintr.Close()
}
