# btk
Keyboard relay from USB to Bluetooth

**NOTE**: Only works on Linux

> More detail and deployment instruction coming soon

## Usage

- Install dependencies

```
sudo apt-get install bluez
```

- Modify `/lib/systemd/system/bluetooth.service`:

Change
```
ExecStart=/usr/lib/bluetooth/bluetoothd
```
to
```
ExecStart=/usr/lib/bluetooth/bluetoothd -C --noplugin=input
```

Then `sudo service bluetooth restart`

- Download the binary program

**Built binary release comming soon, if you want to try now, build it from source**

- Start

```
sudo ./btk
```

## Build

```
go get github.com/inoc603/btk
cd $GOPATH/src/github.com/inoc603/btk
make build
```

## Credits

Inspired by these amazing projects and articles:
- [lvht/btk](https://github.com/lvht/btk)
- [potch8228/gobt](https://github.com/potch8228/gobt)
- [zserge/hid](https://github.com/zserge/hid)
- [Python编程.Bluetooth HID Mouse and Keyboard（一）](http://blog.csdn.net/huipengzhao/article/details/18268201)
