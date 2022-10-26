#!/bin/bash
#
# FIX THIS - re-write in golang

bluetoothmac="D6:30:35:39:28:30"
bluetoothuuid="0000ffe9-0000-1000-8000-00805f9b34fb"
cameraip="192.168.8.120"
camerawifi="CEYOMUR-2a78f93b8ad4"
camerawifikey="12345678"
signaloriginator="+441189627101"
signalreceiver="+447867970260 +447974403527"

JAVA_HOME=/home/plord/src/jdk-17.0.2
export JAVA_HOME
PATH=$PATH:/usr/local/bin
export PATH
DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus
export DBUS_SESSION_BUS_ADDRESS

mkdir -p /tmp/trailcamera
cd /tmp/trailcamera

echo "Wifi on"
nmcli radio wifi on
nmcli connection delete ${camerawifi}
if [ -f signal.pid ]
then
	kill $(cat signal.pid)
	rm -f signal.pid
fi
signal-cli -u ${signaloriginator} daemon >/dev/null 2>&1 &
sleep 10
echo $! > signal.pid

#
# enable trail camera wifi
#
echo "Sending bluetooth command"
bluetoothctl scan on &
sleep 20
kill $!
bluetoothctl connect ${bluetoothmac}
bluetoothctl <<!
gatt.select-attribute ${bluetoothuuid}
gatt.write "0x47 0x50 0x49 0x4f 0x33"
!

sudo nmcli dev wifi rescan

count=10
until (nmcli dev wifi list | grep ${camerawifi})
do
	let count=${count}-1
	if [ ${count} -le 0 ]
	then
		break
	fi
	sleep 5
done
nmcli dev wifi
nmcli dev wifi connect ${camerawifi} password ${camerawifikey} wep-key-type key

#
# wait for auto connect
#
count=10
until curl --output /dev/null --silent --head --fail --max-time 10 "http://${cameraip}/"
do
	let count=${count}-1
	if [ ${count} -le 0 ]
	then
		echo "Timout waiting for camera"
		nmcli connection delete ${camerawifi}
		nmcli radio wifi off
		bluetoothctl disconnect ${bluetoothmac}
		if [ -f signal.pid ]
		then
			kill $(cat signal.pid)
			rm -f signal.pid
		fi
		exit 1
	fi
	sleep 5
done
echo "Camera alive"

#
# battery level
#
curl --silent "http://${cameraip}/?custom=1&cmd=3019"

for mode in 0 1
do
	# 
	# switch to phto / video modes
	#
	curl --output /dev/null --silent "http://${cameraip}/?custom=1&cmd=3001&par=${mode}"

	#
	# get list of files
	#
	IFS="
"
	curl --silent "http://${cameraip}/?custom=1&cmd=3015" | xmllint --xpath '//FPATH/text() | //TIME/text()' - | while read -r filename
	do
		echo "filename - ${filename}"
		read timestamp
		echo "timestamp - ${timestamp}"

		url=http://${cameraip}$(echo "${filename}" | sed -e "s+\\\\+/+g" -e "s+^A:++")
		basename=$(basename ${url})
		echo "url = $url"

		#
		# get one file
		#
		echo "Downloading ${url}"
		curl --output "${basename}" --silent "${url}"
		echo $?

		#
		# send to signal
		#
		# signal-cli -u ${signaloriginator} send +447867970260 +447974403527 -a "${basename}" -m "${timestamp}"
		#signal-cli --dbus send +447867970260 +447974403527 -a "${basename}" -m "${timestamp}"
		signal-cli --dbus send -a "${basename}" -m "${timestamp}" ${signalreceiver}

		#
		# delete local and on camera
		#
		if [ $? == 0 ]
		then
			#
			# delete one file
			#
			curl --output /dev/null --silent "http://${cameraip}/?custom=1&cmd=4003&str=${filename}"
		fi
		rm -f "${basename}"

	done
done

signal-cli --debus receive >/dev/null 2>&1

echo "Wifi off"
nmcli connection delete ${camerawifi}
nmcli radio wifi off
bluetoothctl disconnect ${bluetoothmac}
if [ -f signal.pid ]
then
	kill $(cat signal.pid)
	rm -f signal.pid
fi
