.PHONY: build

build:
	go build -i -o btk cmd/main.go

rpi:
	GOOS=linux GOARCH=arm GOARM=7 go build -i -o btk cmd/main.go
