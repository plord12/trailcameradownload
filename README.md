# trailcameradownload

Periodically download pictures and movies from a [wildlife camera](https://www.amazon.co.uk/gp/product/B09Y8V268F)
and send to signal accounts.

I chose an [odroid c4](https://ameridroid.com/products/odroid-c4) with [dual wifi/bluetooth usb adapter](https://thepihut.com/products/combination-wifi-bluetooth-4-0-usb-adapter).

## OS setup

* Setup as per odroid intructions - I used armbian
* see https://github.com/AsamK/signal-cli to install signal-cli
* `sudo apt-get install vim git man-db bluez psmisc wireless-tools libxml2-utils openjdk-17-jdk golang-1.19`
* `git clone https://github.com/plord12/trailcameradownload.git`
* `cd trailcameradownload`
* `make`

## Running

```
$ trailcameradownload-linux-arm64 -signalrecipient "+44xxxxxxxxxx" -signaluser +44xxxxxxxxxx
2022/11/02 15:42:01 Enabling bluetooth
2022/11/02 15:42:01 Scanning bluetooth for D6:30:35:.*
2022/11/02 15:42:12 Found bluetooth device: D6:30:35:39:28:30
2022/11/02 15:42:13 Connected to  D6:30:35:39:28:30
2022/11/02 15:42:13 Discovering bluetooth services/characteristics
2022/11/02 15:42:14 Enabled WiFi via bluetooth
2022/11/02 15:42:16 Looking for wifi ssid CEYOMUR-.*
2022/11/02 15:42:18 Looking for wifi ssid CEYOMUR-.*
2022/11/02 15:42:20 Looking for wifi ssid CEYOMUR-.*
2022/11/02 15:42:22 Looking for wifi ssid CEYOMUR-.*
2022/11/02 15:42:24 Looking for wifi ssid CEYOMUR-.*
2022/11/02 15:42:27 Connected to wifi ssid CEYOMUR-2a78f93b8ad4
2022/11/02 15:42:27 Date set to 2022-11-02
2022/11/02 15:42:27 Time set to 15:42:27
2022/11/02 15:42:28 Battery at 85%
2022/11/02 15:42:31 2 files on camera
2022/11/02 15:42:31 Downloading http://192.168.8.120/DCIM/PHOTO/IM_00001.JPG
2022/11/02 15:42:36 signal-cli [-u +44xxxxxxxxxx send +44xxxxxxxxxx -m 2022/11/02 15:40:54 -a /tmp/image.1214875969.JPG]
2022/11/02 15:42:40 Downloading http://192.168.8.120/DCIM/MOVIE/VD_00001.MP4
2022/11/02 15:44:29 signal-cli [-u +44xxxxxxxxxx send +44xxxxxxxxxx -m 2022/11/02 15:41:06 -a /tmp/image.3880520381.MP4]
2022/11/02 15:44:41 Disconnected from wifi
```

<img src="Screenshot_20221101-151812_Signal.jpg" width="400">