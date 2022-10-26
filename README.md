# trailcameradownload

Periodically download pictures and movies from a [wildlife camera](https://www.amazon.co.uk/gp/product/B09Y8V268F)
and send to signal accounts.

I chose an [odroid c4](https://ameridroid.com/products/odroid-c4) with [dual wifi/bluetooth usb adapter](https://thepihut.com/products/combination-wifi-bluetooth-4-0-usb-adapter).

## OS setup

* Setup as per odroid intructions - I used armbian
* setup sudoers - FIX THIS, is this necessary ?
* see https://github.com/AsamK/signal-cli to install signal-cli
* `sudo apt-get install vim git man-db bluez psmisc wireless-tools libxml2-utils openjdk-17-jdk`
* `git clone https://github.com/plord12/trailcameradownload.git`
