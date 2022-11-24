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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wifx/gonetworkmanager"
	"tinygo.org/x/bluetooth"
)

var adapter = bluetooth.DefaultAdapter

type Picture struct {
	fileName    string
	tmpFilename string
	timeStamp   string
}

var wg sync.WaitGroup

func main() {

	// parse arguments
	//
	address := flag.String("address", "D6:30:35:.*", "Bluetooth address")
	uuid := flag.String("characteristic", "0000ffe9-0000-1000-8000-00805f9b34fb", "Bluetooth characteristic UUID")
	ssid := flag.String("ssid", "CEYOMUR-.*", "WiFi SSID")
	password := flag.String("password", "12345678", "WiFi password")
	signalUser := flag.String("signaluser", "", "Signal messenger username")
	signalRecipient := flag.String("signalrecipient", "", "Signal messenger recipient - quote for multiple users")
	modelPath := flag.String("model", "detect.tflite", "path to model file")
	labelPath := flag.String("label", "labelmap.txt", "path to label file")
	limits := flag.Int("limits", 5, "limits of items")
	savejpg := flag.Bool("savejpg", false, "save jpg files to $HOME/photos")

	flag.Parse()

	var err error
	var bluetoothDevice *bluetooth.Device

	// enable wifi via bluetooth command
	//
	for attempt := 1; attempt < 10; attempt++ {
		bluetoothDevice, err = connectBluetooth(address)
		if err != nil {
			log.Println("Bluetooth connect failed [" + strconv.Itoa(attempt) + " of 10] - " + string(err.Error()))
			if bluetoothDevice != nil {
				disableBluetooth(bluetoothDevice, uuid)
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
			disableBluetooth(bluetoothDevice, uuid)
		}
		log.Panicf(err.Error())
	}
	for attempt := 1; attempt < 10; attempt++ {
		err = enableWifi(bluetoothDevice, uuid)
		if err != nil {
			log.Println("Enable WiFi failed [" + strconv.Itoa(attempt) + " of 10] - " + string(err.Error()))
		} else {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		disableBluetooth(bluetoothDevice, uuid)
		log.Panicf(err.Error())
	}

	// connect to wifi - loop and wait
	//
	var hostname string
	var nm gonetworkmanager.NetworkManager
	var activeConnection gonetworkmanager.ActiveConnection

	for attempt := 1; attempt < 10; attempt++ {
		nm, activeConnection, hostname, err = connectWifi(ssid, password)
		if err != nil {
			log.Println("WiFi connect failed [" + strconv.Itoa(attempt) + " of 10] - " + string(err.Error()))
			if activeConnection != nil {
				disableBluetooth(bluetoothDevice, uuid)
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
			disableBluetooth(bluetoothDevice, uuid)
			disconnectWifi(nm, activeConnection)
		}

		// wifi failed ... so some diagnostics
		//
		log.Println("WiFi connection failed")

		log.Println("nmcli general status")
		cmd := exec.Command("nmcli", "general", "status")
		stdout, _ := cmd.CombinedOutput()
		log.Println(string(stdout[:]))
		log.Println("nmcli connection show")
		cmd = exec.Command("nmcli", "connection", "show")
		stdout, _ = cmd.CombinedOutput()
		log.Println(string(stdout[:]))
		log.Println("nmcli device status")
		cmd = exec.Command("nmcli", "device", "status")
		stdout, _ = cmd.CombinedOutput()
		log.Println(string(stdout[:]))
		log.Println("nmcli dev wifi list")
		cmd = exec.Command("nmcli", "dev", "wifi", "list")
		stdout, _ = cmd.CombinedOutput()
		log.Println(string(stdout[:]))

		os.Exit(1)
	}

	// get camera status
	//
	battery, _ := status(hostname)
	if battery <= 20 {
		alert(signalUser, signalRecipient, "Warning: battery low at "+strconv.Itoa(battery)+"%", "")
	}

	// download any new pictures
	//
	files, timestamps, err := listFiles(hostname)
	if err != nil {
		if activeConnection != nil {
			disableBluetooth(bluetoothDevice, uuid)
			disconnectWifi(nm, activeConnection)
		}
		log.Panicf(err.Error())
	}

	jobChan := make(chan Picture, 100)
	wg.Add(1)
	go worker(jobChan, hostname, signalUser, signalRecipient, modelPath, labelPath, limits)

	for i := 0; i < len(files); i++ {
		tmpFile, err := download(files[i], hostname)
		if err != nil {
			log.Println("Failed to download " + files[i] + " - " + err.Error())
			os.Remove(tmpFile)
			break
		}
		// queue processing and deleting
		jobChan <- Picture{files[i], tmpFile, timestamps[i]}

		// save a copy of the file
		if *savejpg && filepath.Ext(files[i]) == ".JPG" {
			source, err := os.Open(tmpFile)
			if err != nil {
				log.Println("Unable to open " + files[i] + " for copy - " + err.Error())
				break
			}
			defer source.Close()
			destination, err := ioutil.TempFile(os.Getenv("HOME")+"/photos/", strings.Replace(strings.Replace(timestamps[i]+".*.jpg", "/", "_", -1), " ", "_", -1))
			if err != nil {
				log.Println("Unable to open " + os.Getenv("HOME") + "/photos/" + timestamps[i] + ".*.jpg" + " for copy - " + err.Error())
				break
			}
			_, err = io.Copy(destination, source)
			if err != nil {
				log.Println("Unable to copy " + files[i] + " - " + err.Error())
				break
			}
			defer destination.Close()
		}
	}

	log.Println("Finished download")

	// wait for queue to complete ... dequeue deletes the files once processed so we need to keep wifi working
	//
	close(jobChan)
	wg.Wait()
	log.Println("Finished")

	// disable bluetooth
	//
	disableBluetooth(bluetoothDevice, uuid)

	// disconnect wifi
	//
	disconnectWifi(nm, activeConnection)

}

// process work in a queue
func worker(jobChan <-chan Picture, hostname string, signalUser *string, signalRecipient *string, modelPath *string, labelPath *string, limits *int) {
	defer wg.Done()
	for picture := range jobChan {

		outputfileName, description, err := objectDetect(&picture.tmpFilename, modelPath, labelPath, limits)
		if err != nil {
			log.Println(err.Error())
			err = alert(signalUser, signalRecipient, picture.timeStamp, picture.tmpFilename)
			os.Remove(picture.tmpFilename)
		} else {
			err = alert(signalUser, signalRecipient, picture.timeStamp+" "+*description, picture.tmpFilename+" "+*outputfileName)
			os.Remove(picture.tmpFilename)
			os.Remove(*outputfileName)
		}
		if err != nil {
			log.Println(err.Error())
		} else {
			// all good, can now delete on camera
			//
			err = delete(picture.fileName, hostname)
			if err != nil {
				log.Println("Failed to delete " + picture.fileName + " - " + err.Error())
			}
		}
	}
}

// send an alert via signal
func alert(signalUser *string, signalRecipient *string, message string, attachments string) error {
	if len(*signalUser) > 0 && len(*signalRecipient) > 0 {

		// keep signal happy
		//
		cmd := exec.Command("signal-cli", "-u", *signalUser, "receive")
		stdout, err := cmd.CombinedOutput()
		if err != nil {
			return errors.New("signal-cli failed - " + string(stdout))
		}

		var args []string
		args = append(args, "-u")
		args = append(args, *signalUser)
		args = append(args, "send")
		args = append(args, strings.Split(*signalRecipient, " ")...)
		if len(message) > 0 {
			args = append(args, "-m")
			args = append(args, message)
		}
		if len(attachments) > 0 {
			args = append(args, "-a")
			args = append(args, strings.Split(attachments, " ")...)
		}
		log.Println("signal-cli", args)
		cmd = exec.Command("signal-cli", args...)

		stdout, err = cmd.CombinedOutput()
		if err != nil {
			return errors.New("signal-cli failed - " + string(stdout))
		}
	}

	return nil
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
	log.Println("Scanning bluetooth for " + *address)
	err = adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		log.Println("Found bluetooth device:", result.Address.String(), result.LocalName())
		match, _ := regexp.MatchString(*address, result.Address.String())
		if match {
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
		for attempt := 1; attempt < 2; attempt++ {
			device, err = adapter.Connect(result.Address, bluetooth.ConnectionParams{})
			if err == nil {
				break
			}
		}
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
func disableBluetooth(device *bluetooth.Device, uuid *string) error {

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
				char.WriteWithoutResponse([]byte{0x47, 0x50, 0x49, 0x4f, 0x32})
				log.Println("Disabled WiFi via bluetooth")
			}
		}
	}

	time.Sleep(time.Second)

	err = device.Disconnect()
	time.Sleep(time.Second)
	if err != nil {
		return errors.New("failed to disable bluetooth - " + err.Error())
	}

	return nil
}

// connect to wifi
func connectWifi(ssid *string, password *string) (gonetworkmanager.NetworkManager, gonetworkmanager.ActiveConnection, string, error) {

	log.Println("Looking for WiFi SSID " + *ssid)

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
				return nm, nil, "", errors.New("unable to get WiFi properties - " + err.Error())
			}
			deviceWireless.RequestScan() // note ignore any errors
			accessPoints, err := deviceWireless.GetAllAccessPoints()
			if err != nil {
				return nm, nil, "", errors.New("unable to get WiFi access points - " + err.Error())
			}
			for _, accessPoint := range accessPoints {
				name, err := accessPoint.GetPropertySSID()
				if err != nil {
					return nm, nil, "", errors.New("unable to get WiFi access point name - " + err.Error())
				}

				match, _ := regexp.MatchString(*ssid, name)
				if match {
					connectionMap := make(map[string]map[string]interface{})
					connectionMap["802-11-wireless"] = make(map[string]interface{})
					connectionMap["802-11-wireless"]["security"] = "802-11-wireless-security"
					connectionMap["802-11-wireless-security"] = make(map[string]interface{})
					connectionMap["802-11-wireless-security"]["key-mgmt"] = "wpa-psk"
					connectionMap["802-11-wireless-security"]["psk"] = password
					connectionMap["connection"] = make(map[string]interface{})
					connectionMap["connection"]["id"] = "camera"

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
							log.Println("Connected to WiFi SSID " + name)
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

	return nm, nil, "", errors.New("SSID not found")
}

// disconnect from wifi
func disconnectWifi(nm gonetworkmanager.NetworkManager, activeConnection gonetworkmanager.ActiveConnection) error {

	connection, err := activeConnection.GetPropertyConnection()
	if err == nil {
		connection.Delete()
		log.Println("Disconnected from WiFi")
	}

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

	log.Println(strconv.Itoa(len(sortedFiles)) + " files on camera")

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
	log.Println("Deleted " + file)

	return nil
}

type Function struct {
	XMLName xml.Name `xml:"Function"`
	Cmd     string   `xml:"Cmd"`
	Status  string   `xml:"Status"`
	Value   string   `xml:"Value"`
}

// get status
func status(hostname string) (int, error) {

	var battery int

	// set date
	//
	currentTime := time.Now()
	date := currentTime.Format("2006-01-02")
	_, err := http.Get("http://" + hostname + "/?custom=1&cmd=3005&str=" + date)
	if err != nil {
		log.Println("Unable to set date to " + date + " - " + err.Error())
	} else {
		log.Println("Date set to " + date)
	}
	time := currentTime.Format("15:04:05")
	_, err = http.Get("http://" + hostname + "/?custom=1&cmd=3006&str=" + time)
	if err != nil {
		log.Println("Unable to set time to " + time + " - " + err.Error())
	} else {
		log.Println("Time set to " + time)
	}

	// battery level (?)
	//
	resp, err := http.Get("http://" + hostname + "/?custom=1&cmd=3019")
	if err != nil {
		log.Println("Unable to get 3019 - " + err.Error())
	} else {
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			var function Function
			err = xml.Unmarshal(body, &function)
			if err != nil {
				log.Println("unable to parse 3019 xml - " + err.Error())
			} else {
				battery, _ = strconv.Atoi(function.Value)
				log.Println("Battery at " + function.Value + "%")
			}
		}
	}

	return battery, nil
}
