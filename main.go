package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wifx/gonetworkmanager"
	"tinygo.org/x/bluetooth"
)

var adapter = bluetooth.DefaultAdapter

func main() {

	// parse arguments
	//
	address := flag.String("address", "D6:30:35:39:28:30", "Bluetooth address")
	uuid := flag.String("characteristic", "0000ffe9-0000-1000-8000-00805f9b34fb", "Bluetooth characteristic uuid")
	ssid := flag.String("ssid", "CEYOMUR-2a78f93b8ad4", "WiFi SSID")
	password := flag.String("password", "12345678", "WiFi password")
	signalUser := flag.String("signaluser", "", "Signal messenger username")
	signalRecipient := flag.String("signalrecipient", "", "Signal messenger recipient - quote for multiple users")
	flag.Parse()

	var err error
	var bluetoothDevice *bluetooth.Device

	// enable wifi via bluetooth command
	//
	for attempt := 1; attempt < 10; attempt++ {
		bluetoothDevice, err = connectBluetooth(address)
		if err != nil {
			if bluetoothDevice != nil {
				disableBluetooth(bluetoothDevice)
			}
			// wait a bit betwwen attempts
			//
			time.Sleep(2 * time.Second)
		} else {
			break
		}
	}
	if err != nil {
		if bluetoothDevice != nil {
			disableBluetooth(bluetoothDevice)
		}
		log.Panicf(err.Error())
	}

	err = enableWifi(bluetoothDevice, uuid)
	if err != nil {
		disableBluetooth(bluetoothDevice)
		log.Panicf(err.Error())
	}
	// disable bluetooth
	//
	disableBluetooth(bluetoothDevice)

	// connect to wifi - loop and wait
	//
	var hostname string
	var nm gonetworkmanager.NetworkManager
	var activeConnection gonetworkmanager.ActiveConnection

	for attempt := 1; attempt < 10; attempt++ {
		nm, activeConnection, hostname, err = connectWifi(ssid, password)
		if err != nil {
			if activeConnection != nil {
				disconnectWifi(nm, activeConnection)
			}
			// wait a bit betwwen attempts
			//
			time.Sleep(2 * time.Second)
		} else {
			break
		}
	}
	if err != nil {
		if activeConnection != nil {
			disconnectWifi(nm, activeConnection)
		}
		log.Panicf(err.Error())
	}

	// download any new pictures
	//
	files, timestamps, err := listFiles(hostname)
	if err != nil {
		if activeConnection != nil {
			disconnectWifi(nm, activeConnection)
		}
		log.Panicf(err.Error())
	}
	for i := 0; i < len(files); i++ {
		tmpFile, err := download(files[i], hostname)
		if err != nil {
			log.Println("Failed to download " + files[i] + " - " + err.Error())
			os.Remove(tmpFile)
			break
		}
		if len(*signalUser) > 0 && len(*signalRecipient) > 0 {
			log.Println("signal-cli", "-u", *signalUser, "send", *signalRecipient, "-m", timestamps[i], "-a", tmpFile)
			cmd := exec.Command("signal-cli", "-u", *signalUser, "send", *signalRecipient, "-m", timestamps[i], "-a", tmpFile)
			stdout, err := cmd.CombinedOutput()
			if err != nil {
				log.Println("signal-cli failed - " + string(stdout))
			} else {
				// all good, can now delete on camera
				//
				err = delete(files[i], hostname)
				if err != nil {
					log.Println("Failed to delete " + files[i] + " - " + err.Error())
				}
			}
		}
		os.Remove(tmpFile)
	}

	// keep signal happy
	//
	if len(*signalUser) > 0 {
		exec.Command("signal-cli", "-u", *signalUser, "receive")
	}

	// disconnect wifi
	//
	disconnectWifi(nm, activeConnection)
}

// Enable and connect to bluetooth device
func connectBluetooth(address *string) (*bluetooth.Device, error) {

	// Enable bluetooth
	//
	log.Println("Enabling bluetooth")
	err := adapter.Enable()
	if err != nil {
		return nil, errors.New("failed to enable bluetooth - " + err.Error())
	}

	ch := make(chan bluetooth.ScanResult, 1)

	// Start scanning
	//
	log.Println("Scanning bluetooth")
	err = adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		log.Println("Found bluetooth device:", result.Address.String(), result.LocalName())
		if result.Address.String() == *address {
			adapter.StopScan()
			ch <- result
		}
	})
	if err != nil {
		return nil, errors.New("failed to complete bluetooth scan - " + err.Error())
	}

	// Connect
	//
	var device *bluetooth.Device
	select {
	case result := <-ch:
		device, err = adapter.Connect(result.Address, bluetooth.ConnectionParams{})
		if err != nil {
			return nil, errors.New("failed to connect to bluetooth device - " + err.Error())
		}
		log.Println("Connected to ", result.Address.String())
	}

	return device, nil
}

// enable wifi by sending bluetooth command
func enableWifi(device *bluetooth.Device, uuid *string) error {

	// get services
	//
	log.Println("Discovering bluetooth services/characteristics")
	srvcs, err := device.DiscoverServices(nil)
	if err != nil {
		return errors.New("failed to discover bluetooth services - " + err.Error())
	}

	// find and write to characteristic
	//
	for _, srvc := range srvcs {
		chars, err := srvc.DiscoverCharacteristics(nil)
		if err != nil {
			return errors.New("failed to discover bluetooth characteristics - " + err.Error())
		}
		for _, char := range chars {
			if char.UUID().String() == *uuid {
				len, err := char.WriteWithoutResponse([]byte{0x47, 0x50, 0x49, 0x4f, 0x33})
				if err != nil || len != 5 {
					return errors.New("failed to write to bluetooth characteristic - " + err.Error())
				}
				log.Println("Enabled WiFi via bluetooth")
				return nil
			}
		}
	}

	return errors.New("unable to locate bluetooth characteristic")
}

// disable bluetooth
func disableBluetooth(device *bluetooth.Device) error {

	err := device.Disconnect()
	if err != nil {
		return errors.New("failed to disable bluetooth - " + err.Error())
	}

	return nil
}

// connect to wifi
func connectWifi(ssid *string, password *string) (gonetworkmanager.NetworkManager, gonetworkmanager.ActiveConnection, string, error) {

	log.Println("Looking for wifi ssid " + *ssid)

	// Create new instance of gonetworkmanager
	//
	nm, err := gonetworkmanager.NewNetworkManager()
	if err != nil {
		return nil, nil, "", errors.New("unable to get network manager - " + err.Error())
	}

	// get all network devices
	//
	devices, err := nm.GetPropertyAllDevices()
	if err != nil {
		return nm, nil, "", errors.New("unable to get network devices - " + err.Error())
	}

	// scan through wifi devices
	//
	for _, device := range devices {

		deviceType, err := device.GetPropertyDeviceType()
		if err != nil {
			return nm, nil, "", errors.New("unable to get network device type - " + err.Error())
		}
		if deviceType == gonetworkmanager.NmDeviceTypeWifi {

			deviceWireless, err := gonetworkmanager.NewDeviceWireless(device.GetPath())
			if err != nil {
				return nm, nil, "", errors.New("unable to get wifi properties - " + err.Error())
			}
			deviceWireless.RequestScan() // note ignore any errors
			accessPoints, err := deviceWireless.GetAllAccessPoints()
			if err != nil {
				return nm, nil, "", errors.New("unable to get wifi access points - " + err.Error())
			}
			for _, accessPoint := range accessPoints {
				name, err := accessPoint.GetPropertySSID()
				if err != nil {
					return nm, nil, "", errors.New("unable to get wifi access point name - " + err.Error())
				}

				// FIX THIS - probabally better with regex match
				//
				if name == *ssid {
					connectionMap := make(map[string]map[string]interface{})
					connectionMap["802-11-wireless"] = make(map[string]interface{})
					connectionMap["802-11-wireless"]["security"] = "802-11-wireless-security"
					connectionMap["802-11-wireless-security"] = make(map[string]interface{})
					connectionMap["802-11-wireless-security"]["key-mgmt"] = "wpa-psk"
					connectionMap["802-11-wireless-security"]["psk"] = password

					activeConnection, err := nm.AddAndActivateWirelessConnection(connectionMap, device, accessPoint)
					if err != nil {
						return nm, nil, "", errors.New("unable to connect to access point - " + err.Error())
					}

					// wait for connection
					//
					for attempt := 1; attempt < 20; attempt++ {
						ip4Config, _ := activeConnection.GetPropertyIP4Config()
						if ip4Config != nil {
							cameraIP, err := ip4Config.GetPropertyGateway()
							if err != nil {
								return nm, nil, "", errors.New("unable to get camera IP address - " + err.Error())
							}
							log.Println("Connected to wifi ssid " + *ssid)
							return nm, activeConnection, cameraIP, nil
						} else {
							time.Sleep(time.Millisecond * 250)
						}
					}

					// timeout for this connection
					//
					disconnectWifi(nm, activeConnection)
				}
			}
		}
	}

	return nm, nil, "", errors.New("ssid not found")
}

// disconnect from wifi
func disconnectWifi(nm gonetworkmanager.NetworkManager, activeConnection gonetworkmanager.ActiveConnection) error {

	connection, err := activeConnection.GetPropertyConnection()
	if err == nil {
		connection.Delete()
	}

	log.Println("Disconnected from wifi")

	return nil
}

type File struct {
	XMLName  xml.Name `xml:"File"`
	Name     string   `xml:"NAME"`
	FPath    string   `xml:"FPATH"`
	Size     string   `xml:"SIZE"`
	Timecode string   `xml:"TIMECODE"`
	Time     string   `xml:"TIME"`
	Attr     string   `xml:"ATTR"`
}

type AllFile struct {
	XMLName xml.Name `xml:"ALLFile"`
	Files   []File   `xml:"File"`
}

type List struct {
	XMLName xml.Name `xml:"LIST"`
	Allfile AllFile  `xml:"ALLFile"`
}

// list files on camera sorted by date
func listFiles(hostname string) ([]string, []string, error) {

	var files []File

	for mode := 0; mode < 2; mode++ {

		// switch mode
		//
		_, err := http.Get("http://" + hostname + "/?custom=1&cmd=3001&par=" + strconv.Itoa(mode))
		if err != nil {
			return nil, nil, errors.New("unable to get camera mode set page - " + err.Error())
		}

		// get xml index
		//
		resp, err := http.Get("http://" + hostname + "/?custom=1&cmd=3015")
		if err != nil {
			return nil, nil, errors.New("unable to get camera index page - " + err.Error())
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, nil, errors.New("unable to get camera index page - " + err.Error())
		}

		// log.Println(string(body[:]))

		// parse xml into new slice
		//
		var list List
		err = xml.Unmarshal(body, &list)
		if err != nil {
			return nil, nil, errors.New("unable to parse index xml - " + err.Error())
		}
		if list.Allfile.Files != nil {
			for i := 0; i < len(list.Allfile.Files); i++ {
				files = append(files, list.Allfile.Files[i])
			}
		}
	}

	// sort slice by timecode
	//
	sort.Slice(files, func(i, j int) bool {
		return files[i].Timecode < files[j].Timecode
	})

	var sortedFiles []string
	var sortedTimestamps []string
	for _, file := range files {
		sortedFiles = append(sortedFiles, file.FPath)
		sortedTimestamps = append(sortedTimestamps, file.Time)
	}

	return sortedFiles, sortedTimestamps, nil
}

// download a file
func download(file string, hostname string) (string, error) {

	url := "http://" + hostname + strings.ReplaceAll(file, "\\", "/")[2:]

	log.Println("Downloading " + url)

	tmpFile, err := ioutil.TempFile("", "image.*"+filepath.Ext(file))
	if err != nil {
		return "", errors.New("unable to download file - " + err.Error())
	}
	defer tmpFile.Close()

	resp, err := http.Get(url)
	if err != nil {
		return tmpFile.Name(), errors.New("unable to download file - " + err.Error())
	}
	defer resp.Body.Close()

	_, err = io.Copy(tmpFile, resp.Body)
	if err != nil {
		return tmpFile.Name(), errors.New("unable to download file - " + err.Error())
	}

	return tmpFile.Name(), nil
}

// delete a file
func delete(file string, hostname string) error {

	_, err := http.Get("http://" + hostname + "/?custom=1&cmd=4003&str=" + file)
	if err != nil {
		return errors.New("unable to delete file - " + err.Error())
	}

	return nil
}
