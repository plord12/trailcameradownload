package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/mattn/go-tflite"
	"gocv.io/x/gocv"

	"golang.org/x/image/colornames"
)

type ssdResult struct {
	loc   []float32
	clazz []float32
	score []float32
	mat   gocv.Mat
}

type ssdClass struct {
	loc   []float32
	score float64
	index int
}

type result interface {
	Image() image.Image
}

func loadLabels(filename string) ([]string, error) {
	labels := []string{}
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		labels = append(labels, scanner.Text())
	}
	return labels, nil
}

func copySlice(f []float32) []float32 {
	ff := make([]float32, len(f), len(f))
	copy(ff, f)
	return ff
}

func detect(ctx context.Context, wg *sync.WaitGroup, resultChan chan<- *ssdResult, wanted_width, wanted_height, wanted_channels int, cam *gocv.VideoCapture) {
	defer wg.Done()
	defer close(resultChan)

	input := interpreter.GetInputTensor(0)
	qp := input.QuantizationParams()
	if qp.Scale == 0 {
		qp.Scale = 1
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if len(resultChan) == cap(resultChan) {
			continue
		}

		frame := gocv.NewMat()
		if ok := cam.Read(&frame); !ok {
			frame.Close()
			break
		}

		resized := gocv.NewMat()
		if input.Type() == tflite.Float32 {
			frame.ConvertTo(&resized, gocv.MatTypeCV32F)
			gocv.Resize(resized, &resized, image.Pt(wanted_width, wanted_height), 0, 0, gocv.InterpolationDefault)
			if ff, err := resized.DataPtrFloat32(); err == nil {
				for i := 0; i < len(ff); i++ {
					ff[i] = (ff[i] - 127.5) / 127.5
				}
				copy(input.Float32s(), ff)
			}
		} else {
			gocv.Resize(frame, &resized, image.Pt(wanted_width, wanted_height), 0, 0, gocv.InterpolationDefault)
			if v, err := resized.DataPtrUint8(); err == nil {
				copy(input.UInt8s(), v)
			}
		}
		resized.Close()
		status := interpreter.Invoke()
		if status != tflite.OK {
			log.Println("invoke failed")
			return
		}

		if len(interpreter.GetOutputTensor(0).Float32s()) > len(interpreter.GetOutputTensor(1).Float32s()) {
			// old style
			resultChan <- &ssdResult{
				loc:   copySlice(interpreter.GetOutputTensor(0).Float32s()),
				clazz: copySlice(interpreter.GetOutputTensor(1).Float32s()),
				score: copySlice(interpreter.GetOutputTensor(2).Float32s()),
				mat:   frame,
			}
		} else {
			// new style
			resultChan <- &ssdResult{
				loc:   copySlice(interpreter.GetOutputTensor(1).Float32s()),
				clazz: copySlice(interpreter.GetOutputTensor(3).Float32s()),
				score: copySlice(interpreter.GetOutputTensor(0).Float32s()),
				mat:   frame,
			}
		}

	}
}

var labels []string = nil
var interpreter *tflite.Interpreter = nil

func loadModel(modelPath *string, labelPath *string) error {

	var err error

	labels, err = loadLabels(*labelPath)
	if err != nil {
		return err
	}

	model := tflite.NewModelFromFile(*modelPath)
	if model == nil {
		return errors.New("cannot load model")
	}

	options := tflite.NewInterpreterOptions()
	options.SetNumThread(4)

	interpreter = tflite.NewInterpreter(model, options)
	if interpreter == nil {
		return errors.New("cannot create interpreter")
	}

	status := interpreter.AllocateTensors()
	if status != tflite.OK {
		log.Println("allocate failed")
		return errors.New("allocate failed")
	}

	log.Printf("Loaded model " + *modelPath + " with " + *labelPath)

	return nil
}

func objectDetect(inputVideo *string, limits *int) (*string, *string, error) {

	if labels == nil || interpreter == nil {
		return nil, nil, nil
	}

	var tmpFile *os.File

	if strings.EqualFold(filepath.Ext(*inputVideo), ".JPG") || strings.EqualFold(filepath.Ext(*inputVideo), ".JPEG") {
		tmpFile, _ = ioutil.TempFile("", "detected.*.%01d"+filepath.Ext(*inputVideo))
	} else {
		tmpFile, _ = ioutil.TempFile("", "detected.*"+filepath.Ext(*inputVideo))
	}

	outputVideo := tmpFile.Name()

	ctx, cancel := context.WithCancel(context.Background())

	cam, err := gocv.OpenVideoCapture(*inputVideo)
	if err != nil {
		cancel()
		return nil, nil, errors.New("cannot open input: " + err.Error())
	}
	defer cam.Close()

	vw, err := gocv.VideoWriterFile(outputVideo, cam.CodecString(), cam.Get(gocv.VideoCaptureFPS), int(cam.Get(gocv.VideoCaptureFrameWidth)), int(cam.Get(gocv.VideoCaptureFrameHeight)), true)
	if err != nil {
		cancel()
		return nil, nil, errors.New("cannot open output: " + err.Error())
	}
	defer vw.Close()

	input := interpreter.GetInputTensor(0)
	wanted_height := input.Dim(1)
	wanted_width := input.Dim(2)
	wanted_channels := input.Dim(3)

	var wg sync.WaitGroup
	wg.Add(1)

	// Start up the background capture
	resultChan := make(chan *ssdResult, 2)
	go detect(ctx, &wg, resultChan, wanted_width, wanted_height, wanted_channels, cam)

	sc := make(chan os.Signal, 1)
	defer close(sc)
	signal.Notify(sc, os.Interrupt)
	go func() {
		<-sc
		cancel()
	}()

	objects := make(map[string]float64)
	frames := 0

	for {
		// Run inference if we have a new frame to read
		result, ok := <-resultChan
		if !ok {
			break
		}

		classes := make([]ssdClass, 0, len(result.clazz))
		for i := 0; i < len(result.clazz); i++ {
			idx := int(result.clazz[i]) // was +1
			if idx < 0 {
				continue
			}
			score := float64(result.score[i])
			if score < 0.6 {
				continue
			}
			classes = append(classes, ssdClass{loc: result.loc[i*4 : (i+1)*4], score: score, index: idx})
		}
		sort.Slice(classes, func(i, j int) bool {
			return classes[i].score > classes[j].score
		})
		if len(classes) > *limits {
			classes = classes[:*limits]
		}

		size := result.mat.Size()
		for _, class := range classes {
			label := "unknown"
			if class.index < len(labels) {
				label = labels[class.index]
			}
			c := colornames.Map[colornames.Names[class.index%len(colornames.Names)]]
			gocv.Rectangle(&result.mat, image.Rect(
				int(float32(size[1])*class.loc[1]),
				int(float32(size[0])*class.loc[0]),
				int(float32(size[1])*class.loc[3]),
				int(float32(size[0])*class.loc[2]),
			), c, 6)
			text := fmt.Sprintf("%s: %.1f%%", strings.Replace(label, "_", " ", -1), class.score*100)
			textlocation := image.Pt(int(float32(size[1])*class.loc[1]), int(float32(size[0])*class.loc[0])+70)
			textsize := gocv.GetTextSize(text, gocv.FontHersheySimplex, 2.0, 2)
			gocv.Rectangle(&result.mat, image.Rect(textlocation.X, textlocation.Y, textlocation.X+textsize.X, textlocation.Y-textsize.Y), color.RGBA{255, 255, 255, 0}, -1)
			gocv.PutText(&result.mat, text, textlocation, gocv.FontHersheySimplex, 2.0, c, 2)

			_, exists := objects[label]
			if exists {
				objects[label] = objects[label] + class.score
			} else {
				objects[label] = class.score
			}
			frames++
		}

		vw.Write(result.mat)
		result.mat.Close()
	}

	cancel()
	wg.Wait()

	keys := make([]string, 0, len(objects))
	for key := range objects {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		return objects[keys[i]] > objects[keys[j]]
	})

	description := ""
	for _, name := range keys {
		averageScore := objects[name] * 100.0 / float64(frames)
		log.Printf("%s (%0.1f%%)\n", name, averageScore)
		if averageScore > 5 {
			description = fmt.Sprintf("%s %s (%0.1f%%)", description, strings.Replace(name, "_", " ", -1), averageScore)
		}
	}

	if strings.EqualFold(filepath.Ext(*inputVideo), ".JPG") || strings.EqualFold(filepath.Ext(*inputVideo), ".JPEG") {
		for i := 0; i < 10; i++ {
			formatedFile := fmt.Sprintf(outputVideo, i)
			if _, err := os.Stat(formatedFile); errors.Is(err, os.ErrNotExist) {
				continue
			}
			os.Remove(outputVideo)
			outputVideo = formatedFile
			break
		}
	}
	return &outputVideo, &description, nil
}
