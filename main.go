package main

import (
	"os"
	"os/exec"
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

	exitOnError(
		"Failed to set to piscan",
		exec.Command("hciconfig", "hci0", "piscan").Run(),
	)

	exitOnError(
		"Failed to set device class",
		exec.Command("hciconfig", "hci0", "class", "02540").Run(),
	)

	logrus.WithField("desc", kb.Desc()).Infoln("HID profile registered")

	go kb.Start()
	go kb.HandleEvent()

Loop:
	for {
		select {
		case sig := <-userInterrupt():
			logrus.WithField("signal", sig.String()).
				Errorln("Exiting on user interrupt")
			kb.Stop()
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
