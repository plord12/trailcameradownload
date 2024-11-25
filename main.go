package main

import (
	"bufio"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "net/http/pprof"

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

var modelLoaded = false

func main() {

	// parse arguments
	//
	address := flag.String("address", "D6:30:35:.*", "Bluetooth address")
	uuid := flag.String("characteristic", "0000ffe9-0000-1000-8000-00805f9b34fb", "Bluetooth characteristic UUID")
	ssid := flag.String("ssid", "CEYOMUR-.*", "WiFi SSID")
	password := flag.String("password", "12345678", "WiFi password")
	signalUser := flag.String("signaluser", "", "Signal messenger username")
	signalGroup := flag.String("signalgroup", "", "Signal messenger group id")
	signalRecipient := flag.String("signalrecipient", "", "Signal messenger recipient - quote for multiple users")
	modelPath := flag.String("model", "detect.tflite", "path to model file")
	labelPath := flag.String("label", "labelmap.txt", "path to label file")
	limits := flag.Int("limits", 5, "limits of items")
	savejpg := flag.Bool("savejpg", false, "save jpg files to $HOME/photos")
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to `file`")
	memprofile := flag.String("memprofile", "", "write memory profile to `file`")
	xnnpack := flag.Bool("xnnpack", false, "use XNNPACK delegate")
	undeletedfiles := flag.Bool("undeletedfiles", false,
		"maintain list of undeleted files in $HOME/.undeleted-[Bluetooth address]")
	testfiles := flag.String("testfiles", "", "list of testfiles - disables connecting to camera")
	mount := flag.String("mount", "/mnt/trailcamera", "Locally mounted USB directory")

	flag.Parse()

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatalf("could not create CPU profile: %s", err.Error())
		}
		defer f.Close() // error handling omitted for example
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatalf("could not start CPU profile: %s", err.Error())
		}
		defer pprof.StopCPUProfile()
	}

	var err error
	var bluetoothDevice *bluetooth.Device
	var bluetoothAdress string

	// load model early
	//
	// if failed, report error and continue
	err = loadModel(modelPath, labelPath, xnnpack)
	if err != nil {
		log.Println(err.Error())
	} else {
		modelLoaded = true
	}

	if len(*testfiles) > 0 {
		for _, picture := range strings.Split(*testfiles, ",") {
			outputfileName, description, _, err := objectDetect(&picture, limits, true)
			if err == nil {
				destinationFile := strings.TrimSuffix(picture, filepath.Ext(picture)) + "-out" + filepath.Ext(picture)
				input, err := ioutil.ReadFile(*outputfileName)
				if err != nil {
					log.Printf("Read processed file failed - %s\n", err.Error())
				} else {
					err = ioutil.WriteFile(destinationFile, input, 0644)
					if err != nil {
						log.Printf("Write processed file failed - %s\n", err.Error())
					} else {
						log.Printf("%s -> %s, %s\n", picture, destinationFile, *description)
					}
				}
			} else {
				log.Printf("Detection failed - %s\n", err.Error())
			}
			os.Remove(*outputfileName)
		}
		return
	}

	// check if camera is locally mounted
	//
	_, err = os.Stat(path.Join(*mount, "DCIM"))
	if err == nil {
		log.Printf("Camera USB mounted")

		// list files, sorted by date
		//
		type fileStruct struct {
			fileName string
			modTime  time.Time
		}
		var files []fileStruct

		entries, err := os.ReadDir(path.Join(*mount, "DCIM", "MOVIE"))
		if err != nil {
			log.Println(err.Error())
			alert(signalUser, signalRecipient, signalGroup, "Camera: unable to list files on USB", "")
			os.Exit(1)
		}
		for _, e := range entries {
			var file fileStruct
			fileInfo, _ := e.Info()
			file.fileName = path.Join(*mount, "DCIM", "MOVIE", fileInfo.Name())
			file.modTime = fileInfo.ModTime()
			files = append(files, file)
		}

		entries, err = os.ReadDir(path.Join(*mount, "DCIM", "PHOTO"))
		if err != nil {
			log.Println(err.Error())
			alert(signalUser, signalRecipient, signalGroup, "Camera: unable to list files on USB", "")
			os.Exit(1)
		}
		for _, e := range entries {
			var file fileStruct
			fileInfo, _ := e.Info()
			file.fileName = path.Join(*mount, "DCIM", "PHOTO", fileInfo.Name())
			file.modTime = fileInfo.ModTime()
			files = append(files, file)
		}

		// sort by ModTime
		//
		sort.Slice(files, func(i, j int) bool {
			return files[i].modTime.Before(files[j].modTime)
		})

		log.Printf("%d files on camera\n", len(files))

		alert(signalUser, signalRecipient, signalGroup, "Camera: USB connected "+strconv.Itoa(len(files))+" files to download", "")

		jobChan := make(chan Picture, len(files))
		wg.Add(1)
		go workerLocal(jobChan, signalUser, signalRecipient, signalGroup, limits, len(files))

		for i := 0; i < len(files); i++ {
			// queue processing and deleting
			jobChan <- Picture{files[i].fileName, files[i].fileName, files[i].modTime.String()}

			// save a copy of the file
			if *savejpg && (strings.EqualFold(filepath.Ext(files[i].fileName), ".JPG") || strings.EqualFold(filepath.Ext(files[i].fileName), ".JPEG")) {
				source, err := os.Open(files[i].fileName)
				if err != nil {
					log.Printf("Unable to open %s for copy - %s\n", files[i].fileName, err.Error())
					break
				}
				defer source.Close()
				destination, err := ioutil.TempFile(os.Getenv("HOME")+"/photos/", strings.Replace(strings.Replace(files[i].modTime.String()+".*.jpg", "/", "_", -1), " ", "_", -1))
				if err != nil {
					log.Printf("Unable to open %s for copy - %s\n", os.Getenv("HOME")+"/photos/"+files[i].modTime.String()+".*.jpg", err.Error())
					break
				}
				_, err = io.Copy(destination, source)
				if err != nil {
					log.Printf("Unable to copy %s - %s\n", files[i].fileName, err.Error())
					break
				}
				defer destination.Close()
			}
		}

		log.Println("Finished download")

		// wait for queue to complete
		//
		close(jobChan)
		wg.Wait()
		log.Println("Finished")

	} else {

		// enable wifi via bluetooth command
		//
		for attempt := 1; attempt < 10; attempt++ {
			bluetoothDevice, bluetoothAdress, err = connectBluetooth(address)
			if err != nil {
				log.Printf("Bluetooth connect failed [%d of 10] - %s\n", attempt, string(err.Error()))
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
			log.Println(err.Error())
			alert(signalUser, signalRecipient, signalGroup, "Camera: unable to connect via bluetooth", "")
			os.Exit(1)
		}
		for attempt := 1; attempt < 10; attempt++ {
			err = enableWifi(bluetoothDevice, uuid)
			if err != nil {
				log.Printf("Enable WiFi failed [%d of 10] - %s\n", attempt, string(err.Error()))
			} else {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if err != nil {
			disableBluetooth(bluetoothDevice, uuid)
			log.Println(err.Error())
			alert(signalUser, signalRecipient, signalGroup, "Camera: unable to connect via WiFi", "")
			os.Exit(1)
		}

		// connect to wifi - loop and wait
		//
		var hostname string
		var nm gonetworkmanager.NetworkManager
		var activeConnection gonetworkmanager.ActiveConnection

		for attempt := 1; attempt < 10; attempt++ {
			nm, activeConnection, hostname, err = connectWifi(ssid, password)
			if err != nil {
				log.Printf("WiFi connect failed [%d of 10] - %s", attempt, string(err.Error()))
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

		var undeletedPath string = ""

		// delete any old pictures first
		//
		if *undeletedfiles {
			undeletedPath = os.Getenv("HOME") + "/.undeleted-" + bluetoothAdress
			if _, err := os.Stat(undeletedPath); err == nil {
				file, err := os.Open(undeletedPath)
				if err != nil {
					log.Printf("Unable to open undeleted file - %s\n", err.Error())
				} else {
					defer file.Close()
					scanner := bufio.NewScanner(file)
					for scanner.Scan() {
						delete(scanner.Text(), hostname)
					}
					if err := scanner.Err(); err != nil {
						log.Printf("Unable to read undeleted file - %s\n", err.Error())
					}
					file.Close()
				}
				os.Remove(undeletedPath)
			}
		}

		// download any new pictures
		//
		files, timestamps, err := listFiles(hostname)
		if err != nil {
			if activeConnection != nil {
				disableBluetooth(bluetoothDevice, uuid)
				disconnectWifi(nm, activeConnection)
			}
			log.Println(err.Error())
			alert(signalUser, signalRecipient, signalGroup, "Camera: unable to download files", "")
			os.Exit(1)
		}

		if battery <= 20 {
			alert(signalUser, signalRecipient, signalGroup,
				"Camera: battery low at "+strconv.Itoa(battery)+"%, "+strconv.Itoa(len(files))+" files to download", "")
		} else if battery > 100 {
			alert(signalUser, signalRecipient, signalGroup,
				"Camera: battery charging, "+strconv.Itoa(len(files))+" files to download", "")
		} else {
			alert(signalUser, signalRecipient, signalGroup,
				"Camera: battery at "+strconv.Itoa(battery)+"%, "+strconv.Itoa(len(files))+" files to download", "")
		}

		jobChan := make(chan Picture, len(files))
		wg.Add(1)
		go worker(jobChan, hostname, signalUser, signalRecipient, signalGroup, limits, undeletedPath, len(files))

		for i := 0; i < len(files); i++ {
			tmpFile, err := download(files[i], hostname)
			if err != nil {
				log.Printf("Failed to download %s - %s\n", files[i], err.Error())
				os.Remove(tmpFile)
				break
			}
			// queue processing and deleting
			jobChan <- Picture{files[i], tmpFile, timestamps[i]}

			// save a copy of the file
			if *savejpg && (strings.EqualFold(filepath.Ext(files[i]), ".JPG") || strings.EqualFold(filepath.Ext(files[i]), ".JPEG")) {
				source, err := os.Open(tmpFile)
				if err != nil {
					log.Printf("Unable to open %s for copy - %s\n", files[i], err.Error())
					break
				}
				defer source.Close()
				destination, err := ioutil.TempFile(os.Getenv("HOME")+"/photos/", strings.Replace(strings.Replace(timestamps[i]+".*.jpg", "/", "_", -1), " ", "_", -1))
				if err != nil {
					log.Printf("Unable to open %s for copy - %s\n", os.Getenv("HOME")+"/photos/"+timestamps[i]+".*.jpg", err.Error())
					break
				}
				_, err = io.Copy(destination, source)
				if err != nil {
					log.Printf("Unable to copy %s - %s\n", files[i], err.Error())
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

		if *memprofile != "" {
			f, err := os.Create(*memprofile)
			if err != nil {
				log.Fatalf("could not create memory profile: %s\n", err)
			}
			defer f.Close() // error handling omitted for example
			runtime.GC()    // get up-to-date statistics
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Fatalf("could not write memory profile: %s\n", err)
			}
		}
	}
}

// process work in a queue
func worker(jobChan <-chan Picture, hostname string, signalUser *string, signalRecipient *string, signalGroup *string, limits *int, undeletedPath string, maxFiles int) {
	defer wg.Done()

	var undeletedFile *os.File = nil
	var err error
	var fileCount int = 1

	if len(undeletedPath) > 0 {
		undeletedFile, err = os.OpenFile(undeletedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("Unable to open undeleted file - %s\n", err.Error())
			undeletedFile = nil
		} else {
			defer undeletedFile.Close()
		}
	}

	for picture := range jobChan {

		if modelLoaded {
			outputfileName, description, _, err := objectDetect(&picture.tmpFilename, limits, false)
			if err != nil {
				log.Println(err.Error())
				err = alert(signalUser, signalRecipient, signalGroup, picture.timeStamp, picture.tmpFilename)
				os.Remove(picture.tmpFilename)
			} else {
				if len(*description) > 0 {
					message := fmt.Sprintf("[%d of %d] %s description: %s", fileCount, maxFiles, picture.timeStamp, *description)
					err = alert(signalUser, signalRecipient, signalGroup, message, picture.tmpFilename+" "+*outputfileName)
				} else {
					message := fmt.Sprintf("[%d of %d] %s", fileCount, maxFiles, picture.timeStamp)
					err = alert(signalUser, signalRecipient, signalGroup, message, picture.tmpFilename)
				}
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
					if undeletedFile != nil {
						if _, err := undeletedFile.WriteString(picture.fileName + "\n"); err != nil {
							log.Println("Unable to write to undeleted file - ", err.Error())
						}
						undeletedFile.Sync()
					}
				}
			}
		} else {
			// no object detection
			//
			message := fmt.Sprintf("[%d of %d] %s", fileCount, maxFiles, picture.timeStamp)
			err = alert(signalUser, signalRecipient, signalGroup, message, picture.tmpFilename)
			os.Remove(picture.tmpFilename)
			if err != nil {
				log.Println(err.Error())
			} else {
				err := delete(picture.fileName, hostname)
				if err != nil {
					log.Println("Failed to delete " + picture.fileName + " - " + err.Error())
					if undeletedFile != nil {
						if _, err := undeletedFile.WriteString(picture.fileName + "\n"); err != nil {
							log.Println("Unable to write to undeleted file - ", err.Error())
						}
						undeletedFile.Sync()
					}
				}
			}
		}

		fileCount = fileCount + 1
	}
}

// process work in a queue (local USB)
func workerLocal(jobChan <-chan Picture, signalUser *string, signalRecipient *string, signalGroup *string, limits *int, maxFiles int) {
	defer wg.Done()

	var err error
	var fileCount int = 1

	for picture := range jobChan {

		if modelLoaded {
			outputfileName, description, _, err := objectDetect(&picture.tmpFilename, limits, false)
			if err != nil {
				log.Println(err.Error())
				err = alert(signalUser, signalRecipient, signalGroup, picture.timeStamp, picture.tmpFilename)
			} else {
				if len(*description) > 0 {
					message := fmt.Sprintf("[%d of %d] %s description: %s", fileCount, maxFiles, picture.timeStamp, *description)
					err = alert(signalUser, signalRecipient, signalGroup, message, picture.tmpFilename+" "+*outputfileName)
				} else {
					message := fmt.Sprintf("[%d of %d] %s", fileCount, maxFiles, picture.timeStamp)
					err = alert(signalUser, signalRecipient, signalGroup, message, picture.tmpFilename)
				}
			}
			if err != nil {
				log.Println(err.Error())
			} else {
				// all good, can now delete on camera
				//
				err = os.Remove(picture.fileName)
				if err != nil {
					log.Println("Failed to delete " + picture.fileName + " - " + err.Error())
				}
			}
		} else {
			// no object detection
			//
			message := fmt.Sprintf("[%d of %d] %s", fileCount, maxFiles, picture.timeStamp)
			err = alert(signalUser, signalRecipient, signalGroup, message, picture.tmpFilename)
			if err != nil {
				log.Println(err.Error())
			} else {
				err = os.Remove(picture.fileName)
				if err != nil {
					log.Println("Failed to delete " + picture.fileName + " - " + err.Error())
				}
			}
		}

		fileCount = fileCount + 1
	}
}

// send an alert via signal
func alert(signalUser *string, signalRecipient *string, signalGroup *string, message string, attachments string) error {
	if (len(*signalUser) > 0) && (len(*signalGroup) > 0 || len(*signalRecipient) > 0) {

		// keep signal happy
		//
		// better to do this from cron
		//
		//cmd := exec.Command("signal-cli", "-u", *signalUser, "receive")
		//stdout, err := cmd.CombinedOutput()
		//if err != nil {
		//	return errors.New("signal-cli failed - " + string(stdout))
		//}
		//log.Println(string(stdout[:]))

		var args []string
		args = append(args, "-u")
		args = append(args, *signalUser)
		args = append(args, "send")
		if len(*signalGroup) > 0 {
			args = append(args, "-g")
			args = append(args, *signalGroup)
		} else {
			args = append(args, strings.Split(*signalRecipient, " ")...)
		}
		if len(message) > 0 {
			args = append(args, "-m")
			args = append(args, message)
		}
		if len(attachments) > 0 {
			args = append(args, "-a")
			args = append(args, strings.Split(attachments, " ")...)
		}
		log.Printf("signal-cli %v\n", args)
		cmd := exec.Command("signal-cli", args...)

		stdout, err := cmd.CombinedOutput()
		if err != nil {
			return errors.New("signal-cli failed - " + string(stdout))
		}
	}

	return nil
}

// Enable and connect to bluetooth device
func connectBluetooth(address *string) (*bluetooth.Device, string, error) {

	// Enable bluetooth
	//
	log.Println("Enabling bluetooth")
	err := adapter.Enable()
	if err != nil {
		return nil, "", errors.New("failed to enable bluetooth - " + err.Error())
	}

	ch := make(chan bluetooth.ScanResult, 1)

	// Start scanning
	//
	log.Printf("Scanning bluetooth for %s\n", *address)
	err = adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		log.Printf("Found bluetooth device: %s %s\n", result.Address.String(), result.LocalName())
		match, _ := regexp.MatchString(*address, result.Address.String())
		if match {
			adapter.StopScan()
			ch <- result
		}
	})
	if err != nil {
		return nil, "", errors.New("failed to complete bluetooth scan - " + err.Error())
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
			return nil, "", errors.New("failed to connect to bluetooth device - " + err.Error())
		}
		log.Printf("Connected to %s\n", result.Address.String())
		return device, result.Address.String(), nil
	}

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

	log.Printf("Looking for WiFi SSID %s\n", *ssid)

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
							log.Printf("Connected to WiFi SSID %s\n", name)
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

	log.Printf("%d files on camera\n", len(sortedFiles))

	return sortedFiles, sortedTimestamps, nil
}

// download a file
func download(file string, hostname string) (string, error) {

	url := "http://" + hostname + strings.ReplaceAll(file, "\\", "/")[2:]

	log.Printf("Downloading %s\n", url)

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
	log.Printf("Deleted %s\n", file)

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
		log.Printf("Unable to set date to %s - %s\n", date, err.Error())
	} else {
		log.Printf("Date set to %s\n", date)
	}
	time := currentTime.Format("15:04:05")
	_, err = http.Get("http://" + hostname + "/?custom=1&cmd=3006&str=" + time)
	if err != nil {
		log.Printf("Unable to set time to %s - %s\n", time, err.Error())
	} else {
		log.Printf("Time set to %s\n", time)
	}

	// battery level (?)
	//
	resp, err := http.Get("http://" + hostname + "/?custom=1&cmd=3019")
	if err != nil {
		log.Printf("Unable to get 3019 - %s\n", err.Error())
	} else {
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			var function Function
			err = xml.Unmarshal(body, &function)
			if err != nil {
				log.Printf("unable to parse 3019 xml - %s\n", err.Error())
			} else {
				battery, _ = strconv.Atoi(function.Value)
				log.Printf("Battery at %s%%\n", function.Value)
			}
		}
	}

	return battery, nil
}
