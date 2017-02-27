package main

import (
	"os"
	"os/signal"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"
)

func exitOnError(msg string, err error) {
	if err != nil {
		logrus.WithError(errors.Cause(err)).Fatal(msg)
	}
}

func userInterrupt() chan os.Signal {
	ch := make(chan os.Signal)
	signal.Notify(ch, os.Interrupt)
	return ch
}

func main() {
	kb, err := NewKeyboard()
	exitOnError("Failed to create keyboard", err)

	hidp, err := NewHidProfile("/red/potch/profile")
	exitOnError("Failed to create HID profile", err)

	exitOnError("Failed to export profile", hidp.Export())

	exitOnError("Failed to register profile", hidp.Register(kb.Desc()))

	logrus.Infoln("HID profile registered")

Loop:
	for {
		select {
		case sig := <-userInterrupt():
			logrus.WithField("signal", sig.String()).
				Errorln("Exiting on user interrupt")
			break Loop
		case client := <-hidp.Connection():
			if err := kb.Connect(client); err != nil {
				client.Sctrl.Close()
				client.Sintr.Close()
			}
		case client := <-hidp.Disconnection():
			kb.Disconnect(client)
		}
	}

	// Probably no need of closing profile
	exitOnError("Failed to unregister profile", errors.Cause(hidp.Unregister()))

	logrus.Infoln("HID profile unregistered")

	hidp.Close()
}
