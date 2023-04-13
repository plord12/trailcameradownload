package main

import (
	"os"
	"testing"
)

func TestTF(t *testing.T) {

	model := "detect.tflite"
	labels := "labelmap.txt"
	xxnpack := true
	limits := 10

	err := loadModel(&model, &labels, &xxnpack)
	if err != nil {
		t.Errorf("failed to load model - %v", err)
	}

	var animals = []string{
		"House_Sparrow",
		"Blue_Tit",
		"European_Starling",
		"Eurasian_Blackbird",
		"Wood_Pigeon",
		"European_Robin",
		"Great_Tit",
		"Eurasian_Goldfinch",
		"Eurasian_Magpie",
		"Long-tailed_Tit",
		"Red_Kite",
		"Grey_Heron",
		"Blackcap",
		"Redwing",
		"Eurasian_Green_Woodpecker",
		"Eurasian_Collared_Dove",
		"Common_Hedgehog",
		"Red_Fox",
		"Eastern_Gray_Squirrel",
		"Domestic_Cat",
		"Brown_Rat",
		"Person",
		"Common_Dandelion",
	}

	for _, animal := range animals {
		picture := "testdata/" + animal + ".jpg"
		outputfileName, _, names, err := objectDetect(&picture, &limits, true)
		if err != nil {
			t.Errorf("%s: object detect failed - %v", animal, err)
		}
		if (*names)[0] != animal {
			t.Errorf("%s: didn't match - see %s", animal, *outputfileName)
		} else {
			os.Remove(*outputfileName)
		}
	}

}
