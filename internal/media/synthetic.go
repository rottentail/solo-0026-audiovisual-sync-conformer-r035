package media

import (
	"fmt"
	"math"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
)

const (
	CalibrationSampleRate = 48000
	CalibrationFrames     = 24
	CalibrationAudioMS    = 1000
	CalibrationAudioEvent = 500
	CalibrationVideoEvent = 9
	CalibrationFPS        = 24
)

// CalibrationSpec describes a deterministic calibration pair. The audio
// transient and picture flash are positioned independently so each take can
// carry its own signed sync offset.
type CalibrationSpec struct {
	SampleRate      int
	Channels        int
	Frames          int
	FPS             int
	Width           int
	Height          int
	AudioEventMS    int
	VideoEventFrame int
	PeakAmplitude   int
}

// CalibrationTake builds a bounded PCM WAV and C420 Y4M pair from a spec. The
// audio length covers the full picture duration so whole-take segments need no
// padding when joined on a shared timeline.
func CalibrationTake(spec CalibrationSpec) ([]byte, []byte, error) {
	if spec.SampleRate < 8000 || spec.SampleRate > 192000 {
		return nil, nil, fmt.Errorf("calibration sample rate is out of range")
	}
	if spec.Channels != 1 && spec.Channels != 2 {
		return nil, nil, fmt.Errorf("calibration channels must be 1 or 2")
	}
	if spec.Frames <= 0 || spec.FPS <= 0 {
		return nil, nil, fmt.Errorf("calibration picture geometry is invalid")
	}
	if spec.Width <= 0 || spec.Height <= 0 || spec.Width%2 != 0 || spec.Height%2 != 0 {
		return nil, nil, fmt.Errorf("calibration picture dimensions must be positive and even")
	}
	if spec.VideoEventFrame < 0 || spec.VideoEventFrame >= spec.Frames {
		return nil, nil, fmt.Errorf("calibration picture event frame is outside the take")
	}
	if spec.AudioEventMS < 0 {
		return nil, nil, fmt.Errorf("calibration audio event must not be negative")
	}
	if spec.PeakAmplitude <= 0 || spec.PeakAmplitude > 32767 {
		return nil, nil, fmt.Errorf("calibration amplitude is out of range")
	}
	durationMS := (spec.Frames*1000 + spec.FPS - 1) / spec.FPS
	if spec.AudioEventMS >= durationMS {
		return nil, nil, fmt.Errorf("calibration audio event is outside the take")
	}
	sampleCount := spec.SampleRate * durationMS / 1000
	samples := make([]int16, sampleCount*spec.Channels)
	eventSample := spec.SampleRate * spec.AudioEventMS / 1000
	for frame := eventSample; frame < sampleCount; frame++ {
		elapsed := float64(frame-eventSample) / float64(spec.SampleRate)
		envelope := math.Exp(-elapsed * 7)
		value := math.Sin(2*math.Pi*440*float64(frame-eventSample)/float64(spec.SampleRate)) *
			float64(spec.PeakAmplitude) * envelope
		scaled := int16(math.Round(value))
		for channel := 0; channel < spec.Channels; channel++ {
			samples[frame*spec.Channels+channel] = scaled
		}
	}
	audio, err := EncodePCM16(spec.SampleRate, spec.Channels, samples)
	if err != nil {
		return nil, nil, err
	}

	frameBytes := spec.Width*spec.Height + 2*(spec.Width/2)*(spec.Height/2)
	frames := make([][]byte, 0, spec.Frames)
	for frameIndex := 0; frameIndex < spec.Frames; frameIndex++ {
		frame := make([]byte, frameBytes)
		luma := byte(32)
		if frameIndex == spec.VideoEventFrame {
			luma = 220
		}
		for y := 0; y < spec.Height; y++ {
			for x := 0; x < spec.Width; x++ {
				frame[y*spec.Width+x] = luma
			}
		}
		chromaSize := (spec.Width / 2) * (spec.Height / 2)
		for index := 0; index < chromaSize; index++ {
			frame[spec.Width*spec.Height+index] = 128
			frame[spec.Width*spec.Height+chromaSize+index] = 128
		}
		frames = append(frames, frame)
	}
	info := domain.VideoInfo{
		Width:      spec.Width,
		Height:     spec.Height,
		FPSNum:     spec.FPS,
		FPSDen:     1,
		FrameCount: len(frames),
		Chroma:     "C420jpeg",
	}
	video, err := EncodeY4M(info, frames)
	if err != nil {
		return nil, nil, err
	}
	return audio, video, nil
}

func CalibrationPair() ([]byte, []byte, error) {
	return CalibrationTake(CalibrationSpec{
		SampleRate:      CalibrationSampleRate,
		Channels:        1,
		Frames:          CalibrationFrames,
		FPS:             CalibrationFPS,
		Width:           96,
		Height:          54,
		AudioEventMS:    CalibrationAudioEvent,
		VideoEventFrame: CalibrationVideoEvent,
		PeakAmplitude:   12000,
	})
}

func MustCalibrationPair() ([]byte, []byte) {
	audio, video, err := CalibrationPair()
	if err != nil {
		panic(fmt.Sprintf("calibration media construction failed: %v", err))
	}
	return audio, video
}
